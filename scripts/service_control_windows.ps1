# StonkAgents Daemon - Windows Service Control Script
# Purpose: Helper functions for managing the StonkAgents Windows service
# Requirements: Administrator privileges for start/stop/restart operations
# Usage:
#   .\service_control_windows.ps1 status
#   .\service_control_windows.ps1 start
#   .\service_control_windows.ps1 stop
#   .\service_control_windows.ps1 restart
#   .\service_control_windows.ps1 logs [-Tail 50]

param(
    [Parameter(Mandatory=$true, Position=0)]
    [ValidateSet('status', 'start', 'stop', 'restart', 'logs', 'health')]
    [string]$Action,

    [string]$ServiceName = "StonkAgentsDaemon",
    [int]$Tail = 50
)

# ============================================================================
# Configuration
# ============================================================================

$ErrorActionPreference = "Stop"
$ConfigDir = Join-Path $env:USERPROFILE ".stonkagents"
$LogFile = Join-Path $ConfigDir "logs\daemon.log"
$ErrorLogFile = Join-Path $ConfigDir "logs\daemon-error.log"

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

function Get-ServiceStatus {
    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

    if ($service) {
        Write-Host ""
        Write-Host "================================================" -ForegroundColor Cyan
        Write-Host " Service Status: $ServiceName" -ForegroundColor Cyan
        Write-Host "================================================" -ForegroundColor Cyan
        Write-Host ""

        $statusColor = switch ($service.Status) {
            'Running' { "Green" }
            'Stopped' { "Yellow" }
            default { "Red" }
        }

        Write-Host "Status:        " -NoNewline
        Write-Host $service.Status -ForegroundColor $statusColor

        Write-Host "Display Name:  $($service.DisplayName)" -ForegroundColor White
        Write-Host "Startup Type:  $($service.StartType)" -ForegroundColor White

        # Get process ID if running
        if ($service.Status -eq 'Running') {
            try {
                $processId = (Get-WmiObject -Class Win32_Service -Filter "Name='$ServiceName'").ProcessId
                $process = Get-Process -Id $processId -ErrorAction SilentlyContinue

                if ($process) {
                    Write-Host "Process ID:    $processId" -ForegroundColor White
                    Write-Host "Memory Usage:  $([math]::Round($process.WorkingSet64 / 1MB, 2)) MB" -ForegroundColor White
                    Write-Host "CPU Time:      $($process.TotalProcessorTime.ToString('hh\:mm\:ss'))" -ForegroundColor White
                }
            }
            catch {
                Write-Status "Could not retrieve process information" "Warning"
            }
        }

        # Check log file
        if (Test-Path $LogFile) {
            $logSize = (Get-Item $LogFile).Length / 1KB
            Write-Host "Log Size:      $([math]::Round($logSize, 2)) KB" -ForegroundColor White
        }

        Write-Host ""
        return 0
    }
    else {
        Write-Host ""
        Write-Status "Service not found: $ServiceName" "Error"
        Write-Host "The service may not be installed. Run install_daemon_windows.ps1 first." -ForegroundColor Yellow
        Write-Host ""
        return 1
    }
}

function Start-DaemonService {
    Write-Status "Starting service..." "Info"

    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

    if (-not $service) {
        throw "Service not found: $ServiceName. Install it first with install_daemon_windows.ps1"
    }

    if ($service.Status -eq 'Running') {
        Write-Status "Service is already running" "Info"
        return 0
    }

    & sc.exe start $ServiceName

    if ($LASTEXITCODE -eq 0) {
        # Wait for service to start (max 10 seconds)
        $timeout = 10
        $elapsed = 0
        while ($service.Status -ne 'Running' -and $elapsed -lt $timeout) {
            Start-Sleep -Seconds 1
            $service.Refresh()
            $elapsed++
        }

        if ($service.Status -eq 'Running') {
            Write-Status "Service started successfully" "Success"
            return 0
        }
        else {
            throw "Service did not start within $timeout seconds (status: $($service.Status))"
        }
    }
    else {
        throw "Failed to start service (exit code: $LASTEXITCODE)"
    }
}

function Stop-DaemonService {
    Write-Status "Stopping service..." "Info"

    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

    if (-not $service) {
        throw "Service not found: $ServiceName"
    }

    if ($service.Status -eq 'Stopped') {
        Write-Status "Service is already stopped" "Info"
        return 0
    }

    & sc.exe stop $ServiceName

    if ($LASTEXITCODE -eq 0) {
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
            return 0
        }
        else {
            throw "Service did not stop within $timeout seconds (status: $($service.Status))"
        }
    }
    else {
        throw "Failed to stop service (exit code: $LASTEXITCODE)"
    }
}

function Restart-DaemonService {
    Write-Status "Restarting service..." "Info"

    # Stop service
    Stop-DaemonService | Out-Null
    Start-Sleep -Seconds 2

    # Start service
    Start-DaemonService | Out-Null

    Write-Status "Service restarted successfully" "Success"
    return 0
}

function Show-ServiceLogs {
    if (Test-Path $LogFile) {
        Write-Host ""
        Write-Host "================================================" -ForegroundColor Cyan
        Write-Host " Service Logs (Last $Tail lines)" -ForegroundColor Cyan
        Write-Host "================================================" -ForegroundColor Cyan
        Write-Host ""

        Get-Content -Path $LogFile -Tail $Tail | ForEach-Object {
            # Colorize log levels
            if ($_ -match '\[ERROR\]') {
                Write-Host $_ -ForegroundColor Red
            }
            elseif ($_ -match '\[WARN\]') {
                Write-Host $_ -ForegroundColor Yellow
            }
            elseif ($_ -match '\[INFO\]') {
                Write-Host $_ -ForegroundColor White
            }
            elseif ($_ -match '\[DEBUG\]') {
                Write-Host $_ -ForegroundColor Gray
            }
            else {
                Write-Host $_
            }
        }

        Write-Host ""
        Write-Host "Full log file: $LogFile" -ForegroundColor Gray
        Write-Host ""
    }
    else {
        Write-Status "Log file not found: $LogFile" "Warning"
        Write-Status "The service may not have started yet or logging is not configured" "Info"
    }

    # Show error log if exists
    if (Test-Path $ErrorLogFile) {
        $errorLines = Get-Content -Path $ErrorLogFile -Tail 10
        if ($errorLines) {
            Write-Host "Recent errors (last 10 lines):" -ForegroundColor Red
            $errorLines | ForEach-Object { Write-Host $_ -ForegroundColor Red }
            Write-Host ""
        }
    }

    return 0
}

function Test-ServiceHealth {
    Write-Host ""
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host " Service Health Check" -ForegroundColor Cyan
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host ""

    $healthy = $true

    # Check 1: Service exists
    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($service) {
        Write-Status "Service installed: YES" "Success"
    }
    else {
        Write-Status "Service installed: NO" "Error"
        $healthy = $false
    }

    # Check 2: Service running
    if ($service -and $service.Status -eq 'Running') {
        Write-Status "Service running: YES" "Success"
    }
    else {
        Write-Status "Service running: NO" "Error"
        $healthy = $false
    }

    # Check 3: Config directory exists
    if (Test-Path $ConfigDir) {
        Write-Status "Config directory exists: YES" "Success"
    }
    else {
        Write-Status "Config directory exists: NO" "Error"
        $healthy = $false
    }

    # Check 4: Log file exists
    if (Test-Path $LogFile) {
        Write-Status "Log file exists: YES" "Success"

        # Check log file age
        $logAge = (Get-Date) - (Get-Item $LogFile).LastWriteTime
        if ($logAge.TotalMinutes -lt 5) {
            Write-Status "Recent log activity: YES (last write: $([math]::Round($logAge.TotalMinutes, 1)) min ago)" "Success"
        }
        else {
            Write-Status "Recent log activity: NO (last write: $([math]::Round($logAge.TotalHours, 1)) hours ago)" "Warning"
        }
    }
    else {
        Write-Status "Log file exists: NO" "Warning"
    }

    # Check 5: Process responding
    if ($service -and $service.Status -eq 'Running') {
        try {
            $processId = (Get-WmiObject -Class Win32_Service -Filter "Name='$ServiceName'").ProcessId
            $process = Get-Process -Id $processId -ErrorAction Stop

            if ($process.Responding) {
                Write-Status "Process responding: YES" "Success"
            }
            else {
                Write-Status "Process responding: NO" "Error"
                $healthy = $false
            }
        }
        catch {
            Write-Status "Process responding: UNKNOWN" "Warning"
        }
    }

    Write-Host ""

    if ($healthy) {
        Write-Host "Overall Health: " -NoNewline
        Write-Host "HEALTHY" -ForegroundColor Green
    }
    else {
        Write-Host "Overall Health: " -NoNewline
        Write-Host "UNHEALTHY" -ForegroundColor Red
    }

    Write-Host ""
    return $(if ($healthy) { 0 } else { 1 })
}

# ============================================================================
# Main Control Flow
# ============================================================================

function Main {
    try {
        # Admin check for privileged operations
        if ($Action -in @('start', 'stop', 'restart') -and -not (Test-AdminPrivileges)) {
            throw "The '$Action' operation requires Administrator privileges. Please run PowerShell as Administrator."
        }

        switch ($Action) {
            'status' {
                return Get-ServiceStatus
            }
            'start' {
                return Start-DaemonService
            }
            'stop' {
                return Stop-DaemonService
            }
            'restart' {
                return Restart-DaemonService
            }
            'logs' {
                return Show-ServiceLogs
            }
            'health' {
                return Test-ServiceHealth
            }
            default {
                throw "Unknown action: $Action"
            }
        }
    }
    catch {
        Write-Host ""
        Write-Status $_.Exception.Message "Error"
        Write-Host ""
        return 1
    }
}

# Run main control function
exit (Main)
