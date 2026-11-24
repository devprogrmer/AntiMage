---
name: local-load-testing
description: "Design bounded local load tests for AntiMage API and node usage pipelines."
---

# Local Load Testing

Identify the read path in `internal/app/api/server.go` and the background usage collectors. Use a disposable single-node setup with synthetic users. Separate request latency from the collection and persistence backlog. Dry-run the workload, start with one client, use explicit duration and error abort limits, and measure latency percentiles and queue recovery. Keep supplied credentials out of generated reports.
