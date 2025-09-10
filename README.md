
# Harbor — a tiny, hackable load balancer (WIP)

> Minimal TCP/HTTP load balancer written in Go. Clean, readable code meant for learning first, production second.

Harbor is a **from‑first‑principles** load balancer you can read in one sitting and extend the same day. It currently
does a simple **L4 TCP proxy** and includes a **round‑robin** picker and a pluggable **strategy** interface with a
**least‑connections** sketch. It also ships with a tiny **echo backend** so you can see traffic flow end‑to‑end.

**Status:** early WIP. Great for learning and local demos. Don’t put this in front of customers just yet.

---

## Features (current)

- **L4 proxying with full‑duplex piping** (bytes go both ways) with half‑close semantics (shutdown write after copy).
- **Atomic round‑robin** backend selection in a few lines of code.
- **In‑memory backend pool** guarded by `RWMutex`, with `Add`, `Remove`, and `NextAddr`.
- **Strategy interface** you can implement (e.g., least‑connections) without touching the pool.
- **Tiny echo server** you can run on multiple ports to simulate backends.

## Not (yet) included

- Health checks, retries, circuit breaking
- Timeouts, connection limits, graceful shutdown across goroutines
- TLS termination, HTTP header hygiene, observability/metrics

All of these are on the roadmap below—this repo is your training ground to build them.

---

## Quick start

### Prereqs
- Go 1.21+
- macOS/Linux/WSL (Windows works too, just adjust commands)

### 1) Run two echo backends
Open two terminals and run the echo server on different ports:

```bash
# Terminal A
PORT=9001 go run ./cmd/echo    # or the path where your echo main lives
# Terminal B
PORT=9002 go run ./cmd/echo
```

Each echo instance will log something like:
```
echo listening on :9001
echo listening on :9002
```

### 2) Start Harbor
From the Harbor module root, start the balancer that listens on `:9090` and forwards to `127.0.0.1:9001,9002`:

```bash
go run ./cmd/harbor   # or simply: go run .   (if main.go is the Harbor entrypoint)
```

You should see:
```
Harbor Up
listening on :9090
```

### 3) Send traffic through Harbor

```bash
# Will be distributed across backends
curl -s http://localhost:9090/
curl -s http://localhost:9090/hello
```

Sample response (varies by backend port):
```
echo GET / (served by :9001)
echo GET /hello (served by :9002)
```

If you prefer raw TCP, try:
```bash
printf "GET / HTTP/1.1\r\nHost: x\r\n\r\n" | nc localhost 9090
```

---

## Configuration (today)

For now, the backend list is a simple slice in code, something like:

```go
backends := []string{"127.0.0.1:9001", "127.0.0.1:9002"}
```

That keeps the demo friction‑free. Next steps are to expose an env var like `BACKENDS=host:port,host:port` and add
hot‑swapping via an admin endpoint or file watcher.

---

## Architecture & code tour

```
internal/lb/
  rr.go         # atomic round-robin counter
  pool.go       # thread-safe backend pool (Add/Remove/NextAddr)
  strategy.go   # pluggable Strategy interface (Pick/Start/Done/Rebuild)
  leastconn.go  # minimal least-connections sketch

cmd/echo/       # tiny HTTP echo server (used as backend)
cmd/harbor/     # Harbor entrypoint (listens on :9090, proxies to pool)
```

**Request life‑cycle (happy path):**
1. Harbor accepts a client connection on `:9090`.
2. Picks a backend (round‑robin for now) from the pool.
3. Dials the backend and **bridges** bytes in **both directions** until either side closes.
4. Cleans up both connections.

**Why it’s readable:** 
- The **bridge** is a tiny function with two goroutines and a channel to wait for completion.
- **Round‑robin** is just an `atomic.AddUint64` and a modulo, nothing fancy.
- The **pool** does not know about strategies; it just hands out indices—clean separation of concerns.

---

## Extending Harbor (your TODOs)

Here’s a realistic sequence to make this production‑ish while keeping the code tight:

1. **Time‑boxed hardening (½ day)**
   - Connection/read/write **deadlines** (avoid hung connections).
   - **Max in‑flight** (semaphore) to protect backends.
   - Basic **graceful shutdown** via context + listener close.

2. **Dynamic backends (½–1 day)**
   - `BACKENDS` env var + hot reload.
   - Health checks (TCP or HTTP), only route to healthy nodes.

3. **Strategies (1 day)**
   - Finish **LeastConn** and plug it behind a `STRATEGY` env (`rr`/`least`).
   - Track **in‑flight per backend** with `Start/Done` hooks.

4. **Observability (½ day)**
   - Prometheus counters: accepts, errors, in‑flight, bytes in/out.
   - Simple `/metrics` endpoint.

5. **HTTP polish (1–2 days)**
   - Add an **HTTP mode** using `net/http` with proper hop‑by‑hop header stripping.
   - Request/response timeouts, upstream idle pools.

6. **AI‑friendly extras (stretch)**
   - Per‑endpoint latency SLO routing, request classification (e.g., chat vs. embedding).
   - Token‑aware balancing and **adaptive concurrency**.

---

## Reality check

Harbor is deliberately **minimal**. That’s a feature: you’ll *see* every moving part and you can reason about
behavior under load. But it also means you **must** add timeouts, limits, and health checks before using it in anger.
Treat this repo like a kata: evolve it feature‑by‑feature and measure your impact.

---

## Development

Run tests (once you add them) and use `-race`:
```bash
go test ./... -race -v
```

Lint/format (suggested):
```bash
go fmt ./...
go vet ./...
```

---

## FAQ

**Q: Why store backends in a slice?**  
Simple indexing and cache‑friendly iteration. For frequent add/remove, consider a map + order array. For now, RWMutex + slice is fine.

**Q: Why atomic round‑robin instead of a mutex?**  
A single 64‑bit atomic increment scales well and keeps code honest. You can still gate selection behind pool’s `RLock` for safety.

**Q: Can it load balance raw TCP?**  
Yes—Harbor moves bytes, so HTTP, Redis, PostgreSQL, etc., all work in “just‑pipe” mode. HTTP‑aware features will come later.

---

## License

MIT (placeholder) — choose what fits your goals.

---

## Credits

Built with ❤️ for learning systems design in Go.
