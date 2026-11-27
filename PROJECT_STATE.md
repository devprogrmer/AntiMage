# Anti-Mage Network Intelligence project state

PR #147 remains Draft. Durable history starts only when both Network Intelligence flags are enabled. The writer polls at most four eligible nodes per tick and selects at most eight users per node in user-ID rotation. A separate worker prunes at startup and hourly. Per-node cursors are volatile and restart from the beginning. Timestamp keys make repeat writes idempotent.

Local tests passed for the collector loop against SQLite with a simulated node transport, API, config, the 100000-row boundary with concurrent requests to one repository, startup retention without traffic, and restart ordering. The worker transport fixture does not exercise a live Node Agent connection. Multiple panel writers are outside the tested storage contract.

CI run 45982003811 passed the isolated WireGuard VM. Database run 45982997192 passed all three jobs at e850e185. MySQL and MariaDB ran the migration 95 table/index and transactional insert/read/rollback checks. SQLite ran the worker, API and repository tests under the race detector. The later startup-retention and restart-ordering tests passed locally at 47ce4442. Their follow-up database run 45983224308 was queued when checked.

NEXT_ACTION is to verify the final-head database matrix, run a full worker-to-node staging test and measure persistence load at the configured bounds.

The dashboard renders measured MTU and counter rates, and the roadmap defines evidence gates for every stage. Dedicated gRPC requests opt into Network Intelligence metadata. The targeted API/node tests and the in-memory gRPC transport regression passed. Five browser scenarios passed with API fixtures. Latest full CI was pending at inspection.

Interface operational state is now carried through the existing node telemetry and rendered in the panel. Real Linux kernel sampling passed under race detection. The browser UI was tested with intercepted responses. PR 147 was confirmed Draft. Full CI, tunnel and UDP quality, staging and release rollback remain unverified.

The production panel controller read Network Intelligence metrics from a real Node Agent gRPC handler over loopback mTLS after applying SQLite migrations. Both enabled and disabled agents were exercised. This verifies the authenticated transport and no-data compatibility path. Full staging with live flow persistence and browser rendering remains open.

Linux race validation now covers real encrypted WireGuard traffic through gRPC serialization and peer reassignment. This fixture uses an in-memory gRPC channel. The separate controller test covers mTLS. NEXT_ACTION is to exercise live flow persistence and browser rendering in one isolated staging run.

The remote dev ref was verified as c1a35e44. One-click diagnostics now report node-scoped infrastructure evidence. The six-case Playwright suite passed locally against intercepted API responses and is wired into PR CI. PR 147 remains Draft pending the full release gates.

Measured node memory used and total are exposed only when valid counters exist. Tested over real Node Agent mTLS and in the dashboard browser fixture. PR remains Draft.

Removed the per-observation full expiry scan. Targeted tests passed and Windows tracker cost at 4096 resident flows decreased from 90.3 us to 290 ns per update. The forwarding benchmark and full-stack gate remain open. See the final NEXT_ACTION in MASTER_ROADMAP.md.

Real encrypted WireGuard reassignment telemetry passed over loopback mTLS with Linux race detection. Panel worker restart and live browser verification remain open.

Live WireGuard flow ownership now passes through the production Node Controller over mTLS using migrated SQLite. Linux race validation passed. Durable worker, panel restart and browser remain open.

The history integration suite now validates duplicate suppression and durable reads after reopening SQLite over the production collector and mTLS controller path with a test snapshot. Linux Race and Windows loopback passed. The combined live WireGuard-to-browser path remains open.

The isolated Linux WireGuard race fixture verified live capture through mTLS, the production collector and reopened SQLite. Browser validation with real admin authentication remains outstanding.

The history transport integration now exercises panel startup after database reopen, real administrator session login and the protected history HTTP route. A combined real-traffic browser run is still required.

The built dashboard passed a real authenticated Playwright history read on loopback with a controlled mTLS fixture. The combined browser run with live WireGuard traffic remains open.

The history integration test uses a temporary certificate directory. The SQLite history worker and storage race selection passed as unprivileged Linux, and the authenticated Chrome history scenario passed.

The migration matrix passed all three engines on c164e01c. The expanded same-matrix repository test passed locally on migrated SQLite. Await expanded MySQL and MariaDB results.
