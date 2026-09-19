# StonkAgents Daemon - Windows Service Uninstallation Script
# Purpose: Safely uninstall StonkAgents Windows service and optionally remove user data.
# Requirements: Administrator privileges
# Usage: Run in PowerShell as Administrator: .\uninstall_daemon_windows.ps1

#Requires -RunAsAdministrator

param(
    [string]$InstallPath = "C:\Program Files\StonkAgents",
    [string]$ServiceName = "StonkAgentsDaemon",
    [switch]$RemoveUserData,
    [switch]$Force
)

# ============================================================================
# Configuration
# ============================================================================

$ErrorActionPreference = "Stop"
$BinaryName = "stonkagents.exe"
$BinaryPath = Join-Path $InstallPath $BinaryName
$ConfigDir = Join-Path $env:USERPROFILE ".stonkagents"
# Inbound firewall rule added by setuphelper.exe / the controller (internal/setup FirewallRuleName).
$FirewallRuleName = "StonkAgents Agent"

# ============================================================================
# Helper Functions
# ============================================================================

function Write-Status {
    param([string]$Message, [string]$Type = "Info")
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $color = switch ($Type) {
        "Success" { "Green" }
        "Error" { "Red" }
        "Warning" { "Yellow" }
        default { "White" }
    }
    Write-Host "[$timestamp] [$Type] $Message" -ForegroundColor $color
}

function Test-AdminPrivileges {
    $currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Stop-DaemonService {
    Write-Status "Checking service status..." "Info"

    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

    if ($service) {
        if ($service.Status -eq 'Running') {
            Write-Status "Stopping service..." "Info"
            & sc.exe stop $ServiceName

            # Wait for service to stop (max 30 seconds)
            $timeout = 30
            $elapsed = 0
            while ($service.Status -ne 'Stopped' -and $elapsed -lt $timeout) {
                Start-Sleep -Seconds 1
                $service.Refresh()
                $elapsed++
            }

            if ($service.Status -eq 'Stopped') {
                Write-Status "Service stopped successfully" "Success"
            }
            else {
                Write-Status "Warning: Service did not stop within $timeout seconds" "Warning"
                if (-not $Force) {
                    throw "Service stop timeout. Use -Force to proceed anyway."
                }
            }
        }
        else {
            Write-Status "Service is not running (status: $($service.Status))" "Info"
        }
    }
    else {
        Write-Status "Service not found (may already be uninstalled)" "Info"
    }
}

function Remove-DaemonService {
    Write-Status "Removing service..." "Info"

    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

    if ($service) {
        & sc.exe delete $ServiceName

        if ($LASTEXITCODE -eq 0) {
            Write-Status "Service removed successfully" "Success"
            Start-Sleep -Seconds 2 # Wait for service deletion to complete
        }
        else {
            throw "Failed to delete service (exit code: $LASTEXITCODE)"
        }
    }
    else {
        Write-Status "Service not found, skipping removal" "Info"
    }
}

function Remove-FirewallRule {
    Write-Status "Removing firewall rule '$FirewallRuleName' (if present)..." "Info"

    # Presence is decided from the exit code (netsh output is localized).
    & netsh.exe advfirewall firewall show rule name="$FirewallRuleName" | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Status "Firewall rule not found, skipping" "Info"
        return
    }
    & netsh.exe advfirewall firewall delete rule name="$FirewallRuleName" | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Status "Firewall rule removed" "Success"
    }
    else {
        Write-Status "Could not remove firewall rule (exit code: $LASTEXITCODE); continuing" "Warning"
    }
}

function Remove-DaemonBinary {
    Write-Status "Removing binary files..." "Info"

    if (Test-Path $BinaryPath) {
        Remove-Item -Path $BinaryPath -Force
        Write-Status "Removed: $BinaryPath" "Success"
    }
    else {
        Write-Status "Binary not found: $BinaryPath" "Info"
    }

    # Remove installation directory if empty
    if ((Test-Path $InstallPath) -and ((Get-ChildItem $InstallPath).Count -eq 0)) {
        Remove-Item -Path $InstallPath -Force
        Write-Status "Removed empty directory: $InstallPath" "Success"
    }
}

function Remove-UserData {
    Write-Status "Removing user data..." "Info"

    if (Test-Path $ConfigDir) {
        $size = (Get-ChildItem $ConfigDir -Recurse | Measure-Object -Property Length -Sum).Sum
        $sizeMB = [math]::Round($size / 1MB, 2)

        Write-Host ""
        Write-Host "User data directory: $ConfigDir" -ForegroundColor Yellow
        Write-Host "Size: $sizeMB MB" -ForegroundColor Yellow
        Write-Host ""

        if (-not $RemoveUserData) {
            $response = Read-Host "Delete user data? This includes config, logs, and downloaded files (y/N)"

            if ($response -ne 'y' -and $response -ne 'Y') {
                Write-Status "User data preserved: $ConfigDir" "Info"
                return
            }
        }

        Remove-Item -Path $ConfigDir -Recurse -Force
        Write-Status "User data removed: $ConfigDir" "Success"
    }
    else {
        Write-Status "User data directory not found: $ConfigDir" "Info"
    }
}

function Confirm-Uninstall {
    if ($Force) {
        return $true
    }

    Write-Host ""
    Write-Host "This will uninstall the StonkAgents daemon service." -ForegroundColor Yellow
    Write-Host ""
    Write-Host "The following will be removed:" -ForegroundColor Yellow
    Write-Host "  - Windows service: $ServiceName" -ForegroundColor White
    Write-Host "  - Binary: $BinaryPath" -ForegroundColor White
    Write-Host "  - Firewall rule: $FirewallRuleName" -ForegroundColor White
    Write-Host ""
    Write-Host "User data will be preserved by default:" -ForegroundColor Yellow
    Write-Host "  - Config: $ConfigDir" -ForegroundColor White
    Write-Host ""

    $response = Read-Host "Continue with uninstallation? (y/N)"

    return ($response -eq 'y' -or $response -eq 'Y')
}

# ============================================================================
# Main Uninstallation Flow
# ============================================================================

function Main {
    Write-Host ""
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host " StonkAgents Daemon - Windows Uninstallation" -ForegroundColor Cyan
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host ""

    try {
        # Phase 1: Pre-uninstallation checks
        Write-Status "Checking prerequisites..." "Info"

        if (-not (Test-AdminPrivileges)) {
            throw "This script requires Administrator privileges. Please run PowerShell as Administrator."
        }
        Write-Status "Administrator privileges: OK" "Success"
        Write-Host ""

        # Phase 2: Confirmation
        if (-not (Confirm-Uninstall)) {
            Write-Status "Uninstallation cancelled by user" "Info"
            return 0
        }
        Write-Host ""

        # Phase 3: Stop service
        Stop-DaemonService
        Write-Host ""

        # Phase 4: Remove service
        Remove-DaemonService
        Write-Host ""

        # Phase 5: Remove binary
        Remove-DaemonBinary
        Write-Host ""

        # Phase 6: Remove the agent's inbound firewall rule (best-effort)
        Remove-FirewallRule
        Write-Host ""

        # Phase 8: Handle user data
        Remove-UserData
        Write-Host ""

        # Success summary
        Write-Host "================================================" -ForegroundColor Green
        Write-Host " Uninstallation Complete!" -ForegroundColor Green
        Write-Host "================================================" -ForegroundColor Green
        Write-Host ""
        Write-Host "The StonkAgents daemon has been uninstalled." -ForegroundColor White

        if (Test-Path $ConfigDir) {
            Write-Host ""
            Write-Host "User data preserved at: $ConfigDir" -ForegroundColor Yellow
            Write-Host "To remove manually: Remove-Item -Recurse -Force `"$ConfigDir`"" -ForegroundColor White
        }

        Write-Host ""
        return 0
    }
    catch {
        Write-Host ""
        Write-Status "Uninstallation failed: $_" "Error"
        Write-Host ""
        Write-Host "For troubleshooting:" -ForegroundColor Yellow
        Write-Host "  - Verify Administrator privileges" -ForegroundColor White
        Write-Host "  - Check if service is running: sc.exe query $ServiceName" -ForegroundColor White
        Write-Host "  - Try using -Force parameter to skip service stop timeout" -ForegroundColor White
        Write-Host ""
        return 1
    }
}

# Run main uninstallation
exit (Main)
