package lb

import (
	"sync"
	"time"
)

// Pool stores backends and delegates picking to a Strategy.
// It owns the lock protecting the nodes slice.
type Pool struct {
	mu    sync.RWMutex
	nodes []string
	// for health status
	downUntil map[string]time.Time
	strat     Strategy
}

func NewPool(initial []string, s Strategy) *Pool {
	cp := make([]string, len(initial))
	copy(cp, initial)
	if s == nil {
		s = &RoundRobin{}
	}
	s.Rebuild(len(cp))
	return &Pool{
		nodes:     cp,
		strat:     s,
		downUntil: make(map[string]time.Time),
	}
}

func (p *Pool) SetStrategy(s Strategy) {
	p.mu.Lock()
	p.strat = s
	s.Rebuild(len(p.nodes))
	p.mu.Unlock()
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
	now := time.Now()
	// to retry we will do loop till n
	for tries := 0; tries < n; tries++ {
		idx := p.strat.Pick(n)
		addr := p.nodes[idx]
		// skip if still cooling down.
		if until, cooling := p.downUntil[addr]; cooling && now.Before((until)) {
			continue
		}
		// for leastconn have to start it as its track in-flight
		p.strat.Start(idx)
		p.mu.RUnlock()

		release := func() { p.strat.Done(idx) }

		return addr, release, true
	}
	return "", func() {}, false
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

// MarkDown marks a backend as unavailable for the given cooldown duration.
//
// Acquire() will skip this addr until the cooldown expires.
func (p *Pool) MarkDown(addr string, cooldown time.Duration) {
	if cooldown <= 0 {
		return
	}
	p.mu.Lock()
	p.downUntil[addr] = time.Now().Add(cooldown)
}

// MarkSuccess clears any cooldown mark for a backend.
//
// WHEN to call: after a successful connection/use of a backend that may have
// been marked down earlier. This lets it re-enter rotation immediately.
func (p *Pool) MarkSuccess(addr string) {
	p.mu.Lock()
	delete(p.downUntil, addr)
	p.mu.Unlock()
}

// IsCooling returns true if addr has an unexpired cooldown mark.
func (p *Pool) IsCooling(addr string) bool {
	p.mu.RLock()
	until, ok := p.downUntil[addr]
	p.mu.RUnlock()
	return ok && time.Now().Before(until)
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
	delete(p.downUntil, addr)
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
