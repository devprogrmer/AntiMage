---
name: rest-api-security-review
description: "Review authentication, authorization and input validation of AntiMage REST endpoints."
---

# Rest Api Security Review

Map the master gateway and API route registration in `internal/gateway/server.go` and `internal/app/api/server.go`. Verify role, tenant or administrator scope at the handler and data-access boundary. Review upload and request limits, idempotency where relevant, redaction, and cache handling for subscriptions and credentials. Add benign local tests for unauthenticated requests, cross-scope access and valid authorized requests.
