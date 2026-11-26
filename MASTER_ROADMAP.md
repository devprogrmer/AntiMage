# Anti-Mage Network Intelligence

Base commit is c1a35e44dd644c2a41bc99a4987d613e4d8b79fc. Development branch is feature/network-intelligence-dev. Previous commits 30541f4d and 008d012e remain on feature/network-intelligence.

| Stage | Status | Acceptance remaining |
| --- | --- | --- |
| 0 Audit | Partial | Refresh audit and baseline on the new base |
| 1 Presence and flags | Implemented, targeted tests passed | New-base Linux full regression remains |
| 2 TCP/UDP flow tracking | Attribution core implemented | Real collector, node integration, Linux traffic and resource measurements remain |
| 3 TCP quality | Pending | Validated ACK RTT, retransmission and duplicate ACK fixtures |
| 4 Application detection | Pending | Versioned evidence and confidence with Unknown fallback |
| 5 UDP quality | Pending | Supported protocol measurements and explicit unavailable metrics |
| 6 Infrastructure | Pending | Counter deltas, tunnel and node telemetry |
| 7 Live dashboard | Pending | Existing theme, permissions, responsive UI and real telemetry |
| 8 User details | Pending | Multiple sessions, destinations and valid quality history |
| 9 Diagnostics | Pending | Bounded read-only operations and evidence |
| 10 Optimization | Pending | Authorized scoped changes, audit and tested rollback |
| 11 Alerts/history | Pending | Bounded retention and evidence-backed alerts |
| 12 Security/regression | Pending | API/gRPC, database, race and failure tests |
| 13 Performance | Pending | Feature off/on measurements on 2 CPU and 2 GiB with actual flow and packet rates |
| 14 Staging | Pending | Isolated full-stack traffic and rollback validation |
| 15 Release | Pending | Verified builds, checksums, update and rollback commands |

Presence cherry-pick completed as cb205472. Configuration, origin and Presence tests passed. b9395e56 adds explicit opt-in and proxy configuration regression coverage. d5908cad introduces bounded attribution storage. 8fe51d43 adds replay high-water retention and collector epochs. 2b51e248 enforces forwarding header trust at API ingress. Trusted loopback proxies remain supported and must overwrite incoming forwarding headers.

The attribution package tests pass. Windows race execution is unavailable with CGO disabled. High-water entries consume bounded capacity until rebinding, and admission stops when capacity is reached. There is no packet collector or RPC integration yet. No Stage 2 real-traffic or feature off/on performance result exists.

NEXT_ACTION is to integrate verified WireGuard runtime peer bindings with an isolated Linux metadata collector, exercise ownership changes with real packets and measure resource limits. Keep Xray destination observations Unattributed until a validated authenticated-session source exists. Stages 3 through 15 remain pending. No release artifacts, update commands or production readiness claims are available.

Stage 2 must correlate verified peer ownership with node, interface, address and lifecycle generation. Ambiguous observations remain Unattributed. Xray OnlineMap supplies presence and requires an additional validated source for destination attribution. Production changes require explicit approval.
