# Production baseline and runtime/accounting audit

`scripts/performance/production_baseline.py` is a standalone Python 3.9+ Linux
collector. It needs no Python packages. Run it on the NodeAgent host; output is
one JSON report on stdout. It reads existing state and uses only observational
daemon commands. It never executes the NodeAgent binary, starts/stops services,
changes configuration, resets counters, modifies database rows or installs tools.

From the checkout, capture a two-minute observation:

```sh
sudo python3 scripts/performance/production_baseline.py --sample-seconds 120 \
  --output /var/tmp/antimage-s3-audit.json > /dev/null
```

`--output` writes the same document that goes to stdout, so a dropped SSH session,
closed terminal or broken pipe cannot leave a zero-byte or partial report. Without
`--output`, redirect stdout as before and add `--output` when running over a
connection that may disconnect.

Every run writes exactly one JSON document and exits 0 only when that document is a
complete report. Argument errors, a missing or ambiguous NodeAgent process, a node
identity change during sampling and unexpected exceptions all produce a valid JSON
error object instead:

```json
{"status": "error", "stage": "find_node", "exception_type": "CollectorStageError",
 "message": "antimage-node process not found; pass --pid <PID> if it runs under another name",
 "diagnostics": {"requested_pid": null, "process_name_hints": []}}
```

`stage` names the failing phase (`arguments`, `find_node`, `sampling`, `report`), the
message is the first line only and truncated, and credential-shaped tokens are masked.
A one-line status goes to stderr, for example
`status=ok stage=report report=/var/tmp/antimage-s3-audit.json observation_seconds=120.0`.

When neither `--sqlite` nor `--mysql-defaults` is given, the collector
auto-discovers the panel database from the install `.env` written by the panel
installer. It reads only `SQLALCHEMY_DATABASE_URL` (falling back to
`DATABASE_URL`) from the first of `$ANTIMAGE_ENV_FILE`, `/opt/antimage/.env` and
`/etc/antimage/antimage.env` that exists, and it never executes the file. A
`sqlite:////absolute/path` value is used as an absolute path. For the
`sqlite:///path` form the collector stats both the rooted and the
relative-to-`.env` interpretation and prefers whichever exists, falling back to
the documented relative meaning, so a three-slash absolute path still resolves
without creating or opening a database. A MySQL/MariaDB DSN supplies only the
database name, never the credentials embedded in it. The
resolved source is recorded in `database.discovery` (`ok`, `needs_credentials`,
`not_found`, `not_applicable`, or `explicit` when a flag was passed) with a short
`database.discovery_note` and the exact `database.discovery_probed_paths` that
were tried.

Because `sudo` starts a fresh environment, the installer's `ANTIMAGE_ENV_FILE` /
`ANTIMAGE_APP_DIR` exports are usually absent from a collector run. Discovery
therefore also parses the `Environment=` lines of the `antimage.service` and
`antimage-node.service` units (as text; a unit is never executed) to recover the
real install paths. If the host runs the AntiMage-**node** service, discovery
reports `not_applicable` and states that a node stores no panel database, rather
than implying a missing panel: run the collector on the panel host, or pass
`--sqlite` on a combined host. Panel-derived counts stay `null` in either case.

Auto-discovery is read-only and never fails the run: if no install `.env` is
present, or discovery finds only a MySQL DSN, the report is still a complete JSON
document with every database field marked `unavailable`/`null` and a note
explaining what to pass instead. Do not pass a placeholder path such as
`/absolute/path/to/panel.db`; a placeholder is treated literally and yields
all-null database state without an error.

To point the collector at a database explicitly, append
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

## First production observation: 2026-10-25

The shared AntiMage-s3 report covers 120.149 seconds of process sampling, with
sequential accounting snapshots spanning roughly 123 seconds. It establishes:

- Two logical Xeon Platinum 8260 CPUs and 4,106,092,544 bytes of host RAM.
- NodeAgent mean CPU 43.446% of one core, or 21.723% of the host's two-core
  capacity. This does not establish an improvement from the historical 70%
  observation: workload, binary revision and measurement method differ.
- RSS starts at 45,735,936 bytes, ends at 45,039,616 bytes, and peaks at
  57,823,232 bytes. A two-minute window does not prove long-run heap stability.
- Runtime policy has two VLESS, two Shadowsocks and three WireGuard inbounds.
  The per-protocol policy user counts are 170, 170 and 119 respectively; these
  counts can overlap and must not be added to infer unique configured users.
- The quota enforcement environment override is 2 seconds. File changes in
  several protocol accounting states are activity, not proof of active sessions
  or measurements of the quota/accounting scheduler's call frequency.
- Xray is version 26.7.11. The NodeAgent binary hash is known, but its source
  revision is not established by the report.

DB sources and Xray live counters were unavailable, and policy changed during
the observation. Hence no missing-user, native-to-DB conservation, reflection,
or restart-safety acceptance conclusion is warranted. WG reported 232 unmapped
peers: the original collector missed managed `wireguard/inbounds/*` helper
paths, so its zero matched loaded/active count is not an observed zero. The
collector now checks the actual WG and AWG managed layouts and reports unknown
when no owner mapping exists. It also discovers the authoritative runtime
`API_INBOUND` port instead of always assuming 10085; an explicit port override
remains available. Live query failures expose only a redacted error category.

A follow-up capture with these fixes and access to the actual panel DB is
required to close cross-protocol reconciliation. Neither a reset nor a restart
is needed for that capture. Until then, approximately 650 configured and 291
active users remain external scale estimates, not collector-verified counts.

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
