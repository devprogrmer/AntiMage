# Anti-Mage Network Intelligence project state

PR #147 remains Draft. Durable history starts only when both Network Intelligence flags are enabled. The writer polls at most four eligible nodes per tick and selects at most eight users per node in user-ID rotation. A separate worker prunes at startup and hourly. Per-node cursors are volatile and restart from the beginning. Timestamp keys make repeat writes idempotent.

Local tests passed for the collector loop against SQLite with a simulated node transport, API, config, the 100000-row boundary with concurrent requests to one repository, startup retention without traffic, and restart ordering. The worker transport fixture does not exercise a live Node Agent connection. Multiple panel writers are outside the tested storage contract.

CI run 45982003811 passed the isolated WireGuard VM. Database run 45982997192 passed all three jobs at e850e185. MySQL and MariaDB ran the migration 95 table/index and transactional insert/read/rollback checks. SQLite ran the worker, API and repository tests under the race detector. The later startup-retention and restart-ordering tests passed locally at 47ce4442. Their follow-up database run 45983224308 was queued when checked.

NEXT_ACTION is to verify the final-head database matrix, run a full worker-to-node staging test and measure persistence load at the configured bounds.
