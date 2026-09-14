# AntiMage

<p align="center">
  <img src="dashboard/src/assets/logo.svg" alt="AntiMage" width="180">
</p>

<p align="center">VPN control plane, subscriptions, native runtimes, and multi-node operations.</p>

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

### Install the dev node binary

The `dev` channel installs the latest successful Linux binary artifact from
the `dev` branch and provisions the node runtime prerequisites:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh \
  | sudo bash -s -- install --version dev
```

The installer asks for the node certificate bundle, service port, and Xray API
port. Copy the complete install bundle from **Nodes** in the panel. Defaults
are `62050` for the node service and `62051` for the Xray API.

```bash
sudo systemctl enable --now antimage-node
sudo systemctl status antimage-node --no-pager
sudo journalctl -u antimage-node -n 200 --no-pager
```

Update an existing dev node with:

```bash
sudo antimage-node update --version dev
sudo systemctl restart antimage-node
```

If no successful dev artifact exists, installation stops instead of silently
using another build. Use `--version latest` for the stable channel.

### Install the latest release node binary

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh \
  | sudo bash -s -- install --version latest
```

Pin a published release when reproducibility is required:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh \
  | sudo bash -s -- install --version v0.1.4
```

### Install a dev Docker node

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node.sh \
  | sudo bash -s -- install --version dev
docker pull ghcr.io/devprogrmer/antimage-node:dev
```

### Install a release Docker node

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node.sh \
  | sudo bash -s -- install --version latest
docker pull ghcr.io/devprogrmer/antimage-node:v0.1.4
```

Do not mix binary and Docker installers for one node directory. Select one
installation mode so the service, environment, certificates, and runtime data
have one owner.

### Install multiple binary nodes on one server

Each node needs its own name, data directory, ports, and certificate bundle:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh \
  | sudo bash -s -- install --name antimage-node-1 --version dev

curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh \
  | sudo bash -s -- install --name antimage-node-2 --version latest
```

The installer prompts for different ports when defaults are already in use.
Check each service separately:

```bash
sudo systemctl status antimage-node-1 --no-pager
sudo systemctl status antimage-node-2 --no-pager
sudo journalctl -u antimage-node-1 -n 100 --no-pager
sudo journalctl -u antimage-node-2 -n 100 --no-pager
```

For multiple Docker nodes, use separate compose projects and different data
directories, host ports, container names, Xray API ports, and certificates.
Register each node separately in the panel and never reuse a node certificate.

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

## Command Reference

The installer scripts expose the same operational commands for binary and
Docker modes. After installation, the installed command is usually available
as `antimage` or `antimage-node`.

### Panel commands

```bash
sudo antimage up
sudo antimage down
sudo antimage restart
sudo antimage status
sudo antimage logs
sudo antimage update --version latest
sudo antimage update --dev
sudo antimage backup
sudo antimage migrate
sudo antimage edit-env
sudo antimage ssl
sudo antimage uninstall
```

Panel installation options:

```text
--dev                         latest successful dev build
--version vX.Y.Z              published or pinned version
--database sqlite|mysql|mariadb database backend
--port PORT                   panel HTTP port
```

Do not combine `--dev` and `--version` in one invocation. Back up the
database and `/var/lib/antimage` before update or uninstall.

### Node commands

```bash
sudo antimage-node up
sudo antimage-node down
sudo antimage-node restart
sudo antimage-node status
sudo antimage-node logs
sudo antimage-node update --version latest
sudo antimage-node update --dev
sudo antimage-node core-update
sudo antimage-node edit
sudo antimage-node uninstall
```

Node installer options:

```text
--dev                         latest successful dev node artifact/image
--version latest|vX.Y.Z|dev   stable, pinned release, or dev channel
--name NODE_NAME              select or create a named node instance
```

The `--name` option is for installation and script installation. Every named
instance must have a unique data directory, service port, Xray API port, and
certificate bundle.

### Script management

The installer can update or remove its local management command without
removing the node data:

```bash
sudo antimage-node script-update
sudo antimage-node script-uninstall
sudo antimage script-update
sudo antimage script-uninstall
```

### Environment and data locations

Binary panel defaults:

```text
/opt/antimage              application and binary
/opt/antimage/.env         panel environment
/var/lib/antimage          database, backups, certificates, runtime data
/etc/antimage              system configuration
```

Binary node defaults:

```text
/opt/antimage-node         application and binary
/opt/antimage-node/.env    node environment
/var/lib/antimage-node     node data, certificates, and Xray core
/etc/systemd/system        systemd service definitions
```

Never commit `.env`, node private keys, certificate private keys, database
files, or subscription tokens.

## Firewall and Port Checklist

Open only the ports actually used by the deployment:

| Component | Default ports | Direction |
| --- | --- | --- |
| Panel HTTP | `8000/tcp` | browser to master or reverse proxy to panel |
| Node API | `62050/tcp` | master to node |
| Xray API | `62051/tcp` on localhost by default | node internal |
| L2TP/IPsec | `500/udp`, `4500/udp`, `1701/udp` | clients to node |
| PPTP | `1723/tcp` and GRE | clients to node |
| OpenVPN | inbound-configured UDP/TCP port | clients to node |
| WireGuard | inbound-configured UDP port | clients to node |
| IKEv2 | `500/udp`, `4500/udp` | clients to node |
| HTTPS | `443/tcp` | browser/subscription clients to reverse proxy |

L2TP/IPsec, PPTP, OpenVPN, and WireGuard also require forwarding and NAT.
The node runtime applies its managed rules; avoid a second unrelated firewall
rule set that changes the same interfaces or tables.

## Configuration Guide

## Supported Protocols

### Xray proxy inbounds

- VLESS
- VMess
- Trojan
- Shadowsocks
- Hysteria

Depending on the protocol and Xray version, the panel supports transports and
security modes such as TCP/raw, WebSocket, gRPC, HTTP/2, HTTP/3, XHTTP, KCP,
QUIC, TLS, and Reality.

### Native and remote-access runtimes

- OpenVPN: supervised native runtime, authentication, callbacks, NAT, and
  TProxy. Default pool: `10.66.0.0/16`.
- WireGuard: native interface, peer lifecycle, routing, and usage reflection.
  Default pool: `10.69.0.0/16`.
- L2TP/IPsec: strongSwan/xl2tpd/pppd, automatic pool and NAT. Default pool:
  `10.67.0.0/16`.
- PPTP: pptpd/pppd, automatic pool and NAT. Default pool: `10.68.0.0/24`.
- IKEv2: remote-access runtime. Default pool: `10.70.0.0/16`.
- AnyConnect: remote-access runtime. Default pool: `10.71.0.0/16`.

The node installer provisions supported dependencies for enabled runtimes. The
host still needs a compatible Linux kernel, `/dev/net/tun`, IP forwarding, and
firewall access to the configured ports. Do not manually create protocol
processes, configs, or NAT rules during normal operation.

## First Panel Setup

1. Create the first administrator and sign in.
2. Confirm panel URL, database, session, and subscription settings.
3. Open **Nodes**, add a node, and copy its certificate/install bundle.
4. Install the binary or Docker node using that bundle.
5. Wait for the node to become connected and synchronize it.
6. Create a service with traffic and expiration policy.
7. Create a user and assign the service.
8. Create an inbound, select its node and protocol, and apply the runtime.
9. Open the user's subscription and verify links, credentials, and QR code.
10. Connect a test client before adding production users.

The panel generates runtime configuration, credentials, policy, routing, and
usage accounting automatically.

## IP or Domain Panel Access

For temporary IP-only testing:

```text
http://SERVER_IP:8000/dashboard/login
```

Restrict port `8000` to trusted addresses. For production, point an A/AAAA
record such as `panel.example.com` to the master and choose the domain option
in the installer or SSL settings. The panel obtains the certificate and stores
domain certificates in `.managed` for SNI; ENV certificate paths remain only
the fallback certificate.

A reverse proxy can forward HTTPS to the local panel listener:

```nginx
server {
    listen 443 ssl http2;
    server_name panel.example.com;
    ssl_certificate /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;
    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

Do not write every domain certificate into ENV. Managed certificates are
selected by SNI, allowing multiple domains to use one panel safely.

## Multiple Admins and Domains

Create one admin account per operator and assign the minimum required role.
For multiple subscription domains on one panel:

1. Point every domain, such as `seller-a.example.com` and
   `seller-b.example.com`, to the same master.
2. Issue or import a certificate for each domain.
3. Enable managed SNI serving for each certificate.
4. In each admin's subscription settings, assign its domain, aliases, path,
   ports, and visible services.
5. Test each domain with its own admin account and test subscription.

This remains one panel, process, and database. Admin permissions control what
each operator can manage; SNI controls which certificate is served for each
hostname.

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
