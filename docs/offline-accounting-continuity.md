# Offline Panel / Node Accounting Continuity

Status: incomplete. This document is a coverage report, not a declaration that
the full offline continuity requirement is implemented.

## Implemented in this increment

Xray checkpoints its traffic counters independently of panel requests, every
second by default. Set `ANTIMAGE_NODE_ACCOUNTING_CHECKPOINT_INTERVAL` in the node
service environment to a duration between `1s` and `1m`. Invalid values prevent
startup rather than silently disabling accounting.

The existing Xray usage-state file now contains each last sampled native counter
and its cumulative logical total. A decrease in the native counter starts a new
counter epoch without discarding sampled usage from the previous epoch. Samples
and the outstanding immutable batch are persisted in the same atomic file, with
file sync before replacement. Pending batches are returned with the same ID and
values until ACK; newer usage stays in cumulative totals. ACK advances only its
own baseline and does not delete newer totals. Old pending batches seed the
logical counter migration from their own snapshot.

The collector includes durable series absent from the latest runtime response,
and can send their remaining usage even when its managed runtime is stopped.
Unchanged checkpoints do not write disk. One aggregate stats query runs per
checkpoint, with a timeout, no overlapping sampler runs, and cancellation/join
before runtime shutdown. No coefficient is applied to the stored raw counters.

Corrupt state fails loading and is preserved for investigation. Counter overflow
and more than 65,536 counter series return errors instead of silently pruning
unreflected bytes. This is a safety bound, not finished long-term compaction.

## Protocol Coverage

`Fully` requires the complete requested offline scenario, including independent
quota enforcement and restart/reconnect fixtures. `Partial` identifies retained
state or tested subpaths that do not meet that complete scenario. `Unsupported`
means the required independent mechanism has not been implemented in this PR.

| Protocol | Offline accounting | Runtime restart recovery | Node restart recovery | Offline quota | Reconnect reconciliation |
| --- | --- | --- | --- | --- | --- |
| Xray | Partial | Partial | Partial | Unsupported | Partial |
| WireGuard | Partial | Partial | Partial | Partial | Partial |
| AmneziaWG | Partial | Partial | Partial | Unsupported | Partial |
| OpenVPN | Partial | Partial | Partial | Unsupported | Partial |
| L2TP | Partial | Partial | Partial | Unsupported | Partial |
| PPTP | Partial | Partial | Partial | Unsupported | Partial |
| IKEv2 | Partial | Partial | Partial | Unsupported | Partial |
| AnyConnect | Partial | Partial | Partial | Unsupported | Partial |

WireGuard has persisted pending/carry accounting and live effective-usage
calculation; that does not supply a general independent offline quota scheduler.
The other native collectors persist pending batches but still return early with
an outstanding batch and lack the independent durable sampling added to Xray.
PPP interface counters disappear when an interface/session ends. OpenVPN status
and AnyConnect session counters are session-scoped; strongSwan child-SA counters
are replaced during SA lifecycle changes. WG/AWG kernel counters reset when peers
or interfaces are recreated. Each needs checkpointing and identity-aware recovery
before it can be marked Fully.

## Outstanding Acceptance Work

- Integrate independent sampling and bounded durable compaction for all seven
  native collectors, preserving per-user/inbound identity after sessions vanish.
- Persist and restore the last valid policy and runtime snapshot before serving
  traffic after node restart or machine reboot.
- Add a shared quota scheduler and cached aggregate sampling; verify offline
  50MB cutoff for every protocol using the panel's coefficient semantics.
- Checkpoint controlled runtime transitions before native counters disappear.
  Xray's current reset detection only identifies a lower counter. A reset that
  overtakes the previous sample before the next checkpoint requires runtime
  generation identity and is not solved by comparing counter values alone.
- Verify directory durability on machine reboot and define corruption/storage
  exhaustion enforcement. The current series limit returns an error; it does not
  implement a fail-closed access policy or aggregate compaction.
- Bound unsampled crash-window traffic with real runtime fixtures. A checkpoint
  cannot reconstruct bytes generated after the last sample when native counters
  are destroyed by an abrupt crash.
- Test actual panel staging/DB deduplication and coefficient reconciliation after
  lost ACK, including combined-wrapper failure and reconnect with newer counters.
- Add Linux data-plane tests for runtime restart, node restart, machine reboot,
  offline traffic, quota cutoff, and eventual ACK/pruning for every protocol.

## Verification

New fixtures exercise Xray traffic while a batch awaits ACK, lower native counter
reset, node state reload, stable resend, duplicate ACK, migration from an existing
pending batch, remainder collection with a stopped runtime, persistence failure
rollback, corruption preservation, counter overflow, series bounds, and interval
validation. These are node-side state fixtures, not live VPN or panel DB tests.
