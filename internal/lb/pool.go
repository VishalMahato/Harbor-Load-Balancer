package lb

import "sync"

// Pool stores backends and delegates picking to a Strategy.
// It owns the lock protecting the nodes slice.
type Pool struct {
	mu    sync.RWMutex
	nodes []string
	strat Strategy
}

func NewPool(initial []string, s Strategy) *Pool {
	cp := make([]string, len(initial))
	copy(cp, initial)
	if s == nil {
		s = &RoundRobin{}
	}
	s.Rebuild(len(cp))
	return &Pool{nodes: cp, strat: s}
}

func (p *Pool) SetStrategy(s Strategy) {
	p.mu.Lock()
	p.strat = s
	s.Rebuild(len(p.nodes))
	p.mu.Unlock()
}

// NextAddr returns any backend using the current strategy.
func (p *Pool) NextAddr() (string, bool) {
	p.mu.RLock()
	n := len(p.nodes)
	if n == 0 {
		p.mu.RUnlock()
		return "", false
	}
	idx := p.strat.Pick(n)
	addr := p.nodes[idx]
	p.mu.RUnlock()
	return addr, true
}

// Acquire returns (addr, release, ok). Call release() when done to
// decrement strategy-specific in-flight counters (LeastConn).
func (p *Pool) Acquire() (string, func(), bool) {
	p.mu.RLock()
	n := len(p.nodes)
	if n == 0 {
		p.mu.RUnlock()
		return "", func() {}, false
	}
	idx := p.strat.Pick(n)
	addr := p.nodes[idx]
	p.mu.RUnlock()

	p.strat.Start(idx)
	release := func() { p.strat.Done(idx) }
	return addr, release, true
}

// Add appends a backend; returns false if already present.
func (p *Pool) Add(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.nodes {
		if a == addr {
			return false
		}
	}
	p.nodes = append(p.nodes, addr)
	p.strat.Rebuild(len(p.nodes))
	return true
}

// Remove deletes a backend; returns false if not found.
func (p *Pool) Remove(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := -1
	for idx, a := range p.nodes {
		if a == addr {
			i = idx
			break
		}
	}
	if i < 0 {
		return false
	}
	// delete without preserving order
	p.nodes[i] = p.nodes[len(p.nodes)-1]
	p.nodes = p.nodes[:len(p.nodes)-1]
	p.strat.Rebuild(len(p.nodes))
	return true
}

// List returns a copy of the backends.
func (p *Pool) List() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make([]string, len(p.nodes))
	copy(cp, p.nodes)
	return cp
}
