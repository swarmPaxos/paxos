package shardkv
import "shardmaster"

//
// additions to Clerk state
//
type ClerkImpl struct {
	cacheConfig shardmaster.Config
}

//
// initialize ck.impl.*
//
func (ck *Clerk) InitImpl() {
	ck.impl.cacheConfig = shardmaster.Config{Num: 0}
}

//
// fetch the current value for a key.
// return "" if the key does not exist.
// keep retrying forever in the face of all other errors.
//
func (ck *Clerk) Get(key string) string {
	ck.mu.Lock()
	defer ck.mu.Unlock()

	// if never had config before, ask shardmaster for config
	ck.checkConfig()

	shardNum := key2shard(key)

	// try to get
	// if failed, go ask shardmaster for config
	operation := Op{ Opcode: Get, Key: key, OpID: nrand(), Value: "" }
	argsImpl := GetArgsImpl{ Operation: operation }
	args := &GetArgs{ Impl: argsImpl }

	for {
		gid := ck.impl.cacheConfig.Shards[shardNum]

		if _, exist := ck.impl.cacheConfig.Groups[gid]; !exist {
			panic("the group does not exist!\n")
		}
		for _, s := range ck.impl.cacheConfig.Groups[gid] {
			reply := GetReply{}
			if ok := call(s, "ShardKV.Get", args, &reply); ok {
				if reply.Err == OK {
					return reply.Value
				} else if reply.Err == ErrNoKey {
					return ""
				} else if reply.Err == ErrWrongGroup {
					break
				}
			}
		}
		ck.updateConfig()
	}
}

//
// send a Put or Append request.
// keep retrying forever.
//
func (ck *Clerk) PutAppend(key string, value string, op string) {
	ck.mu.Lock()
	defer ck.mu.Unlock()

	// if never had config before, ask shardmaster for config
	ck.checkConfig()

	shardNum := key2shard(key)

	// try to put append
	// if failed, go ask shardmaster for config
	operation := Op{ Opcode: opcode(op), Key: key, OpID: nrand(), Value: value }
	argsImpl := PutAppendArgsImpl{ Operation: operation }
	args := &PutAppendArgs{ Impl: argsImpl }

	for {
		gid := ck.impl.cacheConfig.Shards[shardNum]

		if _, exist := ck.impl.cacheConfig.Groups[gid]; !exist {
			panic("the group does not exist!\n")
		}
		for _, s := range ck.impl.cacheConfig.Groups[gid] {
			reply := PutAppendReply{}
			ok := call(s, "ShardKV.PutAppend", args, &reply)
			if ok && reply.Err == OK {
				return
			} else if reply.Err == ErrWrongGroup {
				ck.updateConfig()
				break
			}
		}
		ck.updateConfig()
	}
}

func (ck *Clerk) checkConfig() {
	if ck.impl.cacheConfig.Num == 0 {
		// get the config from shardmaster
		ck.updateConfig()
	}

	return
}

func (ck *Clerk) updateConfig() {
	ck.impl.cacheConfig = ck.sm.Query(-1)
}
