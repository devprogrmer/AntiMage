# Network Intelligence

## Presence API

`GET /api/network-intelligence/presence` returns recent presence observations grouped by user, node and VPN protocol. It reuses the existing Online Access Insights records and 75-second freshness window. The response contains up to 250 groups. An absent group does not establish that a user is offline. Data comes from Xray online observations and native VPN sessions, so freshness depends on each existing collector.

The route requires the `full_access` role. It excludes client addresses and session identifiers. Requests have a two-second database deadline and responses use `Cache-Control: no-store`. The default flag is off. To enable the route in an isolated environment, set `ANTIMAGE_NETWORK_INTELLIGENCE_ENABLED=true` before starting the gateway. Restore the default flag on the next approved gateway start to disable it. There is no schema change.

## Checkout audit

The development branch starts at `6dd120caba32460801762271c4ed42c6e0d8434a`. Its Git tree is identical to `8efac522`. Relative to the local `origin/dev` tracking ref, the starting branch has three unique commits and lacks 98 commits from that ref.

| Commit | Change |
| --- | --- |
| `8efac522` | Adds `l2tp-known-good-recovery` to the push trigger for the native protocol E2E workflow. |
| `d2b009e2` | Empty commit to rerun L2TP stability CI. |
| `6dd120ca` | Empty commit to rerun L2TP stability CI. |

The original checkout had zero tracked changes and 437 untracked entries. Of these, 431 were under `.codex-cache`, four were linked worktree directories, and two were separate checkout directories. Cache filenames and extensions include earlier test scripts, process state, captures, profiles and runtime fixture artifacts. The tracked build configuration does not reference the sibling worktrees or checkouts. The lifecycle test script uses `.codex-cache` as an output directory. The original artifacts were left in place. The live production version was unavailable for comparison. Local Git tags and remote tracking references do not identify the running production binaries.

## Flow attribution acceptance

WireGuard attribution requires node, interface, assigned address and verifiable current peer ownership. An address lease must have a generation or equivalent lifecycle boundary. Reassignment fixtures must follow the old flow across that boundary and demonstrate that none of its counters can migrate to the new user. Namespace-wide conntrack tuples alone lack this attribution evidence. Xray needs a source tied to the authenticated proxy session before multiplexed users share egress.

Keep collection outside the forwarding path. Validate the two-interface and two-node cases, concurrent TCP and UDP, expiration, missed events and duplicate observations using real traffic in isolated Linux namespaces. Benchmark feature off and on under the same traffic with 2 CPUs and 2 GiB of memory. Record forwarding throughput, latency, loss, collector CPU and memory, flow count and telemetry bytes. Capture only necessary metadata and publish unavailable metrics as N/A. Game RTT and UDP loss require a valid measurement source.
