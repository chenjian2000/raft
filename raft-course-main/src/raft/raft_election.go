package raft

import (
	"math/rand"
	"time"
)

func (rf *Raft) resetElectionTimerLocked() {
	rf.electionStart = time.Now()
	randRange := int64(electionTimeOutMax - electionTimeOutMin)
	rf.electionTimeOut = time.Duration(rand.Int63()%randRange) + electionTimeOutMin // 通过随机化超时，避免多个节点同时进入 Candidate 状态，导致选举冲突。
}

func (rf *Raft) ifElectionTimeoutLocked() bool {
	return time.Since(rf.electionStart) > rf.electionTimeOut
}

// 判断谁的日志更新（follower只会投票给 日志比自己新的 candidate）
func (rf *Raft) isMoreUpToDateLocked(candidateLastIndex, candidateLastTerm int) bool {
	lastIndex := len(rf.log) - 1
	lastTerm := rf.log[lastIndex].Term
	LOG(rf.me, rf.currentTerm, DVote, "Compare last log, Me: [%d]T%d, Candidate: [%d]T%d", lastIndex, lastTerm, candidateLastIndex, candidateLastTerm)
	// 首先比较 term
	if lastTerm != candidateLastTerm {
		return lastTerm > candidateLastTerm
	}
	return lastIndex > candidateLastIndex
}

// example RequestVote RPC arguments structure.
// field names must start with capital letters!
type RequestVoteArgs struct {
	// Your data here (PartA, PartB).
	Term               int
	CandidateId        int
	CandidateLastIndex int
	CandidateLastTerm  int
}

// example RequestVote RPC reply structure.
// field names must start with capital letters!
type RequestVoteReply struct {
	// Your data here (PartA).
	Term        int
	VoteGranted bool
}

// 接收方调用
// example RequestVote RPC handler.
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (PartA, PartB).
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.VoteGranted = false

	// 任期同步
	if args.Term < rf.currentTerm {
		LOG(rf.me, rf.currentTerm, DVote, "Term is lower than currentTerm")
		return
	}
	if args.Term > rf.currentTerm {
		rf.becomeFollowerLocked(args.Term)
	}
	if rf.votedFor != -1 {
		LOG(rf.me, rf.currentTerm, DVote, "Already voted for %d", rf.votedFor)
		return
	}
	if rf.isMoreUpToDateLocked(args.CandidateLastIndex, args.CandidateLastTerm) {
		LOG(rf.me, rf.currentTerm, DVote, "<- S%d, Reject voted, Candidate less up-to-date", args.CandidateId)
		return
	}
	rf.votedFor = args.CandidateId
	rf.persistLocked()
	reply.VoteGranted = true
	rf.resetElectionTimerLocked()
	LOG(rf.me, rf.currentTerm, DVote, "Vote for %d", args.CandidateId)
}

// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

func (rf *Raft) startElection(term int) {
	votes := 0
	askVoteFromPeers := func(peer int, args *RequestVoteArgs) {
		reply := &RequestVoteReply{}
		ok := rf.sendRequestVote(peer, args, reply)
		rf.mu.Lock()
		defer rf.mu.Unlock()
		if !ok {
			LOG(rf.me, rf.currentTerm, DError, "Send RequestVote to peer %d failed", peer)
			return
		}
		// 任期同步
		if reply.Term > rf.currentTerm {
			rf.becomeFollowerLocked(reply.Term)
			return
		}
		// 检查上下文是否发生了变化
		if rf.contextLostLocked(Candidate, term) {
			LOG(rf.me, rf.currentTerm, DVote, "Context lost while waiting for RequestVoteReply")
			return
		}
		if reply.VoteGranted {
			votes++
			if votes > len(rf.peers)/2 {
				rf.becomeLeaderLocked()
				go rf.replicationTicker(term) // 心跳
			}
		}
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.contextLostLocked(Candidate, term) {
		LOG(rf.me, rf.currentTerm, DVote, "Context lost while waiting for RequestVoteReply")
		return
	}
	// 向所有节点请求投票
	l := len(rf.log)
	for peer := 0; peer < len(rf.peers); peer++ {
		if peer == rf.me { // 处理到自己时，投票给自己
			votes++
			continue
		}
		// 如果不是自己，构造请求参数
		// 是否要投票给candidate，主要看任期和日志（日志由term和idx唯一确定）
		args := &RequestVoteArgs{
			Term:               rf.currentTerm,
			CandidateId:        rf.me,
			CandidateLastIndex: l - 1,
			CandidateLastTerm:  rf.log[l-1].Term,
		}

		go askVoteFromPeers(peer, args)
	}
}

func (rf *Raft) electionTicker() {
	for !rf.killed() {

		// Your code here (PartA)
		// Check if a leader election should be started.
		rf.mu.Lock()
		if rf.role != Leader && rf.ifElectionTimeoutLocked() {
			rf.becomeCandidateLocked()
			// 开始要票
			go rf.startElection(rf.currentTerm)
		}
		rf.mu.Unlock()
		// pause for a random amount of time between 50 and 350
		// milliseconds.
		ms := 50 + (rand.Int63() % 300)
		time.Sleep(time.Duration(ms) * time.Millisecond) // 随机等待一段时间，减少多个节点同时成为候选者的可能性
	}
}
