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
	"sync/atomic" // NEW: metrics atomics
	"syscall"
	"time"

	"github.com/vishalmahato/harbor-load-balancer/internal/lb"
)

func main() {
	fmt.Println("Harbor up ")

	// DEV SEED: keep for now, but gate it.
	backends := []string{"127.0.0.1:9001", "127.0.0.1:9002"}
	fmt.Println("backends", backends)

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

	//  metrics server
	metricsAddr := os.Getenv("LB_METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":9100"
	}
	m := newMetrics()
	startMetricsServer(metricsAddr, m, pool)

	listenAddr := os.Getenv("LB_ADDR")
	if listenAddr == "" {
		listenAddr = ":9090"
	}

	if err := listenAndProxy(listenAddr, pool, m); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

// Listens and dials the backend -- needs update to handle faulty backend connections or when accept fail as the current code make lb stops when accept fails --- done
// to solve it we will intentnaly close and do a clean shutdown
///in case of timeout or temporary error we sleep and retry
// for non temporary err - return
// so whats the role of delay here - without delay the for{accept()} can run thousand of times in seconds - spamming logs and making a hot loop
// exponentially increase the time and stop at 1 second
// on next successfull accept we will reset the delay to 0

func listenAndProxy(addr string, pool *lb.Pool, m *metrics) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Println("Started listening on", addr)

	// set up signal-aware context for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := loadProxyConfigFromEnv()

	// concurrency cap to restrict a limit of users
	sem := make(chan struct{}, cfg.maxInFlight)

	//  tracks in-flight handler goroutines so we can Wait() on shutdown
	var wg sync.WaitGroup

	//  on signal, close the listener to unblock Accept() immediately
	go func() {
		<-ctx.Done()
		log.Println("shutdown signal received; closing listener...")
		_ = ln.Close() // notee : triggers net.ErrClosed in Accept loop
	}()

	// Run the accept loop
	if err := serveLoop(ctx, ln, pool, cfg, sem, &wg, m); err != nil { // NEW: pass metrics
		return err
	}

	// Graceful drain with a deadline
	return drain(&wg, cfg.grace, sem)
}

// serveLoop owns Accept/backoff and spawns per-connection handlers.
func serveLoop(ctx context.Context, ln net.Listener, pool *lb.Pool, cfg proxyConfig, sem chan struct{}, wg *sync.WaitGroup, m *metrics) error { // NEW: metrics
	var tempDelay time.Duration // exponential backoff on temporary/timeout errors e.g., OS under pressure, backlog/buffer/fd limits

	for {
		c, err := ln.Accept()
		if err != nil {
			// listener closed (e.g., during shutdown)
			if errors.Is(err, net.ErrClosed) {
				//  break to drain in-flight connections
				break
			}

			atomic.AddInt64(&m.acceptErrorsTotal, 1)

			if tempDelay == 0 {
				tempDelay = 5 * time.Millisecond
			} else {
				tempDelay *= 2
				if tempDelay > time.Second {
					tempDelay = time.Second
				}
			}
			log.Printf("accept error: %v; retrying in %v", err, tempDelay)
			time.Sleep(tempDelay)
			continue
		}
		tempDelay = 0

		//  concurrency cap (semaphore)
		if !acquireSlot(sem) {
			_ = c.Close()

			// metrics — capacity drop
			atomic.AddInt64(&m.capacityDroppedTotal, 1)

			log.Printf("capacity full (LB_MAX_INFLIGHT=%d); dropped connection", cfg.maxInFlight)
			continue
		}

		// metrics — successful accept
		atomic.AddInt64(&m.acceptsTotal, 1)

		// Enable TCP keepalive on the accepted client socket -- client to LB
		setClientSocketOptions(c, cfg)

		wg.Add(1)
		atomic.AddInt64(&m.activeConns, 1)      // NEW: metrics — gauge inc
		go handleConn(c, pool, cfg, sem, wg, m) // NEW: pass metrics
	}
	return nil
}

// handleConn does per-connection work: retry dial, bridge, cleanup.
func handleConn(c net.Conn, pool *lb.Pool, cfg proxyConfig, sem chan struct{}, wg *sync.WaitGroup, m *metrics) { // NEW: metrics
	// release capacity slot and close client when done
	defer func() { <-sem }()
	defer c.Close()
	//  mark handler complete for shutdown drain
	defer wg.Done()
	defer atomic.AddInt64(&m.activeConns, -1) // metrics — gauge dec

	// dialer with timeout and keepalive for the backend side
	d := &net.Dialer{Timeout: cfg.dialTO, KeepAlive: cfg.kaPeriod}

	backend, release, err := dialBackendWithRetry(pool, d, cfg.maxRetries, cfg.cooldown, m) // NEW: pass metrics
	if err != nil {
		// exhausted retries or no healthy backends
		return
	}

	defer release()
	defer backend.Close()

	if tb, ok := backend.(*net.TCPConn); ok && cfg.noDelay {
		_ = tb.SetNoDelay(true)
	}

	bridge(c, backend, cfg.ioTO)
}

// dialBackendWithRetry selects a backend and dials it, retrying on failures.
// Uses pool.Acquire()/release() and MarkDown(cooldown) on failures.
func dialBackendWithRetry(pool *lb.Pool, d *net.Dialer, maxRetries int, cooldown time.Duration, m *metrics) (net.Conn, func(), error) { // NEW: metrics
	var (
		backend net.Conn
		target  string
		release func()
		ok      bool
		err     error
	)

	//  bounded retry loop across backends
	for attempt := 0; attempt <= maxRetries; attempt++ {

		if attempt > 0 {
			atomic.AddInt64(&m.retriesTotal, 1) //metrics — retry count
		}

		target, release, ok = pool.Acquire()
		if !ok {
			// no healthy backends available -- note:  Acquire skips those in cooldown
			return nil, func() {}, fmt.Errorf("no healthy backends available")
		}

		//  metrics — note pick & total dials
		m.recordPick(target)
		atomic.AddInt64(&m.dialsTotal, 1)

		backend, err = d.Dial("tcp", target)
		if err == nil {
			//  break out and proceed with bridge
			return backend, release, nil
		}

		// dial failed - we will release LeastConn slot immediately
		release()
		// mark the backend down for a short cooldown so Acquire() skips it next time
		pool.MarkDown(target, cooldown)

		// NEW: metrics — per-backend failure & total failures
		m.recordDialFailure(target)

		log.Printf("dial error: target=%s err=%v (attempt %d/%d)", target, err, attempt+1, maxRetries+1)
		continue
	}
	// if still error persists return otherwise we have to release
	return nil, func() {}, err
}

// drain waits for in-flight handlers or times out after grace.
func drain(wg *sync.WaitGroup, grace time.Duration, sem chan struct{}) error {
	// graceful drain — wait for in-flight handlers or time out
	done := make(chan struct{})
	go func() {
		wg.Wait() // waits for all handler goroutines to finish
		close(done)
	}()

	select {
	case <-done:
		log.Println("all connections drained; exiting")
	case <-time.After(grace):
		// We can't force-close individual conns here without tracking them,
		// but we at least report how many are still active (len(sem)).
		log.Printf("grace period %v elapsed; exiting with %d active connection(s)", grace, len(sem))
	}
	return nil
}

// acquireSlot tries a non-blocking semaphore acquire (fast-fail when full).
func acquireSlot(sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		// acquired capacity — proceed
		return true
	default:
		return false
	}
}

// Enable TCP keepalive on  client socket
func setClientSocketOptions(c net.Conn, cfg proxyConfig) {

	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(cfg.kaPeriod)
		if cfg.noDelay {
			_ = tc.SetNoDelay(true) //  controlled by LB_TCP_NODELAY
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

// loadProxyConfigFromEnv
func loadProxyConfigFromEnv() proxyConfig {
	dialTO := envMs("LB_DIAL_TIMEOUT_MS", 1500)
	ioTO := envMs("LB_IO_TIMEOUT_MS", 10000)
	noDelay := envBool("LB_TCP_NODELAY", false)
	kaPeriod := envMs("LB_TCP_KEEPALIVE_MS", 30000)
	maxRetries := envInt("LB_DIAL_RETRIES", 1)        // try up to 1 additional backend by default
	cooldown := envMs("LB_BACKEND_COOLDOWN_MS", 5000) // mark failing backend "down" for 5s
	maxInFlight := envInt("LB_MAX_INFLIGHT", 1024)    // 1024 concurent connection
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
		//if type of b is not net.TCPconn we can't call CloseWrite(),
		// can't half close , it will be closed fully once done
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

// to read bool env vars
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
	if err != nil || n < 0 { // allow 0 to mean "disable"
		return time.Duration(def) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond // returns 0 if n==0 -> “no idle timeout”
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

// slidingConn bumps deadlines on every Read/Write, implementing an idle timeout.
// If idle <= 0, no deadlines are set (i.e., no idle timeout).
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
	if errors.As(err, &ne) && (ne.Timeout()) {
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
