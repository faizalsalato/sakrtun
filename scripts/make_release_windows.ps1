# Packages the Windows build into the zip expected by the app auto-updater.
# Upload SAKRTUN-windows-amd64.zip as an asset of the GitHub release
# (tag vX.Y.Z) in faizalsalato/sakrtun. The app downloads it via
# "Update app".
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

$dist = Join-Path $root "dist"
$exe = Join-Path $dist "SAKRTUN.exe"
if (-not (Test-Path $exe)) {
    throw "dist/SAKRTUN.exe not found. Run build_windows.ps1 first."
}

$out = Join-Path $root "dist\SAKRTUN-windows-amd64.zip"
if (Test-Path $out) {
    Remove-Item -Force $out
}

# Build a clean staging folder with the files the updater should ship.
$staging = Join-Path $env:TEMP "sakrtun-release"
if (Test-Path $staging) {
    Remove-Item -Recurse -Force $staging
}
New-Item -ItemType Directory -Force -Path $staging | Out-Null

Get-ChildItem $dist -File | ForEach-Object {
    if ($_.Name -notlike "*.zip" -and $_.Name -notlike "SAKRTUN-Setup-*.exe") {
        Copy-Item -Force $_.FullName $staging
    }
}
Copy-Item -Recurse -Force (Join-Path $dist "tools") (Join-Path $staging "tools")

Compress-Archive -Path (Join-Path $staging "*") -DestinationPath $out
Remove-Item -Recurse -Force $staging

Write-Host "Release zip written: $out"
Write-Host "Publish it as an asset of release v<version> in faizalsalato/sakrtun."
