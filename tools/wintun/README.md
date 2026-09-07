# Wintun for Windows TUN mode

The Go TUN/tun2socks logic is compiled into SocksRevive PC. Windows still needs the official signed `wintun.dll` driver loader because Windows does not provide a pure userspace TUN interface.

You have two options.

## Option A: self-contained EXE / internal Wintun

1. Put the official amd64 `wintun.dll` here:

```text
tools/wintun/amd64/wintun.dll
```

2. Run:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\embed_wintun_from_tools.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1
```

The script copies the DLL into:

```text
internal/wintunloader/assets/wintun/windows/amd64/wintun.dll
```

Then Go embeds it into `SocksRevivePC.exe`. At runtime the app extracts it automatically before creating the TUN adapter.

## Option B: external DLL fallback

Put `wintun.dll` beside `SocksRevivePC.exe` or in:

```text
tools/wintun/amd64/wintun.dll
```

The app will copy/detect it at runtime.

## Notes

- Run the app as Administrator for TUN mode.
- Use only the signed DLL from the official Wintun package.
- The default adapter/device is `wintun`.
