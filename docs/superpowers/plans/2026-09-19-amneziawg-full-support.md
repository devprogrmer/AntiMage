# AmneziaWG Full Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver independent, kernel-backed AmneziaWG support across AntiMage's registry, API, node runtime, accounting, provisioning, export, and dashboard.

**Architecture:** Add AWG as a sibling native protocol with its own schema, runtime, state, and control client. Reuse only pure WireGuard-era primitives where behavior is genuinely common, while provisioning the official kernel module lazily through versioned DKMS source.

**Tech Stack:** Go, React/TypeScript, netlink, AWG-aware wgctrl, DKMS, nftables, Xray native tunnel routing, Vitest, Go testing.

**Spec:** `docs/superpowers/specs/2026-09-19-amneziawg-full-support-design.md`

## Global Constraints

- Canonical protocol is `amneziawg`; default listener is UDP `51821`.
- No aliasing or fallback to WireGuard.
- AWG state, interfaces, peer suppression, counters, and ports remain independent from WireGuard.
- Every device gets a distinct key pair and IPv4 address.
- Lazy DKMS provisioning must be serialized, idempotent, version-aware, and explicit on failure.
- Kernel/runtime/E2E completion requires a real Ubuntu 24.04 live test.
- The pull request targets `dev` and is not merged.

## Review Focus

- An AWG settings update that omits a field must not regenerate keys or magic headers.
- A removed or over-quota device key must not return after ordinary desired-state reconciliation.
- Counter rollback after interface recreation must not create a huge delta or lose an acknowledged pending batch.
- Concurrent first applies must execute one DKMS installation and give all callers the same result.
- AWG and WireGuard with overlapping user identities must not share state or suppress each other's peers.

---

### Task 1: Protocol Contract And Controller Payload

**Files:**
- Modify: `internal/protocols/registry.go`
- Modify: `internal/app/api/inbounds.go`
- Modify: `internal/app/api/inbounds_test.go`
- Modify: `internal/app/nodecontroller/server.go`
- Test: `internal/protocols/registry_test.go`

**Interfaces:**
- Produces: canonical `amneziawg` settings and normalized native-runtime JSON consumed by Task 3.

- [ ] Write failing table tests proving `amneziawg` normalization, UDP/51821 defaults, parameter ranges, key preservation, distinct H values, device records, and listener/tunnel conflict rejection.
- [ ] Run `go test -count=1 ./internal/protocols ./internal/app/api` and confirm failures name missing AWG support.
- [ ] Implement an independent AWG settings normalizer and validator, register capabilities, and include the settings unchanged in native payload construction.
- [ ] Run the focused tests and confirm they pass.
- [ ] Commit with message `feat: add AmneziaWG protocol contract`.

### Task 2: Device Credentials And Client Export

**Files:**
- Create: `internal/app/user/amneziawg_devices.go`
- Create: `internal/app/user/amneziawg_devices_test.go`
- Create: `internal/app/user/amneziawg_profiles.go`
- Create: `internal/app/user/amneziawg_profiles_test.go`
- Modify: `internal/app/api/users.go`

**Interfaces:**
- Consumes: normalized AWG settings from Task 1.
- Produces: `ReconcileAmneziaWGDevices` and `RenderAmneziaWGProfiles`, including one peer/config per device, consumed by Tasks 3 and 6.

- [ ] Write failing tests for device-zero migration, grow/trim behavior, stable keys on unrelated edits, distinct addresses, optional PSKs, and one complete AWG 1.0 config per device.
- [ ] Run `go test -count=1 ./internal/app/user` and confirm the new symbols are absent.
- [ ] Implement cryptographic key generation, deterministic pool allocation, reconciliation, revocation output, and profile rendering.
- [ ] Run user and API tests and confirm round-trip serialization preserves every AWG field.
- [ ] Commit with message `feat: add AmneziaWG device profiles`.

### Task 3: Independent Node Runtime

**Files:**
- Create: `internal/app/nodeagent/amneziawg_runtime.go`
- Create: `internal/app/nodeagent/amneziawg_runtime_linux.go`
- Create: `internal/app/nodeagent/amneziawg_runtime_stub.go`
- Create: `internal/app/nodeagent/amneziawg_runtime_test.go`
- Modify: `internal/app/nodeagent/native_runtime.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: native-runtime AWG payload and per-device peers from Tasks 1-2.
- Produces: desired `awg<N>` interfaces and an injectable AWG control boundary consumed by accounting and enforcement.

- [ ] Write failing tests for parsing, interface naming, multiple inbounds, peer rendering, all AWG parameters, update/remove/restart, duplicate ports, and simultaneous AWG plus WireGuard desired state.
- [ ] Run the focused nodeagent tests and confirm failure is caused by the missing runtime.
- [ ] Add the pinned AWG-aware control dependency and implement Linux netlink create/configure/delete behind narrow interfaces; add a non-Linux unsupported implementation for compilation and unit tests.
- [ ] Integrate apply ordering and cleanup without modifying WireGuard ownership or state.
- [ ] Run `go test -count=1 ./internal/app/nodeagent` and commit as `feat: add AmneziaWG node runtime`.

### Task 4: Accounting, Online State, And Enforcement

**Files:**
- Create: `internal/app/nodeagent/amneziawg_usage.go`
- Create: `internal/app/nodeagent/amneziawg_usage_state.go`
- Create: `internal/app/nodeagent/amneziawg_usage_test.go`
- Create: `internal/app/nodeagent/amneziawg_policy.go`
- Create: `internal/app/nodeagent/amneziawg_policy_test.go`
- Modify: `internal/app/nodeagent/server.go`

**Interfaces:**
- Consumes: AWG control boundary and peer identity map from Task 3.
- Produces: protocol-scoped usage batches, online IPs, persisted baselines/pending state, and live peer removal.

- [ ] Write failing tests for RX/TX deltas, zero-traffic online handshakes, offline windows, aggregate multi-device usage, ACK replay, restart recovery, counter rollback, disable/enable, expiry, quota, reconnect blocking, and WireGuard coexistence.
- [ ] Run the focused tests and confirm they fail on missing AWG collection/enforcement.
- [ ] Implement AWG-specific persisted state and suppression, mapping device public keys to account IDs while preserving the existing batch protocol.
- [ ] Run nodeagent usage and policy suites and commit as `feat: enforce AmneziaWG usage policies`.

### Task 5: Lazy DKMS Provisioning

**Files:**
- Create: `internal/app/nodeagent/amneziawg_source/source.go`
- Create: `internal/app/nodeagent/amneziawg_source/upstream/`
- Create: `internal/app/nodeagent/amneziawg_provision.go`
- Create: `internal/app/nodeagent/amneziawg_provision_test.go`
- Modify: `internal/app/nodeagent/amneziawg_runtime_linux.go`
- Modify: `THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Produces: `EnsureAmneziaWG(ctx) error`, called once per first enabled apply before link creation.

- [ ] Write failing tests with an injected command/filesystem boundary for already-loaded, matching-DKMS, missing headers, unsupported distro, Secure Boot, failed build, failed load, and concurrent callers.
- [ ] Run the focused tests and confirm provisioning does not exist.
- [ ] Move the pinned official AWG 1.0 source and license from the research tree into the production package; expose version and digest.
- [ ] Implement distro detection, package installation, source extraction, DKMS add/build/install, `depmod`, `modprobe`, locking, and version checks.
- [ ] Run provisioning and nodeagent tests and commit as `feat: provision AmneziaWG with DKMS`.

### Task 6: Routing, Subscription, And API Surface

**Files:**
- Create: `internal/app/nodeagent/amneziawg_routing.go`
- Create: `internal/app/nodeagent/amneziawg_routing_test.go`
- Modify: `internal/app/xrayconfig/virtual_tunnels.go`
- Modify: `internal/app/user/subscription.go`
- Modify: `internal/app/api/inbounds_test.go`
- Modify: `internal/app/api/users_test.go`

**Interfaces:**
- Consumes: inbound pools/tunnel ports from Task 1 and profiles from Task 2.
- Produces: NAT/TProxy rules, Xray dokodemo payloads, API CRUD responses, and per-device subscription artifacts.

- [ ] Write failing integration tests for NAT and TProxy modes, outbound/DNS routing, unique ports, cleanup, AWG plus WireGuard coexistence, CRUD serialization, profile download, and QR payload text.
- [ ] Run focused Go tests and confirm missing AWG routing/export behavior.
- [ ] Implement protocol-scoped routing and wire profile lists into API/subscription responses.
- [ ] Run affected Go packages and commit as `feat: route and export AmneziaWG profiles`.

### Task 7: Dashboard Experience

**Files:**
- Modify: `dashboard/src/utils/inbounds.ts`
- Modify: `dashboard/src/utils/inbounds.test.ts`
- Modify: `dashboard/src/components/InboundsManager/FormDrawer.tsx`
- Modify: `dashboard/src/components/HostsManager.tsx`
- Modify: dashboard translation resources containing inbound labels.

**Interfaces:**
- Consumes: canonical settings and profile endpoints from Tasks 1 and 6.
- Produces: protocol selection, create/edit/clone round trips, validation, status/usage/online display, and per-device config/QR actions.

- [ ] Write failing Vitest cases for defaults, serialization/deserialization, edit/clone preservation, parameter validation, key/header regeneration, and per-device profile actions.
- [ ] Run the focused frontend tests and confirm AWG is unavailable.
- [ ] Add the AWG form using existing controls and WireGuard workflow conventions while retaining independent values and copy.
- [ ] Run all frontend tests, typecheck/lint scripts, and production build; commit as `feat: add AmneziaWG dashboard`.

### Task 8: Full Verification And Ubuntu 24.04 E2E

**Files:**
- Create or modify: `scripts/e2e/amneziawg_ubuntu24.sh`
- Modify: `docs/superpowers/specs/2026-09-19-amneziawg-full-support-design.md` only to append measured verification evidence.

**Interfaces:**
- Consumes: the complete product path from Tasks 1-7.
- Produces: reproducible live evidence and final delivery state.

- [ ] Run `go test -count=1 ./...` with workspace-local Go caches when Windows permissions require it.
- [ ] Run every frontend test, lint/typecheck script, and build listed in `dashboard/package.json`.
- [ ] Run `git diff --check` and inspect branch status and full diff.
- [ ] If an Ubuntu 24.04 host with root and a compatible kernel is available, run the E2E script and record module/DKMS version, handshake, ping, internet, DNS, usage delta, online state, quota disconnect, blocked reconnect, and offline transition.
- [ ] If no live host is available, label provisioning, kernel runtime, and E2E unverified in the final report and PR; do not convert mocked or compile-only evidence into completion.
- [ ] Perform a whole-branch code review, fix Critical/Important findings with RED-to-GREEN tests, and rerun all gates.
- [ ] Commit final verified changes, push `feat/amneziawg-full-support`, and open an unmerged PR to `dev`.
