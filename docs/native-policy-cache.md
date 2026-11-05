# Validated native policy snapshots

IKEv2 and AnyConnect quota/checkpoint workers previously parsed the entire persisted native runtime JSON on every scan, including unrelated protocols' users. Native JSON is now decoded when a runtime policy snapshot is successfully validated and published, or loaded and checksum-verified after restart. Workers read that immutable parsed payload. Xray's existing revision/checksum index also reuses it when rebuilding its index.

Each replacement creates a new parsed snapshot, including a changed checksum at the same revision. Failed writes or invalid input do not publish the new policy. A stopped snapshot shares its immutable payload. There is no lazy mutation, background refresh timer or new file format. Nested slices/maps must remain read-only; callers that need to modify them must copy first.

The cache retains one current decoded policy in memory, trading that resident data for avoiding repeated JSON allocations. Older snapshots become collectible after their readers finish. Disk checksum verification, pending accounting, ACKs, reflection, quota intervals and durable writes are unchanged. Legacy/test-created snapshots without a parsed payload use a non-mutating parse fallback.

## Local benchmark

`BenchmarkIKEv2RuntimePolicy1000Users` exercises the real policy-to-IKEv2 runtime-map path with 1,000 IKEv2 users and 1,000 Xray policy rows, on Windows amd64/i5-13400F:

| Measurement | Before | After |
| --- | ---: | ---: |
| Time per lookup | 4,176,672 ns | 506.6 ns |
| Allocated bytes per lookup | 1,526,729 | 1,049 |
| Allocations per lookup | 3,027 | 6 |

This measures the removed JSON work, not total node CPU, native command time or production throughput. Small runtime-map/key allocations remain. Helper-file JSON caching is a separate work item and is not claimed as solved here.

Tests cover published snapshot reuse, changed checksum at the same revision, immutable older readers, restart loading, stop/copy semantics, concurrent readers and rejected writes/input. Existing integrity/recovery/quota tests and Linux race/native-protocol CI remain required. No production deployment is included.
