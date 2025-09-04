package main

import (
	"fmt"
	"io"
	"net"
	"os"

	"github.com/vishalmahato/harbor-load-balancer/internal/lb"
)

func main() {
	fmt.Println("Harbor Up")

	backends := []string{"127.0.0.1:9001", "127.0.0.1:9002"}

	// Select initial strategy by env: RR (default) or LEAST
	var strat lb.Strategy
	switch os.Getenv("STRATEGY") {
	case "LEAST", "least", "leastconn", "least-requests":
		strat = &lb.LeastConn{}
	default:
		strat = &lb.RoundRobin{}
	}

	pool := lb.NewPool(backends, strat)

	// quick demo of picks
	for i := 0; i < 5; i++ {
		if addr, ok := pool.NextAddr(); ok {
			fmt.Printf("Request %d -> %s\n", i, addr)
		}
	}

	addr := os.Getenv("LB_ADDR")
	if addr == "" {
		addr = ":9090"
	}
	if err := listenAndProxy(addr, pool); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

// simple TCP listener that proxies bytes to a chosen backend
func listenAndProxy(addr string, pool *lb.Pool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Println("listening on", addr)

	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()

			target, release, ok := pool.Acquire()
			if !ok {
				return
			}
			defer release()

			b, err := net.Dial("tcp", target)
			if err != nil {
				fmt.Println("dial error:", err)
				return
			}
			defer b.Close()

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
