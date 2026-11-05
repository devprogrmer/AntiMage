# Native WireGuard helper cache

WireGuard and AmneziaWG accounting/quota paths share a bounded cache of decoded `usage-helper.json` files. Each call still opens, reads and hashes the actual file using a reusable scratch buffer. SHA-256 content comparison avoids decoding unchanged files; it also detects in-place changes with identical size, inode and modification time. No metadata-only refresh interval or stale-policy fallback is introduced.

Missing, unreadable, oversized or malformed replacements return errors to the existing caller. A changed file publishes a new decoded value; previous readers retain their immutable view. Helpers returned by this reader must be treated as read-only. Scalar fields can be normalized in a local struct copy, but nested maps/slices must not be mutated.

The cache stores at most 128 files and eight MiB of source-byte equivalents, evicting least-recently-used entries. Decoded maps have additional memory overhead. The shared scratch buffer is bounded by the existing 64-MiB accounting byte limit and is released after oversized-for-cache inputs. File formats, atomic writes, fsync, checkpoints, ACKs, reflection and quota intervals are unchanged. This cache does not cover mutable ledgers/WAL or native status output.

## Benchmark and tests

For a helper with 1,000 peer IDs and 1,000 policies on Windows amd64/i5-13400F:

| Measurement | Read + decode before | Read + hash + cached decode after |
| --- | ---: | ---: |
| Time per read | 1,639,570 ns | 140,513 ns |
| Allocated bytes | 778,577 | 745 |
| Allocations | 5,040 | 5 |

This measures helper processing, not total node CPU or production traffic. Reads still occur on every existing scan; file I/O has not been deferred.

Tests cover unchanged reuse, same-metadata content changes, invalid replacement/deletion, immutable prior readers, concurrent readers, capacity eviction, type mismatch and oversized inputs. Existing WG/AWG recovery, quota, transition and no-write tests remain required, with Linux race/native-protocol CI before merge. No production deployment is included. Rollback uses the prior binary and requires no data migration.
