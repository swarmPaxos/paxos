package shardmaster

import (
	"time"
	"paxos"
	"reflect"
	"errors"
)

//
// Define what goes into "value" that Paxos is used to agree upon.
// Field names must start with capital letters,
// otherwise RPC will break.
//

const (
	Join         = "Join"
	Leave       = "Leave"
	Move        = "Move"
	Query        = "Query"
)

type opcode string

type Op struct {
	Opcode opcode // Join, Leave, Move, or Query
	GID int64 // used by Join, Leave, Move
	Servers []string // used by Join
	Shard int // used by Move
	Num int // used by Query
}

//
// additions to ShardMaster state
//
type ShardMasterImpl struct {
	toProposeSeq int // ensure that everything before toProposeSeq is committed
	GIDtoShards map[int64]map[int]bool // maps gid back to shard number
}

//
// initialize sm.impl.*
//
func (sm *ShardMaster) InitImpl() {
	sm.impl.toProposeSeq = 0
	sm.impl.GIDtoShards = make(map[int64]map[int]bool)
}

func (sm *ShardMaster) Join(args *JoinArgs, reply *JoinReply) error { // Put
	sm.mu.Lock()
	defer sm.mu.Unlock()

	Operation := Op { Opcode: Join, GID: args.GID, Servers: args.Servers }
	if Err := sm.proposeOp(Operation); Err == "Timeout" {
		return errors.New("Error: shardmaster timeout")
	}
	
	return nil
}

func (sm *ShardMaster) Leave(args *LeaveArgs, reply *LeaveReply) error { // Put
	sm.mu.Lock()
	defer sm.mu.Unlock()

	Operation := Op { Opcode: Leave, GID: args.GID }
	if Err := sm.proposeOp(Operation); Err == "Timeout" {
		return errors.New("Error: shardmaster timeout")
	}

	return nil
}

func (sm *ShardMaster) Move(args *MoveArgs, reply *MoveReply) error { // Put
	sm.mu.Lock()
	defer sm.mu.Unlock()

	Operation := Op { Opcode: Move, GID: args.GID, Shard: args.Shard }
	if Err := sm.proposeOp(Operation); Err == "Timeout" {
		return errors.New("Error: shardmaster timeout")
	}

	return nil
}

func (sm *ShardMaster) Query(args *QueryArgs, reply *QueryReply) error { // Gets
	sm.mu.Lock()
	defer sm.mu.Unlock()

	Operation := Op { Opcode: Query, Num: args.Num }
	if Err := sm.proposeOp(Operation); Err == "Timeout" {
		return errors.New("Error: shardmaster timeout")
	}

	// if query -1 or too large, return the latest
	if args.Num == -1 || args.Num + 1 >= len(sm.configs) {
		reply.Config = sm.configs[len(sm.configs) - 1]
	} else {
		reply.Config =  sm.configs[args.Num]
	}

	return nil
}

//
// proposeOp() only returns when Operation is inserted into the log
// && it ensures that there is no gap before Operation
//
func (sm *ShardMaster) proposeOp(Operation Op) string {
	for {
		sm.px.Start(sm.impl.toProposeSeq, Operation)

		to := 10 * time.Millisecond
		iter := 0

		for {
			if Fate, _Operation := sm.px.Status(sm.impl.toProposeSeq); Fate == paxos.Decided {

				sm.commitOp(_Operation)
				sm.impl.toProposeSeq ++

				// if the operation is successfully inserted into log
				if reflect.DeepEqual(_Operation, Operation) {
					return "OK"

				// if the seqno has be occupied by other operation, break and try another seqno
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

func (sm *ShardMaster) commitOp(val interface{}) {

	if op, ok := val.(Op); ok {
		if op.Opcode == Join {
			sm.join(op)
		} else if op.Opcode == Leave {
			sm.leave(op)
		} else if op.Opcode == Move {
			sm.move(op)
		} else if op.Opcode == Query {
			// do nothing
		}
	}

	sm.px.Done(sm.impl.toProposeSeq)
	return
}

func (sm *ShardMaster) join(op Op) {
	// if the group already exist, return
	if _, exist := sm.configs[len(sm.configs) - 1].Groups[op.GID]; exist {
		return
	}

	// create the new config by copying the current config
	config := sm.copyCurrentConfig()

	sm.impl.GIDtoShards[op.GID] = make(map[int]bool)
	
	// if there is no replica group currently activated
	// we need to allocate the shards to this newly joint gid
	if len(sm.configs[len(sm.configs) - 1].Groups) == 0 {

		for i := 0; i < NShards; i++ {
			config.Shards[i] = op.GID
			sm.impl.GIDtoShards[op.GID][i] = true
		}

	} else {
		// find the group with maximum number of shards
		GID, max := sm.GIDHasMax(op)

		// assign half of the shards of that group to the new group
		count := 0
		for shard, _ := range sm.impl.GIDtoShards[GID] {
			config.Shards[shard] = op.GID
			sm.impl.GIDtoShards[op.GID][shard] = true
			delete(sm.impl.GIDtoShards[GID], shard)

			count ++
			if count == max / 2 {
				break
			}
		}
	}

	// record the information of the new group
	config.Groups[op.GID] = op.Servers

	// add the new config to the array
	sm.configs = append(sm.configs, config)
}

func (sm *ShardMaster) leave(op Op) {
	// if the group already does not exist, return
	if _, exist := sm.configs[len(sm.configs) - 1].Groups[op.GID]; !exist {
		return
	}

	// create the new config by copying the current config
	config := sm.copyCurrentConfig()

	// if there is only one group left until no groups exist
	if len(sm.configs[len(sm.configs) - 1].Groups) == 1 {

		for i := 0; i < NShards; i++ {
			config.Shards[i] = 0
		}

	} else {

		// find the group with minimum number of shards
		GID := sm.GIDHasMin(op)

		// assign the shards of the leaving group to that group
		for shard, _ := range sm.impl.GIDtoShards[op.GID] {
			config.Shards[shard] = GID
			sm.impl.GIDtoShards[GID][shard] = true
		}
	}

	// delete the information of the leaving group
	delete(config.Groups, op.GID)
	delete(sm.impl.GIDtoShards, op.GID)

	// add the new config to the array
	sm.configs = append(sm.configs, config)
}

func (sm *ShardMaster) move(op Op) {
	GID := sm.configs[len(sm.configs) - 1].Shards[op.Shard]

	// if the shard is already in the target group, return
	if GID == op.GID {
		return
	}

	// create the new config by copying the current config
	config := sm.copyCurrentConfig()

	// update invariants
	sm.impl.GIDtoShards[op.GID][op.Shard] = true
	delete(sm.impl.GIDtoShards[GID], op.Shard)
	config.Shards[op.Shard] = op.GID

	// add the new config to the array
	sm.configs = append(sm.configs, config)
}

func (sm *ShardMaster) copyCurrentConfig() Config {
	config := Config{ Num: len(sm.configs) }
	config.Shards = sm.configs[len(sm.configs) - 1].Shards

	config.Groups = make(map[int64][]string)
	for gid, servers := range sm.configs[len(sm.configs) - 1].Groups {
		config.Groups[gid] = servers
	}

	return config
}


func (sm *ShardMaster) GIDHasMin(op Op) int64 {
	// find the group with minimum number of shards
	min := -1
	GID := int64(0)
	for gid, shards := range sm.impl.GIDtoShards {
		if gid == op.GID {
			continue
		}
		if min == -1 || len(shards) < min {
			min = len(shards)
			GID = gid
		}
	}

	return GID
}


func (sm *ShardMaster) GIDHasMax(op Op) (int64, int) {

	// find the group with maximum number of shards
	max := 0
	GID := int64(0)
	for gid, shards := range sm.impl.GIDtoShards {
		if len(shards) > max {
			max = len(shards)
			GID = gid
		}
	}

	return GID, max
}
