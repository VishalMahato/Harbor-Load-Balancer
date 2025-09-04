package lb

import "sync/atomic"

// LeastConn approximates "least-requests": choose the backend with
// the smallest in-flight counter.
type LeastConn struct {
	counts []int64 // one counter per backend
}

func (l *LeastConn) Rebuild(n int) { l.counts = make([]int64, n) }

func (l *LeastConn) Pick(n int) int {
	minIdx, minVal := 0, int64(1<<60)
	for i := 0; i < n; i++ {
		v := atomic.LoadInt64(&l.counts[i])
		if v < minVal {
			minVal, minIdx = v, i
		}
	}
	return minIdx
}

func (l *LeastConn) Start(i int) { atomic.AddInt64(&l.counts[i], 1) }
func (l *LeastConn) Done(i int)  { atomic.AddInt64(&l.counts[i], -1) }
