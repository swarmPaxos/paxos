package paxos

// In all data types that represent RPC arguments/reply, field names 
// must start with capital letters, otherwise RPC will break.

const (
	OK          = "OK"
	Reject      = "Reject"
)

type Response string

type PrepareArgs struct {
	Seqno int
	N int

	Sender int
}

type PrepareReply struct {
	Response Response
	N int
	N_a int
	V_a interface{}
	Decided bool // tells whether the reason of rejection is decided
}

type AcceptArgs struct {
	Seqno int
	N int
	V interface{}

	Sender int
}

type AcceptReply struct {
	Response Response
	V_a interface{} // tells the value if decided
	Decided bool // tells whether the reason of rejection is decided
}

type DecidedArgs struct {
	Seqno int
	V interface{}
	N int

	Sender int

}

type DecidedReply struct {
	Response Response
}

// assume Prepare would only be called if the value is undecided
func (px *Paxos) Prepare(args *PrepareArgs, reply *PrepareReply) error {
	px.mu.Lock()

	if args.Seqno < px.impl.min_seq {
		reply.Response = Reject
		px.mu.Unlock()
		return nil
	}

	val, exist := px.impl.instances[args.Seqno]

	// set the maximum sequence number encountered
	if args.Seqno > px.impl.max_seq {
		px.impl.max_seq = args.Seqno
	}

	// this slot has not been proposed yet or the current number seen is less
	if (!exist) || (val.n_p < args.N) {
		if !exist {

			px.impl.instances[args.Seqno] = instanceInfo{ n_a: -1, n_p: -1, decided: false }
		}

		tmp := px.impl.instances[args.Seqno]
		tmp.n_p = args.N
		px.impl.instances[args.Seqno] = tmp

		reply.Response = OK
		reply.N = args.N

		reply.N_a = tmp.n_a
		reply.V_a = tmp.v_a

	} else {

		reply.Response = Reject
	}

	px.mu.Unlock()
	return nil
}

func (px *Paxos) Accept(args *AcceptArgs, reply *AcceptReply) error {
	px.mu.Lock()

	if args.Seqno < px.impl.min_seq {
		reply.Response = Reject
		px.mu.Unlock()
		return nil
	}

	val, _ := px.impl.instances[args.Seqno]

	// set the maximum sequence number encountered
	if args.Seqno > px.impl.max_seq {
		px.impl.max_seq = args.Seqno
	}

	if args.N >= val.n_p {

		tmp := px.impl.instances[args.Seqno]
		tmp.n_p = args.N
		tmp.n_a = args.N
		tmp.v_a = args.V
		px.impl.instances[args.Seqno] = tmp

		reply.Response = OK
	} else {
		reply.Response = Reject
	}

	px.mu.Unlock()
	return nil
}

func (px *Paxos) Learn(args *DecidedArgs, reply *DecidedReply) error {
	px.mu.Lock()

	if args.Seqno < px.impl.min_seq {
		reply.Response = Reject
		px.mu.Unlock()
		return nil
	}

	_, exist := px.impl.instances[args.Seqno]
	if !exist {
		px.impl.instances[args.Seqno] = instanceInfo{ n_a: -1, n_p: -1, decided: false }
	}

	// set the maximum sequence number encountered
	if args.Seqno > px.impl.max_seq {
		px.impl.max_seq = args.Seqno
	}

	tmp := px.impl.instances[args.Seqno]

	tmp.v_a = args.V
	tmp.n_a = args.N
	tmp.n_p = args.N
	tmp.decided = true
	px.impl.instances[args.Seqno] = tmp

	reply.Response = OK

	px.mu.Unlock()
	return nil
}

//
// add RPC handlers for any RPCs you introduce.
//

type DoneArgs struct {
	Me int
	Done_seq int
}

type DoneReply struct {
}

func (px *Paxos) DoneRequest(args *DoneArgs, reply *DoneReply) error {
	px.mu.Lock()

	// update done_seq accroding to args
	px.impl.done_seq[args.Me] = args.Done_seq

	// find the min done_seq of all (if min_done = -1, then nothing should be deleted)
	min_done_seq := args.Done_seq
	for i := range px.peers {
		if px.impl.done_seq[i] < min_done_seq {
			min_done_seq = px.impl.done_seq[i]
		}
	}

	// update min_seq and delete instances with sequence number less than min_seq
	if min_done_seq >= px.impl.min_seq {
		px.impl.min_seq = min_done_seq + 1
		for k, _ := range px.impl.instances {
			if k < px.impl.min_seq {
				delete(px.impl.instances, k);
			}
		}
	}

	px.mu.Unlock()
	return nil
}