# AntiMage

AntiMage is an independent VPN control panel for Xray services, multi-node
operation, subscriptions, traffic accounting, and system VPN runtimes.

## Included capabilities

- Xray inbound and outbound management
- Multi-node synchronization and runtime health
- WireGuard, OpenVPN, L2TP/IPsec, PPTP, IKEv2, and AnyConnect runtime payloads
- Unified protocol registry and subscription generation
- User limits, expiry, traffic accounting, roles, audit events, and live status
- React dashboard with embedded production assets

## Development

The backend requires Go 1.25 or newer. The dashboard requires Node.js 20 or
newer.

```powershell
./scripts/verify_antimage.ps1
```

The verification script type-checks the dashboard, builds the backend, and
runs the locally supported AntiMage test suites.

## Local release package

```powershell
./scripts/build_binary.ps1
$env:ANTIMAGE_VERSION = "v0.1.0"
./scripts/package_release.ps1
```

The package script creates a Windows archive in `dist/` with the server binary,
CLI binary, templates, license, and SHA-256 checksums.

## Licensing

AntiMage is distributed under the GNU Affero General Public License, version 3.
The complete license text is in `LICENSES/AGPL-3.0.txt`. Additional copyleft
notices for the combined source material are retained in `LICENSES/` and the
source notice files and must remain available in any distribution.

## Release verification

Do not publish a release until the full backend suite, dashboard build, runtime
integration checks, and the target Linux deployment checks pass in CI.
