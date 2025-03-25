package kvraft

import (
	"course/labrpc"
	"crypto/rand"
	"math/big"
)

/*
raft的节点；记录leader的ID；客户端ID；请求的序号
*/
type Clerk struct {
	servers  []*labrpc.ClientEnd
	leaderId int
	// clientId + seqId 保证请求的唯一
	clientId int64
	seqId    int64
}

func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

// 初始化一个 客户端
func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	return &Clerk{
		servers:  servers,
		leaderId: 0,
		clientId: nrand(),
		seqId:    0,
	}
}

// Get fetch the current value for a key.
// returns "" if the key does not exist.
// keeps trying forever in the face of all other errors.
//
// you can send an RPC with code like this:
// ok := ck.servers[i].Call("KVServer.Get", &args, &reply)
//
// the types of args and reply (including whether they are pointers)
// must match the declared types of the RPC handler function's
// arguments. and reply must be passed as a pointer.
/*
1. 构造RPC请求
2. 轮询，直到找到leader
*/
func (ck *Clerk) Get(key string) string {
	args := &GetArgs{
		Key: key,
	}
	for {
		reply := GetReply{}
		ok := ck.servers[ck.clientId].Call("KVServer.Get", &args, &reply)
		if !ok || reply.Err == ErrWrongLeader || reply.Err == ErrTimeout {
			// !ok RPC请求失败
			// reply.Err == ErrWrongLeader 不是leader
			// reply.Err == ErrTimeout （可能是因为发生了leader切换）
			// 失败则轮询下一个节点
			ck.leaderId = (ck.leaderId + 1) % len(ck.servers)
			continue
		}
		return reply.Value
	}
}

// PutAppend shared by Put and Append.
//
// you can send an RPC with code like this:
// ok := ck.servers[i].Call("KVServer.PutAppend", &args, &reply)
//
// the types of args and reply (including whether they are pointers)
// must match the declared types of the RPC handler function's
// arguments. and reply must be passed as a pointer.
// 与Get类似
func (ck *Clerk) PutAppend(key string, value string, op string) {
	args := &PutAppendArgs{
		Key:      key,
		Value:    value,
		Op:       op,
		ClientId: ck.clientId,
		SeqId:    ck.seqId,
	}
	for {
		reply := &PutAppendReply{}
		ok := ck.servers[ck.leaderId].Call("KVServer.PutAppend", &args, &reply)
		if !ok || reply.Err == ErrWrongLeader || reply.Err == ErrTimeout {
			ck.leaderId = (ck.leaderId + 1) % len(ck.servers)
			continue
		}
		ck.seqId++ // 请求序号递增
		return
	}
}

// Put 写入数据
func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}

// Append 追加数据
func (ck *Clerk) Append(key string, value string) {
	ck.PutAppend(key, value, "Append")
}
