$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$dashboard = Join-Path $root 'dashboard'
$env:GOCACHE = Join-Path $root '.gocache'

Push-Location $dashboard
try {
    & node_modules\.bin\tsc.cmd --noEmit --pretty false
    if ($LASTEXITCODE -ne 0) { throw "dashboard typecheck failed" }

    npm run build
    if ($LASTEXITCODE -ne 0) { throw "dashboard production build failed" }
} finally {
    Pop-Location
}

Push-Location $root
try {
    go build -buildvcs=false ./cmd/...
    if ($LASTEXITCODE -ne 0) { throw "backend build failed" }

    go test -buildvcs=false ./cmd/... ./internal/protocols ./internal/app/nodeclient ./internal/app/user ./internal/app/xrayconfig ./internal/app/outboundsub
    if ($LASTEXITCODE -ne 0) { throw "verification test suite failed" }

    & (Join-Path $root 'scripts/verify_release_names.ps1')
} finally {
    Pop-Location
}

Write-Output 'AntiMage verification passed.'
