package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vishalmahato/harbor-load-balancer/internal/lb"
)

func main() {
	fmt.Println("Harbor up")

	//  Allow seed on if LB_ALLOW_DEV_SEED=1.
	backends := parseBackendList(os.Getenv("BACKENDS"))
	if len(backends) == 0 {
		if os.Getenv("LB_ALLOW_DEV_SEED") == "1" {
			backends = []string{"127.0.0.1:9001", "127.0.0.1:9002"} // dev-only
			log.Println("WARNING: using dev seed backends; set BACKENDS in production.")
		} else {
			log.Fatal("no BACKENDS configured; set BACKENDS=host:port[,host:port...]")
		}
	}
	log.Println("backends:", backends)

	// Strategy
	var strat lb.Strategy
	v := strings.ToLower(strings.TrimSpace(os.Getenv("STRATEGY")))
	switch v {
	case "least", "leastconn", "least-requests", "leastrequests", "lc":
		strat = &lb.LeastConn{}
	case "rr", "round-robin", "roundrobin", "":
		strat = &lb.RoundRobin{}
	default:
		strat = &lb.RoundRobin{}
		log.Printf("unknown STRATEGY=%q; defaulting to round-robin", v)
	}

	pool := lb.NewPool(backends, strat)

	// Metrics server
	metricsAddr := os.Getenv("LB_METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":9100"
	}
	m := newMetrics()
	startMetricsServer(metricsAddr, m, pool)

	// Listener addr
	listenAddr := os.Getenv("LB_ADDR")
	if listenAddr == "" {
		listenAddr = ":9090"
	}

	if err := listenAndProxy(listenAddr, pool, m); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

func listenAndProxy(addr string, pool *lb.Pool, m *metrics) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Println("Started listening on", addr)

	// signal-aware context for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := loadProxyConfigFromEnv()

	// concurrency cap
	sem := make(chan struct{}, cfg.maxInFlight)

	// track handler goroutines for drain
	var wg sync.WaitGroup

	// close listener on signal to unblock Accept
	go func() {
		<-ctx.Done()
		log.Println("shutdown signal received; closing listener...")
		_ = ln.Close() // triggers net.ErrClosed in Accept loop
	}()

	// Accept loop
	if err := serveLoop(ctx, ln, pool, cfg, sem, &wg, m); err != nil {
		return err
	}

	// Graceful drain
	return drain(&wg, cfg.grace, sem)
}

// Accept/backoff and per-connection handlers.
func serveLoop(ctx context.Context, ln net.Listener, pool *lb.Pool, cfg proxyConfig, sem chan struct{}, wg *sync.WaitGroup, m *metrics) error {
	var tempDelay time.Duration

	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				// listener closed (shutdown)
				break
			}

			// classify error; backoff only on transient
			if isTransientAcceptErr(err) {
				m.acceptErrorsTotal.Add(1)

				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
					if tempDelay > time.Second {
						tempDelay = time.Second
					}
				}
				log.Printf("accept transient: %v; retrying in %v", err, tempDelay)

				// ctx-aware sleep
				select {
				case <-time.After(tempDelay):
				case <-ctx.Done():
					return nil
				}
				continue
			}
			// non-transient: bubble up
			return err
		}
		tempDelay = 0

		// capacity guard
		if !acquireSlot(sem) {
			_ = c.Close()
			m.capacityDroppedTotal.Add(1)
			log.Printf("capacity full (LB_MAX_INFLIGHT=%d); dropped connection", cfg.maxInFlight)
			continue
		}

		m.acceptsTotal.Add(1)

		// client socket options (client->LB)
		setClientSocketOptions(c, cfg)

		wg.Add(1)
		m.activeConns.Add(1)
		go handleConn(ctx, c, pool, cfg, sem, wg, m)
	}
	return nil
}

// Per-connection: pick backend with retries, bridge, cleanup.
func handleConn(ctx context.Context, c net.Conn, pool *lb.Pool, cfg proxyConfig, sem chan struct{}, wg *sync.WaitGroup, m *metrics) {
	defer func() { <-sem }()
	defer c.Close()
	defer wg.Done()
	defer m.activeConns.Add(-1)

	// backend dialer (base settings; timeout is enforced via DialContext)
	d := &net.Dialer{KeepAlive: cfg.kaPeriod}

	backend, release, err := dialBackendWithRetry(ctx, pool, d, cfg.maxRetries, cfg.cooldown, cfg.dialTO, m)
	if err != nil {
		// exhausted retries / no backend
		return
	}
	defer release()
	defer backend.Close()

	if tb, ok := backend.(*net.TCPConn); ok && cfg.noDelay {
		_ = tb.SetNoDelay(true)
	}

	bridge(c, backend, cfg.ioTO)
}

// Pick + dial with retries; marks failures on cooldown and clears cooldown on success.
func dialBackendWithRetry(ctx context.Context, pool *lb.Pool, d *net.Dialer, maxRetries int, cooldown time.Duration, perAttemptTO time.Duration, m *metrics) (net.Conn, func(), error) {
	var (
		backend net.Conn
		target  string
		release func()
		ok      bool
		err     error
	)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			m.retriesTotal.Add(1)
		}

		target, release, ok = pool.Acquire()
		if !ok {
			return nil, func() {}, fmt.Errorf("no healthy backends available")
		}

		m.recordPick(target)
		m.dialsTotal.Add(1)
		// ctx-aware per-attempt timeout
		dctx, cancel := context.WithTimeout(ctx, perAttemptTO)
		backend, err = d.DialContext(dctx, "tcp", target)
		cancel()

		if err == nil {
			// Success: clear any prior cooldown so it re-enters rotation immediately.
			pool.MarkSuccess(target)
			return backend, release, nil
		}

		// Fail: release LeastConn slot and mark down for cooldown
		release()
		pool.MarkDown(target, cooldown)
		m.recordDialFailure(target)
		log.Printf("dial error: target=%s err=%v (attempt %d/%d)", target, err, attempt+1, maxRetries+1)

		// If the parent ctx is cancelled (shutdown), stop retrying immediately.
		select {
		case <-ctx.Done():
			return nil, func() {}, ctx.Err()
		default:
		}
	}
	return nil, func() {}, err
}

// Graceful drain with deadline.
func drain(wg *sync.WaitGroup, grace time.Duration, sem chan struct{}) error {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		log.Println("all connections drained; exiting")
	case <-time.After(grace):
		log.Printf("grace period %v elapsed; exiting with %d active connection(s)", grace, len(sem))
	}
	return nil
}

// Non-blocking semaphore acquire.
func acquireSlot(sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Client socket options (LB side).
func setClientSocketOptions(c net.Conn, cfg proxyConfig) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(cfg.kaPeriod)
		if cfg.noDelay {
			_ = tc.SetNoDelay(true)
		}
	}
}

type proxyConfig struct {
	dialTO, ioTO, kaPeriod, grace time.Duration
	noDelay                       bool
	maxInFlight                   int
	maxRetries                    int
	cooldown                      time.Duration
}

func loadProxyConfigFromEnv() proxyConfig {
	dialTO := envMs("LB_DIAL_TIMEOUT_MS", 1500)
	ioTO := envMs("LB_IO_TIMEOUT_MS", 10000)
	noDelay := envBool("LB_TCP_NODELAY", false)
	kaPeriod := envMs("LB_TCP_KEEPALIVE_MS", 30000)
	maxRetries := envInt("LB_DIAL_RETRIES", 1)
	cooldown := envMs("LB_BACKEND_COOLDOWN_MS", 5000)
	maxInFlight := envInt("LB_MAX_INFLIGHT", 1024)
	grace := envMs("LB_SHUTDOWN_GRACE_MS", 5000)

	return proxyConfig{
		dialTO:      dialTO,
		ioTO:        ioTO,
		noDelay:     noDelay,
		kaPeriod:    kaPeriod,
		maxRetries:  maxRetries,
		cooldown:    cooldown,
		maxInFlight: maxInFlight,
		grace:       grace,
	}
}

func bridge(a, b net.Conn, idle time.Duration) {
	sa := &slidingConn{Conn: a, idle: idle}
	sb := &slidingConn{Conn: b, idle: idle}

	done := make(chan struct{}, 2)

	// a -> b
	go func() {
		_, _ = io.Copy(sb, sa)
		if t, ok := b.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
		done <- struct{}{}
	}()

	// b -> a
	go func() {
		_, _ = io.Copy(sa, sb)
		if t, ok := a.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done
}

func envBool(name string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "t", "true", "y", "yes", "on":
		return true
	case "0", "f", "false", "n", "no", "off":
		return false
	}
	return def
}

func envMs(name string, def int) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return time.Duration(def) * time.Millisecond
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return time.Duration(def) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// slidingConn enforces idle timeouts by refreshing deadlines per Read/Write.
type slidingConn struct {
	net.Conn
	idle time.Duration
}

func (s *slidingConn) Read(p []byte) (int, error) {
	if s.idle > 0 {
		_ = s.Conn.SetReadDeadline(time.Now().Add(s.idle))
	}
	return s.Conn.Read(p)
}

func (s *slidingConn) Write(p []byte) (int, error) {
	if s.idle > 0 {
		_ = s.Conn.SetWriteDeadline(time.Now().Add(s.idle))
	}
	return s.Conn.Write(p)
}

// Broad-but-safe classification without OS-specific errno checks.
func isTransientAcceptErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "too many open files") || // EMFILE/ENFILE
		strings.Contains(msg, "no buffer space") || // ENOBUFS
		strings.Contains(msg, "not enough memory") || // ENOMEM
		strings.Contains(msg, "resource temporarily") || // EAGAIN/EWOULDBLOCK
		strings.Contains(msg, "try again") ||
		strings.Contains(msg, "connection aborted") || // ECONNABORTED
		strings.Contains(msg, "protocol error")
}

func parseBackendList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
