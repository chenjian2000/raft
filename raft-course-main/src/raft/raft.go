package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	//	"bytes"

	"sync"
	"sync/atomic"
	"time"

	//	"course/labgob"
	"course/labrpc"
)

const (
	electionTimeOutMin time.Duration = 250 * time.Millisecond
	electionTimeOutMax time.Duration = 400 * time.Millisecond

	replicationInterval time.Duration = 200 * time.Millisecond
)

const (
	InvalidIndex int = 0
	InvalidTerm  int = 0
)

type Role string

const (
	Follower  Role = "Follower"
	Candidate Role = "Candidate"
	Leader    Role = "Leader"
)

// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part PartD you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int

	// For PartD:
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state 持久化状态存储器
	me        int                 // this peer's index into peers[] 当前节点在peers数组中的索引
	dead      int32               // set by Kill()

	// Your data here (PartA, PartB, PartC).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.
	role        Role
	currentTerm int
	votedFor    int // -1 means votedFor none

	electionStart time.Time // 选举开始时间
	// 随机化的选举超时时间
	// 这个时间对于follower而言，超时就准备成为candidate；对于candidate而言，超时就是选举超时（表明在当前任期内没有成为leader）；对leader而言没有什么意义
	electionTimeOut time.Duration

	// peer自己的日志
	log *RaftLog

	// 其他peer的日志(只有leader才回用到)
	nextIndex  []int // 记录每个follower的下一个要发送的日志索
	matchIndex []int // 记录每个follower已确认复制的最高日志索引

	commitIndex int           // 已知已提交的最高日志索引
	lastApplied int           // 已应用到状态机的最高日志索引
	applyCh     chan ApplyMsg // 向应用层发送已提交日志的通道
	applyCond   *sync.Cond    // 用于通知日志应用的条件变量
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.role == Leader
}

/*
	状态转换
*/
// 任期校验： 确保只接受更高或者相等的任期
// 投票状态重置： 当进入新的任期时，清除旧任期的投票记录，允许重新投票
// The term is the target term number that the node is expected to transition to.
func (rf *Raft) becomeFollowerLocked(term int) {
	if term < rf.currentTerm {
		LOG(rf.me, rf.currentTerm, DError, "Can't become follower, lower term: T%d", term)
		return
	}
	LOG(rf.me, rf.currentTerm, DLog, "%s->follower, term: T%d->T%d", rf.me, rf.currentTerm, term)
	rf.role = Follower
	shouldPersit := term != rf.currentTerm
	if term > rf.currentTerm { // 当前节点已经进入了新的任期
		rf.votedFor = -1 // 允许在新的任期中重新投票，避免旧的任期的投票影响新的任期
	}
	rf.currentTerm = term
	if shouldPersit {
		rf.persistLocked()
	}
	// follower不需要在角色转换时 重置计时器， follower只在收到合法RPC时重置计时器
}

// 任期递增 + 自投票机制 + 重置计时器
func (rf *Raft) becomeCandidateLocked() {
	if rf.role == Leader {
		LOG(rf.me, rf.currentTerm, DError, "Can't become candidate, already leader")
		return
	}
	LOG(rf.me, rf.currentTerm, DVote, "%s->candidate, term: T%d->T%d", rf.role, rf.currentTerm, rf.currentTerm+1)
	rf.role = Candidate
	rf.currentTerm++
	rf.votedFor = rf.me // candidate只会投票给自己
	rf.persistLocked()
	rf.resetElectionTimerLocked() // 重置计时器，然后进行投票
}

// 必须从candidate -> leader
func (rf *Raft) becomeLeaderLocked() {
	if rf.role != Candidate {
		LOG(rf.me, rf.currentTerm, DError, "Can't become leader, only candidate can become leader")
		return
	}
	LOG(rf.me, rf.currentTerm, DLeader, "Become Leader in term: T%d", rf.currentTerm)
	rf.role = Leader
	for peer := 0; peer < len(rf.peers); peer++ {
		rf.nextIndex[peer] = rf.log.size()
		rf.matchIndex[peer] = 0
	}
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (PartD).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	rf.log.doSnapshot(index, snapshot)
	rf.persistLocked()
}

// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Your code here (PartB).
	if rf.role != Leader {
		return -1, -1, false
	}
	rf.log.append(LogEntry{
		Term:         rf.currentTerm,
		Command:      command,
		CommandValid: true,
	})
	rf.persistLocked()
	// return index, term, isLeader
	LOG(rf.me, rf.currentTerm, DLeader, "Leader accept log [%d]T%d", rf.log.size()-1, rf.currentTerm)
	return rf.log.size() - 1, rf.currentTerm, true
}

// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

// 用于检查节点的状态是否发生了变化， 防止过期的RPC响应 影响当前状态
func (rf *Raft) contextLostLocked(role Role, term int) bool {
	return !(rf.role == role && term == rf.currentTerm)
}

// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (PartA, PartB, PartC).
	rf.role = Follower
	rf.currentTerm = 1
	rf.votedFor = -1
	rf.log = NewLog(InvalidIndex, InvalidTerm, nil, nil)
	rf.matchIndex = make([]int, len(peers))
	rf.nextIndex = make([]int, len(peers))
	rf.applyCh = applyCh
	rf.applyCond = sync.NewCond(&rf.mu)
	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections
	go rf.electionTicker()
	go rf.applicationTicker()
	return rf
}
