# Real Node Host Updates Design

## Goal

Make the Nodes page actions for updating `antimage-node`, Xray-core, and
GeoIP/GeoSite perform real, auditable host changes on supported binary nodes.
An action must never report success when it was skipped.

## Scope

- Binary-mode `antimage-node` installations on supported Linux hosts.
- Stable, dev, and explicit-version node binary updates.
- Explicit Xray-core version updates.
- GeoIP and GeoSite replacement from administrator-supplied HTTPS URLs.
- Accurate API/UI results, operation history, and actionable failures.

Docker-mode self-update remains installer-managed. The API must return a clear
failed-precondition response for Docker nodes instead of a synthetic success.
Host reboot remains outside this feature.

## Node-Agent Architecture

Host actions live in a focused `nodeagent/hostupdate` implementation behind
small injectable interfaces for downloads, command execution, and process
restart scheduling. Production uses the standard HTTP client and `systemctl`;
tests use fakes and local HTTP servers.

Every mutating operation takes an operation ID and is idempotent. The agent
persists the completed operation result beneath its data directory so a retry
does not download or replace the same artifact again.

## Node Service Update

The agent resolves the requested channel/version against the official AntiMage
GitHub release or dev artifact naming already used by the binary installer. It
detects the host architecture, downloads to a private temporary directory,
verifies the published SHA-256 checksum when available, confirms that the
artifact is an executable Linux binary, and invokes its version output before
installation.

The existing binary is copied to a rollback path. The new binary is installed
with mode `0755` using rename on the same filesystem. After the gRPC response
has been flushed, a detached helper restarts the exact systemd unit that owns
the current process. If the new service does not become healthy, the helper
restores the previous binary and restarts it. The update response reports the
resolved version and that restart was scheduled; it does not claim the new
process is healthy before the post-restart check.

The service unit name and executable path are derived from `/proc/self/exe`
and systemd metadata, not from request input. Arbitrary command execution and
arbitrary destination paths are not accepted.

## Xray-Core Update

The requested version is normalized to an upstream Xray release tag. The agent
downloads the matching official archive and checksum, extracts only the Xray
binary into a private temporary directory, validates it with `xray version`,
and atomically replaces the configured `XRAY_EXECUTABLE_PATH` while retaining
a rollback copy.

The managed Xray child is restarted with the existing generated config. If
validation or restart fails, the previous binary is restored and the old
runtime is started again. Native VPN interfaces and accounting state are not
reset by a core update.

## Geo Update

Only `geoip.dat` and `geosite.dat` are accepted. URLs must be HTTPS, must not
contain user information, and must resolve to public addresses; redirects are
revalidated to prevent SSRF. Downloads have time and size limits. Each file is
written with mode `0644`, fsynced, and renamed atomically into the configured
Xray assets directory. Existing files remain available until all requested
downloads validate. A failure leaves the complete previous pair untouched.

After replacement, a running Xray child is restarted with its existing config.
Geo update never changes the node service binary.

## Controller and UI Semantics

The controller removes the `installerManagedRuntimeResult` success conversion.
`Unimplemented` and unsupported install modes remain errors and durable
operations are marked failed. API responses include a human-readable message,
resolved version where applicable, and whether a restart was scheduled.

The Nodes page keeps the three distinct actions. Toasts use the returned
message. Buttons are disabled only when node metrics explicitly identify an
unsupported mode; unknown mode is not assumed to be Docker.

## Security and Failure Handling

- No shell interpolation of request fields.
- HTTPS-only downloads with SSRF-safe resolution and redirect checks.
- Bounded response sizes and timeouts.
- SHA-256 verification where the upstream publishes checksums.
- Private temporary directories and same-filesystem atomic replacement.
- Rollback copy retained until post-restart health succeeds.
- Exact executable and service paths come from local trusted state.
- Concurrent host updates are serialized by a filesystem lock.
- Logs never contain credentials, certificates, or request authorization.

## Verification

Unit tests cover normalization, architecture mapping, checksum failures,
unsafe URLs, redirects, size limits, atomic replacement, rollback, duplicate
operation IDs, install-mode rejection, and controller error propagation.
Nodeagent and controller package tests must pass on Windows using fakes.

A real Ubuntu 24.04 binary-mode test is required before claiming the host
actions are live verified. It must update a node binary, update Xray, replace
both Geo files, reconnect after each restart, and demonstrate rollback after a
deliberately bad artifact. Without this environment the result is reported as
unit/integration tested only.

