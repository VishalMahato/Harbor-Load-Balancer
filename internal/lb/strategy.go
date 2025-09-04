package lb

// Strategy must be concurrency-safe.
type Strategy interface {
	// Pick returns an index in [0..n-1] given the current backend count.
	Pick(n int) int

	// Called when a request is assigned to index i, and when it completes.
	Start(i int)
	Done(i int)

	// Called whenever the pool size changes; strategy can resize internal state.
	Rebuild(n int)
}
