package main

import (
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/vishalmahato/harbor-load-balancer/internal/lb"
)

// metrics holds counters/gauges; hot-path scalars use atomics.
// Per-backend maps are guarded by an RWMutex.
type metrics struct {
	// hot-path counters
	acceptsTotal         atomic.Int64
	acceptErrorsTotal    atomic.Int64
	capacityDroppedTotal atomic.Int64
	activeConns          atomic.Int64 // gauge: inc on start, dec on end
	dialsTotal           atomic.Int64 // total dial attempts
	dialFailuresTotal    atomic.Int64
	retriesTotal         atomic.Int64 // extra attempts beyond first

	// per-backend tallies
	mu                 sync.RWMutex
	picksByBackend     map[string]int64 // Acquire() selections
	dialFailPerBackend map[string]int64 // dial failures by backend
}

// newMetrics constructs an empty metrics bag.
func newMetrics() *metrics {
	return &metrics{
		picksByBackend:     make(map[string]int64),
		dialFailPerBackend: make(map[string]int64),
	}
}

func (m *metrics) recordAcceptOK()        { m.acceptsTotal.Add(1) }
func (m *metrics) recordAcceptError()     { m.acceptErrorsTotal.Add(1) }
func (m *metrics) recordCapacityDropped() { m.capacityDroppedTotal.Add(1) }

func (m *metrics) incActive() { m.activeConns.Add(1) }
func (m *metrics) decActive() { m.activeConns.Add(-1) }

func (m *metrics) recordDialAttempt()  { m.dialsTotal.Add(1) }
func (m *metrics) recordRetryAttempt() { m.retriesTotal.Add(1) }

func (m *metrics) recordDialFailure(addr string) {
	m.dialFailuresTotal.Add(1)
	m.mu.Lock()
	m.dialFailPerBackend[addr]++
	m.mu.Unlock()
}

func (m *metrics) recordPick(addr string) {
	m.mu.Lock()
	m.picksByBackend[addr]++
	m.mu.Unlock()
}

// snapshot prepares a JSON-able view; pool may be nil.
func (m *metrics) snapshot(pool *lb.Pool) map[string]any {
	// Read-only: take RLock and clone the maps.
	m.mu.RLock()
	picks := maps.Clone(m.picksByBackend)     // Go 1.21+
	fails := maps.Clone(m.dialFailPerBackend) // Go 1.21+
	m.mu.RUnlock()

	cooldown := 0
	if pool != nil {
		cooldown = pool.CoolDownSize()
	}

	return map[string]any{
		"accepts_total":            m.acceptsTotal.Load(),
		"accept_errors_total":      m.acceptErrorsTotal.Load(),
		"capacity_dropped_total":   m.capacityDroppedTotal.Load(),
		"active_conns":             m.activeConns.Load(),
		"dials_total":              m.dialsTotal.Load(),
		"dial_failures_total":      m.dialFailuresTotal.Load(),
		"retries_total":            m.retriesTotal.Load(),
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
