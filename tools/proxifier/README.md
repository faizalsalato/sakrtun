# Proxifier (bundled tool)

SAKR TUN launches Proxifier automatically in the **"Proxifier (force apps)"**
route mode. Place your licensed copy of `Proxifier.exe` in this folder
(the whole portable folder layout is expected: the exe, Helper64.exe,
PrxDrvPE*.dll, Settings.ini and Profiles/):

```text
tools/proxifier/
├── Proxifier.exe
├── Helper64.exe
├── ProxyChecker.exe
├── PrxDrvPE.dll
├── PrxDrvPE64.dll
├── Settings.ini
└── Profiles/
```

The app resolves Proxifier in this order:

1. The custom path set in the profile (Main tab, "Proxifier executable")
2. `tools/proxifier/Proxifier.exe` (this bundled copy)
3. `C:\Program Files (x86)\Proxifier\Proxifier.exe`
4. `C:\Program Files\Proxifier\Proxifier.exe`
5. `Proxifier.exe` on the system PATH

The generated profile follows the real Proxifier PE schema (version 102).

> Proxifier is commercial software. It is not redistributed with this
> project's source repository; copy the executable from your own licensed
> installation. The install script (`scripts/init_project.ps1`) does this
> automatically when a Proxifier installation is detected on the machine.
