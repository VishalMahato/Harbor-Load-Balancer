# Learnings



## 2025-09-04 — Daily Journal
###  Key Learnings
-Caddy, Traefik, most Go servers. One accept loop + goroutine per connection. Simple, scales far.
-NGINX (multiple worker processes), HAProxy & Envoy (multiple worker threads). Think several hosts or several doors.
- My Harbor goes in line with caddy and traefik , I have one listener and for each connection i create a go routine  and have only a accept loop

