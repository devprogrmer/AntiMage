# Offline Accounting and Local Quota Coverage

PR #84 is open and unmerged. On HEAD `e328b0e9`, Native Protocol E2E run
`37535911996` passed all eight native protocol jobs, combined SQLite
lost-ACK/replay, durable-state checks, and the Linux race job. Binary Build and
Database Migrations and PR Build also passed on that HEAD. Session/IP/device
and Xray enforcement gaps remain listed separately below. PR #84's current
checks are the authority for any documentation-only follow-up commit.

## Implemented Architecture

All eight protocols now have panel-independent periodic sampling, durable raw
logical totals, immutable pending batches, generation identity and local quota
policy. Serial workers coalesce overdue ticks; PPP shares one worker. Slow native
commands do not block other protocols or overlap within a worker.

`ANTIMAGE_NODE_QUOTA_ENFORCEMENT_INTERVAL` defaults to `100ms`, accepts `50ms`
through `2s`, and accepts `0`, `off`, `disabled`, or `false` to disable quota.
Accounting remains enabled. `ANTIMAGE_NODE_ACCOUNTING_CHECKPOINT_INTERVAL`
defaults to `1s`, accepts `1s` through `1m`; invalid values fail startup.

Scans use WG all-dump, AWG aggregate snapshots, OpenVPN status/management,
shared PPP interface counters, swanctl, occtl, and cached Xray gRPC StatsService.
They do not spawn one accounting command per user. Coincident checkpoint/quota
deadlines can perform two aggregate reads. Normal quota previews do not write
disk; periodic checkpoints and lifecycle/disconnect transitions do.

Generation keys use managed Xray starts, WG boot/interface and managed peer
transition fences, AWG interface/peer generations, OpenVPN process/session
starts, PPP process/interface/session identity, strongSwan IKE/Child-SA identity,
and ocserv process/session starts. Fixtures cover old 10GB plus restarted 11GB
equals 21GB even when the new counter has already exceeded the old value.
Controlled runtime stop/restart checkpoints before destroying native counters.
PPP normal disconnect records final daemon-supplied byte totals.

## Coefficients and Exactly-Once Delivery

Raw counters and batches are never multiplied. Panel database staging applies
`raw_delta * inbound_coefficient * node_coefficient` once. Node quota applies
the same product to unreflected raw usage and adds already-effective panel used
traffic without multiplying it again. A shared per-user view aggregates protocol
and inbound owners. Native snapshots are asynchronous, not an atomic snapshot
across independent daemons/nodes; reflection epochs refresh on later scans.

A durable root receipt maps combined/merged IDs to child raw usage before public
collection returns. ACKed bytes remain chargeable until the panel reflection
marker confirms inclusion. Reflection before a lost ACK does not count pending
bytes twice. Retries preserve original IDs and values.

Migration 61 adds durable database batch tombstones; queue cleanup marks identities
transactionally before deletion. Actual SQLite fixtures for all eight protocols
verify 500MiB, lost ACK, node/repository reload, retry, final 500MiB; raw 100MiB
with coefficients 2 and 1.5 yields 300MiB. Combined child-ACK-before-wrapper-save
recovery traverses panel staging: old 500MiB plus new 100MiB equals 600MiB.

## Policy and Failure Behavior

A checksummed mode-0600 snapshot stores validated desired JSON and per-user policy
before activation, and restores it before serving panel RPCs after node restart.
Exhausted credentials are filtered before activation. Explicit runtime stop
persists a stopped marker. This is desired intent, not proof that a failed native
apply was activated successfully.

Writes use temporary files, file fsync, atomic replacement and Linux directory
synchronization. Corrupt state is preserved/rejected. Receipt compaction retains
other owners' pending evidence; limits are 4096 receipts and 64MiB per accounting
file. Logical-series limits reject growth instead of pruning unreflected bytes.
Prolonged offline operation can reach these bounds and degrade service.

Storage/read errors emit changed-error logs and degraded health. Controlled
reset/removal is refused if required final accounting cannot persist. Periodic
failure does not suspend every protocol automatically: universal fail-closed
suspension is still unimplemented. Unsampled traffic destroyed by abrupt power
loss cannot be reconstructed from checkpoints.

## Acceptance Matrix

`Fully Implemented` requires native traffic with panel down, durable usage, local
quota, runtime and node restart, further traffic, reconnect, DB commit, lost ACK,
retry, exact totals and safe prune. IKEv2 has passed that complete native
sequence locally. The native protocol runs below join native traffic, runtime
restart, durable collector state, quota enforcement, reconnect handling, and
SQLite lost-ACK replay. Xray's existing-stream hard quota remains the one
technically unreliable capability in this matrix.

| Protocol | Offline Accounting | Runtime Restart | Node Restart | Offline Quota | Lost ACK Retry | Reconnect Reconciliation | Coefficient Match |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Xray | Fully Implemented | Fully Implemented | Fully Implemented | Not Technically Reliable | Fully Implemented | Fully Implemented | Fully Implemented |
| WireGuard | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |
| AmneziaWG | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |
| OpenVPN | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |
| L2TP | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |
| PPTP | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |
| IKEv2 | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |
| AnyConnect | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented | Fully Implemented |

Latest native evidence is from workflow run `37535911996` at commit `e328b0e9`:

- All eight native jobs and the combined
  `protocol-lifecycle-exact-once` job passed on this HEAD. Real protocol batches
  were replayed after node-state reload through SQLite; replay did not change the
  total, ACKs survived reload, and acknowledged batches were pruned. Final logged
  SQLite raw/effective totals included Xray 753,692 / 2,261,076 bytes, AmneziaWG
  17,606,904 / 52,820,712, L2TP 17,499,520 / 52,498,560, PPTP 17,596,834 /
  52,790,502, and IKEv2 17,478,016 / 52,434,048 bytes. The combined native
  WireGuard/OpenVPN lost-ACK test committed 105,629,069 raw / 316,887,207
  effective bytes across two batches; AnyConnect committed 17,487,360 /
  52,462,080 bytes. Coefficients were 1.5 × 2 where configured.
- The native 50 MiB threshold was 52,428,800 effective bytes. Cutoff totals in
  the latest run were WireGuard 52,560,596 raw (131,796 over its raw threshold),
  OpenVPN 53,331,003 raw (902,203 over), AmneziaWG 52,820,712 effective (391,912
  over), L2TP 52,498,560 effective (69,760 over), PPTP 52,790,502 effective
  (361,702 over), IKEv2 52,434,048 effective (5,248 over), and AnyConnect
  52,462,080 effective (33,280 over). AnyConnect's last quota run used
  15,268,904 raw quota bytes, delivered 14,468,380 tunneled bytes, and
  disconnected in 19,952 ms. A second real AnyConnect outer IP was rejected;
  the admitted client was 10.253.0.2 with assigned address 192.0.2.117.
- Native transfers measured 4 Mbps upload and 6 Mbps download with the
  configured production shapers. Average upload/download and one-second peak
  upload/download (Mbps) were: WireGuard 4.12/5.93, 5.31/6.79; AmneziaWG
  4.13/5.98, 5.37/6.82; OpenVPN 4.07/6.11, 5.28/7.32; L2TP 4.01/5.93,
  5.17/6.57; PPTP 3.98/5.88, 5.32/6.85; IKEv2 4.34/7.04, 7.54/11.34;
  AnyConnect 3.93/6.10, 6.36/10.78. The harness allows burst peaks up to
  twice the configured rate and checks average throughput within its defined
  tolerance. Xray per-user speed remains `Not Technically Reliable`.
- `native-ikev2` verified two real outer IPs separately from assigned tunnel
  addresses and passed the session/IP limit scenario. `native-l2tp` persisted
  two real session records through the panel API and confirmed disconnects with
  the assigned PPP address. Xray real VLESS traffic verified online/offline IP
  reporting and the existing-stream limitation; SQLite totals were 753,692 raw /
  2,261,076 effective bytes. User removal revoked future authentication but did
  not stop the already-authenticated stream.

On tested code HEAD `e328b0e9`, `native-race`,
`protocol-policy-and-durable-state`, Binary Build, Database Migrations, and PR
Build all passed. The comprehensive checks on any later PR HEAD must also pass.

## Identity and Quota Evidence

WG/AWG identity is public-key credential; OpenVPN is credential/session; PPP is
credential/session; IKEv2 is credential/SA/session; AnyConnect is credential/session;
Xray is credential email plus online remote IP. These are not hardware identities.
Shared keys/credentials cannot identify distinct physical devices. Certificate
fingerprint tracking is not added here. DeviceLimit and IPLimit propagate
separately; tests cover 1/1, 2/1, 1/2. IPLimit counts real remote IPs, not virtual
tunnel addresses or fabricated session addresses.

Synthetic WG traffic advances 7MiB per scan against a 50MiB limit: removal at
56MiB, 6MiB overshoot. IKEv2/AnyConnect fixtures with coefficient product 3 remove
at raw 17MiB, effective 51MiB: 1MiB effective overshoot. These are fixture values,
not measured VPS high-speed latency. Polling overshoot depends on traffic rate,
interval, command latency and disconnect completion; it cannot guarantee zero.

## Remaining Limitations and Verification

- Disappearing native sessions between samples can lose the unseen final tail.
  PPP normal disconnect has final byte hooks; abrupt crashes do not. OpenVPN,
  IKEv2 and AnyConnect require native final-byte event integration for no-tail-loss.
- External WG peer recreation entirely between observations on the same interface
  can evade managed generation fences.
- Real Xray v26.7.11 VLESS testing proves HandlerService user removal succeeds
  while an already-authenticated stream continues transferring bytes. Hard quota
  requires core stream termination, not just authentication removal.
- Xray's `HandlerService.RemoveUser` removes credentials but does not terminate
  authenticated streams, confirmed by native VLESS traffic after removal.
  `nftables`/`tc` can shape marked sockets, and Xray routing can select outbounds
  by user, but reliably applying per-user marks requires rewriting or cloning
  arbitrary user routing/outbound behavior. A cgroup mark would cover the shared
  Xray process and affect unrelated accounts; whole-inbound shaping has the same
  problem. No safe transparent per-user flow mapping is implemented, so hard
  cutoff and per-user speed enforcement remain `Not Technically Reliable`.
- Local `go test ./... -count=1` passes, including nodeagent, nodecontroller, API,
  migrations, SQLite lost-ACK/coefficient/combined recovery and device/IP tests.
- The real pinned Xray VLESS test passes positive native stats, actual online IP,
  idle online state, removal limitation and final offline detection. The temporary
  binary was digest-verified before execution; installed services are unchanged.
- Local WSL validation now exercises the real IKEv2 provisioning and strongSwan
  runtime with native tunnel traffic, CHILD/IKE rekey, runtime restart, offline
  quota enforcement, node-side durable accounting, SQLite panel staging, lost-ACK
  retry, and nft upload/download shaping. The measured quota overshoot was 5,248
  effective bytes at 52,434,048 bytes against a 52,428,800-byte threshold.
- Local WSL native drivers now invoke the production nodeagent accounting and
  quota paths against live WireGuard, OpenVPN, and AnyConnect daemons. WireGuard
  checks real kernel counters, durable pending-batch replay across interface
  restart, ACK, post-ACK traffic, and native peer removal at quota. OpenVPN
  checks status-v3 counters, durable batch replay across nodeagent reload, native
  management `client-kill`, and client reconnect. AnyConnect checks live `occtl`
  counters, durable batch replay, quota disconnect, and reconnect through
  ocserv. These improve native coverage but still do not prove the full panel DB
  reflection and ACK sequence for all three.
- GitHub Actions runs the strongSwan native dataplane with `charon` directly
  inside its private network namespace. The `ipsec start`/starter launcher did
  not create a VICI daemon in that systemd-free PID namespace, so the native CI
  job does not claim to verify that launcher. Production IKEv2 apply remains
  covered by local WSL native provisioning and the Go lifecycle tests.
- The AnyConnect native run exposed and fixed a production config issue: ocserv's
  worker IPC socket and its `occtl` management socket are separate. Runtime
  config now sets `socket-file`, `occtl-socket-file`, and `use-occtl` explicitly.
- That local IKEv2 run did not start the full panel and node transport services
  or cover MySQL/MariaDB. Privileged CI now exercises all eight native daemons
  and SQLite replay in run `37535911996`; database matrices separately passed in
  run `37535912170`.
- Dashboard source is unchanged in this increment; no fresh local dashboard run.

Primary references: [StatsService schema](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto),
[HandlerService schema](https://github.com/XTLS/Xray-core/blob/main/app/proxyman/command/command.proto),
[pinned Xray release](https://github.com/XTLS/Xray-core/releases/tag/v26.7.11).
