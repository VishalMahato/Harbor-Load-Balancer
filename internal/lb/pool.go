package lb

import (
	"sync"
	"time"
)

// Pool stores backends and delegates picking to a Strategy.
// It owns the lock protecting the nodes slice.
type Pool struct {
	mu        sync.RWMutex
	nodes     []string
	downUntil map[string]time.Time // cooldown window per addr
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

// Acquire returns (addr, release, ok). Call release() when done
// to decrement strategy-specific in-flight counters (LeastConn).
func (p *Pool) Acquire() (string, func(), bool) {
	p.mu.RLock()
	n := len(p.nodes)
	if n == 0 {
		p.mu.RUnlock()
		return "", func() {}, false
	}
	now := time.Now()

	// Try up to n picks, skipping those in cooldown.
	for tries := 0; tries < n; tries++ {
		idx := p.strat.Pick(n)
		addr := p.nodes[idx]

		// Skip if cooling down.
		if until, cooling := p.downUntil[addr]; cooling && now.Before(until) {
			continue
		}

		// in-flight ++ for LeastConn (safe: strategies should use atomics)
		p.strat.Start(idx)
		p.mu.RUnlock()

		release := func() { p.strat.Done(idx) }
		return addr, release, true
	}

	p.mu.RUnlock()
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
// Acquire() will skip this addr until the cooldown expires.
func (p *Pool) MarkDown(addr string, cooldown time.Duration) {
	if cooldown <= 0 {
		return
	}
	p.mu.Lock()
	p.downUntil[addr] = time.Now().Add(cooldown)
	p.mu.Unlock()
}

// MarkSuccess clears any cooldown mark for a backend immediately.
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

// CooldownSize returns the number of backends currently marked in active cooldown.
func (p *Pool) CoolDownSize() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	n := 0
	for _, until := range p.downUntil {
		if now.Before(until) {
			n++
		}
	}
	return n
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
