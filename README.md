# AntiMage

AntiMage is an independent VPN operations panel for running a master control
server, managing users and subscriptions, and coordinating one or more remote
VPN nodes. It is designed for real server deployments with auditable
administration, database-backed state, automated configuration generation, and
release artifacts for Docker and binary installations.

## Features

- Master panel with dashboard statistics, admin login, role-based access, API
  keys, sessions, audit-oriented settings, and maintenance controls.
- User lifecycle management: create, edit, renew, delete, expire, limit, and
  account for traffic by user and service.
- Subscription generation for common client formats, QR links, per-user
  credentials, inbound selection, and traffic/expiration enforcement.
- Multi-node operation with node enrollment, authentication, status reporting,
  synchronization, runtime state, and safe node removal.
- Xray configuration management including inbounds, outbounds, routing, DNS,
  Fake DNS, VLESS/Reality-oriented fields, and generated runtime payloads.
- Service management with service-level limits, usage tracking, traffic
  summaries, and admin-scoped controls.
- Integrations for backup/restore, Telegram notifications, webhooks, external
  apps, and optional outbound helpers such as Warp, NordVPN, Windscribe,
  Psiphon, and Tor where configured.
- Docker and binary deployment flows for the master panel and node agent.

## Requirements

Minimum recommended master server:

- Linux x86_64 server with systemd for binary installs, or Docker Engine with
  Docker Compose for container installs.
- 1 CPU core, 1 GB RAM, and persistent disk for `/var/lib/antimage`.
- Root or sudo access.
- A domain name and HTTPS reverse proxy for production use.
- SQLite for small deployments, or MySQL/MariaDB for larger installations.

Node servers should have:

- Linux server with root or sudo access.
- Network access from the master panel to the node endpoint.
- Public IP or routable private network, depending on your topology.
- Kernel/modules and firewall rules required by the VPN protocols you enable.

## Supported Operating Systems

The project targets Linux servers for production master and node deployments.
Release workflows build Linux packages and Windows command binaries. The
installer scripts are intended for common systemd-based Linux distributions
such as Debian, Ubuntu, and compatible server environments.

## Installation Methods

AntiMage supports:

- Binary master installation with `antimage-binary.sh`.
- Docker master installation with `antimage.sh`.
- Binary node installation with `antimage-node-binary.sh`.
- Docker node installation with `antimage-node.sh`.
- Manual development builds from source.

## Docker Installation

Install required packages:

```bash
sudo apt-get update
sudo apt-get install -y curl ca-certificates git docker.io docker-compose-plugin
sudo systemctl enable --now docker
```

Clone the repository:

```bash
git clone https://github.com/devprogrmer/AntiMage.git
cd AntiMage
```

Create an environment file:

```bash
cp .env.example .env 2>/dev/null || touch .env
```

Set production values in `.env`. A small SQLite-style baseline looks like this:

```dotenv
UVICORN_HOST=0.0.0.0
UVICORN_PORT=8000
ANTIMAGE_GATEWAY_ADDR=:8000
ANTIMAGE_DATA_DIR=/var/lib/antimage
ANTIMAGE_CERT_BASE=/var/lib/antimage/certs
ANTIMAGE_CONFIG_DIR=/etc/antimage
JWT_ACCESS_TOKEN_EXPIRE_MINUTES=1440
```

Start with Docker Compose:

```bash
docker compose up -d
docker compose logs -f antimage
```

Or use the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install
```

Pull published images directly:

```bash
docker pull ghcr.io/devprogrmer/antimage:latest
docker pull ghcr.io/devprogrmer/antimage:v0.1.4
docker pull ghcr.io/devprogrmer/antimage-node:latest
docker pull ghcr.io/devprogrmer/antimage-node:v0.1.4
```

For MySQL or MariaDB:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install --database mysql
```

## Binary Installation

Install the master panel:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install --version v0.1.4
```

Default binary paths:

- Application: `/opt/antimage`
- Configuration: `/opt/antimage/.env`
- Data, certificates, and backups: `/var/lib/antimage`
- System service: `antimage.service`
- Default HTTP port: `8000`

Check service state:

```bash
sudo systemctl status antimage --no-pager
sudo journalctl -u antimage -f
```

## Panel Access

After installation, create the first administrator:

```bash
sudo antimage-cli admin create --username admin --role full_access --password 'change-this-strong-password'
```

Open the panel:

```text
https://panel.example.com/dashboard/login
```

For local testing without a reverse proxy, use:

```text
http://SERVER_IP:8000/dashboard/login
```

Production deployments should use HTTPS, a firewall, and a reverse proxy. Do
not expose the management port directly to the public internet unless you have
explicit network controls in place.

## Node Installation

Prepare a node server with sudo/root access, open the required VPN ports, and
make sure the master can reach the node address.

Install the binary node agent:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install --version v0.1.4
```

For multiple node agents on one server:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install --name antimage-node-2 --version v0.1.4
```

Default node paths:

- Application: `/opt/antimage-node`
- Data and certificates: `/var/lib/antimage-node`
- System service: `antimage-node.service`

Install a Docker node:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node.sh | sudo bash -s -- install
```

Connect a node to the master panel:

1. Log in to the AntiMage dashboard as an administrator.
2. Open the Nodes page.
3. Add the node address, port, name, and authentication details requested by
   the panel.
4. Start or restart the node agent.
5. Wait for the node status to become connected.
6. Review reported node version, Xray version, traffic, CPU, memory, and last
   synchronization state.

Node troubleshooting:

- Check node logs with `sudo journalctl -u antimage-node -f`.
- Confirm the master can reach the node host and port.
- Confirm node certificates and enrollment credentials were not copied between
  unrelated nodes.
- Confirm firewall rules allow the selected inbound and management ports.
- Restart the node after changing runtime or certificate settings.

## Configuration Guide

Core settings:

- Configure the Xray executable/version used by nodes.
- Review generated config before applying broad changes.
- Keep protocol-specific settings compatible with the target Xray version.

Proxy settings:

- Manage inbounds, outbounds, routing, DNS, Fake DNS, and Reality/VLESS fields
  from the dashboard.
- Apply changes to the intended node or service scope.
- Verify generated runtime payloads after major routing or DNS edits.

Subscription settings:

- Configure public panel URL, subscription port, generated links, QR display,
  and service visibility.
- Verify every user receives only the links and services intended for them.

Backup settings:

- Store backups outside the application directory when possible.
- Include database, configuration, certificates, and templates.
- Test restore on a non-production instance before relying on a backup policy.

## Administration Guide

User management:

- Create users with explicit traffic and expiration limits.
- Renew or extend users from the Users page.
- Delete users only after confirming active subscriptions and services.
- Use quick traffic actions for operational adjustments.

Traffic management:

- Monitor dashboard totals, user traffic, service traffic, and node state.
- Review created-traffic counters when admins create or edit users.
- Confirm usage recording remains healthy after database or node changes.

Service management:

- Create services for distinct products or traffic pools.
- Assign limits and visibility by service.
- Review service summaries before large user imports or renewals.

Monitoring:

- Watch dashboard statistics after deploys and restarts.
- Use system logs for binary services.
- Use Docker logs for compose deployments.
- Configure Telegram/webhooks only with production-safe tokens and endpoints.

## External Features

AntiMage includes integration points for:

- Backup and restore.
- Telegram notifications.
- Webhooks.
- External applications hosted through the panel.
- Optional helper integrations such as Warp, NordVPN, Windscribe, Psiphon, and
  Tor where the deployment enables them.

Every external integration should be tested with non-production credentials
before being enabled for live users.

## Security

- Create individual admin accounts; avoid shared credentials.
- Use the minimum role required for each administrator.
- Rotate API keys and integration tokens when operators change.
- Keep session duration appropriate for your deployment.
- Put the panel behind HTTPS.
- Restrict management access by firewall, VPN, or trusted IP ranges when
  possible.
- Keep backups and certificates private.
- Review permissions after importing or migrating data.

## Database Operations

Fresh installation migration test:

```bash
go test -buildvcs=false ./internal/app/migrations
```

Upgrade migration test:

```bash
go test -buildvcs=false ./internal/app/migrations ./internal/platform/db
```

Backup and restore test:

```bash
go test -buildvcs=false ./internal/app/backup
```

Before production upgrades:

1. Stop write-heavy jobs or imports.
2. Create a database backup.
3. Back up `/var/lib/antimage` and `/opt/antimage/.env`.
4. Upgrade on a staging copy first.
5. Verify dashboard login, user list, subscriptions, nodes, and traffic totals.

## Docker Operations

Fresh server test:

```bash
docker compose pull
docker compose up -d
docker compose ps
docker compose logs --tail=200 antimage
```

Restart behavior:

```bash
docker compose restart antimage
docker compose logs --tail=200 antimage
```

Environment configuration:

- Keep secrets in `.env`.
- Do not commit production `.env` files.
- Pin image tags for production rollouts.
- Use `latest` only when you intentionally want automatic stable updates.

## Upgrade Guide

Binary upgrade:

```bash
sudo antimage update --version v0.1.4
sudo antimage restart
```

Docker upgrade:

```bash
docker compose pull
docker compose up -d
```

After every upgrade:

- Confirm migrations completed.
- Log in as an administrator.
- Check dashboard statistics.
- Open Users, Nodes, Services, Settings, and Subscriptions.
- Confirm node synchronization.
- Generate a test subscription for a non-production user.

Rollback preparation:

- Keep the previous image tag or binary archive.
- Keep a database backup from before the migration.
- Keep configuration and certificate backups.
- Record the release tag currently installed on every master and node.

## Troubleshooting

Common errors:

- Panel does not open: check service state, port `8000`, firewall, and reverse
  proxy configuration.
- Login fails: verify the admin account exists and the database path/connection
  string points to the expected database.
- Subscriptions are empty: confirm the user has active services, unexpired
  limits, and reachable inbounds.
- Node stays disconnected: check node logs, certificate/enrollment settings,
  network reachability, and time synchronization.
- Traffic is not updating: verify node synchronization and database writes.
- Docker container restarts: inspect `docker compose logs antimage` and `.env`.

Useful logs:

```bash
sudo journalctl -u antimage -f
sudo journalctl -u antimage-node -f
docker compose logs -f antimage
docker compose logs -f antimage-node
```

## Development Verification

Run the full local verifier:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\verify_antimage.ps1
```

Run all Go tests:

```bash
go test -buildvcs=false ./...
```

Run release naming checks:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\verify_release_names.ps1
```

Build release binaries locally:

```bash
bash scripts/build_binary.sh
```

## Release Notes: v0.1.4

AntiMage v0.1.4 adds the production native OpenVPN inbound runtime for Linux
binary nodes, including authentication, user policy enforcement, session and
device tracking, quota/accounting support, and transparent TCP/UDP routing
through Xray using TPROXY.

Highlights:

- Added production native OpenVPN runtime management on Linux nodes.
- Added OpenVPN authentication and user status/expiry enforcement.
- Added traffic quotas, live cutoff, device limits, and session callbacks.
- Added persisted OpenVPN usage accounting and acknowledgement handling.
- Routed OpenVPN TCP/UDP client traffic through Xray with Linux TPROXY.
- Added graceful OpenVPN and TPROXY lifecycle cleanup.
- Added runtime/controller tests for the OpenVPN implementation.
- Verified a real OpenVPN client path through an L3 relay, AntiMage, TPROXY,
  Xray, and Internet egress.

Full notes are kept in `.github/release-notes/v0.1.4.md`.
## Release Notes: v0.1.3

AntiMage v0.1.3 focuses on completing the AntiMage rename, stabilizing node
protobuf generation, improving admin traffic-limit correctness, cleaning
subscription/import behavior, tightening embedded application response headers,
and keeping release builds fast.

Highlights:

- Fixed generated node protobuf descriptors after the AntiMage package rename.
- Removed obsolete panel names from release-facing product code and docs.
- Improved admin per-service traffic controls and created-traffic accounting.
- Improved user management UX and compact delete confirmations.
- Improved legacy import compatibility for usernames, subscriptions, and link
  generation.
- Persisted generated subscription templates across restarts.
- Reduced SQLite usage-lock failures during high-frequency usage recording.
- Added security headers for embedded FastCGI application responses.
- Disabled CodeQL for this release while keeping the normal CI, database,
  dashboard, binary, and release verification workflows active.
- Updated release examples and release notes for `v0.1.3`.

Full notes are kept in `.github/release-notes/v0.1.3.md`.

## License

AntiMage is released under the GNU AGPL-3.0 license. See `LICENSE` and the
notices in `LICENSES/` for details.
