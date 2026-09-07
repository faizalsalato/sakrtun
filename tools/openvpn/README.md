# OpenVPN executable folder

The official OpenVPN Windows build is bundled here:

- Windows: `tools/openvpn/openvpn.exe` (OpenVPN 2.7.6) with its runtime DLLs and `tapctl.exe`
- Linux/macOS: `tools/openvpn/openvpn` (use your distro package)

The app auto-detects this folder first, so the "Executable" field in the OpenVPN tab can stay empty.

Note: the TAP-Windows6 / wintun **drivers** still need to be installed on the machine (they ship with the official OpenVPN installer and are not bundled here).