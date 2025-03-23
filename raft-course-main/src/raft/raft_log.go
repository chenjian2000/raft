package raft

import (
	"course/labgob"
	"fmt"
)

/*
- snapLastIdx int
  - 快照中包含的最后一条日志的索引
  - 用于确定快照覆盖了哪些日志条目
  - 比如 snapLastIdx = 100，表示索引 0-100 的日志都在快照中
  - 如果快照中包含了索引 101 的日志，则 snapLastIdx = 101

- snapLastTerm int
  - 快照中最后一条日志的任期号
  - 用于日志一致性检查
  - 和 snapLastIdx 一起用来确保日志匹配

- snapshot []byte
  - 存储实际的快照数据
  - 包含了状态机在 snapLastIdx 时的完整状态
  - 用于快速恢复状态机状态

- tailLogs []LogEntry
  - 存储快照之后的日志条目
  - 比如如果 snapLastIdx = 100，tailLogs 就存储索引 101 及之后的日志
  - 这些是还没有被快照化的最新日志
*/
type RaftLog struct {
	snapLastIdx  int
	snapLastTerm int
	snapshot     []byte
	tailLogs     []LogEntry
}

func NewLog(snapLastIdx, snapLastTerm int, snapshot []byte, entries []LogEntry) *RaftLog {
	rl := &RaftLog{
		snapLastIdx:  snapLastIdx,
		snapLastTerm: snapLastTerm,
		snapshot:     snapshot,
	}
	rl.tailLogs = append(rl.tailLogs, LogEntry{
		Term: snapLastTerm,
	})
	rl.tailLogs = append(rl.tailLogs, entries...)
	return rl
}

func (rl *RaftLog) readPersist(d *labgob.LabDecoder) error {
	var lastIdx int
	if err := d.Decode(&lastIdx); err != nil {
		return fmt.Errorf("decode last include index failed")
	}
	rl.snapLastIdx = lastIdx

	var lastTerm int
	if err := d.Decode(&lastTerm); err != nil {
		return fmt.Errorf("decode last include term failed")
	}
	rl.snapLastTerm = lastTerm

	var log []LogEntry
	if err := d.Decode(&log); err != nil {
		return fmt.Errorf("decode tail log failed")
	}
	rl.tailLogs = log

	return nil
}

func (rl *RaftLog) persist(e *labgob.LabEncoder) {
	e.Encode(rl.snapLastIdx)
	e.Encode(rl.snapLastTerm)
	e.Encode(rl.tailLogs)
}

func (rl *RaftLog) size() int {
	return rl.snapLastIdx + len(rl.tailLogs)
}

func (rl *RaftLog) idx(logicIdx int) int {
	if logicIdx < rl.snapLastIdx || logicIdx >= rl.size() {
		panic(fmt.Sprintf("%d is out of [%d, %d]", logicIdx, rl.snapLastIdx, rl.size()-1))
	}
	return logicIdx - rl.snapLastIdx
}

func (rl *RaftLog) at(logicIdx int) LogEntry {
	return rl.tailLogs[rl.idx(logicIdx)]
}

func (rl *RaftLog) firstIndexFor(term int) int {
	for i, entry := range rl.tailLogs {
		if entry.Term == term {
			return i + rl.snapLastIdx
		} else if entry.Term > term {
			break
		}
	}
	return InvalidIndex
}

func (rl *RaftLog) last() (int, int) {
	i := len(rl.tailLogs) - 1
	return rl.snapLastIdx + i, rl.tailLogs[i].Term
}

func (rl *RaftLog) tail(startIdx int) []LogEntry {
	if startIdx >= rl.size() {
		return nil
	}
	return rl.tailLogs[rl.idx(startIdx):]
}

func (rl *RaftLog) append(e LogEntry) {
	rl.tailLogs = append(rl.tailLogs, e)
}

func (rl *RaftLog) appendFrom(logicPrevIndex int, entries []LogEntry) {
	rl.tailLogs = append(rl.tailLogs[:rl.idx(logicPrevIndex)+1], entries...)
}

func (rl *RaftLog) String() string {
	var terms string
	prevTerm := rl.snapLastTerm
	prevStart := rl.snapLastIdx
	for i := 0; i < len(rl.tailLogs); i++ {
		if rl.tailLogs[i].Term != prevTerm {
			terms += fmt.Sprintf(" [%d, %d]T%d", prevStart, rl.snapLastIdx+i-1, prevTerm)
			prevTerm = rl.tailLogs[i].Term
			prevStart = i
		}
	}
	terms += fmt.Sprintf("[%d, %d]T%d", prevStart, rl.snapLastIdx+len(rl.tailLogs)-1, prevTerm)
	return terms
}

func (rl *RaftLog) doSnapshot(index int, snapshot []byte) {
	idx := rl.idx(index)
	rl.snapLastIdx = index
	rl.snapLastTerm = rl.tailLogs[idx].Term
	rl.snapshot = snapshot

	tmpLog := make([]LogEntry, 0, rl.size()-rl.snapLastIdx)
	tmpLog = append(tmpLog, LogEntry{Term: rl.snapLastTerm})
	tmpLog = append(tmpLog, rl.tailLogs[idx+1:]...)
	rl.tailLogs = tmpLog
}
