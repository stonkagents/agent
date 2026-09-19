# StonkAgents Daemon - Service Launcher
# Purpose: Run stonkagents-daemon.exe with STONKAGENTS_CONFIG_PATH and STONKAGENTS_SECRETS_PATH
#          set from daemon.env in the same directory. Used by the Windows service so the
#          daemon sees user-chosen config and secrets paths.
# Usage: Run by Windows service (binPath points to this script). Do not run manually unless testing.

$ErrorActionPreference = "Stop"
$InstallDir = $PSScriptRoot
$EnvFile = Join-Path $InstallDir "daemon.env"
$DaemonExe = Join-Path $InstallDir "stonkagents-daemon.exe"

if (-not (Test-Path $EnvFile)) {
    Write-Error "daemon.env not found: $EnvFile"
    exit 1
}

# Read daemon.env (KEY=VALUE, one per line; skip empty and #)
foreach ($line in (Get-Content -LiteralPath $EnvFile -ErrorAction Stop)) {
    $line = $line.Trim()
    if ($line -eq "" -or $line.StartsWith("#")) { continue }
    $i = $line.IndexOf("=")
    if ($i -le 0) { continue }
    $key = $line.Substring(0, $i).Trim()
    $val = $line.Substring($i + 1).Trim() -replace '^["'']|["'']$'
    if ($key -ne "" -and $val -ne "") {
        [Environment]::SetEnvironmentVariable($key, $val, "Process")
        Set-Item -Path "Env:$key" -Value $val -ErrorAction SilentlyContinue
    }
}

if (-not (Test-Path $DaemonExe)) {
    Write-Error "stonkagents-daemon.exe not found: $DaemonExe"
    exit 1
}

# Run from Install Dir so relative paths in config resolve correctly
Set-Location $InstallDir

# Run daemon and wait; when service stops, this process exits and the daemon is terminated
& $DaemonExe
exit $LASTEXITCODE
