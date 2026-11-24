---
name: traffic-accounting
description: "Review AntiMage user, service and outbound traffic accounting, including quota changes, node counter collection and usage reconciliation."
---

# Traffic Accounting

Trace the collection path from `internal/app/api/node_usage_worker.go` through `internal/app/nodecontroller/usage.go` and the usage repositories. For OpenVPN, inspect `internal/app/nodeagent/openvpn_usage_collect.go`. For outbound reports, inspect `internal/app/api/outbound_traffic.go`. Identify whether each source returns snapshots, deltas or reset-on-read counters. Document units and attribution boundaries before changing aggregation.

Design fixtures for a duplicate sample, node restart, stale sample, partial collection failure and failed persistence after a reset request. Check per-user, per-service and outbound totals independently. Verify threshold boundaries and that retrying a persistence operation cannot charge usage twice. Run the relevant Go tests against disposable SQLite and MySQL instances when persistence changes. Report the sampled interval, expected totals and observed results.
