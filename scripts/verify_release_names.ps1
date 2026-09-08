
$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")

$patterns = "\brebecca\b|\bRebecca\b|\bREBECCA\b|vpn-ui|VPN-UI|3x-ui|3X-UI|sanaei|alireza"

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

    if ($output) {
        $output
        throw "Product-facing legacy names were found."
    }
}
finally {
    Pop-Location
}

Write-Output "AntiMage product naming verification passed."
