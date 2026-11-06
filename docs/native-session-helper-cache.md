# Native session/usage helper reuse

OpenVPN, L2TP and PPTP collectors now reuse decoded `usage-helper.json` maps through the bounded, content-verified native helper cache. OpenVPN and PPP quota checks use the same cache for immutable `session-helper.json` user/policy maps. Per-tick scratch fields are assigned to local struct copies; the cached maps remain read-only.

Every read still opens and hashes the current file. A changed policy is visible on the next check even when size and modification time are unchanged. Corrupt replacements return an error instead of using the previous quota policy. The existing cache is shared with WireGuard/AmneziaWG and remains bounded to 128 files / 8 MiB of source JSON. No new polling interval, command, persistence delay or accounting format is introduced.

Live status files, counters, PPP active/final records, process identities, pending batches and ACK/reflection state are not cached by this change. Their freshness and crash-recovery paths remain intact.

## Measurement

`BenchmarkNativeHelperQuota1000Users` measures the actual quota-preview paths with 1,000 configured users per inbound and no active sessions, isolating helper overhead. The OpenVPN case has one inbound; the PPP case has one L2TP and one PPTP inbound. Results below are medians of three runs of 200 iterations on Windows amd64, Intel i5-13400F. They are local microbenchmarks, not server CPU/throughput measurements.

| Quota path | Before ns/op | After ns/op | Before B/op | After B/op | Before allocs/op | After allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| OpenVPN | 3,121,742 | 884,170 | 1,130,483 | 141,030 | 9,082 | 1,994 |
| L2TP + PPTP | 9,940,448 | 1,898,792 | 2,301,201 | 329,774 | 14,406 | 231 |

The OpenVPN integration test warms the cache below quota, lowers the limit while restoring the original file timestamp, and confirms both active sessions are disconnected only after a durable accounting checkpoint. Additional tests corrupt a helper after warming each protocol's quota path and require an error. Existing preview tests prove ordinary quota checks do not write files; the full node suite also covers pending/replay/reflection and native session handling. Linux race and native protocol CI remain merge gates.
