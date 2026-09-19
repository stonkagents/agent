# Build from scratch and generate MSI / setup exe (Windows)

Step-by-step guide to start from a clean state, build the StonkAgents projects, and produce the **setup exe** (recommended) and/or the `.msi` installer (WiX 6 recommended).

---

## 1. Prerequisites

Install once:

| Tool | Purpose | Check |
|------|---------|--------|
| **Go** 1.25.6+ | Build daemon, CLI, helper, genkeys | `go version` |
| **.NET SDK** (e.g. 6.0 or 8.0) | WiX 6 MSI build (no WiX 3 candle/light needed) | `dotnet --version` |
| **PowerShell** 5.1+ | Run build script | Built-in on Windows 10/11 |

- **Go:** [go.dev/dl](https://go.dev/dl/) — add Go to PATH.
- **.NET SDK:** [dotnet.microsoft.com/download](https://dotnet.microsoft.com/download) — use an LTS SDK (6 or 8). WiX 6 extensions are restored via NuGet when you build the `.wixproj`.

Optional: **WiX 3.x** (candle/light in PATH or `WIX` set) — the script can use WiX 3 instead of WiX 6 if you prefer; otherwise the script uses WiX 6 when candle/light are not found.

**StonkAgents installer assets:** The MSI uses custom banner/dialog bitmaps and an icon. Ensure these exist (see `stonkagents-windows-assets/README.txt`): `stonkagents-windows-assets/icons/StonkAgents.ico`, `stonkagents-windows-assets/wix-bitmaps/wix-banner.bmp`, `stonkagents-windows-assets/wix-bitmaps/wix-dialog.bmp`. If missing, the build may fail or the UI may be broken. To **regenerate the same version** (overwrite existing MSI), use `-Force`: `.\scripts\build-msi.ps1 -Version "0.1.0" -OutDir "dist" -Force`.

**Finish-screen portal URL (`PortalUrl`):** the Setup EXE's "Open the StonkAgents portal" button runs `explorer.exe <PortalUrl>`. `PortalUrl` is a WiX preprocessor variable declared in `installer/wix/StonkAgents.Bundle.wixproj` (default `https://stonkagents.com`) and consumed by `installer/wix/StonkAgentsBundle.wxs` (`LaunchArguments`). `scripts/build-msi.ps1` derives it from the environment it bakes in (itself derived from the tracker URL: `tracker.dev.*` is dev, `tracker.stg.*` is stg, anything else prd, on either domain): dev opens `https://dev.stonkagents.com`, stg `https://stg.stonkagents.com`, prd `https://stonkagents.com`. It passes the value as `-p:PortalUrl=...`. Override for one build with `.\scripts\build-msi.ps1 -Version "0.1.0" -PortalUrl "https://dev.stonkagents.com"`, or when invoking WiX directly: `dotnet build installer\wix\StonkAgents.Bundle.wixproj -p:PortalUrl=https://dev.stonkagents.com ...`.

**Firewall rule:** `setuphelper.exe` (the elevated MSI custom action) adds the inbound allow rule `StonkAgents Agent` scoped to `<InstallDir>\stonkagents-daemon.exe` (`netsh advfirewall firewall add rule ... dir=in action=allow program=... enable=yes`), idempotently. The same rule is what the portal's Permissions step checks (`GET /api/v1/setup/status`, check `firewall`) and can re-add through the controller (`POST /api/v1/setup/firewall`).

---

## 2. Start fresh

**Option A – New clone**

```powershell
git clone https://github.com/stonkagents/agent.git
cd agent
```

(Replace with your fork or branch if needed.)

**Option B – Already have the repo**

From the repo root:

```powershell
git pull
go mod download
```

To do a clean build of the installer (removes previous MSI build artifacts):

```powershell
# Optional: remove installer staging dir so the next MSI build is clean
Remove-Item -Recurse -Force installer\wix\build -ErrorAction SilentlyContinue
```

---

## 3. Build the projects

### 3.1 CLI and daemon (optional for MSI)

If you want the CLI and/or daemon in `bin\` for local use:

```powershell
# CLI (bin\stonkagents-cli.exe)
go build -o bin/stonkagents-cli.exe ./cmd/cli

# Daemon in bin\ (optional)
go build -o bin/sync-daemon.exe ./cmd/daemon/main.go
```

On Git Bash you can use `./scripts/build.sh` to build CLI, daemon, and tracker.

The **MSI build script does not use** `bin\`; it builds its own copies of the daemon, genkeys, and setup helper into `installer\wix\build\`.

### 3.2 What the build script does

When you run the build script (next step), it will:

1. Build **stonkagents-daemon.exe** (daemon), **genkeys.exe**, **setuphelper.exe**, **stonkagents-svc.exe**, **stonkagents-controller-svc.exe** into `installer\wix\build\` (plus the generated **stonkagents.cmd** CLI wrapper)
2. Copy **scripts\daemon\start-daemon.ps1** and **scripts\post-install.ps1** into that folder
3. Run WiX 6 (`dotnet build` on `installer\wix\StonkAgents.wixproj`) or WiX 3 (candle + light on `StonkAgents.v3.wxs`) to produce the MSI
4. **(WiX 6 only, unless `-SkipBundle`)** Download Node.js LTS (Windows x64 MSI) to `installer\wix\build\node\` if not present, then build the Burn bundle (`installer\wix\StonkAgents.Bundle.wixproj` / `StonkAgentsBundle.wxs`) to produce **StonkAgents-Setup-&lt;Version&gt;.exe**

No need to pre-build those binaries into `bin\` for the MSI.

---

## 4. Generate the setup exe and MSI

From the **repo root**:

```powershell
.\scripts\build-msi.ps1 -Version "0.1.0" -OutDir "dist"
```

- **Version:** Use the version you’re releasing (e.g. `0.1.0`). It’s embedded in the MSI and in the output filename.
- **OutDir:** Folder for the final artifacts (default `dist`).
- **-SkipBundle:** Build only the MSI (no Node download, no Burn bundle). Use when Node is managed separately or for CI.

**Output (default):** `dist\StonkAgents-Setup-0.1.0.exe` (recommended installer: installs Node if needed, then StonkAgents) and `dist\StonkAgents-0.1.0.msi` (standalone MSI).

### If the script uses WiX 6

- You’ll see: `Using WiX 4/6 (dotnet build .wixproj)`.
- Requires **dotnet** in PATH. Extensions (WixToolset.Util.wixext, WixToolset.UI.wixext) are restored from NuGet during `dotnet build`.

### If the script uses WiX 3

- Candle and light are in PATH (or `WIX` is set).
- The script compiles `installer\wix\StonkAgents.v3.wxs` and produces the same `dist\StonkAgents-<Version>.msi`.

---

## 5. Quick reference

| Goal | Command (repo root) |
|------|----------------------|
| Clean and build setup exe + MSI | `.\scripts\build-msi.ps1 -Version "0.1.0" -OutDir "dist"` |
| Build MSI only (no bundle) | `.\scripts\build-msi.ps1 -Version "0.1.0" -OutDir "dist" -SkipBundle` |
| Build CLI for local use | `go build -o bin/stonkagents-cli.exe ./cmd/cli` |
| Build daemon for local use | `go build -o bin/sync-daemon.exe ./cmd/daemon/main.go` |
| Run tracker (dev) | `.\scripts\run-tracker-windows.ps1` |

**Installation:** After building, see [Windows Installation Guide](../installation/windows.md) for installing via the setup exe (recommended), MSI, or the single-script install.
