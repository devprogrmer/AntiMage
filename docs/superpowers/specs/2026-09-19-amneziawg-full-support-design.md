# AmneziaWG Full Support Design

## Objective

Add `amneziawg` as a first-class AntiMage protocol from the dashboard through the controller and node runtime. It must use the official `amneziawg` Linux kernel module and AWG 1.0 parameters. It must never alias or fall back to WireGuard.

## Completion Standard

A registry entry, form, serialized setting, or mocked process does not prove protocol support. The feature is complete only when the corresponding behavior is exercised at its real boundary.

- Unit and integration tests prove normalization, API contracts, device allocation, export, accounting, enforcement, and provisioning decisions.
- An Ubuntu 24.04 live test must prove module installation, interface creation, configuration, handshake, data plane, DNS, accounting, online state, quota enforcement, reconnect blocking, and offline transition.
- Without that live test, kernel runtime, provisioning, and end-to-end status remain explicitly unverified.

## Protocol Contract

The canonical protocol name is `amneziawg`. The default public listener is UDP port `51821`. An inbound owns a unique `awg<N>` interface and a unique internal tunnel port when TProxy routing is enabled.

AWG settings are independent from WireGuard settings and include:

- IPv4 pool, server address, DNS servers, MTU, persistent keepalive, NAT/TProxy mode, accounting flag, and internal tunnel port.
- Server private and public keys.
- Optional per-device preshared keys.
- AWG 1.0 values `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, and `H1` through `H4`.

Defaults are `Jc=4`, `Jmin=8`, `Jmax=80`, `S1=77`, and `S2=90`. The backend generates four distinct non-reserved magic headers. Validation requires `Jc >= 0`, `0 <= Jmin <= Jmax`, non-negative `S1/S2`, four valid distinct headers, a valid private IPv4 CIDR, a server address inside that CIDR, valid base64 keys, and non-conflicting listener/tunnel ports.

## User And Device Model

Each user has device records scoped to an inbound. Every device owns a private/public key pair, optional PSK, and one tunnel IPv4 address. Device zero may adopt a legacy top-level credential once; subsequent writes use the device array.

Increasing the device limit creates missing slots. Lowering it removes surplus peers from desired runtime state and prevents their reconnect. A user disabled, expired, deleted, or over quota contributes no desired peers. Usage from all device peers aggregates to the account identity.

## Runtime Architecture

The controller includes normalized AWG settings and device peers in the existing native-runtime JSON payload. The nodeagent parses AWG into an independent runtime type.

The Linux runtime:

1. Ensures the `amneziawg` module through lazy provisioning.
2. Creates `awg<N>` with `netlink.GenericLink` and link type `amneziawg`.
3. Assigns the server address and MTU.
4. Configures the listener, server key, AWG parameters, and peers through an AWG-aware wgctrl client.
5. Applies protocol-scoped NAT or TProxy/Xray routing.
6. Deletes stale peers and owned interfaces when users or inbounds disappear.

WireGuard keeps its current upstream control library, interface namespace, state files, and accounting. Shared helpers may cover pure validation, address allocation, and routing primitives only.

## Lazy DKMS Provisioning

The first enabled AWG apply checks for a loaded compatible module. If absent, the node installs the distro packages required for DKMS, build tools, and running-kernel headers, extracts the pinned official module source, registers/builds/installs it through DKMS, runs `depmod`, and loads it.

Provisioning is serialized and idempotent. A matching installed DKMS/module version skips rebuilding. DKMS registration supplies rebuilds for later kernel updates. Errors distinguish unsupported distributions, missing headers, Secure Boot rejection, DKMS build failure, module load failure, and unavailable privileges. No userspace or WireGuard fallback is allowed.

Vendored source retains upstream license and provenance. The production wrapper embeds a pinned source tree and exposes its version and source digest.

## Accounting, Online State, And Enforcement

The node reads real AWG peer counters and latest handshake timestamps from the AWG-aware control API. It maps public keys to device and user identities.

Counter deltas use persisted per-peer baselines and pending batches. ACK advances reflected state exactly once. Restarts recover baselines and pending usage without double counting. A fresh handshake reports online even when traffic delta is zero. The online window follows the existing native WireGuard policy.

Quota, expiry, disable, and device-limit enforcement remove peers from the live AWG interface and suppress them from unchanged desired state until the controller removes or changes the corresponding credential. Reconnect with a revoked key remains blocked.

## Routing

AWG supports the same operator choices as native WireGuard: direct NAT or TProxy into Xray, outbound selection, DNS handling, and accounting. Rules match only AWG interfaces and pools. Interface names, listener ports, internal tunnel ports, and state namespaces cannot collide with WireGuard or another AWG inbound.

## API, Export, And UI

CRUD accepts and returns canonical `amneziawg` settings without dropping unknown-but-supported AWG fields during edit or clone. Capabilities advertise UDP, native runtime, per-device configs, accounting, online state, quota enforcement, routing, PSK, and AWG 1.0 obfuscation.

The dashboard adds AmneziaWG to protocol selection and renders all protocol fields with frontend validation. Key and magic-header generation are explicit actions. Status, usage, and online indicators use existing server responses.

Export returns one importable config per device with `Address`, `DNS`, `MTU`, `PrivateKey`, `PublicKey`, optional `PresharedKey`, `Endpoint`, `AllowedIPs`, `PersistentKeepalive`, and all AWG 1.0 parameters. Existing QR support renders each device config.

## Testing And Delivery

Tests cover protocol normalization, validation, API serialization and CRUD, runtime payloads, interface naming, peer rendering, obfuscation parameters, per-device credentials and addresses, accounting deltas, ACK idempotency, restart recovery, online state, enforcement, multi-user/device/inbound operation, AWG and WireGuard coexistence, port conflicts, export, provisioning decisions, and dashboard round trips.

Required final checks are the full Go suite, all dashboard tests, dashboard build, available lint/typecheck commands, and `git diff --check`. The branch is `feat/amneziawg-full-support`; the pull request targets `dev` and must not be merged as part of this work.

