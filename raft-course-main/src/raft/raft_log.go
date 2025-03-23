package raft

type RaftLog struct {
	snapLastIdx  int
	snapLastTerm int
	snapshot     []byte
	tailLogs     []LogEntry
}
