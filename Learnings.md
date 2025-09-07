# Learnings

## 2025-09-045 — Daily Journal
### Key Learnings 

-CP Keep-Alive (kernel feature):
Sends tiny probes on idle TCP connections. If repeated probes get no ACK, the OS marks the socket dead; your next read/write returns an error instead of hanging. Also keeps NAT/firewall state warm. Tuned via SetKeepAlive(true) + SetKeepAlivePeriod(d) (accepted sockets) and net.Dialer{KeepAlive:d} (outbound).
-Nagle’s algorithm (throughput optimizer):
Batches many small writes into fewer, larger TCP segments to reduce packet overhead. This can add small, variable delays (tens of ms) when you do tiny, chatty writes.
-Nagle’s algorithm (throughput optimizer):
Batches many small writes into fewer, larger TCP segments to reduce packet overhead. This can add small, variable delays (tens of ms) when you do tiny, chatty writes.

## 2025-09-04 — Daily Journal
###  Key Learnings
-Caddy, Traefik, most Go servers. One accept loop + goroutine per connection. Simple, scales far.
-NGINX (multiple worker processes), HAProxy & Envoy (multiple worker threads). Think several hosts or several doors.
- My Harbor goes in line with caddy and traefik , I have one listener and for each connection i create a go routine  and have only a accept loop
-If you spawn a goroutine per connection, clients are served in parallel
