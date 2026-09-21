# vpn-ui Backup Migration Design

## Goal

Add `Restore from vpn-ui` beside AntiMage's existing Backup/Restore controls.
The administrator uploads a Sir-MmD/vpn-ui SQLite backup and imports its
accounts into an existing AntiMage service without replacing the AntiMage
database.

## Source and Safety

The source is a vpn-ui `.db` SQLite backup. It is opened read-only and must
match either the current `accounts` schema or the legacy `client_traffics`
schema. Generic SQLite files are rejected. Upload size and imported account
count are bounded, and the temporary upload is removed after the request.

No source admin, panel password, JWT/session secret, TLS key, node certificate,
host path, raw daemon configuration, WireGuard/AmneziaWG private key, or PSK is
read into AntiMage or returned by the API.

## Destination Model

The administrator selects an existing AntiMage service with active hosts.
Imported accounts use that service's protocols, nodes, and routing. AntiMage
generates fresh subscription and connection credentials, so imported users get
valid profiles for the current AntiMage runtime rather than stale vpn-ui host
configuration.

The importer preserves where available:

- username;
- enabled/disabled state;
- expired and quota-limited effective state;
- total data limit and used traffic;
- expiry timestamp, including vpn-ui millisecond timestamps;
- IP limit and device limit from current vpn-ui accounts;
- account comment.

Legacy backups preserve username, state, traffic, quota, and expiry but report
that IP/device limits are unavailable.

## Duplicate Policy

The default policy skips an existing AntiMage username. The optional rename
policy appends a deterministic `-vpn<source-id>` suffix while respecting
AntiMage's 34-character username limit. Re-uploading the same backup therefore
does not create another renamed copy.

## API and UI

`POST /api/settings/backup/import/vpn-ui` is sudo-protected under the Backups
permission and accepts multipart fields `file`, `service_id`, and
`duplicate_policy`. The response contains detected, imported, skipped, renamed,
and warning counts only.

The existing Backup menu gains a `Restore from vpn-ui` action. Its responsive
dialog contains the `.db` file dropzone, destination-service selector,
duplicate policy, safety warning, progress state, and import summary. This
action remains available in Docker mode because it changes application data,
not host files; native AntiMage `.rbbackup` restore retains its existing binary
mode restriction.

## Runtime Convergence

Each imported account is created through AntiMage's existing user mutation
service. This creates protocol credentials and node operations consistently
with an ordinary user creation. Restored usage and final state are then written
to the new account, and node operation processing is kicked immediately.

## Verification

Reader tests cover current and legacy vpn-ui schemas, usage aggregation,
millisecond expiry conversion, limits, and unrelated SQLite rejection. API
tests cover backup request body allowance and existing backup regression.
Frontend typecheck/build and the complete frontend and Go test suites must
pass. Live validation still requires importing a real vpn-ui backup into a
disposable AntiMage deployment and connecting a regenerated profile; without
that, live migration is not claimed.
