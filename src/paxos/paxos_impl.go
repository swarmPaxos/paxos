package paxos

//
// struct to hold information about each agreement instance
//
type instanceInfo struct {
	n_a int // highest accepted seen
	v_a interface{} // value of highest accepted seen
	n_p int // highest prepare seen
	decided bool // whether this instance has been decided
}

//
// additions to Paxos state.
//
type PaxosImpl struct {
	done_seq []int // for each i, peer[i] is done with seq < done_seq[i]
	max_seq int // maximum sequence number ever encountered
	min_seq int // minimum sequence number held
	instances map[int]instanceInfo // managed by acceptor
}

//
// your px.impl.* initializations here.
//
func (px *Paxos) initImpl() {
	px.impl.done_seq = make([]int, len(px.peers))
	for i := 0; i < len(px.peers); i++ {
		px.impl.done_seq[i] = -1
	}

	px.impl.max_seq = -1
	px.impl.min_seq = 0
	px.impl.instances = make(map[int]instanceInfo)
}

//
// the application wants paxos to start agreement on
// instance seq, with proposed value v.
// Start() returns right away; the application will
// call Status() to find out if/when agreement
// is reached.
//
func (px *Paxos) Start(seq int, v interface{}) {
	px.mu.Lock()
	defer px.mu.Unlock()

	// if get a start request that has been forgotten,
	// which means it is also decided before, we return immediately
	// to let user discover it has been forgotten
	if seq < px.impl.min_seq {
		return
	}

	// if the instance has been decided (learned), we 
	// return immediately to let user discover it has been decided
	if instance, ok := px.impl.instances[seq]; ok {
		if instance.decided {
			return
		}
	}

	// update max sequence number encountered so far
	if seq > px.impl.max_seq {
		px.impl.max_seq = seq
	}

	// sends a prepare message
	go px.SendPrepareMessage(seq, v)
}

//
// the application on this machine is done with
// all instances <= seq.
//
// see the comments for Min() for more explanation.
//
func (px *Paxos) Done(seq int) {
	px.mu.Lock()

	if seq < px.impl.done_seq[px.me] {
		px.mu.Unlock()
		return
	}

	px.impl.done_seq[px.me] = seq
	px.mu.Unlock()

	args := &DoneArgs{ Me: px.me, Done_seq: seq }
	reply := DoneReply{}

	for i, p := range px.peers {
		if px.me == i {
			px.DoneRequest(args, &reply)
		} else {
			call(p, "Paxos.DoneRequest", args, &reply)
		}
	}
}

//
// the application wants to know the
// highest instance sequence known to
// this peer.
//
func (px *Paxos) Max() int {
	px.mu.Lock()
	max_seq := px.impl.max_seq
	px.mu.Unlock()

	return max_seq
}

//
// Min() should return one more than the minimum among z_i,
// where z_i is the highest number ever passed
// to Done() on peer i. A peer's z_i is -1 if it has
// never called Done().
//
// Paxos is required to have forgotten all information
// about any instances it knows that are < Min().
// The point is to free up memory in long-running
// Paxos-based servers.
//
// Paxos peers need to exchange their highest Done()
// arguments in order to implement Min(). These
// exchanges can be piggybacked on ordinary Paxos
// agreement protocol messages, so it is OK if one
// peers Min does not reflect another Peers Done()
// until after the next instance is agreed to.
//
// The fact that Min() is defined as a minimum over
// *all* Paxos peers means that Min() cannot increase until
// all peers have been heard from. So if a peer is dead
// or unreachable, other peers Min()s will not increase
// even if all reachable peers call Done. The reason for
// this is that when the unreachable peer comes back to
// life, it will need to catch up on instances that it
// missed -- the other peers therefore cannot forget these
// instances.
//
func (px *Paxos) Min() int {
	px.mu.Lock()
	min_seq := px.impl.min_seq
	px.mu.Unlock()

	return min_seq
}

//
// the application wants to know whether this
// peer thinks an instance has been decided,
// and if so what the agreed value is. Status()
// should just inspect the local peer state;
// it should not contact other Paxos peers.
//
func (px *Paxos) Status(seq int) (Fate, interface{}) {
	px.mu.Lock()
	defer px.mu.Unlock()
	
	if px.impl.min_seq > seq {
		return Forgotten, nil
	} else if val, exist := px.impl.instances[seq]; exist {
		if val.decided {
			return Decided, val.v_a
		} else {
			return Pending, nil
		}
	} else {
		return Pending, nil
	}
}

func (px *Paxos) SendPrepareMessage(seq int, v interface{}) {
	round := 0
	agreeServer := make([]int, len(px.peers))

	// do this while paxos is not dead
	for !px.isdead() {
		count := 0

		// update proposal number each round
		args := &PrepareArgs{ Seqno: seq, N: px.me + round * len(px.peers), Sender: px.me }
		

		n_a := -1
		v_a := v

		// clear agreeServer
		for i, _ := range agreeServer {
			agreeServer[i] = 0
		}

		// start proposing
		for i, p := range px.peers {

			reply := PrepareReply{}

			// send prepare message to each of the paxos
			if px.me == i {
				px.Prepare(args, &reply)
			} else {
				if ok := call(p, "Paxos.Prepare", args, &reply); !ok {
					continue
				}
			}

			// parse the respones
			if reply.Response == OK {

				if reply.N_a > n_a {
					n_a = reply.N_a
					v_a = reply.V_a
				}
				count ++

				agreeServer[i] = 1
			}
		}

		if count >= len(px.peers)/2 + 1  {
			
			if ok := px.SendAcceptMessage(seq, round, v_a, agreeServer); ok {
				return
			}
		}
		
		round ++
	}
}

func (px *Paxos) SendAcceptMessage(seq int, round int, v interface{}, agreeServer []int) bool {
	count := 0
	args := &AcceptArgs{ Seqno: seq, N: px.me + round * len(px.peers), V: v, Sender: px.me }
	
	acceptServer := make([]int, len(px.peers))

	for i, _ := range acceptServer {
		acceptServer[i] = 0
	}

	//px.clearPeerArr()

	for i, p := range px.peers {

		reply := AcceptReply{}

		// if the paxos does not OK with prepare, just skip it
		if  agreeServer[i] == 0 {
			continue
		}

		// send accept message to those respond OK with prepare
		if px.me == i {
			px.Accept(args, &reply)
		} else {
			if ok := call(p, "Paxos.Accept", args, &reply); !ok {
				continue
			}
		}

		if reply.Response == OK {
			count ++
			acceptServer[i] = 1
		}
	}

	if count >= len(px.peers)/2 + 1 {
		
		px.SendLearnMessage(seq, v, args.N)
		return true
	} else {
		return false
	}
}

func (px *Paxos) SendLearnMessage(seq int, v interface{}, proposalNum int) {
	args := &DecidedArgs{ Seqno: seq, V: v, Sender:px.me, N: proposalNum }
	reply := DecidedReply{}

	for i, p := range px.peers {
		if px.me == i {
			px.Learn(args, &reply)
		} else {
			call(p, "Paxos.Learn", args, &reply)
		}
	}
}