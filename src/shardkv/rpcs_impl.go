package shardkv

// Field names must start with capital letters,
// otherwise RPC will break.

//
// additional state to include in arguments to PutAppend RPC.
//
type PutAppendArgsImpl struct {
	Operation Op
}

//
// additional state to include in arguments to Get RPC.
//
type GetArgsImpl struct {
	Operation Op
}

type TransferArgs struct {
	ConfigNum int
	Data map[string]string
	OpID map[int64]bool
}

type TransferReply struct {
	Ok bool
}