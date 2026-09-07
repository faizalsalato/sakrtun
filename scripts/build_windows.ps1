param(
    [switch]$NoTidy,
    [switch]$Debug
)

$ErrorActionPreference = "Stop"

function Write-Info($Message) {
    Write-Host "[SocksRevivePC] $Message"
}

function Add-PathIfExists($PathToAdd) {
    if ((Test-Path $PathToAdd) -and (($env:PATH -split ';') -notcontains $PathToAdd)) {
        $env:PATH = "$PathToAdd;$env:PATH"
        Write-Info "Added to PATH for this build: $PathToAdd"
    }
}

function Require-Command($Name, $InstallHint) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "$Name was not found in PATH. $InstallHint"
    }
}

function Run-Native($Exe, $Arguments) {
    Write-Info "$Exe $($Arguments -join ' ')"
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Exe failed with exit code $LASTEXITCODE"
    }
}

# Fyne uses CGO on Windows. Try common MSYS2 MinGW locations before failing.
Add-PathIfExists "C:\msys64\ucrt64\bin"
Add-PathIfExists "C:\msys64\mingw64\bin"
Add-PathIfExists "C:\msys64\clang64\bin"

Require-Command "go" "Install Go 1.22+ and reopen PowerShell."
Require-Command "gcc" "Install MSYS2 and run: pacman -S --needed mingw-w64-ucrt-x86_64-gcc"
Require-Command "windres" "Install MSYS2 windres/binutils: pacman -S --needed mingw-w64-ucrt-x86_64-binutils"

$gccVersion = (& gcc --version | Select-Object -First 1)
$windresVersion = (& windres --version | Select-Object -First 1)
Write-Info "Using compiler: $gccVersion"
Write-Info "Using resource compiler: $windresVersion"

$env:CGO_ENABLED = "1"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

New-Item -ItemType Directory -Force -Path "dist" | Out-Null

$internalWintun = "internal\wintunloader\assets\wintun\windows\amd64\wintun.dll"
$toolWintun = "tools\wintun\amd64\wintun.dll"
if (Test-Path $internalWintun) {
    Write-Info "Embedding internal Wintun DLL from $internalWintun"
} elseif (Test-Path $toolWintun) {
    Write-Info "No embedded Wintun asset found; copying $toolWintun beside the EXE as runtime fallback"
    Copy-Item -Force $toolWintun "dist\wintun.dll"
} else {
    Write-Info "No Wintun DLL found in project. TUN mode will ask for wintun.dll unless you embed it before building."
}


# Copy the bundled helper tools (xray, dnstt-client) into dist so the EXE can
# spawn them from its own tools folder.
if (Test-Path "tools") {
    New-Item -ItemType Directory -Force -Path "dist\tools" | Out-Null
    Copy-Item -Recurse -Force "tools\*" "dist\tools"
    Write-Info "Copied tools/ into dist/tools."
}

$manifest = "cmd\socksrevivepc\socksrevivepc.exe.manifest"
$resource = "cmd\socksrevivepc\socksrevivepc_windows.rc"
$syso = "cmd\socksrevivepc\admin_windows.syso"

# Machines without a hardware OpenGL driver (VMs, remote desktop sessions)
# cannot open the Fyne UI. Ship the Mesa software renderer next to the EXE so
# the app opens there too; the app forces the stable "softpipe" backend.
# On machines with a real GPU you can delete these DLLs from dist/ to use the
# hardware driver instead.
$mesaDlls = @(
    "opengl32.dll", "libgallium_wgl.dll", "libLLVM-22.dll", "libSPIRV-Tools.dll",
    "libsystre-0.dll", "libtre-5.dll", "libffi-8.dll", "libstdc++-6.dll",
    "libgcc_s_seh-1.dll", "libwinpthread-1.dll", "libxml2-16.dll",
    "libiconv-2.dll", "libintl-8.dll", "zlib1.dll", "libzstd.dll"
)
$mesaBin = "C:\msys64\ucrt64\bin"
$missingMesa = $false
foreach ($dll in $mesaDlls) {
    $src = Join-Path $mesaBin $dll
    if (Test-Path $src) {
        Copy-Item -Force $src (Join-Path "dist" $dll)
    } else {
        $missingMesa = $true
    }
}
if ($missingMesa) {
    Write-Info "Some Mesa software-OpenGL DLLs were not found in $mesaBin. The EXE still builds; machines without a GPU driver may fail to open the UI (install with: pacman -S mingw-w64-ucrt-x86_64-mesa)."
} else {
    Write-Info "Copied Mesa software OpenGL DLLs to dist/ (UI fallback for machines without GPU drivers)."
}

if (-not (Test-Path $manifest)) {
    throw "Missing Windows UAC manifest: $manifest"
}
if (-not (Test-Path $resource)) {
    throw "Missing Windows resource script: $resource"
}
Write-Info "Embedding UAC manifest: requireAdministrator"
Run-Native "windres" @("-O", "coff", "-F", "pe-x86-64", "-i", $resource, "-o", $syso)
if (-not (Test-Path $syso)) {
    throw "windres finished but $syso was not created."
}

if (-not $NoTidy) {
    Run-Native "go" @("mod", "tidy")
}

Run-Native "go" @("build", "-trimpath", "-ldflags", "-s -w -H=windowsgui", "-o", "dist/SAKRTUN.exe", "./cmd/socksrevivepc")

if (-not (Test-Path "dist/SAKRTUN.exe")) {
    throw "Build finished but dist/SAKRTUN.exe was not created."
}

Write-Info "Built dist/SAKRTUN.exe"
Write-Info "Normal build is GUI-only: no console window is attached."

if ($Debug) {
    Run-Native "go" @("build", "-trimpath", "-o", "dist/SAKRTUN_debug.exe", "./cmd/socksrevivepc")
    if (-not (Test-Path "dist/SAKRTUN_debug.exe")) {
        throw "Build finished but dist/SAKRTUN_debug.exe was not created."
    }
    Write-Info "Built dist/SAKRTUN_debug.exe for crash/debug logs. This debug EXE may show a console by design."
} else {
    if (Test-Path "dist/SAKRTUN_debug.exe") {
        Remove-Item -Force "dist/SAKRTUN_debug.exe"
    }
    Write-Info "Skipped debug console EXE. Use -Debug only when troubleshooting."
}
Write-Info "Windows UAC: SAKRTUN.exe is marked requireAdministrator and should show the admin prompt before starting."
if (Test-Path $internalWintun) {
    Write-Info "Windows TUN: Wintun is embedded into the EXE and will be extracted automatically at runtime."
} elseif (Test-Path "dist\wintun.dll") {
    Write-Info "Windows TUN: copied wintun.dll beside the EXE."
} else {
    Write-Info "Windows TUN: run as Administrator and provide official wintun.dll, or embed it and rebuild."
}
