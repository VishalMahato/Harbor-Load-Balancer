package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/vishalmahato/harbor-load-balancer/internal/lb"
)

// metrics holds counters/gauges; hot-path fields use atomics.
// Per-backend maps are guarded by a mutex (updates only on failures/selection).
type metrics struct {
	acceptsTotal      int64 // total successful accepts
	acceptErrorsTotal int64 // accept errors

	capacityDroppedTotal int64 // dropped at concurrency cap

	activeConns int64 // gauge: in-flight handlers (inc on start, dec on end)

	dialsTotal        int64 // total backend dial attempts
	dialFailuresTotal int64
	retriesTotal      int64 // number of "extra" attempts beyond first

	mu                sync.Mutex
	picksByBackend    map[string]int64 // how often a backend was selected (Acquire)
	dialFailByBackend map[string]int64 // dial failures per backend
}

// newMetrics constructs an empty metrics bag.
func newMetrics() *metrics {
	return &metrics{
		picksByBackend:    make(map[string]int64),
		dialFailByBackend: make(map[string]int64),
	}
}

// recordPick increments selection count for addr.
func (m *metrics) recordPick(addr string) {
	m.mu.Lock()
	m.picksByBackend[addr]++
	m.mu.Unlock()
}

// recordDialFailure increments totals + per-backend failure count.
func (m *metrics) recordDialFailure(addr string) {
	atomic.AddInt64(&m.dialFailuresTotal, 1)
	m.mu.Lock()
	m.dialFailByBackend[addr]++
	m.mu.Unlock()
}

// snapshot prepares a JSON-able view; pool may be nil.
func (m *metrics) snapshot(pool *lb.Pool) map[string]any {
	m.mu.Lock()
	picks := make(map[string]int64, len(m.picksByBackend))
	for k, v := range m.picksByBackend {
		picks[k] = v
	}
	fails := make(map[string]int64, len(m.dialFailByBackend))
	for k, v := range m.dialFailByBackend {
		fails[k] = v
	}
	m.mu.Unlock()

	cooldown := 0
	if pool != nil {
		cooldown = pool.CooldownSize()
	}

	return map[string]any{
		"accepts_total":            atomic.LoadInt64(&m.acceptsTotal),
		"accept_errors_total":      atomic.LoadInt64(&m.acceptErrorsTotal),
		"capacity_dropped_total":   atomic.LoadInt64(&m.capacityDroppedTotal),
		"active_conns":             atomic.LoadInt64(&m.activeConns),
		"dials_total":              atomic.LoadInt64(&m.dialsTotal),
		"dial_failures_total":      atomic.LoadInt64(&m.dialFailuresTotal),
		"retries_total":            atomic.LoadInt64(&m.retriesTotal),
		"cooldown_backends":        cooldown,
		"picks_by_backend":         picks,
		"dial_failures_by_backend": fails,
	}
}

// startMetricsServer launches a minimal HTTP server for /metrics JSON.
func startMetricsServer(addr string, m *metrics, pool *lb.Pool) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.snapshot(pool))
	})
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("metrics server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server error: %v", err)
		}
	}()
}
