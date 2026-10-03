# Offline Accounting and Local Quota Coverage

PR #84 remains Draft. The implementation and regression fixtures below are real,
but the complete native data-plane acceptance scenario has not passed for every
protocol. Separate unit fixtures are not a full end-to-end guarantee.

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
retry, exact totals and safe prune. No protocol has passed that whole native
sequence here. Passing DB/identity/policy fixtures is recorded separately.

| Protocol | Offline Accounting | Runtime Restart | Node Restart | Offline Quota | Lost ACK Retry | Reconnect Reconciliation | Coefficient Match |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Xray | Partial | Partial | Partial | Not Technically Reliable | Partial | Partial | Partial |
| WireGuard | Partial | Partial | Partial | Partial | Partial | Partial | Partial |
| AmneziaWG | Partial | Partial | Partial | Partial | Partial | Partial | Partial |
| OpenVPN | Partial | Partial | Partial | Partial | Partial | Partial | Partial |
| L2TP | Partial | Partial | Partial | Partial | Partial | Partial | Partial |
| PPTP | Partial | Partial | Partial | Partial | Partial | Partial | Partial |
| IKEv2 | Partial | Partial | Partial | Partial | Partial | Partial | Partial |
| AnyConnect | Partial | Partial | Partial | Partial | Partial | Partial | Partial |

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
- Reliable Xray per-user speed shaping remains unimplemented. Shared/multiplexed
  transports have no stable per-user kernel flow identity. Whole-inbound shaping
  would affect unrelated users; a core per-user limiter or isolated transport
  architecture is required.
- Local `go test ./... -count=1` passes, including nodeagent, nodecontroller, API,
  migrations, SQLite lost-ACK/coefficient/combined recovery and device/IP tests.
- The real pinned Xray VLESS test passes positive native stats, actual online IP,
  idle online state, removal limitation and final offline detection. The temporary
  binary was digest-verified before execution; installed services are unchanged.
- No live Linux native/reboot or MySQL accounting runtime validation was performed
  locally. Windows has no C compiler for Go race and no installed WSL distribution.
  Linux CI results are separate evidence, not inferred local success.
- Dashboard source is unchanged in this increment; no fresh local dashboard run.

Primary references: [StatsService schema](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto),
[HandlerService schema](https://github.com/XTLS/Xray-core/blob/main/app/proxyman/command/command.proto),
[pinned Xray release](https://github.com/XTLS/Xray-core/releases/tag/v26.7.11).
