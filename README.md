# SAKR TUN

SAKR TUN is a native desktop tunnel client for Windows and Linux. It provides offline profile management, SSH-based tunnel modes, embedded TUN routing, optional IPv6, UDPGW support, DNSTT, Xray, and OpenVPN integration.

The application uses a native desktop interface. Profiles are created, imported, and exported from the app as `.sakr` files (older `.srpc` profiles are still accepted on import).

## Download and install

Download the latest release from:

```text
https://github.com/faizalsalato/sakrtun/releases/latest
```

Each release ships a single installer executable:

```text
SAKRTUN-Setup-<version>.exe
```

Run it (double-click), accept the administrator prompt, and the app installs to:

```text
C:\Program Files\SAKR TUN
```

with Start Menu and Desktop shortcuts, plus an uninstaller registered in Add/Remove Programs.

## Features

| Feature | Description |
|---|---|
| Direct SSH | Creates a local SOCKS5 server and forwards traffic through SSH. |
| Payload SSH | Connects through an HTTP proxy/payload before the SSH handshake. |
| SSL/TLS SSH | Wraps the SSH tunnel in TLS/SNI. |
| Payload + SSL | Uses TLS/SNI, payload injection, and SSH together. |
| DNSTT + SSH | Uses the embedded DNSTT client and then connects SSH through the local DNSTT endpoint. |
| Xray Core | Starts the bundled Xray executable (or your own) and routes traffic through its local SOCKS inbound. |
| OpenVPN | Runs the bundled OpenVPN process with your `.ovpn` config; OpenVPN owns the adapter and routes. |
| TUN mode | Routes system traffic through the tunnel using embedded tun2socks. |
| UDPGW | Enables UDP traffic for SSH-based modes when a server-side UDPGW service is available. |
| IPv6 | Optional IPv6 routing and IPv6 leak protection. |
| Reconnect | Automatically reconnects after tunnel loss when enabled in the profile. |
| Proxy rotation | Tries multiple proxy hosts until one completes the full tunnel handshake. |
| Kill switch | Blocks internet whenever the VPN is not connected (only the VPN servers stay reachable). |
| Custom DNS | Define your own DNS servers for the SOCKS resolver and the TUN adapter. |
| Share links | Import `vless://`, `trojan://`, `ss://` and `vmess://` links; the Xray JSON config is generated automatically. |
| Manual Xray JSON | Create the Xray configuration directly in the UI (with template and load-from-file). |
| Tool updates | Update the bundled Xray and OpenVPN to their latest releases from inside the app. |
| Self-update | "Update app" downloads the latest installer from GitHub and updates silently. |

## Project folders

```text
profiles/          Local offline .sakr profiles
configs/           Xray configuration files and global settings
tools/xray/        Bundled Xray executable + geoip/geosite data
tools/openvpn/     Bundled OpenVPN executable + DLLs
tools/dnstt/       External DNSTT fallback location
tools/wintun/      Wintun DLL source location before embedding
installer/         Inno Setup script (setup.iss)
scripts/           Build, logo, release and diagnostics scripts
dist/              Build output (SAKRTUN.exe, installer)
logs/              Runtime and crash logs
```

## Profile format

Profiles use the `.sakr` extension. They are managed by the app UI and stored locally in the `profiles/` folder. Older `.srpc` profiles are still accepted on import.

Xray is the only mode that uses a JSON file directly, because Xray Core requires JSON configuration. The default Xray configuration path is:

```text
configs/xray.json
```

You can also create the Xray configuration manually in the Xray tab (JSON editor with a VLESS template), or import share links that generate it automatically.

## Windows build requirements

Install:

- Go 1.22 or newer
- MSYS2 UCRT64 GCC
- MSYS2 binutils, for `windres.exe`
- Mesa software OpenGL DLLs in `tools/mesa/` (downloaded automatically by the init script; used for machines without GPU drivers)

### One-command project initialization

From a fresh clone, run the init script; it installs everything that is missing
(Go, MSYS2 toolchain, Mesa, Inno Setup), downloads the bundled tools (Xray,
OpenVPN), regenerates the logo and builds the exe and the installer:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init_project.ps1
```

It is idempotent (safe to re-run). Use `-SkipBuild` to only prepare the
environment without building:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init_project.ps1 -SkipBuild
```

Automatic setup (toolchain only):

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install_windows_compiler_msys2.ps1
```

After setup finishes, open a new PowerShell window and build:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1
```

Then build the installer:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build_installer.ps1
```

Run:

```powershell
.\dist\SAKRTUN.exe
```

The Windows executable includes a UAC manifest, so Windows asks for Administrator permission when the app starts. Administrator permission is required for Wintun, DNS changes, TUN addresses, and system routes.

The normal Windows build is GUI-only. Helper processes such as route configuration, DNS configuration, Xray, and OpenVPN are started without visible console windows.

Optional debug build:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1 -Debug
```

Debug builds may show a console and should only be used for troubleshooting.

## Linux build requirements

Install Go, GCC, and the desktop dependencies required by Fyne. On Debian or Ubuntu:

```bash
sudo apt update
sudo apt install -y golang gcc libgl1-mesa-dev xorg-dev
```

Build and run:

```bash
chmod +x scripts/build_linux.sh
./scripts/build_linux.sh
./dist/socksrevivepc
```

For TUN mode on Linux, run the app with root privileges:

```bash
sudo ./dist/socksrevivepc
```

## Wintun setup for Windows TUN mode

Windows TUN mode uses the official signed `wintun.dll`, which is **embedded into the executable** at build time (from `internal/wintunloader/assets`). The app extracts it automatically at runtime; users do not need to place any DLL manually.

Fallback lookup locations are also supported:

```text
Same folder as SAKRTUN.exe
tools/wintun/amd64/wintun.dll
PATH
```

Use only the official signed Wintun DLL from the WireGuard/Wintun distribution.

## OpenVPN notes

The OpenVPN tab runs the bundled `tools/openvpn/openvpn.exe` (auto-detected; leave the Executable field empty). The app passes `--config <file>` and, when the profile has credentials, writes a temporary auth-user-pass file that is removed when the tunnel stops.

The OpenVPN process creates and manages its own adapter and routes. The TAP-Windows6/wintun **drivers** must be installed on the machine (they ship with the official OpenVPN installer and are not bundled).

The bundled copy is updated with the "Update OpenVPN" button (downloads the official MSI, installs it silently, and refreshes `tools/openvpn`).

## TUN settings

Recommended Windows values:

```text
Device: wintun
Interface name: wintun
MTU: 1500
CIDR: 198.18.0.1/15
Gateway: 198.18.0.1
Route all traffic: enabled
```

When TUN mode starts, the app:

1. Starts the selected tunnel.
2. Waits for the local SOCKS port.
3. Starts the embedded tun2socks engine.
4. Configures the TUN adapter.
5. Adds system routes.
6. Adds bypass routes for the active server or active proxy.

For route-all mode on Windows, the app installs split default routes through Wintun:

```text
0.0.0.0/1
128.0.0.0/1
```

Only the active proxy from a rotation list is bypassed. The full proxy list is not added to the route table.

## Kill switch

The Kill Switch toggle (top bar) blocks all internet access whenever the VPN is not connected:

- Disconnected: default routes (IPv4 and IPv6) are removed; only the VPN servers of the profile stay reachable so the tunnel can reconnect.
- Connecting: the block stays active.
- Connected with TUN route-all (or OpenVPN): the physical routes are restored and the VPN owns the routing.
- Disconnecting: the block is re-applied immediately.

The state is persisted in `configs/settings.json` and re-applied on startup. Requires administrator rights.

## Custom DNS

The Main tab has a "Custom DNS servers" field (comma separated, IPs or hostnames, optionally with `:port`). When set, these servers are used by:

- the local SOCKS DNS-over-SSH resolver (all SSH modes), and
- the TUN adapter (overriding the TUN tab values).

Leave empty for the defaults (`1.1.1.1`, `8.8.8.8`).

## IPv6 settings

IPv6 is optional per profile.

Recommended when the server supports IPv6:

```text
Enable IPv6 through tunnel: enabled
IPv6 CIDR: fd00:534f:434b::1/64
IPv6 DNS: 2606:4700:4700::1111, 2001:4860:4860::8888
Block IPv6 leaks when IPv6 tunnel is off: enabled
```

Recommended when the server does not support IPv6:

```text
Enable IPv6 through tunnel: disabled
Block IPv6 leaks when IPv6 tunnel is off: enabled
```

When IPv6 routing or IPv6 leak protection is enabled, the app uses these IPv6 split routes:

```text
::/1
8000::/1
```

## UDPGW settings

UDPGW allows real UDP traffic through SSH-based modes.

Supported modes:

```text
Direct SSH
Payload SSH
SSL/TLS SSH
Payload + SSL
DNSTT + SSH
```

Recommended server-side listener:

```text
127.0.0.1:7400
```

Recommended client values in the UDPGW tab:

```text
Enabled: on
Protocol: badvpn
Remote UDPGW host: 127.0.0.1
Remote UDPGW port: 7400
```

`127.0.0.1` is resolved from the server side because the UDPGW connection is made through SSH.

Protocol options:

```text
badvpn    Android-compatible BadVPN UDPGW framing with IPv4 and IPv6 target support
legacy    Older IPv4-only frame format
```

When UDPGW is disabled, SSH modes keep a DNS-only fallback: UDP port 53 is converted to DNS-over-TCP through SSH, while other UDP traffic is dropped.

## DNSTT setup

DNSTT mode uses the embedded DNSTT client. Normal DNSTT profiles do not require an external `dnstt-client.exe`.

Create or edit a profile, choose `DNSTT + SSH`, and set:

```text
Embedded engine: enabled
Resolver type: doh, dot, or udp
Resolver: https://cloudflare-dns.com/dns-query, 1.1.1.1:853, or 1.1.1.1:53
Tunnel domain: DNSTT tunnel domain
Server public key: DNSTT server public key in hex
Local SSH host: 127.0.0.1
Local SSH port: 2222
```

Then fill the SSH tab with the SSH account behind DNSTT.

External DNSTT executable fields are kept only for compatibility with older deployments.

## Xray setup

The official Xray Windows build is bundled in `tools/xray/xray.exe` (with `geoip.dat` and `geosite.dat`), so the Executable field can stay empty. It is updated with the "Update Xray" button.

Edit the Xray configuration file:

```text
configs/xray.json
```

or create the configuration manually in the Xray tab (JSON editor), or use "Import link" with `vless://`, `trojan://`, `ss://` or `vmess://` share links.

The app expects Xray to expose a local SOCKS inbound. Default:

```text
127.0.0.1:10808
```

The SOCKS host and port in the UI must match the inbound configured in the JSON.

## Payload syntax

Supported placeholders:

```text
[host]
[port]
[crlf]
[lf]
[cr]
[split]
[instant_split]
[delay=250]
[rotate=a.com;b.com;c.com]
```

Example:

```text
CONNECT [host]:[port] HTTP/1.1[crlf]Host: [host][crlf]User-Agent: SAKRTUN[crlf][crlf]
```

Payload modes read and log multiple immediate HTTP responses. This supports proxy chains that return more than one status before the tunnel is ready, such as:

```text
HTTP/1.1 403 Forbidden
HTTP/1.1 101 Switching Protocols
SSH-2.0-...
```

HTTP status lines are shown in the Logs tab as `Proxy Status` entries.

## Proxy rotation

In Payload SSH mode, the Proxy host field supports multiple entries. Separate entries with `#`, comma, semicolon, or new lines.

Example with a shared port:

```text
52.85.78.104#52.85.78.91#52.85.78.22#52.85.78.28
```

Set the Proxy port field to the shared port, such as:

```text
80
```

Entries may also include individual ports:

```text
52.85.78.104:80#52.85.78.91:8080#proxy.example.com:443
```

The app tries each proxy until the full sequence succeeds:

```text
TCP connect
Payload response
SSH handshake
```

The `[rotate=...]` payload placeholder supports the same separators:

```text
[rotate=host1#host2#host3]
```

## Auto reconnect

Reconnect options are in the Main tab:

```text
Reconnect: on/off
Reconnect delay seconds: default 3
Reconnect max retries: 0 means unlimited
Reconnect check seconds: default 10
```

When reconnect is enabled, the app checks the tunnel periodically. If the tunnel is lost, it:

1. Stops the active tunnel.
2. Removes routes.
3. Stops tun2socks and releases the TUN adapter.
4. Waits for the configured delay.
5. Starts the profile again.

Disconnect and Cancel stop the reconnect loop for the current session.

## Logs and diagnostics

Runtime logs are written to:

```text
logs/runtime.log
logs/crash.log
```

Windows TUN diagnostics:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\diagnose_windows_tun.ps1
```

Use this when the UI says connected but system traffic is not using the tunnel. The expected Windows route-all entries are:

```text
0.0.0.0/1 through Wintun
128.0.0.0/1 through Wintun
```

When IPv6 routing or IPv6 leak protection is enabled, expected IPv6 routes are:

```text
::/1 through Wintun
8000::/1 through Wintun
```

## Releases and self-update

Releases live at:

```text
https://github.com/faizalsalato/sakrtun/releases
```

Each release carries a single installer asset:

```text
SAKRTUN-Setup-<version>.exe
```

Publishing a new version:

1. Bump `internal/app/version.go` (e.g. `1.0.2`).
2. Bump `#define MyAppVersion` in `installer/setup.iss`.
3. Build:
   ```powershell
   powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1 -NoTidy
   powershell -ExecutionPolicy Bypass -File .\scripts\build_installer.ps1
   ```
4. Create a GitHub release with tag `v1.0.2` and attach `dist\SAKRTUN-Setup-1.0.2.exe`.

Users with the app installed click **Update app**: it downloads the installer from the latest release, runs it silently, and the installer closes and restarts the app automatically.

The bundled tools are updated independently from inside the app:

- **Update Xray** (Xray tab): downloads the latest Xray release and replaces `tools/xray`.
- **Update OpenVPN** (OpenVPN tab): downloads the official MSI, installs it silently (updates drivers), and refreshes `tools/openvpn`.
