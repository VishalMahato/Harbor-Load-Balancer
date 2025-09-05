package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
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

	// real proxy
	listenAddr := os.Getenv("LB_ADDR")
	if listenAddr == "" {
		listenAddr = ":9090"
	}
	//start listenming
	if err := listenAndProxy(listenAddr, pool); err != nil {
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

func listenAndProxy(addr string, pool *lb.Pool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Println("Started listening on", addr)

	dialTO := envMs("LB_DIAL_TIMEOUT_MS", 1500) // default 1.5s
	ioTO := envMs("LB_IO_TIMEOUT_MS", 10000)    // default 10s (v0 single deadline)

	var tempDelay time.Duration // exponential backoff on temporary/timeout errors e.g., OS under pressure, backlog/buffer/fd limits

	for {
		c, err := ln.Accept()
		if err != nil {
			// listener closed (e.g., during shutdown)
			if errors.Is(err, net.ErrClosed) {
				return nil
				// gracefully shuting it down
			}
			// if you ever set ln.(*net.TCPListener).SetDeadline(...),
			// treat deadline expirations as retryable
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				log.Println("accept timeout:", err)
				// backoff to avoid hot loop during bursts
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
					if tempDelay > time.Second {
						tempDelay = time.Second
					}
				}
				time.Sleep(tempDelay)
				continue
			}
			// otherwise: log and bail (or `continue` if you prefer)
			return err
		}
		tempDelay = 0

		go func(c net.Conn) {
			defer c.Close()
			target, release, ok := pool.Acquire()
			if !ok {
				return
			}
			defer release()

			// dial with timeout so dead/blackholed backends fail fast
			b, err := net.DialTimeout("tcp", target, dialTO)
			if err != nil {
				log.Println("dial error:", err)
				return
			}
			defer b.Close()

			// v0: one deadline to prevent zombies (we'll upgrade to sliding later)
			deadline := time.Now().Add(ioTO)
			_ = c.SetDeadline(deadline)
			_ = b.SetDeadline(deadline)

			bridge(c, b)
		}(c)
	}
}

func bridge(a, b net.Conn) {
	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(a, b)
		if t, ok := a.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(b, a)
		if t, ok := b.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done
}

// read millisecond env var with a sane default
func envMs(name string, def int) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return time.Duration(def) * time.Millisecond
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return time.Duration(def) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}
