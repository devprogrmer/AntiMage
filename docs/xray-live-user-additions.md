# Durable Xray user additions without restarting the core

Nodes advertise `durable_user_config_sync`. The panel sends complete credential and quota policy through `SyncConfig` for these nodes, including when an older prepared decision selected a targeted user RPC. The former AddUser/UpdateUser/RemoveUser no-op handlers return an explicit unsupported-operation error instead of falsely acknowledging a change.

The node checkpoints accounting and applies the offline quota guard before durably publishing the desired policy. A conservative config comparison allows live VLESS, VMess, Trojan, Shadowsocks AEAD, Shadowsocks 2022 and Hysteria 2 additions when every existing client and every other Xray setting are unchanged. It also retains the core for an identical config. Unsupported account fields, structural changes, removals and credential edits retain the existing full-apply path. This is not a general hot reload or a per-user active-stream cutoff implementation.

## Applying and retrying

The node uses Xray's HandlerService `GetInboundUsers` and `AlterInbound`/`AddUserOperation`. It verifies credentials and level on readback. An existing identical account makes a retry idempotent; an account collision with different credentials fails. If an older core lacks the read RPC, the node uses the previous full-apply path before attempting any live mutation. A partial RPC failure leaves the last completely applied config file intact and returns an error, allowing the next attempt to verify completed additions and finish the rest. It does not silently restart all users after a partial RPC error.

The guarded durable runtime policy remains authoritative for restart recovery. Once additions succeed, the core config file and applied revision are updated. The existing core PID, stats connection and accounting generation remain unchanged. Live application does not clear another user's pending quota-cutoff marker. Core mutations coordinate with quota processing, and config transitions are serialized from checkpoint through activation.

When a managed core process actually exits, the node clears its matching runtime pointer; an older process exit cannot clear a replacement process.

## Compatibility and limits

Deploy the matching panel before the node, or deploy both from the tested release. Older callers of the no-op RPCs now receive an explicit error. Existing old nodes do not advertise the new capability, so their historical behavior is unchanged until updated. No production deployment is included in this PR.

This fast path covers basic VLESS/VMess UUID accounts, Trojan passwords, multi-user Shadowsocks AEAD and 2022, and Hysteria 2, including VLESS vision and levels. A new credential that collides with any existing credential in the same inbound is refused. Hysteria UUID auth is compared using Xray's UUID lookup rule, which zeroes bytes 6 and 7; Shadowsocks 2022 Base64 keys are decoded before comparison. Shadowsocks legacy single-user to multi-user and Hysteria static-auth to first-user transitions retain full apply, since live AddUser would change how existing authentication works. Short custom UUID aliases, unsupported account fields and other protocols retain full apply. Disabling/removing an account or changing credentials may still restart Xray. Standard Xray's RemoveUser does not close existing streams; isolated quota cutoff remains a separate core capability project. This change must not be presented as solving that gap.

## Verification

- Config planner tests reject structural changes, removals, credential updates, duplicate emails, credential collisions (including Hysteria's normalized UUID aliases), unsafe legacy single-user transitions and unsupported account fields.
- In-memory gRPC tests cover verified readback, immutable applied config on partial failure, idempotent retry, durable restart policy and preservation of another user's pending quota cutoff.
- A migrated SQLite panel/controller test with authenticated TLS verifies that add/update/remove operations select durable SyncConfig for the advertised capability. Against the previous controller it fails with `user not found`, rather than falsely passing.
- `TestRealXrayLiveUserAdditions` runs VLESS, VMess, Trojan, Shadowsocks AEAD, Shadowsocks 2022 and Hysteria 2 against an actual Xray binary. It keeps a witness TCP stream open across additions/retries, authenticates the new account, asserts unchanged core PID and accounting generation, then performs a controlled restart and re-authenticates the new user while verifying the unacknowledged accounting batch remains unchanged.
- The first three protocols passed locally with official checksum-verified Windows amd64 binaries v26.3.27 and v26.10.31; all six passed with v26.10.31. Archive SHA-256: v26.3.27 `d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad`; v26.10.31 `2e04a9995a6f2ad1b28c0bb25c708ecb2695e43a1c9c7889c4b6ac2b48cf7a6a`.
- The existing bulk OnlineMap E2E did not pass against the older v26.3.27 core. Against v26.10.31, both real-core E2Es passed, including separate node/panel database processes, restart, lost-ACK replay, exact-once billing and traffic coefficients. The existing test also reconfirmed that RemoveUser leaves an existing stream open. No assertions were weakened to accommodate the older core.
- CI runs the real tests against its downloaded/checksum-verified Xray core on Linux, plus the full native/race/accounting suites.

No database or accounting format migration is introduced. Rollback uses the previous panel/node release together. Existing financial state and desired runtime-policy formats remain readable.
