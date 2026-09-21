
$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")

$patterns = "\brebecca\b|\bRebecca\b|\bREBECCA\b|3x-ui|3X-UI|sanaei|alireza"

# The vpn-ui name is required where the migration source is identified to admins.
$vpnUiAllowed = @(
    "dashboard/public/statics/locales/en.json",
    "dashboard/public/statics/locales/fa.json",
    "dashboard/src/components/AntiMageBackupPanel.tsx",
    "dashboard/src/service/settings.ts",
    "dashboard/src/service/settings.test.ts",
    "docs/superpowers/plans/2026-09-21-vpn-ui-migration.md",
    "docs/superpowers/specs/2026-09-21-vpn-ui-migration-design.md",
    "internal/app/api/routes.go",
    "internal/app/api/settings_vpn_ui_migration.go",
    "internal/app/api/settings_vpn_ui_migration_test.go",
    "internal/app/vpnuimigration/reader.go",
    "internal/app/vpnuimigration/reader_test.go"
)

$excluded = @(
    "sources",
    "LICENSES",
    "docs/integration",
    "dashboard/node_modules",
    "internal/proto",
    "dashboard/build",
    "internal/gateway/static/dashboard/build",
    "dist",
    ".git",
    "scripts/verify_release_names.ps1"
)

Push-Location $root
try {
    $files = git ls-files | Where-Object {
        $path = $_.Replace("\", "/")
        -not ($excluded | Where-Object { $path -eq $_ -or $path -like "$_/*" })
    }

    $output = if ($files) {
        Select-String -Path $files -Pattern $patterns
    }

    $vpnUiFiles = $files | Where-Object { $vpnUiAllowed -notcontains $_.Replace("\", "/") }
    if ($vpnUiFiles) {
        $output += Select-String -Path $vpnUiFiles -Pattern "vpn-ui|VPN-UI"
    }

    if ($output) {
        $output
        throw "Product-facing legacy names were found."
    }
}
finally {
    Pop-Location
}

Write-Output "AntiMage product naming verification passed."
