# Local staging recovery evidence — 2026-11-28

Scope: disposable local processes, loopback endpoints, test-owned certificates,
temporary SQLite database/configuration and locally built Windows binaries.
No Production service restart, network change, deployment, migration or Git
publication was performed. The unrelated native-session-outbox is preserved.

## Verified scenarios

`TestNetworkIntelligenceProcessRecoveryAndVersionRollback` passed (8.92 s):

1. Start an actual Node Agent RPC implementation in an owned process. Real UDP
   socket exchanges feed the existing portable session adapter and produce two
   quality samples for user 42.
2. Terminate that process. A real RPC request fails instead of returning stale
   telemetry. Restart the RPC endpoint on the same address with the same client.
3. Inject a handler stall, while leaving Hello available. The real Metrics RPC
   exceeds a 150 ms client deadline and returns DeadlineExceeded. Successful
   responses are still produced by the actual Node Agent, not fabricated JSON.
4. Replace peer/account ownership on a reused IP: stop the old capture, unbind,
   advance generation, bind user 43, reject an unsolicited old reply, then make
   fresh socket exchanges. Telemetry contains only the new owner and exactly
   two new upload packets/quality samples, without inherited counters.
5. Replace IP/peer for user 42; only the new 127.0.0.2 address appears.
6. Restart an empty agent; volatile flows do not resurrect.
7. Execute the **actual current node binary**, terminate it, execute the
   **archived baseline node binary with flags off**, then run the current binary
   again, using the same disposable data directory and mTLS endpoint. RPC and
   default-off compatibility are preserved.

`TestNetworkIntelligencePanelRestartBackupAndFlagRollback` passed (0.90 s):

- Each prepare/restore/disabled/re-enable phase runs in a distinct process.
- Fully migrated SQLite DB contains two account-scoped durable-history fixture
  records and accounting data. These stored fixture RTT values are not claimed
  as live network measurements.
- The existing full Backup service exports database plus an owned config file.
  The fixture then changes accounting, deletes history and changes the flags.
- A new panel process restores the archive. Another starts with monitoring off:
  the endpoint returns 404, both flags are restored to false, and user traffic
  is restored to 123456. A final enabled panel reads only the selected account's
  exact original durable-history fixture.

`TestNetworkIntelligencePanelBinaryVersionRollback` passed (2.08 s):

- Actual current panel binary → actual baseline binary (flags off) → baseline
  with history enabled → current binary again, on one disposable DB/endpoint.
- Normal HTTP token authentication and the history endpoint work in both code
  versions; migration-95 data is readable. Flags-off returns 404. The history
  fixture and accounting remain unchanged. The process environment explicitly
  omits inherited endpoints, hooks and credentials; there are no enrolled nodes.

Regression checks passed: API Network suite; focused Node Agent
NetworkIntelligence/NetworkApplication tests; existing interrupted-restore and
database/file rollback tests; Go vet for API and Node Agent. Linux amd64 test
cross-compilation passed, and is not treated as Linux runtime execution.

`TestNetworkIntelligenceBrowserRecoversFromNodeFailures` also passed (14.82 s).
The real authenticated panel and built dashboard use an actual mTLS Node Agent
test subprocess. A separate nonce-protected loopback test controller terminates
or restarts only that owned process and injects an RPC handler stall. Neither
Node Metrics responses nor browser API requests are mocked. The browser sees
503 and removes the old flow on process disconnect and RPC deadline, recovers
after reconnect, shows only the new account after IP reassignment, clears flows
after an empty restart and recovers again. No browser page errors occurred.

## Test artifacts

Baseline source is a read-only `git archive HEAD` at
`1c1dd80f693d5ea760a088241e2705e5be49420b`, extracted into ignored
`artifacts/ni-recovery-1c1dd80f/source`. Existing checkouts/branches were not
changed. Binaries were built with `-buildvcs=false` because automatic VCS stamping
failed in the archived/nested-worktree layout; the source SHA is recorded here.
This changes build metadata, not runtime security. These are test artifacts,
not signed release packages.

| Artifact | SHA-256 |
| --- | --- |
| baseline-node.exe | 19456591C3645F703C2BCB1227349FD4092B7A8BED80A95F5311FBDE8156CB77 |
| current-node.exe | 330E3C584C6912704349F92A579C99A88C7EFA6FFFA670C9712A329A356C5113 |
| baseline-panel.exe | 490D10921182E3C26A42BC526A7A33520BAADE9D4CD0FCB5AB9C734A6155B052 |
| current-panel.exe | 0D3D6DB575337F25C5280793D8DDD0CA20DFDD5D00A5E5253BFD663B8AA6C63A |

Re-run locally from the repository root after building the four artifacts:

```powershell
$env:ANTIMAGE_NI_BASELINE_NODE = (Resolve-Path artifacts/ni-recovery-1c1dd80f/baseline-node.exe).Path
$env:ANTIMAGE_NI_CURRENT_NODE = (Resolve-Path artifacts/ni-recovery-1c1dd80f/current-node.exe).Path
go test ./internal/app/nodeagent -run '^TestNetworkIntelligenceProcessRecoveryAndVersionRollback$' -count=1 -v
go test ./internal/app/api -run '^TestNetworkIntelligencePanelRestartBackupAndFlagRollback$' -count=1 -v
$env:ANTIMAGE_NI_BASELINE_PANEL = (Resolve-Path artifacts/ni-recovery-1c1dd80f/baseline-panel.exe).Path
$env:ANTIMAGE_NI_CURRENT_PANEL = (Resolve-Path artifacts/ni-recovery-1c1dd80f/current-panel.exe).Path
go test ./internal/app/api -run '^TestNetworkIntelligencePanelBinaryVersionRollback$' -count=1 -v
go test ./internal/app/nodeagent -c -o artifacts/ni-recovery-1c1dd80f/node-recovery-tests.exe
$env:ANTIMAGE_NI_NODE_TEST_BINARY = (Resolve-Path artifacts/ni-recovery-1c1dd80f/node-recovery-tests.exe).Path
$env:ANTIMAGE_NI_LOCAL_DASHBOARD = (Resolve-Path dashboard).Path
$env:PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH = 'C:/Program Files/Google/Chrome/Application/chrome.exe'
go test ./internal/app/api -run '^TestNetworkIntelligenceBrowserRecoversFromNodeFailures$' -count=1 -v
```

## Failed attempts and remaining boundary

- Restricted local execution could not open a SQLite temp path; authorized
  local execution reached the actual test.
- An initial same-process panel reinitialization hit the process-scoped cached
  closed DB pool. The test was corrected to execute real process restarts; no
  production DB code or assertion was weakened to conceal the mismatch.
- Initial default builds failed automatic VCS metadata lookup; explicit source
  provenance and `-buildvcs=false` produced the tested binaries.

The RPC/DB/process/binary scenarios above are verified locally. They do **not**
prove a live WireGuard peer mutation, kernel capture behavior after restart,
Linux systemd update/rollback, real tunnel forwarding continuity, multi-node
staging or MySQL/MariaDB backup recovery. Browser recovery during node/RPC faults
is covered above, but not Linux service-manager/tunnel fault injection.
Current UDP application-socket adapters are explicitly separate from raw capture.
Docker's Linux daemon was unavailable in the earlier local environment check.
Stage 14 and full item 4 remain unaccepted until the missing representative Linux
full-stack scenarios run. No release-ready or Production-update claim is made.
