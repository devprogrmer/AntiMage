# Local panel and diagnostics validation

Scope: validate real local socket observations through the existing Node Agent,
mTLS gRPC, SQLite-backed authenticated panel API and production dashboard build.
Keep all changes local; no commit, push, CI inspection or Production operation.

## Test design

`TestNetworkIntelligenceLocalTrafficToAuthenticatedPanel` creates two users and
three session adapters in a temporary database. One user has two addresses and
interfaces. All three UDP sessions contact the same real loopback UDP socket.
Two sessions exchange valid STUN Binding headers; the other exchanges arbitrary
unclassified datagrams. A real TCP socket supplies an additional TCP flow.

The server delays one session's replies by 250/350 ms. Other replies wait 20 ms.
Socket receive times feed the production decoder and SessionCapture adapter.
NodeControl/NodeRuntime are served by the actual Node Agent, not a replacement
Metrics implementation. The controller uses its existing mTLS dial path.
The panel runs its actual Handler and serves the built dashboard. Browser API
responses are not intercepted. The browser authenticates with the normal session
login API and invokes the actual Diagnostics endpoint.

Acceptance assertions:

- Four flows, two user accounts, three distinct session interfaces.
- Same destination across the three UDP sessions; account/session attribution
  remains separate in both the live table and Diagnostics response.
- Matched UDP sample counts, finite RTT and variation, original packet counters.
- Arbitrary UDP remains Unknown with null quality; no fictitious packet loss.
- TCP with no observed ACK timing remains unavailable; no synthetic ACK RTT.
- Only the delayed session produces high-RTT/variation findings. Findings include
  observation evidence, direction, confidence and recommendations that preserve
  uncertainty about network versus remote processing.
- The dashboard displays actual API RTT, samples, evidence and the diagnostic
  session/source/protocol. Multiple flows to the same destination have distinct
  diagnostic React keys.
- Anonymous requests receive 401; an authenticated restricted administrator
  receives 403 without destination disclosure.
- No packet body, local correlation identifier or session identity in output.
- No browser page errors.

## Reproduce locally (PowerShell, repository root)

```powershell
Push-Location dashboard
npm run build
Pop-Location
$env:ANTIMAGE_NI_LOCAL_DASHBOARD = (Resolve-Path dashboard).Path
$env:PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH = 'C:/Program Files/Google/Chrome/Application/chrome.exe'
go test ./internal/app/nodeagent -run '^TestNetworkIntelligenceLocalTrafficToAuthenticatedPanel$' -count=1 -v
Remove-Item Env:ANTIMAGE_NI_LOCAL_DASHBOARD
Remove-Item Env:PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
```

Use an installed Chromium executable if Chrome is elsewhere. Local network
execution must be permitted. The test owns and closes its sockets, RPC and HTTP
servers; database, certificates and test-only admin credentials are temporary.
There is no need to start the production panel or enable a Production flag.

The API-side child test and browser script are
`TestNetworkLiveBrowserAgainstLocalNode` and
`dashboard/e2e/network-intelligence-local-live.mjs`.

## Implementation connected during validation

Diagnostics previously consumed TCP quality only. It now consumes recent
unambiguous supported UDP transaction measurements, with the existing 200 ms
RTT/30 ms variation observation thresholds. Unknown, stale, absent, future,
invalid or Source-query timing is rejected. Values must be finite and within the
10-second supported transaction bound, with 1–32 samples (at least two for
variation). No UDP loss or in-game ping/definitive root cause is inferred.
Flow diagnostics add source/interface/protocol fields without changing existing
response fields or authorization behavior.

## Evidence and boundary

The first full-stack local run passed with four flows, two users and three
sessions: UDP mean 300.61895 ms and variation 99.4437 ms. It reported exactly
`high_udp_transaction_rtt` and `variable_udp_transaction_rtt` for the delayed
session. The final shared-destination/restricted-admin run also passed: UDP mean
300.41645 ms and variation 99.6395 ms, four flows, two users and three sessions.
Anonymous and restricted authenticated API checks returned 401/403. The built
dashboard displayed actual RTT, evidence and per-flow diagnostic identity.

This is **not raw packet capture or WireGuard E2E**. The portable test adapts
actual application-socket observations into IP/transport headers and supplies
test-owned bindings. It cannot validate kernel capture timestamps, WireGuard
peer ownership, TCP ACK timing, GRE topology, or loss in a real routed path.
Those require the existing disposable Linux fixtures and a suitable runtime.
Docker CLI exists locally but its Linux engine was unavailable; WSL inventory
showed only docker-desktop. No service was started and no host network changed.
Stage 3/7/8/9 release acceptance is not declared complete from this fixture alone.
