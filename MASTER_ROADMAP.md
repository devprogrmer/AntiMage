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

eac6ccfa adds metadata-only IPv4/IPv6 TCP/UDP decoding. c60f1324 adds explicit AF_PACKET capture on one interface and an opt-in real TCP/UDP loopback test. All seven package tests passed with the Go race detector in a Linux container using 2 CPUs and 2 GiB RAM, external networking disabled and CAP_NET_RAW. This validates capture and core tests, not per-user live attribution or performance.

NEXT_ACTION is to establish a capture lifecycle barrier around every WireGuard syncconf, targeted mutation, quota removal and runtime shutdown. Close old sockets before ownership changes, then verify applied kernel AllowedIPs against managed peer identity and publish a fresh generation. Desired policy and usage-helper files are written before runtime activation and cannot independently authorize ownership. Receipt timestamps are userspace timestamps and cannot prove packet capture-time ownership. Resolve duplicate observation points, kernel drop reporting and queued download ambiguity before counting per-user traffic. Keep Xray destination observations Unattributed until a validated authenticated-session source exists. Stages 3 through 15 remain pending. No release artifacts, update commands or production readiness claims are available.

Stage 2 must correlate verified peer ownership with node, interface, address and lifecycle generation. Ambiguous observations remain Unattributed. Xray OnlineMap supplies presence and requires an additional validated source for destination attribution. Production changes require explicit approval.

Stage 2 continuation commits are 3532a82d (session capture adapter), 5fd2190a (bounded socket buffer and observation drop diagnostics), 548eac12 (exact kernel peer snapshot verifier) and fb74e3c0 (real TCP/UDP adapter test). All nine networkintelligence tests passed under Linux race detection in an isolated network namespace with CAP_NET_RAW, 2 CPUs and 2 GiB RAM. The real adapter fixture verifies endpoints and binding identity with supplied bindings. It does not prove live WireGuard lifecycle ownership, precise packet counts or performance. Targeted verifier tests passed on Windows. Actual runtime hooks remain the next integration step. No final release acceptance criteria have been relaxed.

Latest continuation adds 7b7a9371 (synchronous socket readiness), 430ba05c and 59a4f524 (bounded TCP ACK timing estimator, real TCP test and core benchmarks), and fc7a0969 (default-disabled capture lifecycle and WireGuard apply/removal/disconnect hooks). All 16 networkintelligence tests passed with Linux race detection, including real capture. Targeted Linux Node Agent WireGuard and lifecycle regression tests passed in 6.573 seconds. The 2 CPU/2 GiB container measured estimator window cycles at 738 ns for 16 segments, 3506 ns for 64, 26469 ns for 256 and 306168 ns for 1024. These are estimator microbenchmarks and do not establish VPN forwarding overhead or deployment capacity.

NEXT_ACTION now requires initializing the node monitoring manager behind a default-off node flag, obtaining applied interface identity and exact kernel peers after successful runtime application, and publishing verified captures. Lifecycle hooks already revoke before apply/removal/disconnect. Complete real WireGuard reassignment tests, collector telemetry and panel integration before Stage 2 acceptance. TCP ACK timing core is implemented, while per-flow wiring, distributions and user-facing measurement labels remain incomplete. PR 147 remains Draft.

33c704ad implements default-off node activation after verified WireGuard apply and bounded telemetry through additive MetricsResponse field 6. The full-access GET /api/network-intelligence/node?node_id endpoint reads snapshots without runtime reconciliation. Node snapshots omit session identities and cap responses at 250 flows. Targeted node activation, API authorization, configuration and proxy tests passed. The networkintelligence package tests passed. 12877f59 adds bounded RTT distributions and 8bd25afd adds a versioned evidence-based signature engine; both still need capture/panel integration and trusted signature provisioning.

NEXT_ACTION is real WireGuard lifecycle/reassignment validation of activation in Linux, end-to-end telemetry tests with old-agent compatibility, and integration of captured TCP quality into the bounded snapshots. Dashboard, diagnostics, historical storage, optimization, resource benchmarks and final release acceptance remain incomplete. Do not mark PR ready or supply production update commands yet.

Latest verified continuation through d9234272 includes per-flow split-leg TCP quality and capture-gap invalidation (99b6fb9f,12a718f5), evidence-only diagnostics (0b2c8687,b5245ce3), explicit signature loading/classification (1e4ee192), real WireGuard and actual Node Agent same-IP reassignment plus simultaneous multi-interface account tests (6493345a,83266c85,0a519f0c,a877814f), resource fixture report (db9ea3bf), observation-rate circuit breaker (9736d567), and live full-access dashboard (d9234272). All collector tests and targeted WireGuard regressions passed in isolated Linux race runs. The actual Node Agent multi-interface publication fixture passed race detection. Dashboard production build and TypeScript passed; all 121 Vitest tests passed with threads disabled. Three Playwright UI tests passed using intercepted API fixtures, validating rendering/access/error behavior rather than a real full-stack telemetry path.

CI failure diagnosis found TestAdminSessionLoginBehindReverseProxy used httptest's untrusted default peer. 237f5415 explicitly models the trusted loopback reverse proxy and preserves the successful-login assertion and separate untrusted-header rejection tests. Remote CI must be rerun after push. Resource fixture results and limitations are in docs/network-intelligence-resource-fixture.md. The provisional node observation ceiling is 10000 packets/second/interface, with no claim of production capacity. On budget exhaustion capture closes and revokes its flow generation.

NEXT_ACTION is to inspect new CI results, validate real overlapping-node and delayed active-flow reassignment, and connect full-stack telemetry/browser acceptance. History, alerts, infrastructure and tunnel diagnostics, permitted optimization/audit/rollback, trusted application database distribution, protocol-aware UDP timing, full staging and release readiness remain incomplete. The signature engine currently requires an administrator-supplied verified database and cannot claim built-in game coverage. PR 147 remains Draft. No final update commands are validated.

0af5b73a fixes the actual dashboard CI hook-dependency errors. Local lint has zero errors and two existing warnings; all 121 dashboard tests passed again with threads disabled. The failed build/dashboard runs 45944611689 and 45944611755 require a new push to revalidate this fix. 83bbd50d adds bounded volatile per-user quality history/alerts with strict timestamps, retention and reset-safe deltas. It is not yet wired to storage/API/charts and does not persist through restart. 454a4a08 and 44838e66 add cached read-only Linux fragmentation/reassembly/softnet/interface counters as interval rates in node telemetry. Windows delta/targeted activation tests passed; Linux parser execution still remains. First samples and counter resets have no rates.

NEXT_ACTION is to run Linux infrastructure parsers and latest CI, then wire bounded history to actual sample aggregation and authorized charts. Durable history/notification delivery, GRE/path probes, permitted MSS optimization with audit/rollback, overlapping-node and delayed-packet acceptance, full regression/staging and release remain pending. Preserve Draft status.
