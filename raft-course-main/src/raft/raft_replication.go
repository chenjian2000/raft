package raft

import (
	"sort"
	"time"
)

type LogEntry struct { // 将请求抽象成日志
	Term         int
	Command      interface{} // 请求
	CommandValid bool        // 是否需要应用到状态机
}

type AppendEntriesArgs struct {
	Term     int
	LeaderId int

	// 日志同步时，需要先找到“匹配点”再进行日志同步
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry // 需要同步的日志

	LeaderCommit int
}

type AppendEntriesReply struct {
	Term    int
	Success bool
}

// 接收方调用（follower & candidate）
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	reply.Term = rf.currentTerm
	reply.Success = false
	// 1. 任期对齐
	if args.Term < rf.currentTerm {
		LOG(rf.me, rf.currentTerm, DLog2, "Term is lower than currentTerm")
		return
	}
	if args.Term >= rf.currentTerm {
		rf.becomeFollowerLocked(args.Term)
	}
	// 2. 判断日志匹配
	if args.PrevLogIndex >= len(rf.log) { // 日志长度不匹配
		LOG(rf.me, rf.currentTerm, DLog2, "<- S%d, Reject log, Follower log too short, Len:%d < Prev:%d", args.LeaderId, len(rf.log), args.PrevLogIndex)
		return
	}
	if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm { // 日志任期不匹配
		LOG(rf.me, rf.currentTerm, DLog2, "<- S%d, Reject log, Follower log term mismatch, Term:%d < Prev:%d", args.LeaderId, rf.log[args.PrevLogIndex].Term, args.PrevLogTerm)
		return
	}
	// 3. 日志同步
	rf.log = append(rf.log[:args.PrevLogIndex+1], args.Entries...)
	reply.Success = true
	LOG(rf.me, rf.currentTerm, DLog2, "Follower accept logs: (%d, %d]", args.PrevLogIndex, args.PrevLogIndex+len(args.Entries))
	// 4. 更新commitIndex
	if args.LeaderCommit > rf.commitIndex {
		LOG(rf.me, rf.currentTerm, DApply, "Follower update the commit index %d->%d", rf.commitIndex, args.LeaderCommit)
		rf.commitIndex = args.LeaderCommit
		rf.applyCond.Signal()
	}
	rf.resetElectionTimerLocked() // 收到合法RPC时重置计时器
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

// 找到大多数节点都已复制的最高日志索引
func (rf *Raft) getMajorityMatchedLocked() int {
	tmpIndexes := make([]int, len(rf.peers))
	copy(tmpIndexes, rf.matchIndex)
	sort.Ints(sort.IntSlice(tmpIndexes))
	// 确保大多数节点都复制了这个索引之前的日志
	/*
		- 假设有5个节点，matchIndex 数组为 [5,4,3,2,1]
		- 排序后仍为 [5,4,3,2,1]
		- majorityIdx = (5-1)/2 = 2
		- 返回 tmpIndexes[2] = 3
		- 这意味着至少有3个节点（过半数）已经复制了索引3及之前的所有日志
		- 因此索引3之前的日志可以安全地提交
	*/
	majorityIdx := (len(rf.peers) - 1) / 2
	LOG(rf.me, rf.currentTerm, DDebug, "Match index after sort: %v, majority[%d]=%d", tmpIndexes, majorityIdx, tmpIndexes[majorityIdx])
	return tmpIndexes[majorityIdx]
}

// 发送方调用（leader）
func (rf *Raft) startReplication(term int) bool {
	replicateToPeer := func(peer int, args *AppendEntriesArgs) {
		reply := &AppendEntriesReply{}
		ok := rf.sendAppendEntries(peer, args, reply)

		rf.mu.Lock()
		defer rf.mu.Unlock()
		if !ok {
			LOG(rf.me, rf.currentTerm, DError, "Send AppendEntries to %d failed", peer)
			return
		}
		// 对齐任期
		if reply.Term > rf.currentTerm {
			rf.becomeFollowerLocked(reply.Term)
			return
		}

		// 检查上下文是否丢失
		if rf.contextLostLocked(Leader, term) {
			LOG(rf.me, rf.currentTerm, DLog, "-> S%d, Context Lost, T%d:Leader->T%d:%s", peer, term, rf.currentTerm, rf.role)
			return
		}

		// 处理 reply
		if !reply.Success {
			// follower 日志不匹配，回退（直接回退一个term）
			idx, term := args.PrevLogIndex, args.PrevLogTerm
			for idx > 0 && rf.log[idx].Term == term {
				idx--
			}
			rf.nextIndex[peer] = idx + 1
			LOG(rf.me, rf.currentTerm, DLog2, "Follower reject logs: (%d, %d]", args.PrevLogIndex, args.PrevLogIndex+len(args.Entries))
			return
		}
		// follower 日志匹配，更新 matchIndex 和 nextIndex
		rf.matchIndex[peer] = args.PrevLogIndex + len(args.Entries)
		rf.nextIndex[peer] = rf.matchIndex[peer] + 1
		// follower 日志匹配，更新 commitIndex
		majorityMatched := rf.getMajorityMatchedLocked()
		if majorityMatched > rf.commitIndex {
			LOG(rf.me, rf.currentTerm, DApply, "Leader update the commit index %d->%d", rf.commitIndex, majorityMatched)
			rf.commitIndex = majorityMatched
			rf.applyCond.Signal() // leader触发“将日志apply到状态机”（条件：超过半数的follower接受了这笔日志，即返回了reply.Success == true）
		}
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.contextLostLocked(Leader, term) {
		LOG(rf.me, rf.currentTerm, DLog, "Leader %d context lost", rf.me)
		return false
	}
	for peer := 0; peer < len(rf.peers); peer++ {
		if peer == rf.me {
			rf.matchIndex[peer] = len(rf.log) - 1
			rf.nextIndex[peer] = len(rf.log)
			continue
		}
		prevLogIndex := rf.nextIndex[peer] - 1
		prevLogTerm := rf.log[prevLogIndex].Term
		args := &AppendEntriesArgs{
			Term:         term,
			LeaderId:     rf.me,
			PrevLogIndex: prevLogIndex,
			PrevLogTerm:  prevLogTerm,
			Entries:      rf.log[prevLogIndex+1:],
		}

		go replicateToPeer(peer, args)
	}
	return true
}

func (rf *Raft) replicationTicker(term int) {
	for !rf.killed() {
		ok := rf.startReplication(term)
		if !ok {
			break
		}
		time.Sleep(replicationInterval) // 等待一段时间后再次发送心跳，心跳周期必须显著小于选举超时时间
	}
}
