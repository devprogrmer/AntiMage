# AntiMage Platform Roadmap

This roadmap tracks the independent AntiMage control-plane work described in the platform requirements. PR #84 was merged into `dev` on 2026-10-11 at `41116ac35b49ff65c079f926099eee5c7fd6a1de`. Its accounting, quota and recovery behavior is the integration baseline and must be retained when rebasing the platform phases.

## Delivery order

Each phase should be a reviewable PR targeting `dev`. A later phase may temporarily target the preceding phase branch when it needs unmerged APIs, then be rebased and retargeted after the preceding PR is accepted and merged. The current integration task authorizes ordered merges of #85, #86 and #87 only after local acceptance, required CI on one final SHA and final GitHub diff review. Production runtime hardening follows a green unified baseline; Phase D remains deferred. Keep unrelated local files out of every commit.

| Phase | Scope | Current state | Exit evidence |
| --- | --- | --- | --- |
| A. Product foundation | AntiMage shell/primitives/navigation; Simple/Advanced mode; real-data Command Center; persistent diagnostics and UI; sanitized request errors and IDs; generic persistent operations; read-only Update Center/version catalog; no dashboard regressions. | Complete at `d6248dae50e411fcfaf131fab84de31b85c513d6`, merged as PR #85 (`6d3a56da7fc602d2338b5b3bcbc3ec51a7c008b3`). Exact-SHA full Go/race, migration, native, browser, binary and Docker CI passed. The Update Center reports unavailable node lifecycle versions where the current API cannot verify them. | Merged to `dev` after complete final-SHA acceptance and GitHub diff review. |
| B. Updates and version state | Build catalog; exact stable/dev selection; panel/node update state machines; checksum verification; rollback; restart/reconnect/version/runtime verification; persisted history; drift; locks; safe bulk behavior. | In Progress on `codex/antimage-next-phase-b`, being rebased on the merged A baseline. Local coverage includes shared CLI/RPC fencing, persisted executor leases and resource generations, exact artifact verification, install/restore recovery, bounded process cancellation, lost-ACK reconciliation, rollback retention, durable bulk/canary coordination, sanitized structured audit, ownership diagnostics and legacy-agent TLS compatibility. Final post-rebase local acceptance, PR review and exact-SHA CI remain gates. | Full Go/dashboard suites, migration upgrade tests, race checks, installer harness, and Linux/systemd lifecycle evidence including failed update and verified rollback. |
| C. Delivery and protocol profiles | Per-host client settings, presets, WireGuard advanced settings, client routing rules, subscription placeholder policies/engine, smart profile delivery, preserve existing URLs. | Not Started. Existing unrelated/local WireGuard dual-stack edits are preserved but are not Phase C acceptance. | Backend/API/runtime/UI coverage, compatibility tests for existing subscription links, protocol-specific validation and native connectivity evidence. |
| D. Access and policy | Policy engine, periodic quota cycles, sessions/devices/IP views and actions, accurate capability labels for protocols without reliable device identity. | Not Started. | Persisted policy evaluation, permission tests, live session/runtime evidence and explicit unsupported-state tests. |
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

Phase A was merged after exact-SHA acceptance. Phase B remains in progress and its rebased commit is not yet pushed.

## Cross-phase acceptance gate

Do not call a feature complete until its persisted state, service/API, authorization, dashboard flow, error handling, tests, documentation and applicable native/integration evidence are all present. UI-only and backend-only work does not pass this gate. Never label protocol capability as supported when runtime evidence is unreliable.

The current WSL environment has systemd and root execution through `wsl -u root`. Local evidence now includes shipped Node/Panel A/B processes, TLS artifact IO, twelve installation and twelve restore interruption cases, fresh-process/version checks and persisted Panel startup rollback reconciliation. Node controller/production-agent TLS recovery has separate integration coverage. Final exact-snapshot full acceptance and final-SHA CI remain required. Native PPP remains outside this phase.
