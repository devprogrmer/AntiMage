# AntiMage Platform Roadmap

This roadmap tracks the independent AntiMage control-plane work described in the platform requirements. PR #84 remains separate and is still open. No dependency on its APIs has been identified in the update work currently in progress; before a later accounting-related phase starts, compare it against the merged `dev` APIs.

## Delivery order

Each phase should be a reviewable PR targeting `dev`. A later phase may target the preceding phase branch when it needs unmerged APIs. Do not merge these PRs as part of this work. Keep unrelated local files out of every commit.

| Phase | Scope | Current state | Exit evidence |
| --- | --- | --- | --- |
| A. Product foundation | AntiMage shell/primitives/navigation; Simple/Advanced mode; real-data Command Center; persistent diagnostics and UI; sanitized request errors and IDs; generic persistent operations; read-only Update Center/version catalog; no dashboard regressions. | Complete at `8580ced8a346ab27417811b427fbdb739df46ef3`, PR #85 (open, unmerged). Primary-worktree race and final-SHA acceptance passed. The Update Center reports the available panel/catalog data and explicitly shows node lifecycle versions as unavailable where the current API cannot verify them. Update execution, rollback, and installer behavior remain Phase B. | Close every Phase A item in the status table below, push the Phase A branch, open its PR to `dev`, pass all required workflows on one SHA, and review the PR diff. |
| B. Updates and version state | Build catalog; exact stable/dev selection; panel/node update state machines; checksum verification; rollback; restart/reconnect/version/runtime verification; persisted history; drift; locks; safe bulk behavior. | In Progress on `codex/antimage-next-phase-b`, based on #85. Local coverage includes shared CLI/RPC fencing, persisted executor leases and resource generations, exact artifact verification, install/restore recovery, bounded process cancellation, lost-ACK reconciliation, rollback retention, durable bulk/canary coordination, sanitized structured audit, ownership diagnostics and legacy-agent TLS compatibility. Primary-worktree full race passed; final lightweight acceptance, commit/PR review and same-SHA CI remain release gates. | Full Go/dashboard suites, migration upgrade tests, race checks, installer harness, and Linux/systemd lifecycle evidence including failed update and verified rollback. |
| C. Access and policy | Centralized access-policy enforcement, periodic quota cycles, sessions/devices/IP views and actions, accurate capability labels for protocols without reliable device identity. | In Progress on `codex/antimage-phase-c-access-policy`. Existing quota lifecycle, session inventory and device/IP views are reused; this phase closes the device-revocation authorization and consistency gaps and adds end-to-end acceptance. | Persisted policy evaluation, permission tests, live session/runtime evidence and explicit unsupported-state tests. |
| D. Delivery and protocol profiles | Per-host client settings, presets, WireGuard advanced settings, client routing rules, subscription placeholder policies/engine, smart profile delivery, preserve existing URLs. | Not Started. Existing unrelated/local WireGuard dual-stack edits are preserved but are not Phase D acceptance. | Backend/API/runtime/UI coverage, compatibility tests for existing subscription links, protocol-specific validation and native connectivity evidence. |
| E. Fleet operations | Nodes, groups/clusters, health and telemetry, smart selection, failover, desired-state reconciliation, bounded self-healing, accounting visibility, event timeline. | Not Started. | Multi-node integration tests, failure injection, reconciliation/failover evidence and no fabricated metrics. |
| F. Security and recovery | Scoped API tokens, 2FA/WebAuthn where feasible, secret protections, certificate operations, GeoIP/GeoSite operations, backups and disaster-recovery verification. | Not Started. | Authorization/security tests, secret-redaction checks, certificate/asset lifecycle tests and restore verification isolated from production data. |
| G. Operator experience and scale | Command palette, global search, inline health, service wizards, Simple/Advanced modes, accessibility, large-installation performance and final visual differentiation. | Not Started. Simple/Advanced navigation is being established as a Phase A foundation; this phase remains deferred. | Keyboard/mobile/RTL review, permission-aware search, synthetic scale measurements, screenshots and full acceptance suite. |

## Phase A evidence status

| Item | Status | Evidence or boundary |
| --- | --- | --- |
| AntiMage-native shell, primitives, navigation and Simple/Advanced mode | Implemented | Dedicated shell and navigation model are present; browser smoke covers primary navigation in LTR and RTL. |
| Command Center and preserved legacy dashboard routes | Implemented | Existing route registrations remain; the Command Center displays unavailable values instead of inventing fleet or traffic aggregates. |
| Diagnostics collector, persistence, authorization and dashboard | Implemented | API integration tests cover refresh, source failures, resolution, acknowledgement, permissions and redaction. |
| Sanitized browser errors and request/correlation IDs | Implemented | Request-error unit tests and API request-ID tests cover sanitized metadata and response propagation. |
| Generic operations schema, store, API and history view | Implemented | Store/reopen tests and API route tests cover persistent records and request IDs. Concrete updater execution adapters remain Phase B work. |
| Read-only Update Center and version catalog | Implemented | Catalog provider tests cover stable/dev metadata; runtime, installed metadata and desired targets remain separate when reported. |
| LTR desktop, RTL desktop and mobile drawer smoke | Verified | Playwright checks render errors, navigation, overflow, technical-string direction, drawer side, accessible controls and dialog behavior. |
| Node version tuple on legacy nodes | Deferred | The page reports the lifecycle tuple as unavailable until the node API supplies verified desired, installed and running versions; it does not infer one version from another. |
| Update execution, checksum enforcement, rollback and rollout coordination | Deferred | These belong to Phase B and are not acceptance claims for Phase A. |

Phase A acceptance was completed on SHA `8580ced8a346ab27417811b427fbdb739df46ef3`; PR #85 remains separate and unmerged. Phase B is not complete and has not been pushed.

## Phase C evidence status

| Item | Status | Evidence or boundary |
| --- | --- | --- |
| Persisted admin/user policy and runtime enforcement | Existing foundation | Admin role permissions and user limits/reset strategy are stored in the existing schema; centralized user permission checks and native-session policy evaluation remain the source of decisions. |
| Periodic quota cycles and lifecycle transitions | Existing foundation | Lifecycle integration tests cover due resets, reactivation, next plans, expiry and queued node operations. |
| IP/session/device inventory and protocol capability boundaries | Existing foundation | Existing node/API tests cover cross-protocol sessions and device history. Revoke UI remains limited to WireGuard/AmneziaWG records with stable device IDs; other protocols are not presented as revocable devices. |
| Device revoke authorization and state consistency | Implemented locally | API tests verify missing revoke permission returns 403, successful revoke removes the device, ends its session and queues sync in one transaction, and queue failure rolls back all changes. Dashboard hides the action when the admin lacks permission. |
| Release validation | In progress | Full Go, dashboard, lint and browser smoke checks are being run on the Phase C branch; commit, PR and final-SHA CI remain. |

## Cross-phase acceptance gate

Do not call a feature complete until its persisted state, service/API, authorization, dashboard flow, error handling, tests, documentation and applicable native/integration evidence are all present. UI-only and backend-only work does not pass this gate. Never label protocol capability as supported when runtime evidence is unreliable.

The current WSL environment has systemd and root execution through `wsl -u root`. Local evidence now includes shipped Node/Panel A/B processes, TLS artifact IO, twelve installation and twelve restore interruption cases, fresh-process/version checks and persisted Panel startup rollback reconciliation. Node controller/production-agent TLS recovery has separate integration coverage. Final exact-snapshot full acceptance and final-SHA CI remain required. Native PPP remains outside this phase.
