# AntiMage Source and Capability Matrix

This document defines how AntiMage combines the two upstream panels. It is a
working contract for implementation and release review.

## Provenance and licensing

| Source | Upstream | License | AntiMage obligation |
| --- | --- | --- | --- |
| Rebecca | `rebeccapanel/Rebecca` | AGPLv3 | Preserve copyright and license notices; publish corresponding source |
| VPN-UI | `Sir-MmD/vpn-ui` | GPLv3 | Preserve copyright and license notices; publish corresponding source |
| AntiMage additions | This project | Must remain compatible with the combined copyleft obligations | Mark modified files and retain source availability |

AntiMage is a combined work. No upstream source is silently renamed, stripped
of attribution, or represented as original AntiMage work.

## Capability ownership

| Capability family | Rebecca baseline | VPN-UI baseline | AntiMage integration boundary |
| --- | --- | --- | --- |
| Xray users and inbounds | Native Go services and React dashboard | Extended Xray/core support | Shared domain model with protocol adapters |
| Multi-node operations | Native | Primarily single-panel core management | Rebecca control plane remains authoritative |
| Subscriptions | V2Ray, Sing-box, Clash, ClashMeta | Broader protocol exports | Unified subscription service and format tests |
| Protocols | VMess, VLESS, Trojan, Shadowsocks | OpenVPN, L2TP/IPsec, PPTP, IKEv2, WireGuard, AmneziaWG, GRE, MTProto, SSH, AnyTLS, TUIC, NaiveProxy | Adapter registry with explicit lifecycle and health contracts |
| Roles and access | Admin/user and node-aware workflows | Multi-admin and reseller controls | One authorization boundary at the service/domain layer |
| Traffic, expiry, limits | Traffic and expiry controls | Device, speed, freeze, reseller metering | Shared accounting and idempotent state transitions |
| Core lifecycle | Xray-oriented | Xray plus system daemons and bundled cores | Desired/applied runtime state per adapter |
| Operator UX | Rebecca dashboard | VPN-UI redesign and bulk workflows | Unified dashboard, schema-driven forms, live status and audit |

## Integration rules

1. A protocol is integrated through an adapter, not by scattering daemon calls
   through handlers or UI components.
2. Desired state is persisted before planning, and applied state is persisted
   only after runtime activation succeeds.
3. Authorization and cross-caller invariants live at the service/domain
   boundary, not only in the dashboard.
4. Every imported workflow needs validation, permission checks, responsive UI,
   and an end-to-end test before it is called complete.
5. Existing assertions and security checks are preserved or strengthened.

## First implementation slices

1. Establish the unified protocol adapter contract and capability registry.
2. Bring VPN-UI protocol metadata into that registry without changing runtime
   behavior.
3. Add one end-to-end adapter path, starting with WireGuard or OpenVPN, with
   plan/apply/observe semantics.
4. Unify subscriptions, permissions, audit events, and live connection state.
5. Expand protocol coverage one adapter at a time with focused tests.

