---
name: grpc-contract-review
description: "Review AntiMage node gRPC contracts, generated bindings and compatibility between master and node."
---

# Grpc Contract Review

Read the existing schema under `internal/proto/node/v1/`, the node client in `internal/app/nodeclient/` and the node agent in `internal/app/nodeagent/`. Preserve field numbers and reserved tags, use the existing generator, and verify old and new client/server combinations. Review cancellation, deadlines, authenticated TLS and safe retry semantics. Exercise contract changes with local loopback tests.
