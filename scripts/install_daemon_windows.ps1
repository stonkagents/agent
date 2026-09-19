# StonkAgents Daemon - Windows Service Installation Script
# Purpose: Install stonkagents as a Windows service with auto-start on boot
# Requirements: Administrator privileges, Go installed
# Usage: Run in PowerShell as Administrator: .\install_daemon_windows.ps1

#Requires -RunAsAdministrator

param(
    [string]$InstallPath = "C:\Program Files\StonkAgents",
    [string]$ServiceName = "StonkAgentsDaemon",
    [string]$DisplayName = "StonkAgents Daemon",
    [string]$Description = "StonkAgents: P2P knowledge sync for AI agents"
)

# ============================================================================
# Configuration
# ============================================================================

$ErrorActionPreference = "Stop"
$BinaryName = "stonkagents.exe"
$BinaryPath = Join-Path $InstallPath $BinaryName
$ConfigDir = Join-Path $env:USERPROFILE ".stonkagents"
$LogsDir = Join-Path $ConfigDir "logs"
$ConfigFile = Join-Path $ConfigDir "config.yaml"

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

function Test-GoInstalled {
    try {
        $goVersion = & go version 2>$null
        return $true
    }
    catch {
        return $false
    }
}

function Build-DaemonBinary {
    Write-Status "Building daemon binary..." "Info"

    # Get project root (script is in scripts/ directory)
    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $projectRoot = Split-Path -Parent $scriptDir

    Push-Location $projectRoot

    try {
        # Build for Windows x64
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        & go build -o $BinaryName -ldflags="-s -w" cmd/daemon/main.go

        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE"
        }

        if (-not (Test-Path $BinaryName)) {
            throw "Binary file not created: $BinaryName"
        }

        Write-Status "Binary built successfully: $BinaryName" "Success"
        return Join-Path $projectRoot $BinaryName
    }
    finally {
        Pop-Location
    }
}

function Install-DaemonBinary {
    param([string]$SourcePath)

    Write-Status "Installing binary to $InstallPath..." "Info"

    # Create installation directory
    if (-not (Test-Path $InstallPath)) {
        New-Item -ItemType Directory -Path $InstallPath -Force | Out-Null
        Write-Status "Created directory: $InstallPath" "Info"
    }

    # Copy binary
    Copy-Item -Path $SourcePath -Destination $BinaryPath -Force
    Write-Status "Binary installed: $BinaryPath" "Success"

    # Verify binary works
    try {
        $version = & $BinaryPath --version 2>$null
        Write-Status "Binary verification successful" "Success"
    }
    catch {
        Write-Status "Warning: Could not verify binary" "Warning"
    }
}

function Initialize-ConfigDirectory {
    Write-Status "Initializing configuration directory..." "Info"

    # Create config directory
    if (-not (Test-Path $ConfigDir)) {
        New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null
        Write-Status "Created directory: $ConfigDir" "Info"
    }

    # Create logs directory
    if (-not (Test-Path $LogsDir)) {
        New-Item -ItemType Directory -Path $LogsDir -Force | Out-Null
        Write-Status "Created directory: $LogsDir" "Info"
    }

    # Copy default config if doesn't exist
    if (-not (Test-Path $ConfigFile)) {
        $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
        $projectRoot = Split-Path -Parent $scriptDir
        $exampleConfig = Join-Path $projectRoot "config.yaml.example"

        if (Test-Path $exampleConfig) {
            Copy-Item -Path $exampleConfig -Destination $ConfigFile -Force
            Write-Status "Created default config: $ConfigFile" "Success"
        }
        else {
            Write-Status "Warning: config.yaml.example not found, skipping config creation" "Warning"
        }
    }
    else {
        Write-Status "Config file already exists: $ConfigFile" "Info"
    }
}

function Install-DaemonService {
    Write-Status "Installing Windows service..." "Info"

    # Check if service already exists
    $existingService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existingService) {
        Write-Status "Service already exists. Stopping and removing..." "Warning"

        if ($existingService.Status -eq 'Running') {
            Stop-Service -Name $ServiceName -Force
            Start-Sleep -Seconds 2
        }

        & sc.exe delete $ServiceName
        Start-Sleep -Seconds 2
    }

    # Create service using sc.exe
    $scArgs = @(
        "create", $ServiceName,
        "binPath=", "`"$BinaryPath`"",
        "start=", "auto",
        "DisplayName=", "`"$DisplayName`""
    )

    & sc.exe @scArgs

    if ($LASTEXITCODE -ne 0) {
        throw "Failed to create service with sc.exe (exit code: $LASTEXITCODE)"
    }

    Write-Status "Service created: $ServiceName" "Success"

    # Set service description
    & sc.exe description $ServiceName $Description

    # Configure service recovery actions (restart on failure after 60 seconds)
    & sc.exe failure $ServiceName reset= 86400 actions= restart/60000/restart/60000/restart/60000

    Write-Status "Service recovery configured: restart on failure after 60s" "Success"
}

function Start-DaemonService {
    Write-Status "Starting service..." "Info"

    & sc.exe start $ServiceName

    if ($LASTEXITCODE -eq 0) {
        Start-Sleep -Seconds 2
        $service = Get-Service -Name $ServiceName

        if ($service.Status -eq 'Running') {
            Write-Status "Service started successfully" "Success"
        }
        else {
            Write-Status "Service started but status is: $($service.Status)" "Warning"
        }
    }
    else {
        Write-Status "Warning: Service created but failed to start (exit code: $LASTEXITCODE)" "Warning"
        Write-Status "You can start it manually with: sc.exe start $ServiceName" "Info"
    }
}

# ============================================================================
# Main Installation Flow
# ============================================================================

function Main {
    Write-Host ""
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host " StonkAgents Daemon - Windows Installation" -ForegroundColor Cyan
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host ""

    try {
        # Phase 1: Pre-installation checks
        Write-Status "Checking prerequisites..." "Info"

        if (-not (Test-AdminPrivileges)) {
            throw "This script requires Administrator privileges. Please run PowerShell as Administrator."
        }
        Write-Status "Administrator privileges: OK" "Success"

        if (-not (Test-GoInstalled)) {
            throw "Go is not installed or not in PATH. Please install Go from https://go.dev/dl/"
        }
        Write-Status "Go installation: OK" "Success"

        Write-Host ""

        # Phase 2: Build binary
        $builtBinary = Build-DaemonBinary
        Write-Host ""

        # Phase 3: Install binary
        Install-DaemonBinary -SourcePath $builtBinary
        Write-Host ""

        # Phase 4: Initialize configuration
        Initialize-ConfigDirectory
        Write-Host ""

        # Phase 5: Install service
        Install-DaemonService
        Write-Host ""

        # Phase 6: Start service
        Start-DaemonService
        Write-Host ""

        # Success summary
        Write-Host "================================================" -ForegroundColor Green
        Write-Host " Installation Complete!" -ForegroundColor Green
        Write-Host "================================================" -ForegroundColor Green
        Write-Host ""
        Write-Host "Service Name:    $ServiceName" -ForegroundColor White
        Write-Host "Binary Location: $BinaryPath" -ForegroundColor White
        Write-Host "Config Location: $ConfigFile" -ForegroundColor White
        Write-Host "Logs Location:   $LogsDir" -ForegroundColor White
        Write-Host ""
        Write-Host "Useful Commands:" -ForegroundColor Cyan
        Write-Host "  Check status:  sc.exe query $ServiceName" -ForegroundColor White
        Write-Host "  Start service: sc.exe start $ServiceName" -ForegroundColor White
        Write-Host "  Stop service:  sc.exe stop $ServiceName" -ForegroundColor White
        Write-Host "  View logs:     Get-Content `"$LogsDir\daemon.log`" -Tail 50" -ForegroundColor White
        Write-Host ""
        Write-Host "The service is configured to start automatically on boot." -ForegroundColor Yellow
        Write-Host ""

        return 0
    }
    catch {
        Write-Host ""
        Write-Status "Installation failed: $_" "Error"
        Write-Host ""
        Write-Host "For troubleshooting, check:" -ForegroundColor Yellow
        Write-Host "  - Administrator privileges" -ForegroundColor White
        Write-Host "  - Go installation (go version)" -ForegroundColor White
        Write-Host "  - Windows Event Viewer for service errors" -ForegroundColor White
        Write-Host ""
        return 1
    }
}

# Run main installation
exit (Main)
