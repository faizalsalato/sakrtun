# Builds the SAKR TUN installer (dist/SAKRTUN-Setup-1.0.0.exe) with Inno
# Setup. Run build_windows.ps1 first. The user only needs to run the setup
# exe: it installs to Program Files, creates shortcuts and registers an
# uninstaller.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

$issCandidates = @(
    "C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
    "C:\Program Files\Inno Setup 6\ISCC.exe",
    "C:\ProgramData\chocolatey\bin\ISCC.exe"
)
$iscc = $null
foreach ($c in $issCandidates) {
    if (Test-Path $c) {
        $iscc = $c
        break
    }
}
if (-not $iscc) {
    throw "Inno Setup 6 (ISCC.exe) not found. Install it with: choco install innosetup -y"
}

$dist = Join-Path $root "dist"
if (-not (Test-Path (Join-Path $dist "SAKRTUN.exe"))) {
    throw "dist/SAKRTUN.exe not found. Run build_windows.ps1 first."
}

& $iscc (Join-Path $root "installer\setup.iss")
if ($LASTEXITCODE -ne 0) {
    throw "ISCC failed with exit code $LASTEXITCODE"
}

Get-ChildItem $dist -Filter "SAKRTUN-Setup-*.exe" | ForEach-Object {
    Write-Host "Installer built: $($_.FullName)"
}
