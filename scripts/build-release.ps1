# StonkAgents - Cross-Platform Release Build Script
# Purpose: Build daemon and tracker binaries for Windows and macOS
# Usage: .\scripts\build-release.ps1 [-IncludeTracker] [-Version "1.0.0"]

param(
    [switch]$IncludeTracker,
    [string]$Version = "0.1.0",
    [string]$OutputDir = "dist"
)

$ErrorActionPreference = "Stop"

# ============================================================================
# Configuration
# ============================================================================

$Platforms = @(
    @{ GOOS = "windows"; GOARCH = "amd64"; Ext = ".exe"; Name = "windows-amd64" },
    @{ GOOS = "darwin";  GOARCH = "amd64"; Ext = "";     Name = "darwin-amd64" },
    @{ GOOS = "darwin";  GOARCH = "arm64"; Ext = "";     Name = "darwin-arm64" }
)

$DaemonSource = ".\cmd\daemon\main.go"
$TrackerSource = ".\tracker\cmd\tracker\main.go"

# ============================================================================
# Helper Functions
# ============================================================================

function Write-Status {
    param([string]$Message, [string]$Type = "Info")
    $timestamp = Get-Date -Format "HH:mm:ss"
    $color = switch ($Type) {
        "Success" { "Green" }
        "Error"   { "Red" }
        "Warning" { "Yellow" }
        "Build"   { "Cyan" }
        default   { "White" }
    }
    Write-Host "[$timestamp] " -NoNewline -ForegroundColor DarkGray
    Write-Host $Message -ForegroundColor $color
}

function Build-Binary {
    param(
        [string]$Source,
        [string]$Output,
        [string]$GOOS,
        [string]$GOARCH,
        [string]$Version
    )
    
    $env:GOOS = $GOOS
    $env:GOARCH = $GOARCH
    $env:CGO_ENABLED = "0"
    
    $ldflags = "-s -w -X main.Version=$Version"
    
    Write-Status "Building $Output (GOOS=$GOOS, GOARCH=$GOARCH)..." "Build"
    
    & go build -ldflags $ldflags -o $Output $Source
    
    if ($LASTEXITCODE -ne 0) {
        throw "Build failed for $Output"
    }
    
    $size = (Get-Item $Output).Length / 1MB
    $sizeStr = "{0:N1}" -f $size
    Write-Status "Created: $Output ($sizeStr MB)" "Success"
}

# ============================================================================
# Main Build Process
# ============================================================================

Write-Host ""
Write-Host "================================================" -ForegroundColor Cyan
Write-Host " StonkAgents - Release Build" -ForegroundColor Cyan
Write-Host " Version: $Version" -ForegroundColor Cyan
Write-Host "================================================" -ForegroundColor Cyan
Write-Host ""

# Verify we're in the right directory
if (-not (Test-Path "go.mod")) {
    Write-Status "Error: Run this script from the project root (where go.mod is located)" "Error"
    exit 1
}

# Create output directory
if (Test-Path $OutputDir) {
    Write-Status "Cleaning existing $OutputDir directory..." "Warning"
    Remove-Item -Recurse -Force $OutputDir
}

New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
Write-Status "Created output directory: $OutputDir" "Info"

# Build for each platform
$buildCount = 0
$includeTrackerInt = if ($IncludeTracker) { 1 } else { 0 }
$totalBuilds = $Platforms.Count * (1 + $includeTrackerInt)

foreach ($platform in $Platforms) {
    $platformDir = Join-Path $OutputDir $platform.Name
    New-Item -ItemType Directory -Path $platformDir -Force | Out-Null
    
    # Build daemon
    $daemonOutput = Join-Path $platformDir ("sync-daemon" + $platform.Ext)
    Build-Binary -Source $DaemonSource -Output $daemonOutput `
                 -GOOS $platform.GOOS -GOARCH $platform.GOARCH -Version $Version
    $buildCount++
    
    # Build tracker (optional)
    if ($IncludeTracker) {
        $trackerOutput = Join-Path $platformDir ("cs-tracker" + $platform.Ext)
        Build-Binary -Source $TrackerSource -Output $trackerOutput `
                     -GOOS $platform.GOOS -GOARCH $platform.GOARCH -Version $Version
        $buildCount++
    }
}

# Reset environment
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

# Summary
Write-Host ""
Write-Host "================================================" -ForegroundColor Green
Write-Host " Build Complete!" -ForegroundColor Green
Write-Host "================================================" -ForegroundColor Green
Write-Host ""
Write-Host "Built $buildCount binaries:" -ForegroundColor White

foreach ($platform in $Platforms) {
    $platformDir = Join-Path $OutputDir $platform.Name
    Write-Host "  $($platform.Name):" -ForegroundColor Cyan
    Get-ChildItem $platformDir | ForEach-Object {
        $size = $_.Length / 1MB
        $sizeStr = "{0:N1}" -f $size
        Write-Host "    - $($_.Name) ($sizeStr MB)" -ForegroundColor White
    }
}

Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  Windows MSI: .\scripts\build-msi.ps1 -Version $Version -OutDir $OutputDir" -ForegroundColor White
Write-Host "  macOS DMG:   On macOS run ./scripts/macos/sign-release.sh $Version" -ForegroundColor White
Write-Host "  Testers:      .\scripts\gen-tester-package.ps1 -TrackerURL <ngrok-url>" -ForegroundColor White
Write-Host ""
