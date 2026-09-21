# vpn-ui Backup Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Import accounts from a Sir-MmD/vpn-ui SQLite backup into a selected existing AntiMage service from the existing Backup menu.

**Architecture:** A read-only source adapter fingerprints and parses vpn-ui SQLite databases into neutral account records. A sudo-protected API validates the destination service and creates AntiMage users through existing mutation rules, then restores source usage without importing foreign secrets. The dashboard provides a dedicated upload dialog with service and duplicate-policy controls.

**Tech Stack:** Go, database/sql, modernc SQLite, Chi HTTP, React, TypeScript, Chakra UI, React Query.

**Spec:** `docs/superpowers/specs/2026-09-21-vpn-ui-migration-design.md`

## Global Constraints

- Foreign databases are opened read-only and schema-fingerprinted.
- Existing AntiMage restore behavior and `.rbbackup` format remain unchanged.
- Admin credentials, private keys, PSKs, TLS keys, and raw host configuration are never imported.
- Every imported user belongs to an existing AntiMage service selected by the administrator.
- Duplicate usernames default to skip; deterministic rename is optional.
- No migration result may expose source secrets.

## Review Focus

- Legacy vpn-ui backups without the `accounts` table must fall back to `client_traffics` plus inbound client JSON.
- Millisecond expiry values must not be interpreted as seconds.
- A malformed or non-vpn-ui SQLite database must create no users.
- Duplicate imports must not duplicate already migrated accounts.
- Disabled, expired, limited, and unlimited accounts must retain their effective state.

---

### Task 1: Source Reader

**Files:**
- Create: `internal/app/vpnuimigration/types.go`
- Create: `internal/app/vpnuimigration/reader.go`
- Test: `internal/app/vpnuimigration/reader_test.go`

**Interfaces:**
- Produces: `Analyze(ctx context.Context, path string) (Analysis, error)` with normalized `Account` values and warnings.

- [x] Write fixtures for current `accounts`, legacy `client_traffics`, limits, expiry, and malformed schema.
- [x] Implement read-only SQLite opening, schema fingerprinting, account normalization, and secret-free summaries.
- [x] Run `go test -count=1 ./internal/app/vpnuimigration`.

### Task 2: Apply Service and API

**Files:**
- Create: `internal/app/api/settings_vpn_ui_migration.go`
- Modify: `internal/app/api/routes.go`
- Test: `internal/app/api/settings_vpn_ui_migration_test.go`

**Interfaces:**
- Consumes: `vpnuimigration.Analysis`.
- Produces: `POST /api/settings/backup/import/vpn-ui` multipart endpoint accepting `file`, `service_id`, and `duplicate_policy`.

- [x] Add API-level tests for deterministic duplicate rename and imported effective status.
- [x] Implement bounded upload handling and current-admin destination checks.
- [x] Create users through the existing user mutation service, preserve usage/status safely, and return imported/skipped/warnings counts.
- [x] Run `go test -count=1 ./internal/app/api ./internal/app/vpnuimigration`.

### Task 3: Dashboard Flow

**Files:**
- Modify: `dashboard/src/service/settings.ts`
- Modify: `dashboard/src/service/settings.test.ts`
- Modify: `dashboard/src/components/AntiMageBackupPanel.tsx`
- Modify: `dashboard/public/statics/locales/en.json`
- Modify: `dashboard/public/statics/locales/fa.json`

**Interfaces:**
- Consumes: migration multipart API and `/v2/services` options.
- Produces: `Restore from vpn-ui` action next to existing Backup/Restore controls.

- [x] Add a service serialization test.
- [x] Implement upload progress and typed result handling.
- [x] Add a responsive dialog with file dropzone, destination service, duplicate policy, warning copy, progress, and result toast.
- [x] Run frontend tests and production build.

### Task 4: Verification and Delivery

**Files:**
- Modify generated embedded dashboard assets through the repository build command when required.

- [x] Run `gofmt` on changed Go files.
- [x] Run migration and API package tests.
- [x] Run `go test -count=1 ./...`.
- [x] Run all frontend tests and build.
- [x] Run `git diff --check` and inspect tracked changes.
- [ ] Commit, push `feat/vpn-ui-backup-migration`, and open a PR to `dev` without merging.
