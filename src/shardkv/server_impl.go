package shardkv

import (
	"shardmaster"
	"paxos"
	"time"
	"encoding/gob"
)

//
// type that represents "value" included in Paxos proposals
// Field names must start with capital letters
//
const (
	Put          = "Put"
	Append       = "Append"
	Get          = "Get"
	Noop         = "Noop"
)

type opcode string

type Op struct {
	Opcode opcode
	Key string
	Value string
	OpID int64
}

//
// additions to ShardKV state
//
const (
	ACTIVE		= true
	DISABLED	= false
)

type ShardKVImpl struct {
	data map[string]string
	opID map[int64]bool // this opID only contains committed operation. 
	toProposeSeq int // ensure that everything before toProposeSeq is committed
	config shardmaster.Config
	latestConfig shardmaster.Config
	status bool
	received bool
}

//
// This functions is used to check Paxos log
//
func Equal(a interface{}, b interface{}) bool {
	if a_op, ok := a.(Op); ok {
		if b_op, ok := b.(Op); ok {
			if a_op == b_op {
				return true
			}
		}
	} else if a_config, ok := a.(shardmaster.Config); ok {
		if b_config, ok := b.(shardmaster.Config); ok {
			if a_config.Num == b_config.Num {
				return true
			}
		}
	} else if a_transfer, ok := a.(TransferArgs); ok {
		if b_transfer, ok := b.(TransferArgs); ok {
			if a_transfer.ConfigNum == b_transfer.ConfigNum {
				return true
			}
		}
	} else {
		panic("Paxos log not Operation, Reconfig, or Transfer")
	}
	return false
}



//
// initialize kv.impl.*
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) InitImpl() {
	kv.impl.data = make(map[string]string)
	kv.impl.opID = make(map[int64]bool)
	kv.impl.config = shardmaster.Config{Num: 0}
	kv.impl.config = shardmaster.Config{Num: 0}
	kv.impl.toProposeSeq = 0
	kv.impl.status = ACTIVE
	kv.impl.received = true

	gob.Register(shardmaster.Config{})
	gob.Register(TransferArgs{})
}

//
// RPC handler for client Get requests
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) Get(args *GetArgs, reply *GetReply) error {
	
	kv.mu.Lock()
	defer kv.mu.Unlock()

	// if according to the current status, the shard does not belong to me,
	// or I am disabled (i.e. waiting for a Transfer or not updated to the 
	// latest config), propose a Noop to check if I missed any operation
	gid := kv.impl.config.Shards[key2shard(args.Impl.Operation.Key)]
	if gid != kv.gid || kv.impl.status == DISABLED {
		
		operation := Op{ Opcode: Noop, OpID: nrand() }
		
		if Err := kv.proposeOp(operation); Err == "Timeout" {
			reply.Err = "Timeout"
			return nil
		}

		// if I am still disabled or does not own the shard, return Err
		gid = kv.impl.config.Shards[key2shard(args.Impl.Operation.Key)]
		if gid != kv.gid || kv.impl.status == DISABLED {
			reply.Err = ErrWrongGroup
			return nil
		}
	}

	// propose the operation using paxos
	reply.Err = kv.proposeOp(args.Impl.Operation)
	if reply.Err == "Timeout" {
		return nil
	}
	
	// perform this Get
	if value, exist := kv.impl.data[args.Impl.Operation.Key]; exist {
		reply.Err = OK
		reply.Value = value
	} else {
		reply.Err = ErrNoKey
		reply.Value = "" 
	}

	return nil
}

//
// RPC handler for client Put and Append requests
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) PutAppend(args *PutAppendArgs, reply *PutAppendReply) error {

	kv.mu.Lock()
	defer kv.mu.Unlock()

	// if according to the current status, the shard does not belong to me,
	// or I am disabled (i.e. waiting for a Transfer or not updated to the 
	// latest config), propose a Noop to check if I missed any operation
	gid := kv.impl.config.Shards[key2shard(args.Impl.Operation.Key)]
	if gid != kv.gid || kv.impl.status == DISABLED {
		
		operation := Op{ Opcode: Noop, OpID: nrand() }
		
		if Err := kv.proposeOp(operation); Err == "Timeout" {
			reply.Err = "Timeout"
			return nil
		}

		// if I am still disabled or does not own the shard, return Err
		gid = kv.impl.config.Shards[key2shard(args.Impl.Operation.Key)]
		if gid != kv.gid || kv.impl.status == DISABLED {
			reply.Err = ErrWrongGroup
			return nil
		}
	}

	// propose the operation using paxos
	reply.Err = kv.proposeOp(args.Impl.Operation)
	return nil
}

//
// RPC handler for Transfer
// 
//-----------------------------------------------------------------------------
func (kv *ShardKV) Transfer(args *TransferArgs, reply *TransferReply) error {

	kv.mu.Lock()
	defer kv.mu.Unlock()


	// if I have not heard about that config, return without accepting
	if args.ConfigNum > kv.impl.config.Num {

		reply.Ok = false
		return nil
	}

	// if I already know this Transfer, ignore
	if (kv.impl.received && kv.impl.config.Num == args.ConfigNum) ||
			kv.impl.config.Num > args.ConfigNum {

		reply.Ok = true
		return nil
	}

	// if this is the Transfer I am waiting for
	if Err := kv.proposeOp(*args); Err != "Timeout" {
		reply.Ok = true
	} else {
		reply.Ok = false
	}

	return nil
}

//
// ask the shardmaster if there's a new configuration;
// if so, re-configure.
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) tick() {

	kv.mu.Lock()
	defer kv.mu.Unlock()

	kv.impl.latestConfig = kv.sm.Query(-1)

	if kv.impl.received {
		if kv.impl.latestConfig.Num == kv.impl.config.Num {
			kv.impl.status = ACTIVE
		} else if kv.impl.latestConfig.Num == kv.impl.config.Num + 1 {
			kv.proposeOp(kv.impl.latestConfig)
		} else {
			newConfig := kv.sm.Query(kv.impl.config.Num + 1)
			kv.proposeOp(newConfig)
		}
	}
}

//
// proposeOp() only returns when Operation is inserted into the log
// && it ensures that there is no gap before Operation
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) proposeOp(Operation interface{}) Err {

	for {

		kv.px.Start(kv.impl.toProposeSeq, Operation)

		to := 10 * time.Millisecond
		iter := 0

		for {
			if Fate, _Operation := kv.px.Status(kv.impl.toProposeSeq); 
				Fate == paxos.Decided {

				kv.commitOp(_Operation)
				kv.impl.toProposeSeq ++

				// if the operation is successfully inserted into log
				if Equal(_Operation, Operation) {
					return OK

				// if the seqno has be occupied, try next seq
				} else {
					break
				}
			}

			time.Sleep(to)
			if to < 10 * time.Second {
				to *= 2
			}

			// if paxos are not responding for more than 10 iterations
			iter++
			if iter == 10 {
				return "Timeout"
			}
		}
	}
}

//-----------------------------------------------------------------------------
func (kv *ShardKV) commitOp(val interface{}) {

	// if regular operation
	if op, ok := val.(Op); ok {
		// if this operation has not be committed
		if _, opExist := kv.impl.opID[op.OpID]; !opExist {
			if op.Opcode == Put {
				kv.impl.data[op.Key] = op.Value
				kv.impl.opID[op.OpID] = true
			} else if op.Opcode == Append {
				kv.impl.data[op.Key] = kv.impl.data[op.Key] + op.Value
				kv.impl.opID[op.OpID] = true
			} else if op.Opcode == Get {
				// do nothing
			} else if op.Opcode == Noop {
				// do nothing
			}
		}
	}

	// if reconfiguration
	if op, ok := val.(shardmaster.Config); ok {

		if op.Num != kv.impl.config.Num + 1 {
			panic("reconfig num with wrong config num")
		}

		if op.Num == 1 {
			kv.impl.received = true
			kv.impl.status = ACTIVE
		} else {
			kv.sender(op)
			kv.receiver(op)
		}
		kv.impl.config = op
	}

	// if transfer
	if op, ok := val.(TransferArgs); ok {

		// if I already know this Transfer, ignore
		if (kv.impl.received && kv.impl.config.Num == op.ConfigNum) ||
			kv.impl.config.Num > op.ConfigNum {
			panic("transfer seen the second time")
		}

		// commit Transfer
		if op.ConfigNum == kv.impl.config.Num {

			for k, v := range op.Data {
				kv.impl.data[k] = v
			}

			for k, _ := range op.OpID {
				kv.impl.opID[k] = true
			}

			kv.impl.received = true

		} else {
			panic("transfer log with wrong config num")
		}
	}

	kv.px.Done(kv.impl.toProposeSeq)

	return
}

//
// Check whether kv is a sender and send if needed
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) sender(newConfig shardmaster.Config) {

	// for every shard that belonged to me in the last config
	for shard, oldGid := range kv.impl.config.Shards {

		// if it now belongs to someone else, that one is my receiver
		newGid := newConfig.Shards[shard]
		if oldGid == kv.gid && newGid != kv.gid && newGid != 0 {

			// generate the data map to send
			dataToSend := make(map[string]string)

			for k, v := range kv.impl.data {
				if newConfig.Shards[key2shard(k)] == newGid &&
					kv.impl.config.Shards[key2shard(k)] == oldGid {
					dataToSend[k] = v
				}
			}

			// send the data map to the receiver
			go func() {
				for {
					for _, server := range newConfig.Groups[newGid] {

						args := &TransferArgs{
							ConfigNum: newConfig.Num,
							Data: dataToSend,
							OpID: kv.impl.opID }
						reply := TransferReply{ Ok: false }

						if ok := call(server, "ShardKV.Transfer", args, &reply); 
							reply.Ok && ok {
							return
						}
					}
				}
			}()
		}
	}
}

//
// Check whether kv is a receiver and set invariants accordingly
//
//-----------------------------------------------------------------------------
func (kv *ShardKV) receiver(newConfig shardmaster.Config) {

	// for every shard that now belongs to me
	for shard, newGid := range newConfig.Shards {

		// if it belonged to someone else in the last config
		oldGid := kv.impl.config.Shards[shard]
		if newGid == kv.gid && oldGid != kv.gid && oldGid != 0 {

			kv.impl.received = false
			kv.impl.status = DISABLED
			return
		}
	}
}
