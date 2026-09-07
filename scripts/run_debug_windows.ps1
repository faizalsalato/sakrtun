$ErrorActionPreference = "Stop"

function Test-IsAdmin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

$root = (Get-Location).Path
$scriptPath = $MyInvocation.MyCommand.Path

if (-not (Test-IsAdmin)) {
    Write-Host "SocksRevivePC debug runner needs Administrator permission for TUN/routes. Showing UAC prompt..."
    $args = "-NoExit -ExecutionPolicy Bypass -File `"$scriptPath`""
    Start-Process -FilePath "powershell.exe" -ArgumentList $args -WorkingDirectory $root -Verb RunAs -Wait
    exit $LASTEXITCODE
}

$exe = Join-Path $root "dist\SocksRevivePC_debug.exe"
if (-not (Test-Path $exe)) {
    throw "dist\SocksRevivePC_debug.exe not found. Build it only when troubleshooting with: powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1 -Debug"
}
Write-Host "Starting debug build as Administrator. If the GUI closes, check logs\crash.log and logs\runtime.log."
& $exe
Write-Host "Process exited with code $LASTEXITCODE"
if (Test-Path "logs\crash.log") {
    Write-Host "Last crash log lines:"
    Get-Content "logs\crash.log" -Tail 80
}
