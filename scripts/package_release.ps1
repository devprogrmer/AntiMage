$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$dist = Join-Path $root "dist"
$server = Join-Path $dist "antimage-server.exe"
$cli = Join-Path $dist "antimage-cli.exe"
$templates = Join-Path $dist "templates"
$license = Join-Path $root "LICENSE"
$version = if ($env:ANTIMAGE_VERSION) { $env:ANTIMAGE_VERSION } else { "dev" }
$packageDir = Join-Path $dist "release"
$packageRoot = Join-Path $packageDir "antimage-$version-windows-amd64"
$archivePath = Join-Path $dist "antimage-$version-windows-amd64.zip"
$checksumPath = Join-Path $dist "checksums.txt"

foreach ($path in @($server, $cli, $templates, $license)) {
	if (-not (Test-Path $path)) {
		throw "Missing release input: $path. Run scripts/build_binary.ps1 first."
	}
}

if (Test-Path $packageRoot) {
	Remove-Item -LiteralPath $packageRoot -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $packageRoot | Out-Null

Copy-Item -LiteralPath $server -Destination $packageRoot
Copy-Item -LiteralPath $cli -Destination $packageRoot
Copy-Item -LiteralPath $templates -Destination $packageRoot -Recurse
Copy-Item -LiteralPath $license -Destination (Join-Path $packageRoot "LICENSE")

if (Test-Path $archivePath) {
	Remove-Item -LiteralPath $archivePath -Force
}
Compress-Archive -LiteralPath $packageRoot -DestinationPath $archivePath

$artifacts = @($server, $cli, $archivePath)
$lines = foreach ($artifact in $artifacts) {
	$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $artifact).Hash.ToLowerInvariant()
	"$hash  $(Split-Path -Leaf $artifact)"
}
$lines | Set-Content -LiteralPath $checksumPath -Encoding ascii

Write-Host "AntiMage release archive: $archivePath"
Write-Host "Checksums: $checksumPath"
