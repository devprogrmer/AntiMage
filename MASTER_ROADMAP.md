# Anti-Mage Network Intelligence

## Merge-gate verification in progress — 2026-11-28

Inspected PR head 12ec9ea1 and its actual Linux logs, not just check colors:
Network Intelligence run 46145078044 passed raw UDP capture, real WireGuard
publication/reassignment, two-node overlapping addresses, GRE fault injection,
real WireGuard-to-browser history and audited optimization rollback. The 2-vCPU/
2-GiB forwarding fixture measured median OFF/ON 570.680/569.177 Mbps (-0.263%),
agent CPU 1.293/7.070 ms, zero echo-loss delta and exact 10000-packet observation
in ON trials. These results supersede earlier uninspected-Linux/benchmark notes.
Database, protocol, build and browser checks were also successful on that head.

The newly added subprocess/binary/browser recovery tests were skipped in that
Linux job because opt-in binaries/env were absent. The dashboard-browser job now
builds pinned baseline/current panel/node binaries and a race-enabled helper,
then explicitly executes those recovery tests on Linux and retains logs and
source/checksum evidence. New workflow scripts are not a pass until CI executes.

Tracked dev workflows publish build images/artifacts/manifests and GitHub Pages;
no direct operational-server SSH/deployment step was found. External deployment
hooks/watchers are outside this source inspection. No Production action is
authorized or performed. Keep the PR Draft until the new-head merge gates pass.
NEXT_ACTION: inspect the new Linux recovery job, fix reproduced failures, and
reconcile remaining acceptance criteria before Ready/Merge. No admin bypass.

## Delivery instruction — 2026-11-28

User explicitly authorized committing/pushing all accumulated Network
Intelligence changes to feature/network-intelligence-dev / PR 147. This
supersedes earlier local-only delivery holds below, but does not authorize
Production deployment or imply release readiness. Final pre-commit module,
API Network and focused Node Agent tests passed. Existing local integration,
browser, backup and binary rollback evidence remains documented below.
NEXT_ACTION after push: validate CI for the new head, retain Draft/release gates,
and complete outstanding disposable Linux WireGuard/full-staging acceptance.
Exclude the unrelated pre-existing native-session-outbox and ignored test
artifacts. No merge or Production operation is included in this delivery.

## Newest continuation: local staging recovery — 2026-11-28

Local-only item 4 work; no commit/push, CI inspection, merge or Production action.
Added and ran real subprocess/RPC/backup/binary/browser recovery tests:

- Node process termination rejects telemetry; reconnect on the same mTLS
  endpoint/client works. Actual stalled RPC returns DeadlineExceeded. Reused IP
  owner/peer and changed IP/peer publish only fresh owner counters/UDP quality;
  old unsolicited replies are rejected. Empty restart does not resurrect flows.
- Actual current node binary → archived HEAD baseline node with flags off →
  current binary passed on the same temporary data directory/mTLS endpoint.
- Full panel backup/restore uses four independent processes (prepare, restore,
  disabled rollback, re-enable); owned config flags, accounting and scoped
  migration-95 history survive. The previous same-process attempt exposed the
  process-scoped closed DB cache; the test now uses actual process restarts,
  without changing DB pool behavior or weakening an assertion.
- Actual current panel binary → baseline flags off → baseline history enabled →
  current passed. HTTP authentication, scoped history and accounting remained
  correct across versions. Baseline is 1c1dd80f693d5ea760a088241e2705e5be49420b,
  built from git archive; no checkout switch. Explicit -buildvcs=false was used
  after automatic VCS metadata lookup failed, with SHA/checksums recorded.
- Real authenticated API/built-dashboard browser recovery passed after owned
  node process death, actual RPC timeout, reconnect, IP reassignment and empty
  restart. Stale flows disappeared on 503; the replacement account was isolated;
  recovery restored fresh rows. No API/Node response mocks or browser page errors.

Tests: ProcessRecoveryAndVersionRollback 8.92s; PanelRestartBackupAndFlagRollback
0.90s; PanelBinaryVersionRollback 2.08s; BrowserRecoversFromNodeFailures 14.82s.
API Network regression, focused agent tests, existing backup interrupted-restore
and DB/file rollback tests passed; Go vet passed. Linux amd64 API/agent test
binaries cross-compiled (runtime execution not implied). Full checksums and local
reproduction: docs/network-intelligence-local-recovery.md. Test binaries/source
archives live under ignored artifacts/ni-recovery-1c1dd80f.

Boundary: portable socket/session adapters and Windows binaries are tested;
live kernel WireGuard ownership changes, routed tunnel continuity, Linux
systemd update/rollback, full multi-node staging and MySQL/MariaDB backup recovery
still need a suitable disposable runtime. No claim that Stage 14 or the entire
item 4 is release-complete. No production configuration or TC rule changed.

NEXT SESSION CONTINUATION (newest): preserve all local changes, await explicit
push approval. Do not repeat verified local process/browser/binary recovery
without a change. Remaining acceptance: disposable Linux WireGuard/kernel/tunnel
recovery and full multi-node staging; then final release/security/database and
update/rollback validation. Preserve unrelated untracked native-session-outbox.

## Latest continuation: local panel/Diagnostics — 2026-11-28

User reports existing remote CI green. Local item 3 was authorized; no remote
CI inspection, commit, push, merge or deployment was performed.

Fixed a missing connection: Diagnostics consumed TCP quality only. It now
consumes fresh supported UDP transaction RTT/variation, rejects invalid, stale,
future, absent and Source-query timing, and reports observations without
inventing loss, in-game ping or a definitive cause. Responses add optional
interface/source/protocol fields; the panel displays them and uses full flow
identity for finding keys so shared destinations do not mix sessions.

Verified `TestNetworkIntelligenceLocalTrafficToAuthenticatedPanel` and child
`TestNetworkLiveBrowserAgainstLocalNode`: actual local UDP/TCP socket I/O feeds
the decoder/session test adapter; the real Node Agent serves mTLS gRPC and the
real SQLite-backed authenticated API serves the built dashboard. No Metrics
response or browser API route is mocked. Four flows, two users and three session
interfaces sharing one UDP destination remained separate. The delayed session
had mean UDP transaction RTT 300.41645 ms and variation 99.6395 ms; only it
produced high_udp_transaction_rtt and variable_udp_transaction_rtt. Browser
assertions verified values, samples, evidence and diagnostic flow identity.
Unknown UDP quality and unobserved TCP ACK timing remained unavailable; UDP
loss stayed null. Anonymous/restricted authenticated requests returned 401/403.
Private bodies/session identifiers were absent; no browser page errors.

Other checks passed: full networkintelligence suite, focused agent tests, API
Network regression (including real mTLS history fixture), Go vet module/API,
production dashboard build, service Vitest and all nine Network Intelligence
Playwright regressions. Lint passed with two existing unrelated warnings;
build retained existing CSS/chunk warnings. UDP Diagnostic tests cover invalid
values/protocols/methods/times/sample counts and both measurement directions.

Boundary: the portable test uses application-socket receive times with test
IP/TCP headers and bindings. It does NOT exercise Linux raw capture, WireGuard
peer publication, actual TCP ACK timing or GRE faults. Docker Linux daemon was
unavailable; WSL inventory showed only docker-desktop. No service was started
or host network changed. Full item 3 kernel/WireGuard-quality/fault-to-dashboard
acceptance remains pending; no stage is declared release-complete from this.

NEXT SESSION CONTINUATION (newest): retain all changes locally until explicit
push approval. Local socket→agent→mTLS→API→browser acceptance has passed; do not
repeat without changes. Remaining: disposable Linux capture/WireGuard quality
and fault-to-dashboard validation, then staging recovery/release gates. Preserve
untracked native-session-outbox. Reproduction/bounds are documented in
docs/network-intelligence-local-panel-validation.md.

## Local-only continuation — 2026-11-28

User explicitly requested application/UDP work locally, with no push until a
later instruction. No commit, push, CI inspection, merge or Production action
was performed in this continuation. Existing remote head remains unchanged.

Stages 4/5: added bounded paired STUN, Valve **direct** GameNetworkingSockets
challenge and Discord Voice discovery parsing/correlation; full Source query
GameIDs use the existing signature engine's versioned v2 catalogue (CS2/Dota 2
server-query scope). Shared transports never establish the game executable.
Port-only/unknown/truncated/conflicting evidence remains Unknown. UDP RTT and
variation require fresh unambiguous transactions; Packet Loss remains null/N/A.
Retries, duplicate replies, reordering, capture gaps and lease replacement are
covered. No extra worker or forwarding/configuration operation was added.
Node telemetry and the existing dashboard expose method, samples, evidence,
revision, timestamps and limitations. TCP ACK history semantics are unchanged.

Verified locally: full networkintelligence tests including real UDP loopback;
Node Agent NetworkIntelligence/NetworkApplication tests; Go vet; dashboard
production build and TypeScript check; service Vitest; lint (two pre-existing
warnings); all nine Network Intelligence Playwright scenarios using installed
Chrome, including UDP evidence/query/Unknown rendering and existing permissions,
history, optimization/rollback and diagnostics UI scenarios. Fuzz parser passed
86,556 executions over 11 seconds. Unknown-header parser benchmark: 3.337 ns/op,
0 B/op, 0 allocs/op on this Windows i5-13400F; this is not a forwarding benchmark.
Linux amd64/arm64 networkintelligence test binaries cross-compiled.

Failures/limits: normal restricted loopback timed out; the same test passed via
authorized local execution. Initial Playwright attempts encountered restricted
loopback, an owned prior test's occupied port, and missing bundled Chromium;
after closing that test and using installed Chrome, all nine scenarios passed.
The new UI test initially selected exact text from a cell that also contained
the sample count; corrected to assert the complete cell RTT AND sample count.
Race could not execute because CGO and a C compiler are unavailable. The added
Linux raw-capture UDP test requires the disposable guest marker and was compiled,
not executed here. Real game clients/SDR, full WireGuard-to-dashboard E2E for
these protocol measurements and OFF/ON forwarding overhead remain unverified.
Neither Stage 4 nor Stage 5 is declared release-complete from local fixtures.

NEXT SESSION CONTINUATION (latest): retain this local working tree; do not push
or inspect CI unless the user explicitly resumes those actions. On authorization,
run the new raw UDP capture test and race/forwarding suites in the disposable
Linux guest; validate supported real-client exchanges and catalogue evidence.
Do not name an encrypted/shared application without proof. See
docs/network-intelligence-udp-evidence.md for supported measurements and limits.
Preserve the unrelated untracked native-session-outbox directory.

Base commit is c1a35e44dd644c2a41bc99a4987d613e4d8b79fc. Development branch is feature/network-intelligence-dev. Previous commits 30541f4d and 008d012e remain on feature/network-intelligence.

| Stage | Verified progress | Release acceptance still required |
| --- | --- | --- |
| 0 Audit | Base and existing runtime contracts reviewed | Check current branch against dev and record final production compatibility risks |
| 1 Presence and flags | Feature-gated API and authorization tests | Verify flags off/on and shutdown in full-stack staging |
| 2 TCP/UDP tracking | Encrypted WireGuard reassignment and multi-interface fixture passed | Real overlapping nodes, delayed packets, and complete panel telemetry path |
| 3 TCP quality | ACK estimator and real capture test passed | Validate live panel quality with induced delay, retransmission and capture gaps |
| 4 Application detection | Signature classifier connected to node telemetry | Verified signature distribution and actual game/application destination scenarios |
| 5 UDP quality | Real UDP flow counters | Protocol-specific RTT/jitter/loss validation, unavailable where unsupported |
| 6 Infrastructure | Kernel counter rates and MTU wired through node and panel | Real tunnel health, GRE and path diagnostics with induced faults |
| 7 Live dashboard | Build and intercepted browser tests passed | Browser with live panel and Node Agent under traffic |
| 8 User details | Account history chart and live flows available | Live multi-session account and persisted history browser validation |
| 9 Diagnostics | Read-only evidence findings available | Validate correct findings and safe probe scope in staging |
| 10 Optimization | Complete: scoped per-user WireGuard TCP MSS, administrator audit and real rollback verified | Keep both feature flags off until an authorized staged rollout |
| 11 Alerts and history | Durable writer and migration matrix tested | Live worker persistence, expiration, alert delivery and restart in staging |
| 12 Security and regression | Targeted authorization and race tests passed | Full final-head compatibility, threat review, regression and CI |
| 13 Performance | Isolated socket-load measurements recorded | Production-shaped forwarding/load comparison including storage, CPU/RAM and loss |
| 14 Staging | Individual traffic and browser fixtures available | Full-stack staging with live traffic, failure recovery and rollback |
| 15 Release | Pending | All stage gates, green final CI, panel/node builds, checksums and tested update/rollback commands |

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

Linux infrastructure parsers and activation passed race detection in the 2 CPU/2 GiB isolated container (1.040s). The latest dashboard/binary matrix and database CI passed, while full build/native-race remained running at inspection. Isolated WireGuard CI failed specifically at mount --make-shared /run/netns with Permission denied; e2ecaff7 exposes this environment failure without suppressing assertions. Local isolated Docker WireGuard traffic tests remain successful.

7b7546bb wires actual account quality history into node telemetry and full-access GET /api/network-intelligence/history?node_id=&user_id=. It aggregates before live-flow truncation, weights upload ACK samples, rejects stale/duplicate observations, downsamples at five observed seconds, retains4096points globally/60per user for30minutes, and transmits128latest points with truncation flag. Targeted node/API history tests passed. Storage is explicitly volatile and collection is on telemetry reads, so continuous durable history acceptance is not met.

NEXT_ACTION is authorized user-history chart integration, a valid CI namespace fixture execution strategy preserving isolation/security, and durable background sampling/retention with backward-compatible migrations. Remaining production acceptance includes notifications, GRE/path/protocol-aware UDP, safe optimization audit/rollback, overlapping-node/delayed active traffic, full-stack staging and release commands. PR147 remains Draft.

History charts, provisional SQLite-tested durable schema/repository (2fff761d), and VM-based CI isolation (368aff39) are now available for review. Browser fixtures passed four tests, including history gaps and retention labels. The VM run still needs CI validation. Next implement bounded background persistence and validate MySQL/MariaDB migrations and real full-stack staging.

Durable quality history now has a default-off panel worker controlled by ANTIMAGE_NETWORK_HISTORY_PERSISTENCE alongside the main feature flag. It polls up to four nodes per minute in rotation and retains up to eight fresh account observations per node poll. Authorized reads in durable mode use the database even during a node outage. Targeted history/config tests and in-memory SQLite offline-node API coverage passed. Single authoritative panel writer is required. Full worker integration, MySQL/MariaDB CI, retention when there are no fresh inputs, broader sampling coverage, notifications and release validation remain. VM CI run 45982003811 was still running at inspection.

Durable history workers now have independent retention, rotated user selection, and SQLite integration coverage for limits, ordering, restart, and concurrent admission. The WireGuard VM CI passed. Expanded migration 95 assertions run on MySQL and MariaDB in Test Database Migrations. See PROJECT_STATE.md for exact coverage and remaining release work.

Database CI 45982997192 passed on e850e185. MySQL and MariaDB verified the migration 95 history table and retention index through a transactional insert, read and rollback. The SQLite job passed the targeted Race suite. Local tests added in 47ce4442 exercise startup retention with both flags and durable ordering after repository recreation. The full live Node Agent-to-panel history path and persistence load remain staging requirements.

The dashboard now renders sampled interface MTU and interval counter rates. Lint, TypeScript and five intercepted Playwright scenarios passed. NEXT_ACTION is a live full-stack check of node history and interface telemetry, followed by the outstanding tunnel and UDP measurement gates.

Dedicated gRPC Metrics requests now explicitly opt into sensitive Network Intelligence snapshots. Health and ordinary Metrics preserve the existing runtime response and avoid collecting or serializing flow metadata. The real gRPC handler passed the in-memory transport regression. Full live browser-to-node staging and final CI remain acceptance gates.

Stage 6 evidence: the node reads Linux interface operstate and the panel displays it alongside MTU. Real kernel loopback sampling passed under the race detector. Five browser tests passed with API fixtures. Interface operstate is a link indicator. Peer liveness and end-to-end GRE path validation remain separate acceptance criteria.

Stage 12/14 evidence: controller-to-real-Node-Agent metrics over loopback mTLS passed with migrated SQLite state and two agents (enabled and disabled). NEXT_ACTION is staging with live attributed traffic through the same authenticated transport, durable storage, and browser. No stage-completion status changed.

The isolated real WireGuard reassignment fixture now reads live TCP and UDP telemetry through the registered Node Agent gRPC service before and after peer reassignment. It checks both protocols and exact account/node identity. The fixture passed with Linux race detection in 2.337 seconds. Browser-to-panel execution, durable worker staging and the remaining release gates are still open.

Stage 9 now reports node-scope softnet drops and IPv4 fragmentation/reassembly failures using recent measured counter rates. It excludes stale and missing intervals. Targeted Go/API tests and six browser fixture scenarios passed, with dashboard lint and type checking. The browser suite is now included in the isolated Network Intelligence CI workflow. Live induced-fault staging and final CI acceptance remain open.

The Network Intelligence panel now shows valid node memory counters from the same authenticated metrics response. Real controller-to-agent mTLS tests and six browser fixture cases passed. This adds dashboard resource visibility. Forwarding overhead, UDP/tunnel measurements and the integrated release gates remain open.

Stage 13 tracker microbenchmark on Windows: 1, 1024 and 4096 resident flows took 268 ns, 19.9 us and 90.3 us per observation before the change and 233 ns, 270 ns and 290 ns after it, with zero allocations per update. Snapshot still expires flows and replay high-water marks still bound admission. This isolates tracker cost. The Linux forwarding benchmark is still required.

NEXT_ACTION: extend TestNetworkIntelligenceReadsRealNodeMetricsOverMTLS with the real traffic fixture from internal/app/nodeagent/network_intelligence_wireguard_linux_test.go in a disposable Linux VM. Assert attributed TCP/UDP telemetry, worker persistence after restart and live browser rendering without intercepted API responses. Check current PR CI while developing. Then proceed with the remaining acceptance criteria in the table. Keep PR Draft until all gates pass.

## NEXT SESSION CONTINUATION

The existing real encrypted WireGuard reassignment fixture now queries the real Node Agent over loopback mTLS and passed the Linux race test with 2 CPUs, 2 GiB RAM and no external network. Both TCP and UDP must match the new account and node after reassignment. The fixture still bypasses the panel.

NEXT_ACTION: connect this fixture to the production Node Controller and the panel history worker in the disposable Linux lab. Verify SQLite persistence across a panel restart and use the authenticated dashboard without intercepted responses. Check PR CI alongside this work, then continue the remaining acceptance criteria in the table.

The real WireGuard reassignment fixture now calls the production panel Node Controller, backed by migrated SQLite, to read TCP and UDP flow ownership over mTLS. The isolated Linux race run passed in 6.901 seconds with 2 CPUs and 2 GiB RAM.

NEXT_ACTION: expose the disposable live Node Agent endpoint to an API-package integration fixture so the actual history worker can sample through the production controller. Reopen the panel database and verify authenticated history reads, then run the browser against that live API. Keep PR Draft pending all gates.

The production history collector, controller and history handler now have a loopback mTLS integration test backed by migrated file SQLite. Two polls of the same fixture snapshot yield one point. The test closes the RPC server and database, reopens SQLite, and verifies timestamp, account, node and RTT through the full-access handler. Linux Race and Windows loopback tests passed. The source snapshot is controlled by the test, and the handler is invoked with a supplied authenticated principal.

NEXT_ACTION: feed the existing live WireGuard agent into this history transport test instead of the controlled snapshot. Use the real admin login and browser against the running API, then continue the remaining acceptance gates. Keep PR Draft.

The production history collector now runs in the live WireGuard reassignment fixture through the authenticated Node Controller. It saves one deduplicated sample for the new owner, shuts down the Node Agent RPC server, then reads that sample from reopened file SQLite. The same collector loop remains in use by the panel with its existing configuration and scheduling.

NEXT_ACTION: verify the durable data through the real login and HTTP middleware with the running dashboard. Reuse the existing real WireGuard fixture and follow the login setup in internal/app/api/admin_auth_test.go. Confirm the browser reads live flow and history routes without interception. Continue the remaining stage gates and leave PR Draft.

The durable-history integration now reopens the database through the production panel constructor, starts its real HTTP router, verifies anonymous requests are denied, logs in as a full-access administrator and reads the persisted sample using the issued session cookie. This test uses the controlled gRPC snapshot. The separate Linux WireGuard fixture verifies live capture and storage through the same production collector.

NEXT_ACTION: add and execute a Playwright scenario against a single loopback origin serving the built dashboard and the real panel API, with the WireGuard fixture supplying live node data and presence for node 7/account 42. Assert both flow and history requests reach the real server. Continue the remaining stage gates and keep PR Draft.

Playwright now runs against the built dashboard and real panel routes on a single loopback origin without intercepting HTTP. It logs in with a real session, selects node 7 and account 42, and checks the durable sample returned from the reopened SQLite database and rendered in the history table. The Windows Chrome run passed after rebuilding an outdated dashboard bundle. The isolation workflow now runs this scenario against a fresh production build. The sample originates from the controlled mTLS fixture.

NEXT_ACTION: reuse the Playwright scenario with a panel started from the live Linux WireGuard fixture's SQLite database and Node Agent endpoint. Keep the agent available while the browser asserts TCP/UDP flows and durable history for the new owner. Then perform the remaining Stage 0-15 acceptance, including the forwarding benchmark, outage recovery and release rollback. Keep PR Draft.

SQLite migration CI, PR Build, Binary Build and dashboard-browser failed on the same test setup issue: the temporary panel constructor tried to create /var/lib/antimage/certificates on a non-root runner. The integration test now supplies a temporary CertificateBase. The exact SQLite race test selection passed in isolated Linux as UID 65534, and Windows Chrome passed the real panel browser scenario again.

NEXT_ACTION: inspect CI for the certificate fixture fix, and continue combining the real WireGuard fixture with the authenticated dashboard browser scenario. Preserve the Stage 0-15 acceptance gates above and keep PR Draft.

PR database matrix 46003449308 passed SQLite, MySQL 8.4 and MariaDB 10.6 on c164e01c. The matrix now also runs the production history repository against each migrated disposable database, including the 100000-row boundary under concurrent admission, duplicate suppression, scoped reads, ordering and retention. The expanded tests passed locally on a freshly migrated SQLite file. MySQL and MariaDB results for the expanded tests are pending.

NEXT_ACTION: verify the expanded three-engine CI matrix. Continue combining the live WireGuard fixture with the authenticated browser, then work through the remaining acceptance gates. Keep PR Draft.

Expanded database matrix 46004216171 on 43809939 reported MySQL success, SQLite failure and MariaDB in progress at inspection. The SQLite workflow supplied a relative filename to both the CLI and Go tests. Go runs package tests from their source directory, so the relative URL does not refer to the file migrated at checkout root. The workflow now constructs an absolute URL from GITHUB_WORKSPACE. The local reproduction failed with no such table on the relative URL and both durable repository tests passed on the same migrated database using the absolute URL. Access to the failure log and publication require the GitHub tool review to be available.

NEXT_ACTION: compare the verified local relative-path failure against the GitHub job log. Commit the workflow correction and checkpoint, push to PR 147, and verify all three expanded matrix jobs on that commit. In parallel, continue the live WireGuard-to-browser staging integration. Keep PR Draft.
GitHub confirmed both MySQL and MariaDB passed the expanded repository matrix on 43809939. Both SQLite failures matched the local missing-table reproduction. CI must validate the absolute-path correction.

The SQLite absolute URL fix is published as 1ac0ee2e. The replacement PR database run is 46029983562 and all three jobs were running at inspection. The browser script now has local, syntax-checked assertions for real WireGuard TCP/UDP ownership and durable sample rendering. The Docker Desktop Linux engine pipe was unavailable, so that mode has not been run.

NEXT_ACTION: check run 46029983562. Restore access to a disposable Linux lab, connect the existing WireGuard fixture to the real panel browser harness and execute network-intelligence-live.mjs with --real-wireguard. The script changes are local until that mode passes. Continue the open Stage 0-15 gates and keep PR Draft.

Database compatibility update: corrected PR run 46029983562 passed all three expanded jobs on 1ac0ee2e (SQLite, MySQL 8.4, MariaDB 10.6). This includes migration 95 and the production repository tests for capacity, concurrent admission, idempotency, ordered scoped reads and retention. No re-run on 1ac0ee2e is required.

The local browser integration now has a test-binary handoff. The Linux Node Agent test can keep its mTLS endpoint live after collection and invoke the API test binary with its migrated fixture SQLite file. The API test starts the actual panel and uses the existing Playwright script in real WireGuard mode. The refactored controlled scenario passed in Chrome. The new live mode remains unexecuted.

NEXT_ACTION: in a disposable Linux guest with WireGuard, Node and Chromium, build the API test binary with go test -c ./internal/app/api and point ANTIMAGE_NI_BROWSER_PANEL_TEST to its absolute path. Build dashboard assets and set ANTIMAGE_NI_BROWSER_DASHBOARD to the dashboard directory. Run TestNetworkIntelligenceRealWireGuardPublication with ANTIMAGE_WIREGUARD_NI_E2E=1, then run adjacent regression checks before publishing the local browser integration. The only local WSL distribution is Docker Desktop and is stopped. The existing disposable guest CI route remains available for provisioning the combined test. Continue open Stage 0-15 acceptance and keep the PR Draft.

The disposable guest CI job now packages the Linux API test binary, Node, Playwright and a fresh dashboard build. It installs Chromium inside the guest and invokes the authenticated browser against the real WireGuard Node Agent while its RPC server is still live. The Windows host could not execute the Linux guest cleanup tests because it lacks POSIX process-group operations. The existing guest workflow will execute them on Linux.

NEXT_ACTION: inspect the new Network Intelligence Isolation result. Fix and rerun any guest provisioning, live flow, or history browser failures. The replacement three-engine database matrix is already verified on 1ac0ee2e. Once the combined test passes, continue the open stage acceptance gates. Keep PR Draft.

While combined staging CI 46030755602 ran, the node snapshot was made newest-first before applying its 250-flow API cap. History still aggregates the full bounded set. A 251-flow Node Agent regression verifies that the oldest flow is omitted and the newest appears first. Targeted Node Agent tests passed on Windows.

NEXT_ACTION: keep checking the combined live WireGuard/browser run and address any failures, then finish the open stage gates in the table. The 250-flow ordering fix is independent of guest provisioning. Keep PR Draft.

Combined run 46030755602 reached the real flow API and passed TCP/UDP and account attribution assertions. The browser then failed to locate the protocol badge using a word boundary on concatenated cell text. The locator now matches the actual badge inside the cell. The history assertion still requires successful guest execution.

NEXT_ACTION: inspect the combined guest retry, validate the durable history browser assertion and address any remaining failures. Continue the open stage gates and keep PR Draft.

While the guest retry was queued, Stage 9 gained evidence for measured per-interface RX/TX drops and errors. It reports the highest valid sampled rate per category with interface and interval, and filters unavailable values. Targeted diagnostics and API tests passed. This is interface evidence; path loss and GRE availability still require separate validation.

NEXT_ACTION: check the live browser guest retry, then validate interface evidence with induced faults in staging and continue the open stage gates. Keep PR Draft.

One-click diagnostics now report missing or stale node telemetry even when the node supplies no flows. The loopback mTLS integration verifies the node-scope finding. The live guest retry was still queued at inspection.

NEXT_ACTION: read guest run 46031543178 and resolve any issues in the combined scenario, then proceed with induced-fault staging and the remaining release gates.

Local batch begins with preserving the selected node and account when presence expires so an administrator can continue reading already-selected durable history. Selecting another node clears the old account selection. All seven dashboard Playwright fixture tests and TypeScript passed. Lint passed with two existing warnings. This does not add discovery of accounts that were offline before the page was opened.

NEXT_ACTION: leave this batch uncommitted until the remaining work is ready to commit together. Check guest run 46031543178 (dashboard-browser passed, isolated-wireguard was queued), then continue the combined staging, UDP/tunnel measurements, optimization with tested rollback, benchmarks and release acceptance from the table. Keep PR Draft.

The current isolated-wireguard CI failure occurred before test execution: the QEMU guest had no reachable SSH provisioning path. Its user-mode network was configured inside 127.0.0.0/8, which Linux reserves for loopback. The guest now receives an address from a private 10.31.0.0/24 network; the test discovers the guest interface address and binds its fixture gRPC server on all guest interfaces, while ordinary local tests bind loopback. The current Codex Windows sandbox also blocks local loopback connects (`connectex: ... forbidden by its access permissions`); rerunning the focused API and Node Controller mTLS tests with approved local-network access passed (API 3.383s, Node Controller 1.814s). This Windows sandbox denial is not treated as a product regression.

The private subnet fixed guest SSH provisioning, and selecting the first global IPv4 address fixed Ubuntu's predictable interface naming. The first complete guest run then confirmed real TCP/UDP WireGuard capture and peer reassignment, but browser node telemetry returned 503 because the parent fixture stopped its in-process Node Agent before the panel restart. The test now leaves the Agent running while closing and reopening the panel database. Network Intelligence Isolation run 46039891480 passed on 9f6becb0: real WireGuard TCP/UDP capture, peer reassignment, mTLS telemetry, durable SQLite history after restart, authenticated browser flow rendering, and both guest binaries' race-instrumented suites. Dashboard-browser, expanded database matrix, and binary build also passed on that SHA. PR Build Go race and Native Protocol E2E were still running when this evidence was recorded. Stage completion statuses remain unchanged; PR 147 remains Draft.

NEXT_ACTION: continue the existing unmet Stage 0-15 acceptance criteria and recheck PR Build and Native Protocol E2E for 9f6becb0; do not infer release readiness from the passing Network Intelligence isolation run.

Grouped pending batch verification: preserved selected durable history after user presence disappears, isolated the live mTLS RPC test address inside the disposable guest network, and retained the selected node option when presence records expire. The focused Chrome history test and TypeScript passed. API history mTLS test passed on Windows. Production shaping, full CI and live guest execution remain pending.

The live Linux fixture ordering is now stronger. The WireGuard Node Agent remains active while the browser reads real TCP/UDP flows. The test then stops the agent, reopens SQLite, verifies durable history directly, starts a fresh panel over that database and asks Playwright to read/render the persisted history again.

2026-11-27 verification on `54b36cd0b30da8364e27e2d34926581d3c587020`: GitHub PR checks now report success for the published PR build/race job, isolated Network Intelligence WireGuard scenario, native protocol E2E, SQLite/MySQL/MariaDB migration matrix, dashboard browser checks, and cross-platform binaries. The PR remains Draft. The exact same source address, interface, UDP tuple, and ports bound to two different nodes now have a focused regression test proving separate user/byte counters and isolation after reassignment on only one node. `go test ./internal/app/networkintelligence -count=1` and the focused overlap/reassignment tests pass locally; `git diff --check` passes. Windows race execution cannot run here because CGO is disabled and no C compiler is installed; Linux race coverage is provided by the passing isolated CI job. This focused tracker test is not a substitute for induced multi-node live traffic.

NEXT_ACTION: continue remaining open acceptance gates: run a live two-node overlapping-address WireGuard scenario; integrate/validate real UDP quality and game signatures with evidence; induced GRE/MTU/path faults; validate optimization audit and rollback; production-shaped forwarding benchmarks/resource impact; full-stack staging recovery and tested release rollback/update artifacts. Recheck CI after the pending local test commit. Do not mark any stage complete solely from unit/CI success; keep PR 147 Draft and do not touch Production.

A repeatable resource run is now wired into the isolated Network Intelligence guest: a separate non-race test binary runs the existing Feature OFF/ON paced UDP echo comparison under the guest's 2 vCPU / 2 GiB limit, and its JSON log is retained as a CI artifact even on failure. It reports elapsed rate/latency, process CPU, Go heap, captured packet/flow counts and collector drops. Scope is explicitly limited to the echo fixture plus collector; this does not measure VPN forwarding impact or production-user capacity. Git Bash shell syntax and diff checks passed locally. The Linux-specific guest cleanup unittest cannot run on this Windows host (`os.killpg` is unavailable), so CI must validate it and the new guest measurement before Stage 13 advances.

NEXT_ACTION: validate the new 2-vCPU/2-GiB resource artifact and remaining checks on the latest head; then use the recorded evidence alongside the still-required production-shaped WireGuard forwarding test. Continue open acceptance gates for real two-node overlapping addresses, verified application signature data, UDP quality, induced GRE/MTU faults, optimization/rollback and full release validation. Keep PR 147 Draft; no Production actions.

The resource workflow now executes successfully in the disposable guest, with no echo timeouts and zero kernel observation drops in the paced trials. However, review of run 46058877330 found its flow-count and serialized-snapshot metrics were sampled after collector shutdown, so all monitoring-on trials incorrectly reported zero flows. Those fields are explicitly rejected as evidence. The measurement now snapshots before teardown and fails when it observes no attributed live flow. CI on the revised test must pass and produce nonempty flow snapshots before considering this part of Stage 13 verified.

Follow-up to Linux run 46060432036: the corrected benchmark advanced through the monitoring-on flow assertion (otherwise it would have failed before teardown), but then failed because `SessionCapture.Stop()` returns `context.Canceled` for ordinary cancellation. Existing capture tests explicitly expect this return value. The resource test now treats only that normal cancellation as success and continues to fail on other stop errors. No completed monitoring-on metrics were emitted in this attempt; rerun remains required.

CI run 46060431914 also exposed a separate native IKEv2 accounting mismatch after CHILD_SA rekey: the test observed 840 bytes where its 1008-byte oracle expected. Three pings succeeded, but the second accounting checkpoint was short by 168 bytes. This is outside Network Intelligence; do not waive the assertion. It requires reproducing and tracing strongSwan/XFRM generation accounting after rekey before the protocol suite can be considered green.

2026-11-27: latest head `52ef85110f3069ba6c35f5a55b8031dc0b3de007` passed isolated run 46061180817. The corrected resource test reports nonzero live flow snapshots for 10-500 clients, zero echo timeouts and zero reported collector kernel drops. At 500 clients OFF/ON throughput was 896.0/907.2 echoes per second, process CPU 737.959/1029.176 ms, and ending Go heap 1,133,120/2,876,856 bytes. SQLite, MySQL, MariaDB, dashboard browser and native IKEv2 passed; the earlier IKEv2 mismatch did not recur. Some cross-platform and native protocol/race jobs were still running at last poll. This fixture is loopback only and not a production-shaped forwarding result.

NEXT_ACTION: verify every CI job on `52ef8511`; then run production-shaped WireGuard forwarding and live two-node overlap tests, evidence-based UDP/game detection, induced GRE/MTU faults, optimization audit/rollback and release rollback. Keep PR Draft and do not access Production.

2026-11-28: added `TestNetworkIntelligenceRealWireGuardOverlappingAddressAcrossNodes`, a disposable Linux E2E with two independent Node Agent instances, two client namespaces, the same WireGuard tunnel source IP on both nodes, distinct account/node identities, and bidirectional TCP and UDP traffic. The test asserts real handshakes, destination tuples, both-direction byte counters, and per-agent isolation. Linux amd64 nodeagent test-binary cross-compilation and focused Windows nodeagent tests passed. The live kernel test has not run on this Windows host; execute it in the disposable guest CI before treating the criterion as passed. PR remains Draft.

NEXT_ACTION: run Network Intelligence Isolation CI on the new two-node fixture. Fix any real guest/test issues, then capture the successful two-node evidence. Do not claim two-node acceptance from cross-compilation or unit tests alone.

The first live guest execution (run 46073124864) reached the new two-node test and failed during fixture setup because a veth interface name exceeded Linux's 15-character limit. This was a test-fixture setup failure before any traffic or ownership assertion. Shortened all generated interface names; rerun in the disposable Linux guest is required.

The next isolated run (46073917634) passed interface creation and began sending actual traffic, but the client timed out waiting for the UDP reply. The two simulated nodes share the disposable host network namespace, so overlapping WireGuard AllowedIPs made the return route ambiguous. The fixture now adds per-destination source-policy routing tables so each test service reply returns through its own WireGuard interface. The run did not pass the ownership assertions; rerun is required.

2026-11-28 live two-node overlap verification on `f03aa841aa9be0ce5b2d028e2a898f552e17bfe5`: Network Intelligence Isolation job 140759867632 in run 46074788377 passed `TestNetworkIntelligenceRealWireGuardOverlappingAddressAcrossNodes` in 0.90s. The fixture used two independent Node Agent instances and encrypted WireGuard tunnels with the same client tunnel address (`10.99.99.2`) but distinct node IDs (701/702) and accounts (41/42). Both carried actual bidirectional TCP and UDP traffic. The test verified handshakes, per-node destination tuples, nonzero upload/download counters, and that each agent's tracker contained only its assigned node/account/interface. This closes the live-overlap test criterion for this fixture. It does not establish production multi-host capacity or cover every multi-node deployment topology. Other PR checks on this SHA were still running at inspection; PR remains Draft.

NEXT_ACTION: finish and verify all remaining CI jobs on this source. The other open acceptance gates remain application signature/UDP quality validation, GRE/MTU induced faults, audited optimization rollback, production-shaped forwarding benchmark, and full release rollback/update validation.

2026-11-28 UDP/signature validation: the Network Intelligence and Node Agent Go packages pass locally. Added a Node Agent snapshot integration test using an explicitly synthetic RFC 5737 UDP destination signature: the exact destination-prefix/protocol/port match is labeled `probable` with revision/evidence, a same-port destination outside the signed prefix remains `Unknown`, and UDP does not acquire TCP quality or fabricated UDP RTT/loss. This verifies signature loading-to-snapshot wiring and fail-closed classification only; it is not evidence of real game recognition. Valve's Steamworks SDR documentation describes relay/fake-IP behavior and does not publish an authoritative exclusive destination range suitable for this classifier, so the sample `162.254.199.170:27051` must remain unclassified absent a verified, current, application-exclusive signature source. UDP RTT/loss remains unavailable without protocol sequence/echo evidence; ordinary flow counters alone cannot validate it. Tests: `go test ./internal/app/networkintelligence -count=1`, focused detector/session tests, focused Node Agent signature/UDP snapshot tests, and `git diff --check` passed on Windows. Linux packet capture and real-game UDP validation remain open.

NEXT_ACTION: obtain an authoritative, versioned signature feed or a protocol-aware isolated game/SDR test source before labeling real destinations; add protocol-level UDP timing/loss only where sequence/response evidence is available. Continue induced GRE/MTU faults, audited optimization rollback, production-shaped forwarding benchmark, full staging/release rollback validation. Keep PR 147 Draft and do not access Production.

2026-11-28 isolated tunnel-fault fixture: added Linux-only `TestNetworkInfrastructureFaultInjectionInDisposableGuest`, gated on the explicit disposable-guest marker and root. It creates only its own namespace, veth and GRE devices; checks healthy GRE reachability and sampled interface MTU; induces a DF/MTU failure and recovery, verifies real IPv4 `FragCreates` counter growth and sampled interval rate after oversized non-DF probes, applies 100% `netem` loss and checks qdisc drop evidence/recovery, then injects/removes a fixture-only blackhole route and checks recovery. The test is included by the existing guest Node Agent test filter. Linux amd64 cross-compilation and local `TestNetworkInfrastructure*` tests passed; this Windows host cannot execute namespace/GRE kernel actions. Await isolated Linux CI execution before treating any induced-fault gate as passed. The fixture does not yet prove diagnostic engine classifications or GRE behavior across production topology.

Verification update: isolated Network Intelligence run `46076988527`, job `140766940327`, passed on `cfbf3df9bd9b43f707213847c456318d9ca829ef`. The guest log confirms `TestNetworkInfrastructureFaultInjectionInDisposableGuest` passed in 5.20s; the same job also passed real two-node overlapping-address WireGuard TCP/UDP, real WireGuard publication/reassignment, and infrastructure parsers. Thus the induced kernel fault/recovery fixture passed in the disposable Ubuntu guest. This does not prove automated diagnostic classification, production GRE topology, or production impact; no Production network was touched. Other PR checks were still running at inspection.

NEXT_ACTION: connect fault evidence to bounded, read-only diagnostic findings and validate on representative GRE topology; continue the outstanding optimization audit/rollback, production-shaped forwarding benchmark, staging/release rollback gates. Keep PR 147 Draft and do not access Production.

2026-11-28 production-shaped WireGuard benchmark harness: added opt-in `TestNetworkIntelligenceWireGuardForwardingResourceComparison` in a dedicated non-race Node Agent test binary, run only inside the marked 2-vCPU/2-GiB disposable guest. It creates a real encrypted WireGuard client-to-node-to-downstream UDP echo/forwarding path, warms and verifies the peer handshake, then alternates three monitoring-OFF and three monitoring-ON trials. Each trial sends 5,000 1-KiB sequence-tagged datagrams, verifies every echoed sequence (measured loss), and logs packet rate, Mbps, bytes, client/server/collector-process CPU, client/server max RSS, collector-process heap/RSS, flow attribution, capture/kernel drops and isolated interface drop deltas. The workflow retains a separate benchmark log artifact; existing loopback figures remain explicitly non-forwarding. Linux amd64 cross-compilation and the network-intelligence package tests pass locally. Actual Guest execution is pending CI and no forwarding benchmark result is claimed yet.

NEXT_ACTION: run and inspect the dedicated WireGuard forwarding benchmark artifact in the disposable guest. Fix only reproduced setup or measurement issues, then document all six raw trials and medians. Compare process and system limits carefully; this fixture is not production capacity validation. Keep PR 147 Draft and do not access Production.

2026-11-28 benchmark result on `1b975410221a38420d3db9491792043c159cc061`: Network Intelligence Isolation job `140777303538` in run `46080247862` succeeded on its marked disposable Ubuntu guest (kernel 5.15, guest RAM 1,958 MiB). The live encrypted WireGuard forwarding fixture passed all six 5,000-datagram trials and attributed the monitoring-on flow: all trials had 0/5,000 echo loss, capture kernel drops and fixture NIC RX/TX drop deltas were zero, and the collector observed 10,000 packets for the one bidirectional flow per monitoring-on trial. Median OFF was 72,887 WireGuard packets/s and 597.09 Mbps; median ON was 39,977 packets/s and 327.49 Mbps, a measured throughput decrease of 45.15%. Median Node Agent process CPU rose from 1.236 ms to 50.521 ms per 5,000-echo trial; median Go heap after the trial was about 0.845 MB OFF versus 2.464 MB ON. Guest available RAM during the trials was about 1.62-1.66 GiB. The aggregate guest CPU percentage readings were noisy and are not used as a comparative claim. This is real encrypted forwarding in an isolated fixture, not a production node or game workload. The zero-loss result does not offset the large throughput regression: Stage 13 performance acceptance is NOT PASSED. Investigate and reduce capture/collector overhead, then rerun under identical guest conditions with retained raw evidence before claiming performance acceptance. PR remains Draft; no Production changes.

NEXT_ACTION: diagnose the reproduced ~45% throughput reduction with Network Intelligence capture enabled. Profile packet capture/collector processing and fixture CPU contention; avoid changing the primary forwarding path. Add a reasonable measured acceptance bound only if already specified by project policy, otherwise report the measured regression without redefining acceptance. Rerun the same 2-vCPU/2-GiB OFF/ON WireGuard benchmark after any fix. Continue unrelated open gates for real game/UDP signature evidence, optimization audit/rollback and full staging/release rollback validation. Keep PR 147 Draft.

2026-11-28 Stage 10 code review: Network Intelligence currently exposes read-only presence, node metrics/diagnostics, and history routes. The node proto/service has no network-optimization mutation or rollback RPC, and the Node Agent has no per-user MSS/network-policy apply path. The existing generic `recent_actions` audit/rollback infrastructure is not connected to Network Intelligence. Therefore there is no Network Intelligence optimization operation that can be practically applied or rolled back yet; Stage 10 remains Pending. On this Windows host `CGO_ENABLED=0`, so `recent_actions_test.go` (build-tagged `cgo`) is excluded; the attempted filtered API command reported `[no tests to run]` and is not counted as a test pass. No operating rules, firewall, TC, routes, or production services were changed.

NEXT_ACTION: implement an additive, fail-closed, feature-flagged admin operation end-to-end: resolve the selected user's current authoritative WireGuard peer/session on the node; scope any supported TCP MSS rule only to that peer and owned interface; reject Xray/unsupported peers and rule conflicts; expose preview/apply/reset via an additive mTLS RPC and full-access API; persist exact before/after state in the existing audit history; and make rollback conditional on the rule still matching the applied state. Prove apply, traffic effect, audit entry, reset and rollback in the marked disposable Linux namespace fixture, then run the cgo audit/API tests in Linux CI and verify older Node Agents fail closed. Keep all flags off by default and PR #147 Draft.

2026-11-28 implementation update: added an opt-in admin-only per-user WireGuard TCP MSS operation through an additive Node Runtime gRPC method. Both Network Intelligence and Optimization flags must be enabled to apply; rollback/list remain available for cleanup. The node resolves only its authenticated active account/session/peer/interface bindings, refuses ambiguity and MTU-incompatible MSS, checks existing mangle rules, records a private exact-rule ledger, and supports conditional/idempotent rollback plus reset before WireGuard ownership changes. The panel exposes active users, apply, status and rollback; API mutations use the existing recent-actions audit trail. Added an isolated Linux kernel test that captures the actual SYN MSS for the bound source, checks an unbound control source, unbinds and restarts before rollback, and verifies conflict refusal. Added an idempotent SQLite audit rollback test to the disposable Linux guest CI script. Windows validation: focused Go packages passed; dashboard service test, targeted Biome lint and production dashboard build passed; Linux amd64 Node Agent test-binary cross-compilation and `git diff --check` passed. The build retains existing CSS/chunk warnings. This Stage remains Pending until the disposable Linux kernel apply/traffic/rollback test and CGO audit test pass in CI; no Production network/configuration changed.

NEXT_ACTION: diagnose the measured WireGuard forwarding throughput regression under capture, then complete the outstanding application-signature/UDP, full-stack staging and release gates. Keep PR 147 Draft; do not enable flags or touch Production.

CI result on `9ec36a10496bbdb8cd73467a7d668c79b1274f23`: the isolated Linux job executed the kernel optimization fixture and exposed a stale-generation error when the test attempted to rebind after an explicit unbind; fixed by advancing the authenticated binding generation and timestamp for the conflict-preflight assertion. On the next guest attempt (`4363769548c79718320d1816c64deba6a7521a61`), the kernel Optimization test passed end-to-end: authoritative binding, 1340 MSS observed in a real TCP SYN, unbound control address unchanged, explicit rollback after unbind, restart-idempotent rollback, and terminal-rule conflict refusal. The two-node overlapping-IP and GRE/MTU fault fixtures passed too. The live dashboard returned a valid HTTP 200 history payload but did not render it. Inspection found the two sibling panels used the same React key (`node`); namespaced keys were added so their state reconciliation cannot collide. The mocked E2E now awaits/verifies responses; the live browser logs selected account value/options and page text on failure. The CGO audit test was placed after the failing Node Agent command, so it did not run; moved it earlier so the next guest run executes it independently. No Production change.

2026-11-28 Stage 10 verified complete on PR head `109d051eeeca98282b3ac14782d2ba61817a96ba` (implementation `9ec36a10`, UI Apply/Rollback E2E `61a30d16`, legacy-agent compatibility test `109d051e`). Isolated Linux CI run `46118207923` / job `140896653500` passed `TestNetworkIntelligenceOptimizationApplyRollbackInDisposableGuest`: the active authenticated peer's real TCP SYN carried MSS 1340; a second unbound address retained its default MSS; rollback after peer unbind removed only the recorded rule; retry after Agent restart remained idempotent; conflicting terminal mangle rules were refused. The same run passed `TestNetworkOptimizationAuditRollbackIsIdempotent` under CGO/SQLite. Dashboard-browser job `140896653705` passed all 8 Playwright tests, including full-access admin selection, confirmation and Apply/Rollback interaction through a deterministic API fixture; the authenticated durable-history browser check also passed. Dashboard Go suite `140896653419` passed, including `internal/app/nodecontroller` with the new mTLS compatibility test proving a legacy Runtime service without the additive method returns `UNIMPLEMENTED` and does not apply an operation. Database matrix on this head passed SQLite, MySQL and MariaDB. Both flags remain off by default; no Production setting, route, firewall, TC rule, or service was changed. The same isolated guest's independent forwarding benchmark still shows a separate Stage 13 release blocker: 0/5,000 echo loss in all six trials, but median throughput fell from 463.59 Mbps with capture off to 297.25 Mbps with capture on (-35.88%); median collector CPU rose from 1.37 ms to 54.83 ms per trial. Do not treat Stage 10 completion as overall release readiness.

2026-11-28 performance investigation: retained CPU profile from isolated run `46122235460` / artifact `15254595495` on `78fcf563` shows 1.17 s sampled CPU over 14.68 s: `unix.Recvmsg` accounts for 64.96% cumulative samples, `SessionCapture.Consume` 19.66%, and `Tracker.Observe` 8.55%. This establishes receive/syscall overhead as the primary measured target in the UDP fixture, not a proven production bottleneck. The workload and profile generation passed; the CI profile-summary step failed writing into the root-owned evidence directory. Pending batch fixes write summaries to runner-owned temporary files and replace per-packet receive calls with bounded 8-datagram `recvmmsg(MSG_WAITFORONE)` reads (512 KiB temporary packet buffers per capture, cleared after processing/on exit). The syscall waits only for the first packet and drains queued packets without waiting to fill a batch. Packet-budget, ownership, decode and rejection checks are preserved. Kernel correctness, race checks, ABI compatibility and unchanged OFF/ON benchmark must pass before accepting the change or claiming the throughput regression fixed.

NEXT_ACTION: validate the receive-batch change in the disposable Linux guest, inspect the unchanged six-trial WireGuard benchmark and CPU profiles, fix reproduced failures, and keep Stage 13 unaccepted until performance evidence warrants acceptance. Keep PR Draft and all Production flags off.

2026-11-28 receive batching verified on `78378f558cee6889c10f9ef153cc0685a23c71d9`: Network Intelligence isolated run `46122976569`, job `140911289174`, passed the Linux batch-drain/clear/reuse test, real capture, WireGuard/MTLS publication, overlap, optimization, audit, fault tests, six-trial forwarding benchmark and profile summary. Median OFF/ON throughput was 504.84/408.71 Mbps (-19.04%); all six trials had 0/5,000 echo loss, all three ON trials observed 10,000 packets, and capture/kernel/interface drops were zero. Median agent CPU OFF/ON was 1.509/50.335 ms; ON post-trial heap approximately 1.41 MB. This is an independent CI guest run and cannot establish the full change from the earlier -35.88% as attributable to batching alone. The separate 30-trial CPU profile still places 62.4% cumulative CPU in receive/syscall (1.25 s total samples in 14.93 s), versus 21.6% in SessionCapture.Consume. Stage 13 remains unaccepted: batching is correct but does not eliminate substantial forwarding overhead. Local module tests passed; Linux test binaries compiled for amd64, 386, arm64, arm and s390x. No Production actions.

NEXT SESSION CONTINUATION: current receive batch is correct but the performance gate remains open. Investigate a bounded Linux PACKET_RX_RING/TPACKET approach to remove receive syscalls without busy-spinning or adding batching delay; preserve kernel-drop evidence, packet privacy, ownership barriers, TCP timing limitations and capture failure isolation. Validate kernel ABI/bounds/cleanup/race tests and rerun the unchanged six OFF/ON WireGuard trials plus profiling. Do not claim Stage 13 complete based on green correctness CI. Existing untracked native-session-outbox directory is unrelated and must remain untouched.

2026-11-28 next performance iteration: replace batched receive on the capture path with a bounded TPACKET_V2 receive ring. V2 delivers individual frames without a block-retirement delay; empty rings sleep in poll with a 200 ms cancellation bound. Default mapped kernel memory is 1 MiB per active capture (8 x 128 KiB frames, allowing complete maximum-size IP packets); explicit receive-buffer requests map at least two frames and stay below the existing 4 MiB setting limit. The parser validates native-endian header bounds, full snapshot length, family and interface before decoding. Borrowed bytes are cleared before atomic ownership return; metadata alone escapes the ring. Setup failure fails only observation, and unmap/socket close release resources. Unit fixtures cover bounds, copy independence, clearing and cursor wrap; the existing real capture, cancellation, budget, ownership, TCP quality and WireGuard fixtures must execute in Linux CI before accepting this iteration. Stage 13 remains open pending unchanged OFF/ON benchmark and profiler evidence. No Production actions.

2026-11-28 ring iteration not accepted: run `46124047655` / job `140914554257` passed correctness but median OFF/ON throughput was 326.35/216.57 Mbps (-33.64%); echo loss remained zero, while capture drops were 274/15/80 in the three ON trials. CPU profile attributed 80.41% to Poll. Investigation identified startup ordering that permits classic socket receive-queue packets before RX_RING is installed, which can keep Poll readable after ring frames drain. The follow-up opens the socket with protocol zero and binds/enables reception only after the ring is mapped, so classic queued packets cannot precede ring activation. Added an isolated idle-interface test asserting cancellation and bounded poll wake-ups, plus an additive PollCalls collector counter. Must rerun real traffic, idle/race tests and the unchanged benchmark; this is not yet evidence that the performance problem is fixed.

2026-11-28 V2 ring validation on `7e3681a7`: idle non-spinning test passed but the unchanged forwarding benchmark remained unacceptable (-30.32%, 461.75/321.77 Mbps median OFF/ON), with capture-only drops of 465/1002/66; all echo datagrams still arrived. Follow-up replaces fixed huge per-packet V2 frames with TPACKET_V3 variable records in bounded 256 KiB blocks (default four blocks/1 MiB). A 1 ms block retirement reduces observation wakeups; per-packet software timestamps supplied by the kernel, not block delivery time, feed ACK RTT and jitter. This changes telemetry delivery timing only. Tests now assert timestamp preservation across two records, block ownership/clear/wrap, invalid block/stride/record bounds, and idle waiting. V3 kernel packet/drop statistics use the matching V3 API. No benchmark assertion or workload was weakened. Pending Linux traffic/profile result; Stage 13 remains unaccepted.

2026-11-28 V3 validation on `39562863`: run `46126042939`, job `140920758056`, passed idle/ring bounds/ownership/timestamp tests and the real WireGuard scenarios. Median OFF/ON 544.19/485.71 Mbps (-10.75%); collector CPU 1.296/10.554 ms; all echo loss and kernel/NIC drops zero. CPU profile now totals 270 ms over 14.03 s versus 1.94 s for V2; syscall work is no longer dominant. Flow counters omitted 4/4/12 received packets because kernel timestamps may be reordered across observation directions. Follow-up preserves cumulative counters for same-lease reordered packets while clearing uncertain TCP timing and retaining the monotonic observation watermark; pre-lease packets remain rejected. Added deterministic tests for these cases. Internal block-end publication now publishes each dirty flow cumulatively once per block, reducing global map/lock churn without sampling or altering accounting. Added tests for exact counter preservation, duplicate flush and revoked-owner rejection; local full module suite passes. Must inspect the next unchanged forwarding/CPU/race result before performance acceptance.

2026-11-28 block-publication validation on `2604f82d`: run `46127101433` / job `140924014567` passed strengthened exact-publication assertions: all three ON trials captured/published exactly 10,000 packets with zero capture drops; all six trials had zero echo loss and NIC drop deltas. Median OFF/ON was 573.54/509.59 Mbps (-11.15%); collector CPU fell to 7.738 ms ON versus 1.398 ms OFF. Thus timestamp/counter correctness is verified but the performance gate is still open. Next correction limits socket-local copies to 128 header bytes using classic BPF RET, while independently trusting only the kernel's original wire-length field for payload length/counter validation. No packet sampling or primary-path filter is introduced. Header parsing retains rejection of fragments, extension headers, malformed/truncated transport headers and invalid wire lengths; complete DecodePacket callers retain their original strict full-packet contract. New local tests compare full versus clipped IPv4/IPv6 TCP/UDP metadata and maximum IPv4/TCP options, and reject insufficient snapshots/wire lengths. Await the same exact-count forwarding/CPU/race fixture before acceptance.

2026-11-28 header-only validation on `d73c0751`: run `46128043875` / job `140926904976` passed full kernel/race capture and exact publication. Median OFF/ON 554.35/508.38 Mbps (-8.29%); agent CPU 1.678/6.902 ms; all trials had zero echo loss, and all three ON trials published/captured exactly 10,000 packets with zero kernel/NIC drops. Next tuning increases block retirement from 1 to 32 ms, well below the 2-5 s live UI interval: this delays telemetry delivery only, not packet forwarding, and ACK timing continues to use original per-packet kernel timestamps. It reduces collector wakeups on low/medium packet rates without discarding observations. The unchanged workload, strict packet counts, CPU/RSS and gap metrics must be checked again; no acceptance threshold has been relaxed or invented. Performance gate remains open until results justify acceptance.

2026-11-28 32 ms telemetry-retirement validation on `742da35e`: run `46128913299` / job `140929562237` passed the real traffic suite and exact 10,000-packet publication in all three ON trials; echo loss and kernel/NIC drops were zero. Median OFF/ON throughput 464.48/435.47 Mbps (-6.25%), agent CPU 1.281/8.301 ms; post-trial ON heap approximately 0.89 MB and RSS approximately 21-23 MB, plus the bounded 1 MiB kernel ring. This is measured overhead, not a zero-impact claim. Next removes repeated wall-clock reads within an already-owned immutable kernel block and defers TCP percentile/summary allocations to block publication. Every TCP sequence/ACK still feeds the estimator at its original kernel timestamp. An internal timed-consume hook is available only to the capture implementation; public Consume retains fresh-clock validation. A new deterministic test verifies identical batched versus per-packet RTT summaries, counters and retransmissions; the full local networkintelligence suite passes. Pending final Linux benchmark and CI; Stage 13 and PR release gates are not declared complete from this fixture alone.
