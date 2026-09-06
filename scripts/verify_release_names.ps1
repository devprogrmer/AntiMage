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

$args = @("-n", $patterns, ".")
foreach ($path in $excluded) {
	$args += "-g"
	$args += "!$path/**"
}
$args += "-g"
$args += "!scripts/verify_release_names.ps1"
$args += "-g"
$args += "!go.sum"

Push-Location $root
try {
	$output = & rg @args
	if ($LASTEXITCODE -eq 0) {
		$output
		throw "Product-facing legacy names were found."
	}
	if ($LASTEXITCODE -gt 1) {
		throw "Name verification failed with ripgrep exit code $LASTEXITCODE."
	}
} finally {
	Pop-Location
}

Write-Output "AntiMage product naming verification passed."
