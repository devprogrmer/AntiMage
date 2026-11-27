# Anti-Mage Network Intelligence

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
| 10 Optimization | Pending | Scoped administrator changes, collision checks, audit and verified rollback |
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
