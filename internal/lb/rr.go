package lb

import "sync/atomic"

type RoundRobin struct{ ctr atomic.Uint64 }

func (r *RoundRobin) Pick(n int) int {
	i := r.ctr.Add(1)
	return int(i % uint64(n))
}
func (r *RoundRobin) Start(int)   {}
func (r *RoundRobin) Done(int)    {}
func (r *RoundRobin) Rebuild(int) {}
