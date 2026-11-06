# Confirm Xray startup before acknowledging activation

`exec.Start` only proves that an executable was spawned. It can return success
while the real core immediately fails to bind a listener. Previously the node
could acknowledge that operation and advance its applied revision.

The node now waits for early process exits. For managed configurations with
`API_INBOUND` and `StatsService`, it also requires a successful, non-resetting
Stats RPC. The startup window is limited to five seconds and includes a 250 ms
initial process-liveness observation. Legacy/custom configs without that API
retain the liveness check; this does not claim complete protocol readiness for
those configs. A core can still fail later during normal operation.

Controlled stops wait for the owned process's `Wait` to finish, with a two-second
bound, before attempting a replacement. This avoids starting a new core while
the previous process is still releasing its listeners. The running core uses
a lifetime independent of the request context. An already canceled request is
rejected before stopping the old process. Once the replacement is spawned,
confirmation uses its own bounded window: a short quota-sampling deadline cannot
kill an otherwise healthy replacement. This adds no delay to durable accounting.

No new periodic polling is added. Startup probes never reset native counters;
the existing durable checkpoint still precedes a controlled process transition.

## Verification

`TestRealXrayStartupBindFailure` reserves a local TCP port and submits a valid
Xray config whose user listener collides with that port. Official Xray 26.10.31
accepts its `-test` preflight. Before the fix AntiMage acknowledged the failed
activation; after the fix it returns an error without advancing the revision or
claiming a running runtime. The test checks that a fresh node can read the same
pending usage receipt, releases the port, retries the same operation and confirms
working Stats RPC after the successful response.

The process-lifetime test also verifies that canceling the accepted request
leaves the actual helper process running and that stop returns after it is reaped.
The helper keeps a live timer; an empty `select {}` would let the Go runtime
terminate it as deadlocked and would not simulate a long-running VPN process.

The native Xray CI job explicitly runs the bind-failure test alongside its
live-user and exact-once accounting tests. Local Windows testing and Linux
native/race CI are separate evidence. No Production service was changed.

## Remaining recovery work

This change detects startup failure; it is not an automatic rollback transaction.
The guarded desired policy remains durable so offline recovery and a later retry
continue enforcing the latest restrictions. A failed replacement may leave the
core stopped. Restoring an older config without intersecting current credentials
and quota can revive revoked users, so full last-known-good recovery remains a
separate task. Never restore old accounting files to work around a bind failure.
