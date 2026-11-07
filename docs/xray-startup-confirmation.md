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
After the guarded policy/config or quota revocation has been durably committed,
the owned transition completes with its bounded startup window even if the
request expires at that boundary. A real-core regression cancels the sampling
context during file sync and verifies that the previous active stream still
closes and exact-once accounting remains intact.

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

## Recovery authentication and quota bookkeeping

Restored revocation markers now interpret SS/Hysteria `users` aliases using
the same non-null `clients` precedence as quota filtering. Previously a valid
alias user was marked already revoked, allowing a later quota check to skip
removal. Unit tests reproduced that failure. Real-core tests now restore active
alias users, transfer data, exhaust their quota and verify that neither their
credentials nor the shared server key can authenticate afterward.

For panel-managed Hysteria listeners, offline activation also removes the
transport's static `auth` fallback while preserving per-user authentication,
TLS and other transport options. A real-core RED test showed that deleting the
last live users through HandlerService enabled that shared auth before a
subsequent restart. The fixed test denies it even in that interval. Intentional
unmanaged static-auth listeners retain their original behavior.

## Recovery after an inbound port change

For a change to one numeric inbound port, the node retains the previously
applied policy on disk until startup succeeds. Recovery requires the running
config to match that policy, the guarded native policy to be identical, and
all other Xray config values to remain the same. If the replacement fails,
the node reevaluates offline quotas and expiry, restarts the prior listener,
and returns the original update as failed without advancing its revision.
The real-core test covers an occupied port, offline startup from the retained
policy, and a successful retry after the port becomes available.

For other changes, the guarded desired policy remains durable. If a replacement
fails while removing an account or changing access rules, the node keeps the
latest restrictions and may leave Xray stopped. The real-core test verifies
that an update removing a credential cannot restore the earlier listener.
Existing sessions can disconnect during a failed restart and recovery.
