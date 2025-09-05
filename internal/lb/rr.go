package lb

import "sync"

// create Pool struct to store backends in it

type Pool struct {
	mu    sync.RWMutex // mutex to make it thread safe and stoping the nodes to get corrupted by multiple threads / goroutines
	nodes []string     // slice to stroe backends
	strat Strategy     // have the strategy for the pool
}

//returns , next Node addr and node
func (p *Pool) NextAddr() (string, bool) {
	// Now i have to write a algo to select a backend
	// First things first have to lock it for reading
	p.mu.RLock() // lcoked
	n := len(p.nodes)
	//  what if no nodes exist - just return false with empty addr
	if n == 0 {
		p.mu.RUnlock()
		return "", false
	}
	idx := p.strat.Pick(n) // pick a node index to where we have to forward
	addr := p.nodes[idx]   // get the address of that node
	p.mu.RUnlock()         // read it , now unlock
	return addr, true

}

//  create a constructor to instanciate a New Pool --  not a method cause Pool do not exist before this
func NewPool(initial []string, s Strategy) *Pool {
	cp := make([]string, len(initial))
	copy(cp, initial)
	if s == nil {
		s = &RoundRobin{}
	}
	s.Rebuild(len(cp))
	return &Pool{nodes: cp, strat: s}
}

// Acquire returns (addr, release, ok). Call release() when done
// decrement strategy-specific in-flight counters (LeastConn).
func (p *Pool) Acquire() (string, func(), bool) {
	//do a read lock on Pool as it's being read
	p.mu.RLock()
	n := len(p.nodes)

	// edge case if n == 0 have to return false
	if n == 0 {
		p.mu.RUnlock()
		return "", func() {}, false
	}
	idx := p.strat.Pick(n)
	addr := p.nodes[idx]
	p.strat.Start(idx) // call this befor unlock as , you just unloacked and another routine increase the value we will get a error

	p.mu.RUnlock()

	release := func() { p.strat.Done(idx) }
	return addr, release, true

}
