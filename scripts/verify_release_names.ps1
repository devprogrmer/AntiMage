
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
    "dist",
    ".git"
)

Push-Location $root
try {
    $files = Get-ChildItem -Recurse -File | Where-Object {
        $path = $_.FullName.Replace("\", "/")
        -not ($excluded | Where-Object { $path -like "*/$_/*" })
    }

    $output = $files | Select-String -Pattern $patterns

    if ($output) {
        $output
        throw "Product-facing legacy names were found."
    }
}
finally {
    Pop-Location
}

Write-Output "AntiMage product naming verification passed."