
# Harbor — load balancer (WIP)

> TCP/HTTP load balancer written in Go. Clean, readable code meant for learning and Understanding Load Balancers.

Harbor is a **from‑first‑principles** load balancer. It currently does a simple **L4 TCP proxy**
and includes a **round‑robin** picker and a pluggable **strategy** interface with a
**least‑connections** sketch. It also ships with a tiny **echo backend** so you can see traffic flow end‑to‑end.

**Status:** WIP

---

## Features (current)

- **L4 TCP proxy** with full-duplex piping and half-closes (shutdown write after copy).
- **Atomic round‑robin** backend selection.
- **Env-driven backends:** BACKENDS=host:port,host:port… with dev seed via LB_ALLOW_DEV_SEED=1 for testing.
- **Graceful shutdown**: SIGINT/SIGTERM → close listener, drain in-flight with grace period.
- **Accept backoff** for transient errors (ctx-cancelable).
- **Per-conn timeouts** with sliding deadlines (refresh on every read/write).
- **Retry & cooldown**: failed backend is temporarily marked down; success clears cooldown.
- **Metrics endpoint** (/metrics) with basic counters/gauges.
- **Strategy interface** you can implement (e.g., least‑connections) without touching the pool.
- **Tiny echo server** you can run on multiple ports to simulate backends. for testing purpose

## Not (yet) included

- TLS termination, HTTP header hygiene, observability/metrics

All of these are on the roadmap below—this repo is your training ground to build them.

---


## FAQ

**Q: Why store backends in a slice?**  
Simple indexing and cache‑friendly iteration. For frequent add/remove, consider a map + order array. For now, RWMutex + slice is fine.

**Q: Why atomic round‑robin instead of a mutex?**  
A single 64‑bit atomic increment scales well and keeps code honest. You can still gate selection behind pool’s `RLock` for safety.

**Q: Can it load balance raw TCP?**  
Yes—Harbor moves bytes, so HTTP, Redis, PostgreSQL, etc., all work in “just‑pipe” mode. HTTP‑aware features will come later.


