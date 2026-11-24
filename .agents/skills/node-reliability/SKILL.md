---
name: node-reliability
description: "Review AntiMage node enrollment, health, recovery, runtime synchronization and Go worker reliability."
---

# Node Reliability

Start with `internal/app/api/node_recovery_worker.go`, `internal/app/api/node_operations_worker.go`, `internal/app/nodecontroller/controller.go` and `internal/app/nodeclient/tls.go`. Trace the transitions between reported health, desired configuration and observed runtime. Identify which operation owns retry and reconciliation. Preserve cancellation, timeouts, bounded work and authenticated TLS.

Exercise startup, transient disconnection, stale health, unsuccessful reconciliation and reconnect in a local fixture. Check that one unhealthy node cannot stall the fleet and that repeating the same operation converges without duplicate side effects. Report the transition, retry semantics and recovery evidence.
