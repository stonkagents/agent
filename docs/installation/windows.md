# Windows Installation Guide

This guide explains how to install StonkAgents on Windows: single-script install with optional paths, MSI installer, or quick user install.

## Prerequisites

- **Windows 10** (version 1903+) or **Windows 11**
- **PowerShell 5.1+** (included with Windows 10/11)
- For **single-script** or **MSI**: Administrator privileges (UAC once) for installing the Windows service
- For **quick install** (no service): Go 1.25.6+ in PATH is required; Administrator is NOT required

---

## Option A: Single-script install (recommended for service)

One script that self-elevates (UAC), installs the binary and config, and installs/starts the Windows service. You can choose install, secrets, and data (download/upload) locations.

### Run the script

From the project root (or from a release zip that contains the script and `sync-daemon.exe`):

```powershell
.\scripts\install_stonkagents.ps1
```

If not running as Administrator, the script will re-launch with elevation (UAC once).

### Optional paths

| Parameter      | Default                         | Description                          |
| -------------- | ------------------------------- | ------------------------------------ |
| `-InstallPath` | `C:\Program Files\StonkAgents`     | Binary, config, launcher, daemon.env |
| `-SecretsPath` | `%USERPROFILE%\.stonkagents`      | Directory for `secrets.env`          |
| `-DataPath`    | `%USERPROFILE%\.stonkagents\data` | Download/upload and chunks DB        |

Examples:

```powershell
.\scripts\install_stonkagents.ps1 -InstallPath "D:\StonkAgents" -DataPath "E:\StonkAgentsData"
.\scripts\install_stonkagents.ps1 -SecretsPath "D:\Secrets" -DoNotStartService
```

Other options: `-BuildFromSource` (build from repo if Go is present), `-ServiceName`, `-SkipPrereqCheck`, `-DoNotStartService`.

### What gets installed

- **Install dir:** `sync-daemon.exe`, `config.yaml`, `start-daemon.ps1`, `daemon.env`
- **Secrets dir:** `secrets.env` (created and key generated if missing, when Go or `genkeys.exe` is available)
- **Data dir:** used as `data_dir` in config; daemon creates `downloads/`, chunks DB, etc. here
- **Service:** `StonkAgentsDaemon` (auto-start, restart on failure). The service runs the launcher script, which reads `daemon.env` for config and secrets paths and starts the daemon.

### Binary source

- Place a pre-built `sync-daemon.exe` next to the script or in `.\bin\`, **or**
- Run from the repo with Go installed and use `-BuildFromSource` to build the daemon

Release zip can include: `install_stonkagents.ps1`, `sync-daemon.exe`, and optionally `genkeys.exe` for key generation without Go.

---

## Option B: Setup exe (recommended)

**StonkAgents-Setup-&lt;Version&gt;.exe** is the recommended installer. It installs **Node.js LTS** (if not already installed), then the StonkAgents MSI (daemon, controller, secrets, post-install). One double-click gives you Node (when needed), the daemon, and optional global `stonkagents` CLI (the StonkAgents agent CLI, `stonkagents` npm package).

1. Download **StonkAgents-Setup-0.1.0.exe** (or the version you need).
2. Run it; when Windows asks to allow changes, click **Yes**.
3. The wizard installs Node.js (if missing), then StonkAgents. No extra steps required.

**From PowerShell:** Use the current directory prefix so the exe is found: `.\StonkAgents-Setup-0.1.0.exe`. To capture a log: `.\StonkAgents-Setup-0.1.0.exe /log C:\temp\bundle-log.txt`. Silent install: `.\StonkAgents-Setup-0.1.0.exe /quiet`.

The standalone **StonkAgents-&lt;Version&gt;.msi** (Option C) remains available for environments where Node is already installed or managed separately.



**Dev and staging builds** install next to production, not over it: `StonkAgents Dev` and `StonkAgents Staging` have their own install and data folders, services, ports (daemon 7861/7851, controller 7860/7850), firewall rule, CLI profile and UpgradeCodes. The paths in this page are production's; see [Side-by-side environments](environments.md) for the full table.

---

## Option C: MSI installer (standalone)

An MSI package provides a setup UI where you choose the install location (binary, config, launcher). Use this when Node.js is already on the machine or managed by your organization. Secrets and data directories default to your user profile (`.stonkagents` and `.stonkagents\data`) and are used by the post-install step.

### Build the MSI and setup exe

**Prerequisites:** Go 1.25.6+ and either:

- **WiX 6 (recommended):** [.NET SDK](https://dotnet.microsoft.com/download) (e.g. 6.0 or 8.0) in PATH — no WiX 3 install needed; extensions come from NuGet when building the `.wixproj`.
- **WiX 3.x:** [WiX Toolset 3.x](https://wixtoolset.org/) (candle and light in PATH or `WIX` env set).

From the repo root:

```powershell
.\scripts\build-msi.ps1 -Version "0.1.0" -OutDir "dist"
```

**Output:** `dist\StonkAgents-Setup-0.1.0.exe` (recommended installer) and `dist\StonkAgents-0.1.0.msi` (standalone MSI). To build only the MSI (no Node download, no setup exe), use `-SkipBundle`.

For full **start-fresh steps** (clone, build projects, generate installers), see [Build and MSI (Windows)](../development/build-and-msi-windows.md).

### Install via setup exe or MSI (end-user: everything in one step)

1. **Double-click** the setup exe (e.g. `StonkAgents-Setup-0.1.0.exe`) or the MSI (e.g. `StonkAgents-0.1.0.msi`).
2. When Windows asks **“Do you want to allow this app to make changes?”**, click **Yes**. (The installer needs this to install files and create the Windows service.)
3. Choose the install location (default: `C:\Program Files\StonkAgents`) and complete the wizard.
4. The installer will:
   - Copy files to the chosen folder
   - Create config and **secrets** in your user profile (`%USERPROFILE%\.stonkagents`, including `secrets.env`)
   - **Create and start the Windows services** `StonkAgentsDaemon` and `StonkAgentsController` (daemon runs in the background, starts automatically on reboot)
   - Register and start the scheduled task **"StonkAgents command tools"**, which finishes the setup in the background after the installer has closed (see below)

The setup exe shows the step label (`Step 1 of 2: Installing the Node.js runtime, 1 to 3 minutes` when Node.js is missing, then `Step 2 of 2: Agent service, under a minute`), the progress bar and the current action under it. The whole install takes 1 to 2 minutes. Cancel rolls the install back. The finish page says the command tools are being set up in the background and offers **Open portal**.

**The command tools finish in the background.** Installing the `stonkagents` npm CLI (about 660 packages, 600 MB), onboarding it with the daemon's key and installing and starting the OpenClaw gateway task takes 3 to 5 minutes on a fresh machine and 5 to 15 on a slow disk, behind an antivirus or with npm trouble. Since 2.6.0 that no longer happens inside the installer: `setuphelper.exe` registers the scheduled task **"StonkAgents command tools"** (Task Scheduler library root; `StonkAgents Dev command tools` and `StonkAgents Staging command tools` for those environments) for the user logged on at the console, running with that user's own token (no stored password, "run only when user is logged on", highest available privileges), and starts it as soon as the services are up. The task runs `stonkagents-tools.exe --env <env> --background --data-dir "<data folder>"` from the install folder, hidden:

1. `post-install.ps1`: wait for the daemon, `npm install -g stonkagents` (the package spec comes from `STONKAGENTS_NPM_SPEC`, legacy `STONKAGENTS_NPM_SPEC`), `doctor --fix`, `onboard --skip-daemon`.
2. `gateway-setup.exe`: `stonkagents gateway install`, the hidden-window task, start.

Progress goes to the status file `%USERPROFILE%\Documents\.stonkagents\data\command-tools.json` (the data folder of the environment), rewritten on every change and at least every 5 seconds:

```json
{
  "state": "running",
  "phase": "Downloading the command tools",
  "detail": "213 package files ready",
  "started_at": "2026-09-17T12:00:00Z",
  "updated_at": "2026-09-17T12:01:42Z",
  "log_path": "C:\Users\me\AppData\Local\Temp\stonkagents-tools.log",
  "attempt": 1,
  "pid": 4242
}
```

`state` is `running`, `ready` or `failed` (`not_started` when the file is missing); a final state adds `finished_at`, `error` (failed only, the exit status plus the script's last line), `cli_path` (the installed shim) and `gateway_task`. The controller serves it on `GET http://127.0.0.1:7840/setup/command-tools` (also through the daemon at `/api/v1/controller/setup/command-tools`), adding `cli_present`, `gateway_running` and `task_name`; a running job whose file has not moved for 2 minutes is reported as failed. The portal shows a **Command tools** chip next to the agent status (`setting up`, with the phase and elapsed time; `ready`, which hides itself after a minute; or `failed` with the error and a **Retry** button) and keeps the **Open OpenClaw** link disabled until the state is ready.

**Retry:** the portal's Retry button (`POST /setup/command-tools/retry` on the controller, through the daemon proxy) starts the scheduled task again, registering it first when it is missing. By hand: Task Scheduler, run "StonkAgents command tools"; or `schtasks /Run /TN "StonkAgents command tools"`; or double-click `stonkagents-tools.exe` in the install folder (it runs silently and updates the same status file). The Start Menu shortcut **Configure StonkAgents AI** runs only the script part in a visible PowerShell window.

Logs: `%TEMP%\stonkagents-tools.log` (the script's whole output, npm's per-file lines included) and `%TEMP%\gateway-setup.log`, both of the user the task ran as; see [Side-by-side environments](environments.md) for the dev and staging names. The MSI log (`%TEMP%\StonkAgents_*.log` from the setup exe) shows whether the task was registered and started (`command tools task "..." runs as ...`, `command tools task started`).

No extra steps or commands are required. The daemon runs as a service after install.

**Optional, StonkAgents AI onboarding with default credit:** If you use the StonkAgents agent CLI ([stonkagents CLI](https://github.com/stonkagents/cli), `stonkagents` npm package) and the tracker exposes the guest-key API, use the Start Menu shortcut **"Configure StonkAgents AI"**. It takes the daemon's peer key and runs `stonkagents onboard --auth-choice stonkagents-ai ... --skip-daemon --skip-health --non-interactive` (config only, with a 300 s limit; the gateway task is installed and started by `gateway-setup.exe`, which the background command tools job runs after the script). Node.js must be in PATH (the script installs `stonkagents` globally with npm). Tracker URL defaults to `https://tracker.stonkagents.com` or set `STONKAGENTS_TRACKER_URL`.

**If the service “started and then stopped”:** Open `C:\Program Files\StonkAgents\daemon-service.log` (or the install folder you chose). The daemon’s stdout/stderr are written there so you can see the error (e.g. config, secrets, or log directory). Fix the cause (e.g. run `stonkagents-cli init`, ensure `secrets.env` has a real key, or fix permissions on the data/logs path) and start the service again.

### Uninstall

Use Windows “Add or remove programs” or run the MSI with uninstall. The installer stops and removes the services, the firewall rule, the "StonkAgents command tools" scheduled task (ending it if it is still running) and the install directory; secrets and data directories under your profile (the status file included) are left in place unless you remove them manually. The npm CLI, its config under `~\.openclaw` and the "OpenClaw Gateway" task are the CLI's own and stay as well.

---

## Option C: Quick user install (no service)

### Single-Command Install

```powershell
iwr -useb https://raw.githubusercontent.com/StonkAgents/StonkAgents/main/scripts/quick_install.ps1 | iex
```

The script will:

1. ✅ Build the daemon binary (`sync-daemon.exe`)
2. ✅ Install binary to `%LOCALAPPDATA%\StonkAgents\bin`
3. ✅ Create config directory at `%USERPROFILE%\.stonkagents\`
4. ✅ Generate keys and secrets in `%USERPROFILE%\.stonkagents\secrets.env`
5. ✅ Add the install directory to your user PATH

### Step 3: Verify Installation

Check that the service is running:

```powershell
sc.exe query StonkAgentsDaemon
```

Expected output:

```
SERVICE_NAME: StonkAgentsDaemon
TYPE               : 10  WIN32_OWN_PROCESS
STATE              : 4  RUNNING
```

## Start the daemon

```powershell
sync-daemon.exe
```

## Configuration

### Config File Location

The daemon config file is located at:

```
%USERPROFILE%\.stonkagents\config.yaml
```

Example: `C:\Users\YourName\.stonkagents\config.yaml`

### Log Files Location

Logs are written to:

```
%USERPROFILE%\.stonkagents\logs\daemon.log
```

View logs in real-time:

```powershell
Get-Content "$env:USERPROFILE\.stonkagents\logs\daemon.log" -Tail 50 -Wait
```

### Service Recovery Configuration

The service is configured to automatically restart on failure:

- **Failure action**: Restart service
- **Restart delay**: 60 seconds
- **Reset period**: 24 hours

To modify recovery settings:

```powershell
sc.exe failure StonkAgentsDaemon reset= 86400 actions= restart/60000/restart/60000/restart/60000
```

## Uninstallation

Remove binaries and data:

By default, user data is preserved during uninstallation. To remove it:

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\.stonkagents"
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\StonkAgents"
```

Or use the `-RemoveUserData` flag during uninstallation:

```powershell
.\scripts\uninstall_daemon_windows.ps1 -RemoveUserData
```

## Troubleshooting

### No folder under Program Files after install

If you run the MSI and **no StonkAgents folder** appears under `C:\Program Files\`:

1. **Use the latest MSI** — Rebuild with `.\scripts\build-msi.ps1 -Version "0.2.0" -OutDir "dist"` and install `dist\StonkAgents-0.2.0.msi`. The installer is set so that even if the post-install step (service creation) fails, the **files are not rolled back** — you will get `C:\Program Files\StonkAgents` with the binaries.
2. **Run the MSI elevated** — Right-click the MSI → "Run as administrator" so it can write to Program Files.
3. **Check install log** — Run `msiexec /i dist\StonkAgents-0.2.0.msi /l*v install.log` and search for errors or "rollback".

After a successful file copy you should see `C:\Program Files\StonkAgents` with `stonkagents-daemon.exe`, `stonkagents-svc.exe`, `stonkagents-controller-svc.exe`, `setuphelper.exe`, `genkeys.exe`, `stonkagents.cmd`, and `start-daemon.ps1`. If the Windows service was not created, run `.\scripts\install_stonkagents.ps1` from the repo or configure the service manually.

### Daemon Won't Start

**Check Windows Event Viewer**:

1. Press `Win + R`, type `eventvwr.msc`, press Enter
2. Navigate to: Windows Logs → Application
3. Look for errors from "StonkAgentsDaemon"

**Check log file**:

```powershell
Get-Content "$env:USERPROFILE\.stonkagents\logs\daemon.log" -Tail 50
```

**Verify binary exists**:

```powershell
Test-Path "C:\Program Files\StonkAgents\stonkagents-daemon.exe"
```

**Try running manually** (for debugging):

```powershell
& "C:\Program Files\StonkAgents\stonkagents-daemon.exe"
```

### Daemon Crashes on Startup

**Check config file syntax**:

```powershell
notepad "$env:USERPROFILE\.stonkagents\config.yaml"
```

**Reset to default config**:

```powershell
Copy-Item -Path ".\config.yaml.example" -Destination "$env:USERPROFILE\.stonkagents\config.yaml" -Force
```

**Restart service**:

```powershell
.\scripts\service_control_windows.ps1 restart
```

### Permission Errors

Re-run the install command to refresh PATH and binaries.

### Port Already in Use

If the default port (7800) is in use, edit the config file:

```yaml
# %USERPROFILE%\.stonkagents\config.yaml
server:
  port: 7801 # Change to a different port
```

Then restart the daemon.

### Firewall Blocking Connections

**Add firewall rule** for P2P port (4001 by default):

```powershell
New-NetFirewallRule -DisplayName "StonkAgents P2P" -Direction Inbound -Protocol TCP -LocalPort 4001 -Action Allow
New-NetFirewallRule -DisplayName "StonkAgents HTTP" -Direction Inbound -Protocol TCP -LocalPort 7800 -Action Allow
```

## Advanced Configuration

### Custom Installation Path

**Single-script install** (recommended): choose install, secrets, and data paths:

```powershell
.\scripts\install_stonkagents.ps1 -InstallPath "D:\StonkAgents" -SecretsPath "D:\Secrets" -DataPath "E:\Data"
```

**Legacy script** (install dir only):

```powershell
.\scripts\install_daemon_windows.ps1 -InstallPath "D:\StonkAgents"
```

### Custom Service Name

Use a different service name:

```powershell
.\scripts\install_stonkagents.ps1 -ServiceName "MyStonkAgents"
# or
.\scripts\install_daemon_windows.ps1 -ServiceName "MyStonkAgents" -DisplayName "My StonkAgents Daemon"
```

### Service Account Configuration

By default, the service runs as `LocalSystem`. To run as a specific user:

```powershell
sc.exe config StonkAgentsDaemon obj= "DOMAIN\Username" password= "YourPassword"
```

**Security Note**: Using `LocalSystem` is recommended for most users unless you have specific security requirements.

### Auto-start After Reboot

If you want auto-start, run the daemon via your preferred startup mechanism.

## Security Best Practices

### 1. Keep Software Updated

Regularly update to the latest version:

```powershell
git pull origin main
.\scripts\uninstall_daemon_windows.ps1
.\scripts\install_daemon_windows.ps1
```

### 2. Restrict File Permissions

Ensure only administrators can modify the binary:

```powershell
icacls "C:\Program Files\StonkAgents" /inheritance:r /grant:r "Administrators:(OI)(CI)F"
```

### 3. Monitor Service Health

Set up scheduled health checks:

```powershell
# Create scheduled task for daily health check
$action = New-ScheduledTaskAction -Execute "PowerShell.exe" -Argument "-File C:\path\to\scripts\service_control_windows.ps1 health"
$trigger = New-ScheduledTaskTrigger -Daily -At 9am
Register-ScheduledTask -Action $action -Trigger $trigger -TaskName "StonkAgents Health Check" -Description "Daily health check for StonkAgents daemon"
```

### 4. Review Logs Regularly

Check for suspicious activity:

```powershell
Get-Content "$env:USERPROFILE\.stonkagents\logs\daemon.log" | Select-String -Pattern "ERROR|WARN"
```

## FAQ

### Q: Does the daemon work with Windows Defender?

**A**: Yes, the daemon is safe and should work without issues. If Windows Defender blocks it, add an exclusion:

```powershell
Add-MpPreference -ExclusionPath "C:\Program Files\StonkAgents"
```

### Q: Can I run multiple instances?

**A**: No, only one instance can run at a time due to port binding. Use different config files with different ports if needed.

### Q: How do I check the daemon version?

**A**: Run the binary manually:

```powershell
& "C:\Program Files\StonkAgents\stonkagents-daemon.exe" --version
```

### Q: Can I use this on Windows Server?

**A**: Yes, the installation process is identical for Windows Server 2019+.

### Q: How do I backup my configuration?

**A**: Copy the config directory:

```powershell
Copy-Item -Recurse "$env:USERPROFILE\.stonkagents" "$env:USERPROFILE\.stonkagents-backup-$(Get-Date -Format 'yyyy-MM-dd')"
```

## Getting Help

- **GitHub Issues**: [https://github.com/stonkagents/agent/issues](https://github.com/stonkagents/agent/issues)
- **Documentation**: [https://docs.stonkagents.com](https://docs.stonkagents.com)

## Next Steps

After installation, see:

- [Configuration Guide](../configuration.md) - Customize your daemon settings
- [API Documentation](../api/README.md) - Integrate with the REST API
- [P2P Networking](../networking.md) - Understand P2P protocols
- [Security Guide](../security.md) - Harden your installation
