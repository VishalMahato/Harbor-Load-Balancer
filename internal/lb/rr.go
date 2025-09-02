// roundrobin algo for the
package lb

import "sync/atomic"

var rr uint64

// Next returns an index in [0..n-1]
func Next(n int) int {
	i := atomic.AddUint64(&rr, 1)
	return int(i % uint64(n))
}
