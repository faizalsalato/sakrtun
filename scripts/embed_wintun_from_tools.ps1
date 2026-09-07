$ErrorActionPreference = "Stop"

$source = "tools\wintun\amd64\wintun.dll"
$targetDir = "internal\wintunloader\assets\wintun\windows\amd64"
$target = Join-Path $targetDir "wintun.dll"

function Test-PEAmd64($Path) {
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 1024) { throw "Invalid wintun.dll: file is too small." }
    if ($bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) { throw "Invalid wintun.dll: missing MZ header." }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -le 0 -or $bytes.Length -lt ($peOffset + 6)) { throw "Invalid wintun.dll: bad PE header." }
    if ($bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset+1] -ne 0x45 -or $bytes[$peOffset+2] -ne 0x00 -or $bytes[$peOffset+3] -ne 0x00) {
        throw "Invalid wintun.dll: missing PE signature."
    }
    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    if ($machine -ne 0x8664) {
        $hex = "0x{0:x4}" -f $machine
        throw "Wrong wintun.dll architecture: got $hex, expected amd64/x64 machine 0x8664. Use wintun\bin\amd64\wintun.dll from the official Wintun ZIP."
    }
}

if (-not (Test-Path $source)) {
    throw "Missing $source. Put the official signed amd64 wintun.dll there first."
}

Test-PEAmd64 $source
New-Item -ItemType Directory -Force -Path $targetDir | Out-Null
Copy-Item -Force $source $target
Write-Host "Embedded Wintun asset prepared: $target"
Write-Host "Now rebuild with: powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1"
