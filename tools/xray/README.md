# Xray executable folder

The official Xray Windows build is bundled here:

- Windows: `tools/xray/xray.exe` (Xray 26.3.27)
- Linux/macOS: `tools/xray/xray`

Bundled data files (`geoip.dat`, `geosite.dat`) are included for routing rules.

The default Xray profile starts:

```text
xray run -config configs/xray.json
```

Edit `configs/xray.json` with your real VLESS/VMess/Trojan/Shadowsocks settings. The app expects Xray to expose a SOCKS inbound at `127.0.0.1:10808`, unless you change that in the Xray tab.

If you replace the executable yourself, keep the name `xray.exe` (Windows) or `xray` (Linux/macOS), or point the profile to your own path.
