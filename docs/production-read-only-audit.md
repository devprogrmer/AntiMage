# Production baseline and runtime/accounting audit

`scripts/performance/production_baseline.py` is a standalone Python 3.9+ Linux
collector. It needs no Python packages. Run it on the NodeAgent host; output is
one JSON report on stdout. It reads existing state and uses only observational
daemon commands. It never executes the NodeAgent binary, starts/stops services,
changes configuration, resets counters, modifies database rows or installs tools.

From the checkout, capture a two-minute observation:

```sh
sudo python3 scripts/performance/production_baseline.py --sample-seconds 120 > antimage-s3-audit.json
```

If the panel SQLite database is accessible on that host, append
`--sqlite /absolute/path/to/existing/panel.db`. The path must be the real panel
database; no database is created. SQLite uses `mode=ro` and `query_only=ON`.
For MySQL/MariaDB, append `--mysql-defaults /root/existing-client.cnf --database antimage`.
The defaults file must already exist with permissions 0600; credentials are
never command arguments or report content. Database sessions use read-only
transactions. No credentials are requested or extracted from application config.

The default node state is `/var/lib/antimage-node`; override with `--data-dir`
if needed. Use `--node-id` only with the matching panel node ID. A disagreement
with the runtime callback identity disables receipt comparison. With multiple
NodeAgents, select `--pid`; the collector refuses an ambiguous selection.
`--xray-api-port` defaults to 10085 and always uses loopback. Observation duration
is bounded to 60–900 seconds. Endpoint reads at the boundaries have their own
timeouts and timestamps; collection takes longer than the CPU observation window.

## Report interpretation

The report contains per-protocol counts for Xray inbounds and native OpenVPN,
WireGuard, AmneziaWG, L2TP, PPTP, IKEv2 and AnyConnect. User IDs, inbound tags,
usernames, endpoints, emails, keys, credentials, receipts and raw daemon replies
are held only in memory and never emitted. Unknown protocol labels are redacted.
Xray ledger entries use the `xray` family; the collector resolves their inbound
tags to the actual Xray protocol. Unresolvable owners remain in the `xray` bucket.

Panel expectations combine service/enabled-host assignments, Xray proxy types
and exclusions with the observed node inbound inventory. This does not prove
that an entire inbound missing from the node snapshot was correctly deployed.
Without database access, panel counts and committed reflection are unavailable.
The collector does not guess or connect to a remote panel using stored tokens.

Daemon evidence differs by protocol:

| Protocol | Read-only evidence | Limitation |
| --- | --- | --- |
| Xray | StatsService counters with `reset=false` | Counter presence is not online state or complete loaded-user inventory |
| WireGuard / AmneziaWG | `wg` / `awg show all dump`, managed helper mappings | Recent handshake is an activity heuristic; unmanaged/unknown peers are counted separately |
| OpenVPN | Managed v3 `status.tsv` files | Inactive auth accounts are not enumerable through status; stale status files are rejected |
| L2TP / PPTP | Durable PPP records plus live PID/start/boot/interface identity and interface counters | Finalized, exited, zombie and reused-interface records are excluded |
| IKEv2 | `swanctl --list-sas --raw`, remote EAP identity, child-SA counters | Default host VICI socket may be inaccessible from a private runtime namespace |
| AnyConnect | `occtl -j show users` on existing managed sockets | Active sessions do not enumerate all inactive auth accounts |

The collector never enters another namespace. It checks whether its network
namespace matches the NodeAgent and marks live evidence unavailable on mismatch.
Missing binaries, sockets, state files, unsupported shapes, size limits or schema
columns remain explicit observation gaps. `null` is not zero or PASS. Helper and
policy inventories are shown separately from live daemon-loaded users.

Byte comparisons retain their different meanings: native counters, NodeAgent
logical counters, owner ledger totals, raw reflection watermarks and DB billed
usage are not silently equated. DB `users.used_traffic` spans protocols and nodes
and can include coefficients. It is therefore not reported as per-protocol raw
usage. Differences during the window are candidates, since sources are sequential
snapshots and checkpoint/reflection lag and reconnects can affect them. New or
reset native series are reported as uncomparable, not as zero-byte traffic.

Only committed `node_wireguard_usage_reflection` records for the matching node
and accounting epoch count as DB reflection evidence. A transport ACK, staged
watermark, queued delivery or soft deletion is not reflection/retirement proof.
The collector validates referenced legacy-anchor checksums and reports duplicate
owner/child indicators and counter inconsistencies. It does not remove anchors.
Historical exact-once/restart safety cannot be proved by two live snapshots;
the report keeps that limitation explicit.

CPU uses the NodeAgent PID's user+system time only: 100% is one logical core.
Host-normalized CPU, affinity and available cgroup limits are reported separately.
RSS/VSZ and thread counts are sampled repeatedly. Accounting state-file changes
are timestamped alongside CPU samples; these are activity observations, not exact
tick counts or causal hotspot attribution. The current production process does
not expose safe aggregate Go heap/GC/goroutine or internal quota/RPC/DB counters,
so those values remain unavailable. No profiler, signal, trace or network debug
endpoint is enabled by this script.

Use the measured host constraints, actual protocol mix and observed scale as the
acceptance workload. Current external observations of approximately 650 users,
291 active users and 70% CPU are planning inputs, not an independently normalized
baseline. Neither collector unit tests nor small accounting benchmarks establish
production CPU/RAM acceptance.

## Validation

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/performance -p 'test_production_baseline.py' -v
```

Fixtures check DB byte preservation/read-only enforcement, all protocol
assignments, missing schemas, node/epoch boundaries, raw ACK insufficiency,
Xray-family attribution, migration anchors, live counter discrepancies,
credential redaction, PPP stale/zombie/finalized records and version discovery
without executing NodeAgent.
