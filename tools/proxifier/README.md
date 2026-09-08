# Proxifier (bundled tool)

SAKR TUN launches Proxifier automatically in the **"Proxifier (force apps)"**
route mode. Place your licensed copy of `Proxifier.exe` in this folder:

```text
tools/proxifier/Proxifier.exe
```

The app resolves Proxifier in this order:

1. The custom path set in the profile (Main tab, "Proxifier executable")
2. `tools/proxifier/Proxifier.exe` (this bundled copy)
3. `C:\Program Files (x86)\Proxifier\Proxifier.exe`
4. `C:\Program Files\Proxifier\Proxifier.exe`
5. `Proxifier.exe` on the system PATH

> Proxifier is commercial software. It is not redistributed with this
> project; copy the executable from your own licensed installation. The
> install script (`scripts/init_project.ps1`) does this automatically when a
> Proxifier installation is detected on the machine.
