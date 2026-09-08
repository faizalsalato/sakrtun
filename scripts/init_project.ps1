# SAKR TUN project initialization.
# Prepares a fresh clone so it can build out of the box:
#   1. Go toolchain            (Chocolatey, if missing)
#   2. MSYS2 UCRT64 gcc        (windres, CGO for Fyne)
#   3. Mesa software OpenGL    (DLLs shipped beside the EXE for machines without GPU drivers)
#   4. Inno Setup 6            (installer compiler)
#   5. Xray                    (latest official build -> tools/xray)
#   6. OpenVPN                 (official MSI -> tools/openvpn)
#   7. Logo                    (logo.png + logo.ico)
#   8. Build                   (dist/SAKRTUN.exe + dist/SAKRTUN-Setup-*.exe)
#
# Every step is idempotent: safe to re-run any time.
param(
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

function Write-Step([string]$Message) {
    Write-Host ""
    Write-Host "[init] $Message" -ForegroundColor Cyan
}

function Refresh-Path {
    $env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [Environment]::GetEnvironmentVariable("Path", "User")
}

function Get-Cmd([string]$Name) {
    return Get-Command $Name -ErrorAction SilentlyContinue
}

Refresh-Path

# --- 1. Go ---------------------------------------------------------------
Write-Step "1/8 Go toolchain"
if (Get-Cmd "go") {
    Write-Host "OK: $(go version)"
} else {
    Write-Host "Go not found; installing with Chocolatey..."
    if (-not (Get-Cmd "choco")) {
        throw "Chocolatey is required (https://chocolatey.org/install). Install it, then re-run this script."
    }
    choco install golang -y --no-progress
    Refresh-Path
    Write-Host "OK: $(go version)"
}

# --- 2. MSYS2 UCRT64 compiler -------------------------------------------
Write-Step "2/8 MSYS2 UCRT64 compiler (gcc + windres)"
$bash = "C:\msys64\usr\bin\bash.exe"
if (-not (Test-Path $bash)) {
    Write-Host "MSYS2 not found; installing with Chocolatey..."
    choco install msys2 -y --no-progress --params="/InstallDir:C:\msys64"
}
& $bash -lc "pacman -Sy --needed --noconfirm mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-binutils" | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "pacman failed to install the UCRT64 toolchain."
}
Write-Host "OK: gcc installed"

# --- 3. Mesa software OpenGL --------------------------------------------
Write-Step "3/8 Mesa software OpenGL (UI fallback for machines without GPU)"
$toolsMesa = Join-Path $root "tools\mesa"
New-Item -ItemType Directory -Force -Path $toolsMesa | Out-Null
if ((Test-Path (Join-Path $toolsMesa "opengl32.dll")) -and (Test-Path (Join-Path $toolsMesa "libgallium_wgl.dll"))) {
    Write-Host "OK: mesa DLLs already present"
} else {
    Write-Host "Downloading the official mesa-dist-win release..."
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/pal1000/mesa-dist-win/releases/latest" -TimeoutSec 60
    $url = ($rel.assets | Where-Object { $_.name -like "*release-mingw.7z" }).browser_download_url
    if (-not $url) {
        throw "mesa-dist-win release-mingw.7z not found in release $($rel.tag_name)."
    }
    $sevenZip = "C:\Program Files\7-Zip\7z.exe"
    if (-not (Test-Path $sevenZip)) {
        choco install 7zip -y --no-progress
    }
    $arch = Join-Path $env:TEMP "mesa.7z"
    $ext = Join-Path $env:TEMP "mesa-ext"
    Invoke-WebRequest -Uri $url -OutFile $arch -TimeoutSec 900
    if (Test-Path $ext) { Remove-Item -Recurse -Force $ext }
    & $sevenZip x $arch "-o$ext" -y | Out-Null
    $x64 = Get-ChildItem $ext -Recurse -Directory -Filter "x64" | Select-Object -First 1
    if (-not $x64) {
        throw "x64 folder not found in the mesa archive."
    }
    Copy-Item (Join-Path $x64.FullName "opengl32.dll"), (Join-Path $x64.FullName "libgallium_wgl.dll") $toolsMesa -Force
    Write-Host "OK: mesa $($rel.tag_name) installed in tools/mesa"
}

# --- 4. Inno Setup -------------------------------------------------------
Write-Step "4/8 Inno Setup 6 (installer compiler)"
$iscc = "C:\Program Files (x86)\Inno Setup 6\ISCC.exe"
if (Test-Path $iscc) {
    Write-Host "OK: Inno Setup found"
} else {
    Write-Host "Inno Setup not found; installing with Chocolatey..."
    choco install innosetup -y --no-progress
}

# --- 5. Xray --------------------------------------------------------------
Write-Step "5/8 Xray (bundled executable)"
$toolsXray = Join-Path $root "tools\xray"
New-Item -ItemType Directory -Force -Path $toolsXray | Out-Null
if (Test-Path (Join-Path $toolsXray "xray.exe")) {
    Write-Host "OK: xray.exe already present"
} else {
    Write-Host "Downloading the latest Xray release..."
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/XTLS/Xray-core/releases/latest" -TimeoutSec 60
    $url = ($rel.assets | Where-Object { $_.name -eq "Xray-windows-64.zip" }).browser_download_url
    if (-not $url) {
        throw "Xray asset Xray-windows-64.zip not found in release $($rel.tag_name)."
    }
    $zip = Join-Path $env:TEMP "xray.zip"
    $ext = Join-Path $env:TEMP "xray-ext"
    Invoke-WebRequest -Uri $url -OutFile $zip -TimeoutSec 600
    if (Test-Path $ext) { Remove-Item -Recurse -Force $ext }
    Expand-Archive -Path $zip -DestinationPath $ext -Force
    foreach ($f in @("xray.exe", "geoip.dat", "geosite.dat")) {
        Copy-Item (Join-Path $ext $f) $toolsXray -Force
    }
    Write-Host "OK: Xray $($rel.tag_name) installed in tools/xray"
}

# --- 6. OpenVPN ------------------------------------------------------------
Write-Step "6/8 OpenVPN (bundled executable)"
$toolsO = Join-Path $root "tools\openvpn"
New-Item -ItemType Directory -Force -Path $toolsO | Out-Null
if (Test-Path (Join-Path $toolsO "openvpn.exe")) {
    Write-Host "OK: openvpn.exe already present"
} else {
    $sysBin = Join-Path ${env:ProgramFiles} "OpenVPN\bin"
    if (Test-Path (Join-Path $sysBin "openvpn.exe")) {
        Write-Host "Copying the system OpenVPN install..."
        Copy-Item (Join-Path $sysBin "*") $toolsO -Force
    } else {
        Write-Host "Downloading the official OpenVPN MSI and installing silently (drivers included)..."
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/OpenVPN/openvpn/releases/latest" -TimeoutSec 60
        $ver = $rel.tag_name.TrimStart("v")
        $msiUrl = $null
        foreach ($build in @("I001", "I002", "I003", "I004", "I005")) {
            $candidate = "https://swupdate.openvpn.org/community/releases/OpenVPN-$ver-$build-amd64.msi"
            try {
                $r = Invoke-WebRequest -Uri $candidate -Method Head -TimeoutSec 30 -UseBasicParsing
                if ($r.StatusCode -eq 200) {
                    $msiUrl = $candidate
                    break
                }
            } catch { }
        }
        if (-not $msiUrl) {
            throw "OpenVPN MSI not found for version $ver."
        }
        $msi = Join-Path $env:TEMP "openvpn.msi"
        Invoke-WebRequest -Uri $msiUrl -OutFile $msi -TimeoutSec 600
        Start-Process msiexec -ArgumentList "/i", "`"$msi`"", "/qn", "/norestart" -Wait
        Copy-Item (Join-Path $sysBin "*") $toolsO -Force
    }
    Write-Host "OK: openvpn installed in tools/openvpn"
}

# --- 7. Proxifier ------------------------------------------------------------
Write-Step "7/9 Proxifier (bundled, optional)"
$toolsP = Join-Path $root "tools\proxifier"
New-Item -ItemType Directory -Force -Path $toolsP | Out-Null
if (Test-Path (Join-Path $toolsP "Proxifier.exe")) {
    Write-Host "OK: Proxifier.exe already present"
} else {
    $pfCandidates = @(
        (Join-Path ${env:ProgramFiles(x86)} "Proxifier\Proxifier.exe"),
        (Join-Path $env:ProgramFiles "Proxifier\Proxifier.exe")
    )
    $found = $null
    foreach ($cand in $pfCandidates) {
        if (Test-Path $cand) {
            $found = $cand
            break
        }
    }
    if ($found) {
        Write-Host "Copying the installed Proxifier..."
        Copy-Item (Join-Path (Split-Path $found) "*") $toolsP -Force
        Write-Host "OK: Proxifier copied to tools/proxifier"
    } else {
        Write-Host "Proxifier not detected. Proxifier is commercial software, so it cannot be downloaded automatically." -ForegroundColor Yellow
        Write-Host "If you own a license, copy Proxifier.exe (and its DLLs) into tools\proxifier manually." -ForegroundColor Yellow
    }
}

# --- 8. Logo ---------------------------------------------------------------
Write-Step "8/9 Logo"
powershell -ExecutionPolicy Bypass -File (Join-Path $root "scripts\generate_logo.ps1")

# --- 9. Build ----------------------------------------------------------------
if ($SkipBuild) {
    Write-Step "9/9 Build skipped (-SkipBuild)"
} else {
    Write-Step "9/9 Build (exe + installer)"
    powershell -ExecutionPolicy Bypass -File (Join-Path $root "scripts\build_windows.ps1") -NoTidy
    if ($LASTEXITCODE -ne 0) {
        throw "build_windows.ps1 failed."
    }
    powershell -ExecutionPolicy Bypass -File (Join-Path $root "scripts\build_installer.ps1")
    if ($LASTEXITCODE -ne 0) {
        throw "build_installer.ps1 failed."
    }
}

Write-Host ""
Write-Host "[init] Done. Outputs:" -ForegroundColor Green
Write-Host "  dist\SAKRTUN.exe"
Write-Host "  dist\SAKRTUN-Setup-*.exe"
