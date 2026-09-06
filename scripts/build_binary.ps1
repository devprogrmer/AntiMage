$ErrorActionPreference = "Stop"

$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $root

$dashboardSource = Join-Path $root "dashboard\build"
if (-not (Test-Path (Join-Path $dashboardSource "index.html"))) {
	$dashboardSource = Join-Path $root "dashboard\dist"
}
if (-not (Test-Path (Join-Path $dashboardSource "index.html"))) {
	throw "Dashboard build is missing. Expected dashboard/build/index.html or dashboard/dist/index.html."
}

$embedTarget = Join-Path $root "internal\gateway\static\dashboard\build"
if (Test-Path $embedTarget) {
	Remove-Item -LiteralPath $embedTarget -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $embedTarget | Out-Null
Copy-Item -Path (Join-Path $dashboardSource "*") -Destination $embedTarget -Recurse -Force
New-Item -ItemType File -Force -Path (Join-Path $embedTarget ".gitkeep") | Out-Null

$dist = Join-Path $root "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null

$distTemplates = Join-Path $dist "templates"
if (Test-Path $distTemplates) {
	Remove-Item -LiteralPath $distTemplates -Recurse -Force
}
$templates = Join-Path $root "templates"
if (Test-Path $templates) {
	Copy-Item -LiteralPath $templates -Destination $distTemplates -Recurse -Force
}

$serverOutput = Join-Path $dist "antimage-server.exe"
$cliOutput = Join-Path $dist "antimage-cli.exe"
$env:GOCACHE = Join-Path $root ".gocache"
$env:GOMODCACHE = Join-Path $root ".gomodcache"
$env:CGO_ENABLED = "0"
go build -trimpath -buildvcs=false -o $serverOutput ./cmd/antimage_gateway
if ($LASTEXITCODE -ne 0) {
	exit $LASTEXITCODE
}
go build -trimpath -buildvcs=false -o $cliOutput ./cmd/antimage_cli
if ($LASTEXITCODE -ne 0) {
	exit $LASTEXITCODE
}

Write-Host "AntiMage gateway built at $serverOutput"
Write-Host "AntiMage CLI built at $cliOutput"
