# Node maintenance startup recovery

Phase B remains **In Progress**. This document describes current recovery
behavior and outstanding acceptance requirements, not a completion claim.

## Persistent boundaries

Node operation metadata stores `phase_started_at`, `restart_requested_at`,
`reconnect_deadline`, `health_deadline`, `last_recovery_attempt_at`,
`recovery_attempt_count`, and `recovery_error`. The restart boundary and absolute
deadlines are persisted before a restart-producing RPC. Waiting never grants a
new reconnect window. Process and sample timestamps must be newer than that
boundary. A successful update commit additionally hashes the installed binary
and compares its size and checksum with the immutable target.

## Phase policy

| Persisted phase | Classification | Current action / remaining requirement |
| --- | --- | --- |
| queued, preflight, resolving_version | safe_to_retry | Retry the persisted immutable target only when no restart dispatch boundary exists; otherwise inspect external state. Untouched rollout children remain with their scheduler. |
| downloading, verifying | must_reconcile_external_state | Verify fresh runtime or recover through a verified backup. Operation-owned partial/complete artifact inspection and safe download continuation are still required. |
| backing_up | must_reconcile_external_state | Never overwrite an existing operation backup. Backup state inspection before resuming this phase is still required. |
| installing | must_reconcile_external_state | Never blindly reinstall. Fresh target runtime and installed checksum may commit; otherwise only a verified backup may be restored. Disk-installed target plus old process restart recovery remains to be implemented. |
| restarting, waiting_for_reconnect | must_reconcile_external_state | Continue only inside the persisted absolute reconnect deadline; reject stale process/heartbeat evidence. On failed update verification, validate the original backup before automatic rollback. |
| verifying_version, health_check | must_reconcile_external_state | Require fresh healthy target runtime and exact installed bytes. Commit uses the persisted health deadline. |
| rolling_back, rollback_restarting, rollback_verifying, restoring | must_reconcile_external_state | Verify fresh previous runtime inside its original deadline. Node-side rollback job inspection, partially applied restore continuation, and restored-disk identity checks remain required. No blind duplicate restore is dispatched. |
| validating_backup | must_reconcile_external_state | Backup validation and dispatch reconciliation must be completed before this phase can resume automatically. |
| unknown phase / missing essential metadata | terminal_failure | Persist explicit recovery/rollback error, then atomically persist failure and release the reservation. No fabricated success or destructive retry. |

## Startup and locks

Startup reads all active generic operations without a history limit, including
standalone node updates and rollbacks. Interrupted rollout children use the same
reconciler. Existing active locks are retained, missing reservations are reclaimed
through exclusive creation, and terminal transitions release their reservation
in the same transaction. Missing, terminal, or incorrectly targeted lock owners
are repaired transactionally with an `orphan_lock_repaired` transition event.
Repeating that repair does not create another event once the orphan is removed.

An in-process worker registry prevents duplicate recovery workers on one
controller. Migration 67 adds a separate durable executor lease with executor ID,
acquisition/expiry timestamps, monotonically increasing generation and write
revision. Acquisition/takeover use an atomic conditional database write; the
database clock determines expiry. A write to the lease row fences operation
state changes in the same transaction. Unleased node mutations cannot override
an owner. Node update, manual rollback and startup recovery acquire the lease;
workers renew it every ten seconds and cancel execution on failed renewal.
Renewal changes no business deadlines. A crashed worker's 45-second lease can be
taken over without removing its resource reservation. Automatic recovery stops
after three persisted attempts. Migration 68 adds a durable generation and owner
per target resource. Every lease acquisition/takeover advances that resource
generation atomically while verifying the operation's exclusive target lock.
Fenced saves and renewals write-lock both the lease and resource generation rows;
a previous operation cannot retain authority after a new operation owns the same
target. Resource generation does not reset when the operation ID changes.

Focused SQLite tests cover independent database connections and independent
controller instances, lease takeover, stale success/renewal/release rejection,
transactional rejection of unleased node mutation, and unchanged reconnect
metadata. This proves database ownership, not complete remote execution fencing.
Node service-update RPCs now carry the fence operation ID, command ID, resource
ID, lease generation and resource generation; acknowledgements echo those fields. The controller validates its
lease before dispatch and after receiving the response, then validates the exact
echo. A persisted node CLI journal rejects old generations after agent restart.
Queued update/rollback jobs inherit those fields, take a persistent execution
lock, and check the journal before any backup/install/restore/restart action.
Completed commands cannot execute again, including after a generation takeover.
The rollback watchdog carries the same fencing evidence. The persistent node
journal also keeps a resource-global generation and owner across operation IDs.
Its generation lock is separate from the long-running execution lock, so a newer
command can supersede an old job paused between boundaries. Activation, install
metadata, restore, rollback restart and service restart run inside a boundary
guard that checks the current resource owner while holding the generation lock.
A command already inside one boundary is not preempted halfway through that
action; new acceptance waits for that boundary or its bounded process timeout.

Focused evidence includes the real RPC handler plus production journal (systemd
enqueue substituted), separate Linux processes using the embedded job guard,
and delayed-result rejection after database takeover. The guard harness counts
one protected action, not a full binary install/restart lifecycle. Cross-operation
update/rollback ownership and a paused old job superseded before activation have
focused SQLite/Linux coverage. Shared legacy RPC fencing and limited runtime
reconciliation now have focused coverage (see `node-destructive-resource-audit.md`).
Interruption inside a destructive command and full service-manager crash
recovery still require proof. MySQL/MariaDB lease
semantics now have focused local evidence below. The real remote lifecycle still
requires acceptance evidence.

## Acceptance limits

The original four expired-operation cases now have explicit reconciliation for
unrecoverable metadata. Adjacent missing-metadata phases, repeated startup,
orphan repair, unchanged expired deadlines, installed byte verification, and
recovery diagnostics have focused tests. These do not prove the complete Linux
update/rollback lifecycle, partial download/install recovery, or a bounded
production watchdog. The full lifecycle/failure matrix, semantic audit coverage,
all database backends, final full Go/race/frontend suite, and same-SHA CI remain
release gates. Nothing here authorizes marking Phase B complete.

## Operation-owned artifact cache

The embedded downloader stores exact target metadata and raw artifacts under
`.maintenance-artifacts/<operation_id>` in the installer directory. Independent
processes revalidate the full bytes before reuse. A fully written partial file
can be promoted without another request; an incomplete file is removed only
from that operation's directory and the identical target is downloaded again.
Corrupted completed bytes and changed target metadata fail before extraction.
Download deadlines persist in the cache record and expired incomplete downloads
fail immediately. Private cache and file locks reject symlink state paths.

The TLS helper harness passes the original 14 validation cases plus 14 recovery
invocations plus actual downloader termination during a partial TLS download.
It counts network requests to prove complete-cache reuse and checks
that sibling operation files survive partial cleanup. This proves the production
downloader helper, not controller termination followed by a full install/restart
lifecycle. Those crash boundaries remain part of the Linux acceptance gate.

## Local focused evidence, 2026-10-09

The current uncommitted Phase B tree passes the focused race suites for
operations, nodecontroller, nodeagent, system and process cancellation. The API
ownership diagnostic test covers missing, terminal and mismatched lock owners,
exhausted recovery, secret omission, and retaining the unknown-outcome lock while
repairing definite orphans. These are not final whole-repository acceptance runs.

Real local MySQL 8.4.11 and MariaDB 11.8.6 pass migration verification through
version 69. Independent database pools assert distinct server connection IDs.
Five operation-type conflicts enforce one resource winner, live-lease exclusion,
monotonic takeover, stale database-write rejection and cross-operation resource
generation. The database-only matrix passed ten repetitions on each backend.
The expanded test also runs the installed Node fencing helper after takeover and
rejects the old Node command on both backends. CI service versions remain separate
acceptance targets; no remote workflow result is claimed here.

Migration 69 adds structured event type and a JSON evidence payload. Lease
acquisition/takeover, unknown outcomes and reconciliation retain operation,
request, actor, target and executor identity. A regression test excludes arbitrary
secret/config metadata. Semantic coverage for all CLI and rollout paths remains
unfinished.

The exact embedded install and restore helpers each pass twelve fresh-process
crash cases across Node and Panel files, including a partial multi-file commit.
They verify actual bytes, size, mode and ownership, retain the verified backup,
and reuse an already committed replacement without changing production inode or
mtime. Durable original deadlines prevent unfinished transactions from receiving
a new timeout after restart. These tests inject process exits at rename boundaries;
they do not simulate power loss or prove the complete controller/database/service
restart and health lifecycle.

Legacy destructive RPCs now reject missing fencing/idempotency capabilities before
dispatch. Verified binary update preflight requires those explicit capabilities.
Non-binary native config actions use the same filesystem resource journal without
claiming binary replacement capability. Runtime RPCs reject an absent fence in
every install mode. A native/CLI test proves cross-origin stale rejection and
completed-command deduplication. Connected legacy agents receive a capability
warning in diagnostics. Full compatibility UI and transport/lifecycle proof remain
acceptance work.

## Additional local evidence, 2026-10-10

The root Linux lifecycle harness builds the shipped Node and Panel executables
with distinct A/B versions, downloads exact artifacts over local TLS, uses the
embedded backup/install/restore helpers, and runs isolated systemd units in
private network namespaces. Six install interruptions and six restore
interruptions per service recover to fresh B/A processes with matching actual
executable bytes. Completed-helper repetition preserves inode/mtime and the
original transaction deadline. This covers real rename/process interruption,
not power-loss durability.

Panel restore cases also seed actual persisted operations/locks before startup.
The restarted gateway consumes the filesystem evidence, reports the completed
operation through the authenticated API, retains the initiating request ID and
releases its lock without repeating restore. Both maintenance information and
system statistics report the injected running build version.

A Linux Go integration test reconstructs the Node controller against migrated
SQLite and connects to the production agent over TLS gRPC. Production restore
files and an actual runtime worker allow the controller to complete rollback
without a second destructive RPC or executable replacement. The worker is an
isolated fixture; shipped Node executable lifecycle is covered separately by
the systemd harness above.

Native daemon stop tests cover forced parent/group termination and a graceful
supervisor exit whose worker ignores the signal. Actual workers are killed and
reaped for OpenVPN, AnyConnect and PPTP stop paths, using shell fixtures without
launching native VPN services or PPP devices. External IKEv2 and L2TP helpers
use owned process-group cancellation.

Structured audit reads are bounded and sanitized. Identical rejection evidence
is deduplicated across database sessions without changing operation state;
orphan repair records detection and repair once. Browser maintenance status,
history and websocket snapshots exclude download URLs and raw command output,
while retaining state, versions, request IDs and numeric progress. Durable
recovery payloads remain intact on the server.

MySQL 8.4.11 and MariaDB 11.8.6 migration and five cross-operation fencing cases
passed again locally. Dashboard tests passed 104/104. These focused results do
not replace the final exact-snapshot whole-repository/race/frontend acceptance
or final-SHA CI. Phase B remains In Progress and unpushed.

## Primary-worktree acceptance, 2026-10-10

The whole repository passed `CGO_ENABLED=1 go test -race ./... -count=1 -timeout=45m` in the primary worktree after the Node rollback ownership and Panel unknown-installer-outcome fixes. API took 953.089 seconds; no bcrypt-heavy tests were excluded. The full normal `CGO_ENABLED=1 go test ./... -count=1 -timeout=45m` also passed in the primary worktree; API took 115.721 seconds.

The pinned Xray 26.7.11 Linux artifact was checksum-verified against the CI pin, then `TestRealXrayOnlineAndStatsE2E` passed locally under the race detector using that binary.

The exact Phase B dashboard snapshot has 102 tests; the earlier 104 count included two unrelated local WireGuard tests, which are preserved outside this phase. Production build, lint and seven browser checks cover LTR/RTL desktop, both mobile drawer directions, rollback confirmation and frozen rollout review.

The shipped Node lifecycle harness now also starts and observes a real runtime worker through the production fenced RPC. The worker is a shell fixture, not a native VPN or a claim of Xray protocol connectivity. Legacy TLS integration proves connection/visibility with unavailable identities, no queued sync mutation and no destructive dispatch. Four network helper tests prove actual parent/child group cancellation and reaping. Recovery startup adds a persistent, deduplicated structured audit event. Real MySQL/MariaDB tests additionally preserve case-sensitive command identities while deduplicating identical audit evidence across database sessions.

Final pushed SHA, PR and required CI evidence are recorded in the PR after publication; this local acceptance section alone is not a Phase B completion claim.

Final diff review also corrected the manual Node rollback failure path: once restore was dispatched, transport or health errors retain the resource reservation and original restart deadlines for reconciliation. A stale executor cannot write a failure over a newer owner. Five regressions cover lost ACK, health timeout, preflight failure, stale ownership and the automatic restore recovery phase. The same ownership rule now covers a Panel installer returning a nonzero exit after a possible file commit; a focused race test proves that a conflicting restart remains blocked. Final whole-repository acceptance is rerun after this fix.
