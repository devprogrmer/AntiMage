---
name: gaming-network-optimization
description: "Evaluate AntiMage routing latency, jitter, loss and throughput for gaming traffic using controlled comparisons."
---

# Gaming Network Optimization

Review `internal/app/nodeagent/outbound_latency.go`, `route_probe.go` and `route_stats.go` in the same directory. Identify probe protocol, target, interface and timeout. Compare direct and proxied paths using the same authorized endpoint and sampling window. Record distribution and sample count for RTT, jitter and loss alongside throughput and resource use. Isolate DNS setup time from sustained transport latency. Use an isolated client and change one variable per run. Report settings, raw observations and the rollback for any proposed change.
