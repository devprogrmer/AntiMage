# NodeAgent production-shaped performance evidence

## Status

The accounting index change has focused correctness/race coverage. The earlier
96-user accounting-only workload is **not production CPU/RAM acceptance**.

This capture harness runs the actual `nodeagent.New(LoadConfig()).Run(ctx)` in a
separate executable. It adds file-only Go profiling; it does not replace runtime
collectors, accounting, quota, RPC services, or Controller persistence. It does
not expose pprof over the network or change the shipped NodeAgent entry point.

**A real mixed-protocol scenario adapter and a suitable Linux host are still
required.** Neither a fake scenario nor a successful verifier unit test proves
the production target. There is deliberately no synthetic adapter that can be
mistaken for native acceptance. No production CPU/RAM acceptance result has been
recorded with this harness yet.

## Focused receipt-decoding measurement

`BenchmarkReflection650UsersEightInbounds` isolates 650 configured users with
eight cumulative receipt owners each. The inbound count is a synthetic comparison
dimension, not a measured AntiMage-s3 protocol/inbound mix. Each owner retains
100 unreflected bytes and all 5,200 results are checked by a regression test.

On the same WSL Linux amd64 host (i5-13400F, Go benchmark parallelism 16), three
one-second benchmark repetitions gave:

| Measurement per batch | Before | After |
| --- | ---: | ---: |
| Time | 49.15–50.91 ms | 7.26–7.43 ms |
| Allocated bytes | 56,219,769–56,219,796 | 7,389,881–7,389,888 |
| Allocations | 733,200 | 96,217 |

Queries follow Controller's actual inbound-major ordering. The baseline was
compiled from the unchanged parent implementation using a Go file overlay;
the same benchmark and tests were used for both implementations.

The implementation reuses decoded receipts within one evaluation call, bounded
by 1,024 entries and 1 MiB of encoded receipt bytes. Decoded object overhead is
additional; this is not a claim that cache heap usage is capped at 1 MiB.
It retains no receipt between calls, requires identical receipt bytes for reuse,
and still checks epoch and each owner's cumulative total. Interleaved receipts,
changed receipts, wrong epochs, missing receipts, oversized receipts, entry-limit
overflow, and independent single-owner evaluation have regression coverage. Persistence,
reflection authority, quota cadence, and anchor-retirement rules are unchanged.
Receipts outside the cache bounds are decoded normally without dropping owners.

This is a focused allocation/latency result, **not** isolated NodeAgent CPU,
RSS/GC stability, native traffic reconciliation, or production acceptance. The
real host report and mixed-runtime workload remain necessary.

## Build and capture

On the same suitable Linux host/toolchain for both revisions:

```sh
go build -o /opt/antimage-perf/antimage-node ./cmd/antimage_node_perf
python3 scripts/performance/node_runtime_load.py \
  --scenario /opt/antimage-perf/scenario.json \
  --output /var/tmp/antimage-perf-run-001
```

Use a disposable, isolated deployment. The scenario owns its interfaces,
namespaces, VPN clients, test database, TLS material, ports and data directories.
Never point this at a live production data directory. Required privileges must
be granted by the environment, not bypassed. Preserve the proven L2TP CI driver.

The profiler accepts the standard NodeAgent environment plus
`ANTIMAGE_PERF_OUTPUT` (a **new absolute directory**) and
`ANTIMAGE_PERF_WARMUP_SECONDS`. The supervisor sets both per process segment.
The binary supports `session-event` exactly as the production entry point does.
Profiles/evidence are private (directory 0700, files 0600 with restrictive umask).
Do not put passwords or tokens in command arguments: command arrays are retained
in the run contract. Environment values are not copied into the contract.

Example scenario contract (the external adapter below must actually be implemented
for the target deployment; these paths are not provided test fixtures):

```json
{
  "source_revision": "FULL_GIT_SHA_OF_BINARY",
  "users": 1000,
  "inbounds": 8,
  "protocols": ["xray", "wg", "openvpn"],
  "warmup_seconds": 60,
  "duration_seconds": 1800,
  "minimum_steady_seconds": 1200,
  "quota_interval_seconds": 0.1,
  "restart_seconds": [600, 1200],
  "node": ["/opt/antimage-perf/antimage-node"],
  "prepare": ["/opt/antimage-perf/scenario", "prepare"],
  "ready": ["/opt/antimage-perf/scenario", "ready"],
  "workload": ["/opt/antimage-perf/scenario", "run"],
  "cleanup": ["/opt/antimage-perf/scenario", "cleanup"],
  "verify_cleanup": ["/opt/antimage-perf/scenario", "verify-cleanup"],
  "env": {
    "SERVICE_HOST": "127.0.0.1",
    "SERVICE_PORT": "62050",
    "ANTIMAGE_NODE_DATA_DIR": "/opt/antimage-perf/node-state"
  }
}
```

Replace the example quota interval with the **unchanged actual production
cadence**. Use the actual production user count if higher than the 1000-user
floor, actual protocol proportions, active fraction, traffic rates, settings,
reflection lag, quota headroom, controller sync frequency and reconnect pattern.
Record these and machine/kernel/core/CPU/memory/cgroup limits, storage and pinned
core versions alongside each run. Floors alone do not establish equivalence.

## Scenario adapter contract

Every command receives `ANTIMAGE_PERF_RUN_DIR`; `contract.json` is there before
preparation. Commands are argv arrays; no shell interpolation occurs.

* `prepare`: create isolated DB/config/certs and native resources. Use real panel
  migrations. Record the expected `(protocol, user_id, inbound_tag)` inventory.
  Provision Xray and native users through the real Controller sync/RPC path.
* `ready`: bounded check of NodeAgent gRPC and runtime readiness. It also runs
  after each NodeAgent restart. It must not depend on a stale readiness file.
* `workload`: use real protocol clients and measured traffic. Keep active and
  inactive users, normal quota enforcement, accounting collection, Controller
  config activity and DB persistence running. Independently enumerate runtime
  users to detect extra/missing identities. Quota-denied controls and real
  reconnects must execute. Count completed operations, never planned operations.
* For each configured restart, quiesce traffic, drain protocol counters, preserve
  pending state, and append a `pre_restart` accounting checkpoint. Create
  `restart-ready-N` in the run directory. The supervisor gracefully terminates
  only its owned NodeAgent group, verifies exit 0, starts a new process against
  the **same durable data**, checks readiness, then creates `restart-complete-N`.
  The adapter reconciles, appends `post_restart`, and resumes traffic.
* Include a Controller restart and reflection/replay. To test lost ACK, commit
  the panel transaction, drop the outgoing ACK using a controlled proxy, capture
  `pre_lost_ack_replay`, retry the real RPC delivery, then capture
  `post_lost_ack_replay`. Pause traffic across each checkpoint pair. Raw ACK is
  never authority to retire an anchor; preserve the durable retirement proof rule.
* Once `finish-requested` exists, stop traffic, drain protocol/accounting counters,
  commit/reflect all remaining usage, write `workload.json`, and exit 0 within
  120 seconds. An early workload exit is a failure, even with exit status 0.
* `cleanup` must be idempotent and remove only scenario-owned resources.
  `verify_cleanup` fails for surviving owned processes, namespace/interface leaks,
  open owned sockets or temporary resources. This remains mandatory after a
  failed/cancelled run. The supervisor also TERM/KILLs and reaps its process groups;
  it does not assume that terminating a group cleans separately daemonized VPNs.

`workload.json` schema 1 includes `source_revision`,
`node_executable_sha256`, `quota_interval_seconds`, `inventory`, `counts` and
`snapshots`. The executable checksum is in `contract.json`. Inventory entries
contain `protocol`, `user_id`, `inbound_tag`.

Counts: `accounting_ticks`, `quota_evaluations`, `controller_syncs`, `db_queries`,
`reflection_acks`, `reconnects`, `node_restarts`, `controller_restarts`,
`quota_denials`. Obtain counts from observed completed operations/SQL tracing and
runtime instrumentation; the current capture binary does **not** add production
quota/accounting call counters. Supplying guessed values is not acceptable.
Retain source traces for review. DB/query activity belongs to the separately
measured Controller; it must not be charged to NodeAgent CPU.

Each snapshot has a `phase` and an `owners` array covering the exact inventory.
Phases start with `baseline`, end with `final`, and include the consecutive
restart/replay pairs described above. Each owner includes:

```json
{
  "protocol": "wg",
  "user_id": 12,
  "inbound_tag": "wg-1",
  "active": true,
  "runtime_present": true,
  "generated_payload_bytes": 800,
  "source_raw_bytes": 1000,
  "node_raw_bytes": 1000,
  "controller_raw_bytes": 900,
  "node_pending_raw_bytes": 100
}
```

All byte fields are nonnegative integers, cumulative from a common baseline,
including across process restarts. Protocol counters and payload bytes are
different quantities: encapsulation overhead must be independently recorded;
never pretend that an application payload equals a WG interface counter.
Fixtures use usage coefficient 1; report **raw** Controller DB bytes, not
coefficient-adjusted billed bytes. Keep generated traffic, native counters,
NodeAgent durable owner totals and DB rows as separate evidence sources.

At every quiescent checkpoint the verifier requires, **per owner**:

* payload <= protocol raw;
* protocol raw = NodeAgent raw;
* NodeAgent raw = Controller committed raw + still-pending raw;
* monotonic counters, exact inventory membership, active and inactive users;
* replay/restart pairs cannot change cumulative bytes;
* final pending bytes = 0; each required protocol generated actual payload.

Global sums alone are insufficient: a transfer between users must fail. Tests
explicitly cover this. These checks cannot establish that an adapter read genuine
counters; adapter code/source traces must be reviewed before acceptance.

## Evidence and comparison

`process.jsonl` samples `/proc/<node-pid>/stat` user+system CPU only, never
children's CPU. `/proc/status` supplies RSS/high-water RSS/thread count. PID start
ticks detect identity reuse. Restart downtime and per-segment warmup are excluded
from steady-state CPU; sampling gaps fail validation. CPU uses **one core = 100%**,
not percentage divided by the machine's CPU count.

Each `node-N` directory records build identity, PID, Go version, GOMAXPROCS,
`runtime.jsonl`, CPU/heap/alloc/goroutine profiles. Runtime measurements are trimmed
to the same measured intervals as process CPU. Reports include allocation rate,
GC count/CPU, heap/goroutine maxima and trends. Capture overhead is included and
must be kept identical before/after. Profiles are flushed before group teardown.
The binary must contain clean VCS build metadata matching the scenario revision;
build without `-buildvcs=false` on the target host. A dirty/unknown revision or a
different runtime PID fails evidence verification.

```sh
go tool pprof -top /var/tmp/antimage-perf-run-001/node-0/cpu.pprof
go tool pprof -top -alloc_space /var/tmp/antimage-perf-run-001/node-0/allocs.pprof
go tool pprof -top -inuse_space /var/tmp/antimage-perf-run-001/node-0/heap.pprof
```

`measurement.json` reports measured results, not automatic production acceptance.
It deliberately says `REQUIRES_MEMORY_TREND_AND_WORKLOAD_EVIDENCE_REVIEW`.
Inspect memory after comparable warmup and GC, peaks, trends across long runs,
goroutine retention and profile ownership. Extend the run if caches are still
warming. Investigate continuous growth; do not mask it with periodic restarts.
Include an uninterrupted long segment in the production campaign as well as
restart segments.

Run the identical adapter, seed, traffic plan, intervals, CPU pinning/cgroup
limits and host for each comparison. Retain the before and after binary/checksum,
source SHA, raw samples and profiles. First measure before touching JSON/state
serialization. No batching, snapshot reuse or encoding change may weaken fsync,
durability, reflection identity, rollback, retirement authority or lost-ACK safety.

Acceptance still requires NodeAgent steady CPU <20%, stable bounded memory/GC,
unchanged per-owner accounting and quota behavior, all native protocol and
race/migration gates green on the final exact SHA, and independently reviewed
evidence that the workload is production-equivalent. This document records no
such result yet.

## Harness checks (not production acceptance)

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/performance -p 'test_*.py' -v
go test -race ./cmd/antimage_node_perf -count=1
```

Xray's pinned core still lacks proven safe per-user active-stream termination and
per-user speed enforcement. Keep these limitations documented; broad socket,
conntrack or nftables termination is not a substitute.
