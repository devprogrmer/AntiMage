# Phase C access and policy acceptance

This phase reuses the persisted admin permissions, service assignments, quota
cycles and native session policy engine. It does not replace Phase B maintenance
or introduce Phase D delivery features.

## Device revocation contract

The authenticated device DELETE endpoint requires the persisted revoke permission
and the user's service scope. Authorization runs before mutation. Device deletion,
active session closure, the WireGuard revocation tombstone and the runtime sync
enqueue commit in one transaction. An enqueue failure rolls everything back.
A repeated request for the removed stable identity returns 404 and creates no
duplicate work.

WireGuard reconciliation must not recreate the credential-derived revoked peer.
The tombstone blocks that device slot until explicit credential rotation; it
stores a credential fingerprint, not a private key. Unlimited-device
reconciliation preserves remaining devices even when indexes have gaps.

Revocation syncs fan out to every enabled node, including disconnected nodes.
Their durable pending operations cannot dispatch until reconnect. A successful
fenced recovery sync acknowledges the queued revoke. Other global sync operations
retain their existing connected-node fanout behavior.

## Real runtime acceptance

`internal/app/api/phase_c_wireguard_e2e_test.go` launches production Panel and Node
executables against a migrated temporary SQLite database and temporary TLS
certificates. It authenticates through the real Panel API and uses production
TLS/gRPC handshake, fencing, queue and runtime sync paths.

The test first provisions three real peers, revokes one while connected, and
checks the successful persisted sync result. Both unrelated peers and the same
interface index must remain. It then crashes the Node, confirms the existing
runtime still contains the remaining target/control peers, revokes the target
while disconnected, and restarts the same Node. Acceptance requires actual peer
removal, preserved control peer/session and a successful correlated node operation.
Queue insertion alone cannot pass.

The dedicated `phase-c-wireguard-e2e` CI job uses Ubuntu and two runtime backends:
automatic kernel WireGuard selection and forced real `wireguard-go`. Network and
mount namespaces isolate the runtime and its sysfs ownership checks. If namespaces
are unavailable, temporary names, ports, directories and credentials still isolate
the test on its disposable runner. Process groups and interfaces are cleaned up.

Local acceptance has passed with both backends in namespaces. Final release
acceptance also requires this gate to pass on the final pushed SHA.

## Other acceptance gates

- API authorization tests assert 403 with unchanged device/session/tombstone/queue
  state for missing permissions and unassigned services.
- Transaction failure and repeated-request tests cover rollback and idempotency.
- Reconciliation tests cover revoked identity suppression, credential rotation
  and preservation of other devices.
- Legacy TLS/capability tests keep old agents visible without dispatching unsafe
  destructive commands or inventing runtime evidence.
- Full Go and race suites cover the existing lifecycle/reset policy engine.
- SQLite, MySQL and MariaDB migration/runtime tests preserve supported databases.
- Dashboard service-scope tests, production build, lint and seven browser tests
  cover permission-aware actions, desktop/mobile navigation and LTR/RTL behavior.

## Boundaries

The real runtime witness is WireGuard, including its userspace implementation.
AmneziaWG retains its existing device API capability; this witness does not claim
a new native AmneziaWG connectivity proof. Protocols without stable device identity
remain outside device revoke actions. Old agents need an upgrade for fenced
configuration mutations. A deliberately revoked WireGuard slot becomes available
again only after explicit credential rotation. Native PPP work and Phase D are
outside this phase.
