$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$patterns = "\bRebecca\b|\brebecca\b|vpn-ui|VPN-UI|rebeccapanel|rb-|rb_|rb[A-Z]|RB_"

$excluded = @(
    "sources",
    "LICENSES",
    "docs/integration",
    "dashboard/node_modules",
    "internal/proto",
    "dashboard/build",
    "internal/gateway/static/dashboard/build",
    "dist"
)

$pathspecs = @(".")
foreach ($path in $excluded) {
    $pathspecs += ":(exclude)$path/**"
}
$pathspecs += ":(exclude)scripts/verify_release_names.ps1"
$pathspecs += ":(exclude)go.sum"

Push-Location $root
try {
    $output = & git grep -n -I -E $patterns -- @pathspecs

    if ($LASTEXITCODE -eq 0) {
        $output
        throw "Product-facing legacy names were found."
    }

    if ($LASTEXITCODE -gt 1) {
        throw "Name verification failed with git grep exit code $LASTEXITCODE."
    }
} finally {
    Pop-Location
}

$global:LASTEXITCODE = 0
Write-Output "AntiMage product naming verification passed."
