# SAKR TUN

SAKR TUN is a native desktop tunnel client for Windows and Linux. It provides offline profile management, SSH-based tunnel modes, embedded TUN routing, optional IPv6, UDPGW support, DNSTT, and Xray integration.

The application uses a native desktop interface. Profiles are created, imported, and exported from the app as `.sakr` files.

## Features

| Feature | Description |
|---|---|
| Direct SSH | Creates a local SOCKS5 server and forwards traffic through SSH. |
| Payload SSH | Connects through an HTTP proxy/payload before the SSH handshake. |
| SSL/TLS SSH | Wraps the SSH tunnel in TLS/SNI. |
| Payload + SSL | Uses TLS/SNI, payload injection, and SSH together. |
| DNSTT + SSH | Uses the embedded DNSTT client and then connects SSH through the local DNSTT endpoint. |
| Xray Core | Starts an external Xray executable and routes traffic through its local SOCKS inbound. |
| TUN mode | Routes system traffic through the tunnel using embedded tun2socks. |
| UDPGW | Enables UDP traffic for SSH-based modes when a server-side UDPGW service is available. |
| IPv6 | Optional IPv6 routing and IPv6 leak protection. |
| Reconnect | Automatically reconnects after tunnel loss when enabled in the profile. |
| Proxy rotation | Tries multiple proxy hosts until one completes the full tunnel handshake. |

## Project folders

```text
profiles/          Local offline .sakr profiles
configs/           Xray configuration files
tools/xray/        Xray executable location
tools/wintun/      Wintun DLL source location before embedding
dist/              Build output
logs/              Runtime and crash logs
```

## Profile format

Profiles use the `.sakr` extension. They are managed by the app UI and stored locally in the `profiles/` folder. Older `.srpc` profiles are still accepted on import.

Xray is the only mode that uses a JSON file directly, because Xray Core requires JSON configuration. The default Xray configuration path is:

```text
configs/xray.json
```

## Windows build requirements

Install:

- Go 1.22 or newer
- MSYS2 UCRT64 GCC
- MSYS2 binutils, for `windres.exe`

Automatic setup:

```powershell
cd SocksRevivePC
powershell -ExecutionPolicy Bypass -File .\scripts\install_windows_compiler_msys2.ps1
```

After setup finishes, open a new PowerShell window and build:

```powershell
cd SocksRevivePC
powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1
```

Run:

```powershell
.\dist\SocksRevivePC.exe
```

The Windows executable includes a UAC manifest, so Windows should ask for Administrator permission when the app starts. Administrator permission is required for Wintun, DNS changes, TUN addresses, and system routes.

The normal Windows build is GUI-only. Helper processes such as route configuration, DNS configuration, Xray, and legacy helper binaries are started without visible console windows.

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
cd SocksRevivePC
chmod +x scripts/build_linux.sh
./scripts/build_linux.sh
./dist/socksrevivepc
```

For TUN mode on Linux, run the app with root privileges:

```bash
sudo ./dist/socksrevivepc
```

## Wintun setup for Windows TUN mode

Windows TUN mode requires the official signed `wintun.dll`.

For 64-bit Windows, place the DLL here before building:

```text
tools/wintun/amd64/wintun.dll
```

Embed it into the final executable:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\embed_wintun_from_tools.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1
```

After embedding, the app extracts the DLL automatically at runtime. Users do not need to manually place `wintun.dll` beside the executable.

Fallback lookup locations are also supported:

```text
Same folder as SocksRevivePC.exe
tools/wintun/amd64/wintun.dll
PATH
```

Use only the official signed Wintun DLL from the WireGuard/Wintun distribution.

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

Place Xray here:

```text
tools/xray/xray.exe   # Windows
tools/xray/xray       # Linux/macOS
```

Edit the Xray configuration file:

```text
configs/xray.json
```

The app expects Xray to expose a local SOCKS inbound. Default:

```text
127.0.0.1:10808
```

The SOCKS host and port in the UI must match the inbound configured in `configs/xray.json`.

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

## Distribution notes

For normal Windows users, distribute:

```text
dist/SocksRevivePC.exe
```

Do not distribute debug builds unless support logs are needed.

If Xray mode is required, include the Xray executable and configuration:

```text
tools/xray/xray.exe
configs/xray.json
```

If Wintun is not embedded, include the official signed DLL beside the EXE or in the supported tools folder.
