# AntiMage audit remediation implementation plan

**Goal:** Close the security and operational gaps identified in the 2026-11-05 comparison, preserving VPN connections and durable accounting.

**Architecture:** Keep the existing Go/React structure. Deliver independently testable changes, starting with node control-plane authentication, then runtime continuity and observability. Do not deploy to production as part of implementation. An existing feature is not automatically rewritten merely because a competitor implements it differently.

**Tech stack:** Existing Go gRPC/TLS, native protocol workers, durable accounting and React dashboard.

**Spec:** The 2026-11-05 comparison report, source snapshots and local reproduction evidence. User authorized proceeding without further design questions.

## Constraints

- Preserve pending usage, ACK identities, reflection and traffic coefficients.
- No unauthenticated bootstrap or trust-on-first-client fallback.
- Existing install bundles use the same pinned certificate at both ends; preserve that authenticated path.
- Separate master certificates require an explicit trust file; a missing or malformed file must fail startup.
- Do not restart, stop, update or deploy Production during this work.
- Keep unrelated checkouts, caches and worktrees intact.

## Work items and acceptance gates

- [ ] Node TLS: reject foreign, absent, expired and changed client certificates; accept current bundles and explicit master pin; cover real TLS handshakes and startup errors.
- [ ] Xray user mutations: actual gRPC Add/Update/Remove, durable desired state, retry/reconcile, no global restart for user-only diffs. Observe uninterrupted witness traffic.
- [ ] Xray quota cutoff: close only exhausted users' streams. Investigate core capabilities; do not claim RemoveUser closes active streams or introduce a global restart as the solution.
- [ ] Runtime rollout: validate with target core, retain last-known-good state, rollback on startup failure, preserve accounting generation checkpoints.
- [ ] Native speed collectors: produce real per-user samples for WG/AWG/OpenVPN/PPP/IKEv2; verify reset/reconnect and time units.
- [ ] Native policy caching: avoid repeated helper JSON parsing while invalidating on actual file/config changes.
- [ ] Accounting health UI: last sample/checkpoint/ACK/reflection, pending bytes and degraded persistence states.
- [ ] Backup safety: encryption, preview/check, off-host destinations, retention, isolated restore exercise and crash-safe rollback.
- [ ] UI completeness: notifications inbox, scoped subscription placeholders, sales administration and independent API-key scopes.
- [ ] Subsequent capabilities: authenticated Prometheus, SSO/passkeys, PWA, tunnel orchestration and additional protocols with explicit runtime/client compatibility tests.

## Verification

For each item: demonstrate the original failure, implement the change, rerun focused tests and relevant race/build checks, review compatibility and document limitations. Mark complete only on new evidence; do not equate source availability or green CI with live Production validation. Track deferred items explicitly instead of declaring the whole audit remediated.
