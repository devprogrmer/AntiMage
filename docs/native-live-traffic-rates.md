# Native live traffic rates

WireGuard, AmneziaWG, OpenVPN, L2TP, PPTP and IKEv2 now populate the existing per-user live-speed response. Xray and AnyConnect retain their existing collectors.

## Sampling and units

The node observes directional counters already read by accounting/checkpoint/quota workers. No additional native command, stats RPC, atomic write or fsync is scheduled. Upload means bytes received from the client, download means bytes sent to it. Values sent to the panel are bytes per second, before billing coefficients. The existing panel handles presentation units.

Each successfully read complete snapshot replaces that inbound's display sample. Multiple peers and inbounds are added per protocol/user. A one-second minimum display window avoids repeated allocation and unstable rates on faster quota ticks. This does not delay financial checkpoints or enforcement. Failed/incomplete scans retain the last valid sample for at most five seconds; then it expires. A long sampling gap requires a fresh baseline.

The first observation, changed native generation, different owner or decreased counter produces zero for the first new window. Peer/session identity includes the existing interface/daemon/session generation. IKE rekey preserves the CHILD_SA identity when its native counters survive. Disconnecting sessions disappear after a successful complete snapshot, or expire after a failed scan. Display state is bounded and lives only in memory.

## Accounting separation

The final usage RPC creates a response envelope with fresh native speeds. It does not change the pending batch ID, usage samples, online-IP samples, delivery receipt, ACK state, durable baseline, reflection reconciliation or WAL. Retrying an unacknowledged batch can therefore show fresh display speeds without charging its volume again. A restart restores pending financial data, while live display rates start with a new baseline.

## Validation

- Deterministic unit tests cover elapsed time, direction, aggregation, resets, reassignments, stale expiry, out-of-order observations, saturating arithmetic and concurrent collection.
- Collector integration tests exercise all six paths, including immutable pending batch retries and OpenVPN restart recovery.
- An AWG test confirms unchanged counter snapshots do not gain durable writes just because the display window advances.
- Existing accounting/recovery/quota tests remain required, together with Linux race/native-protocol CI.

On a Windows amd64 i5-13400F, the warmed 1,000-user sampler measured 53,961 ns/op and 0 B/op, 0 allocs/op. A quota tick before the next display window measured 24.30 ns/op and 0 allocations. These are local sampler overhead measurements, not production CPU comparisons or a VPN throughput test.

## Operational limits

The display depends on successful existing worker scans. It does not inspect applications, decrypt traffic or identify websites. No production restart or deployment was performed to validate this change. After deploying a tested release, confirm live rates against a controlled directional transfer on each enabled protocol. The change can be rolled back by deploying the prior binary; it introduces no disk format or database migration.
