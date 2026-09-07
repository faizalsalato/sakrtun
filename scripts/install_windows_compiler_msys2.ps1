$ErrorActionPreference = "Stop"

function Write-Info($Message) {
    Write-Host "[SocksRevivePC] $Message"
}

$msysRoot = "C:\msys64"
$bash = Join-Path $msysRoot "usr\bin\bash.exe"

if (-not (Test-Path $bash)) {
    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
        throw "winget was not found. Install MSYS2 manually from https://www.msys2.org, then run this script again."
    }

    Write-Info "Installing MSYS2 with winget..."
    winget install --id MSYS2.MSYS2 -e --accept-source-agreements --accept-package-agreements
    if ($LASTEXITCODE -ne 0) {
        throw "winget failed to install MSYS2. Install MSYS2 manually from https://www.msys2.org."
    }
}

if (-not (Test-Path $bash)) {
    throw "MSYS2 was not found at $msysRoot after installation. Install MSYS2 manually or adjust this script path."
}

Write-Info "Installing UCRT64 GCC and resource compiler toolchain..."
& $bash -lc "pacman -Sy --needed --noconfirm mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-binutils"
if ($LASTEXITCODE -ne 0) {
    throw "pacman failed to install mingw-w64-ucrt-x86_64-gcc/binutils. Open 'MSYS2 UCRT64' and run: pacman -S --needed mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-binutils"
}

$ucrtPath = "C:\msys64\ucrt64\bin"
$currentUserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($currentUserPath -split ';') -notcontains $ucrtPath) {
    [Environment]::SetEnvironmentVariable("Path", "$ucrtPath;$currentUserPath", "User")
    Write-Info "Added $ucrtPath to your user PATH. Open a new PowerShell window before building."
} else {
    Write-Info "$ucrtPath is already in your user PATH."
}

Write-Info "Done. Open a new PowerShell window and run: powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1"
