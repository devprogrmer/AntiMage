# Node destructive resource audit

Status: shared API/CLI/RPC ownership, leases and journal fencing are implemented.
Local acceptance evidence is recorded below and in node-update-recovery.md;
publication and final-SHA CI remain required before Phase B completion.

All conflicting actions use the intended resource identity `node:<node_id>`.
The generation must come from `operation_resource_fences`, not a separate Core
or Geo counter. A controller-local mutex or an active-update query is not a
resource reservation.

## Conflict matrix

Every pair in the following group conflicts. Each action can replace a running
process, its executable or configuration, or stop the host containing it.

| Action | Production path | Destructive effect | Protection and evidence boundary |
| --- | --- | --- | --- |
| Node update / rollback | `host_actions.go`, `rollback.go`, installed node CLI | Binary replacement, restore, service restart | Shared ownership; file transaction and service recovery harnesses |
| Core update | `updateRuntimeNow` -> Node `UpdateRuntime` | Installer replaces Xray; `startXray` kills and starts runtime | Shared ownership and native boundary; bounded running-version reconciliation |
| Core restart | `restartNow` -> Node `RestartRuntime` | Stops Xray and native protocol runtimes, applies configuration | Shared ownership and native boundary; fresh runtime process evidence |
| Geo update / activation | `updateGeoNow` -> Node `UpdateGeo` | Renames staged assets into production, then calls `startXray` | Shared ownership; committed dataset hash and reload-only recovery |
| Node service restart | `restartServiceNow` -> Node `RestartService` | Queues installed `fenced-restart` CLI | Acceptance and execution guard; fresh reconnect required before release |
| Host reboot | `rebootHostNow` -> Node `RebootHost` | Queues installed `fenced-reboot` CLI | Acceptance and execution guard; fresh reconnect required before release |
| Start / reconnect / full configuration sync | `Connect`, manual `Sync`, queued `applyOperation`, missing-user fallback | Writes production config and calls `startXray`; applies native runtime | Shared ownership; config hash recovery without blind replay |
| Stop | `StopNodeRuntime` -> Node `StopRuntime` | Stops Xray and native protocol runtimes | Shared ownership; persisted stop receipt and actual process evidence |

Geo download staging alone can be non-conflicting only when it writes isolated,
operation-owned files and does not activate or reload. The current public Geo
operation includes activation and restart, so it belongs to the conflict group.

User mutations falling back to full configuration sync inherit that conflict.
Targeted user mutations need a separate audit before declaring them independent.
Tor/Windscribe/Psiphon maintenance must also be checked for configuration/restart
side effects before being classified as independent.

## Entry points that must share ownership

`runDurableCommand` checks `HasActiveNodeUpdate` before and after enqueue, but
those queries do not close the race between checking and dispatching.
`processSingleOperation` and `applyOperation` dispatch queued maintenance through
the `*Now` helpers directly, bypassing those checks. Global configuration fan-out
and startup synchronization also reach the runtime-mutating RPCs.

`maintenanceMu` is process-local and only covers Core/Geo handlers. It cannot
serialize them against delayed installed-CLI jobs or survive a Node restart.

These controller paths now call `executeLegacyNodeCommand`: it reserves the
same target, acquires its executor lease, persists a command identity and
absolute deadline, and carries resource generation to the Node. Results echo
target/command/generation and pass a fresh ownership check. Runtime status,
revision and capabilities projections also use a transactionally fenced write.
The native callback holds the shared execution and generation locks throughout
the action. Delayed service jobs check the journal at their systemctl boundary.

Runtime-mutating RPCs without fencing fail closed. Administrator operational
CLI requests enter the authenticated application API; installed service helper
commands validate ownership at destructive boundaries. Legacy TLS agents remain
connected and visible, but destructive actions require an agent upgrade. Missing
capabilities never imply remote fencing or safe takeover.

## Recovery requirement

A transport error after dispatch is an unknown outcome. It must retain resource
ownership for reconciliation, rather than enter the legacy blind retry queue.
Inspect installed bytes, command journal, process start and fresh reconnect
evidence. Do not infer that activation never happened from a missing ACK.

Regression coverage includes delayed activation versus newer owners, stale
result rejection, lost ACK and bounded recovery. Actual file and Linux service
interruption evidence is described below. This document alone does not replace
the final exact-snapshot race suite or same-SHA CI.

Focused tests cover cross-type reservations, resource generation increments,
stale native boundary rejection, command execution count, non-replay after an
interrupted native callback, config-hash reconciliation, stale samples, and
unchanged expired deadlines. `nodeagent`, `nodecontroller`, and `operations`
passed the local Linux race suite. These are focused evidence, not the complete
service-manager crash matrix or MySQL/MariaDB proof.
