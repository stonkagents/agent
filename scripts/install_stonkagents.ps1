# StonkAgents - Single-script Windows install
# Self-elevates (UAC), checks prerequisites, installs binary + config, installs/starts Windows service.
# User can choose install, secrets, and data (download/upload) locations.
# Usage: .\install_stonkagents.ps1 [-InstallPath ...] [-SecretsPath ...] [-DataPath ...] [-BuildFromSource] [-DoNotStartService]

param(
    [string]$InstallPath = "C:\Program Files\StonkAgents",
    [string]$SecretsPath = "$env:USERPROFILE.stonkagents",
    [string]$DataPath = "$env:USERPROFILE.stonkagents\data",
    [switch]$SkipPrereqCheck,
    [switch]$BuildFromSource,
    [string]$ServiceName = "StonkAgentsDaemon",
    [switch]$DoNotStartService
)

$ErrorActionPreference = "Stop"
$BinaryName = "stonkagents.exe"

# ============================================================================
# Self-elevate: if not admin, re-launch with UAC and exit
# ============================================================================

function Test-AdminPrivileges {
    $currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Invoke-SelfElevate {
    if (Test-AdminPrivileges) { return $false }
    $scriptPath = $MyInvocation.PSCommandPath
    $argsList = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $scriptPath)
    if ($InstallPath -ne "C:\Program Files\StonkAgents") { $argsList += "-InstallPath", $InstallPath }
    if ($SecretsPath -ne "$env:USERPROFILE.stonkagents") { $argsList += "-SecretsPath", $SecretsPath }
    if ($DataPath -ne "$env:USERPROFILE.stonkagents\data") { $argsList += "-DataPath", $DataPath }
    if ($SkipPrereqCheck) { $argsList += "-SkipPrereqCheck" }
    if ($BuildFromSource) { $argsList += "-BuildFromSource" }
    if ($ServiceName -ne "StonkAgentsDaemon") { $argsList += "-ServiceName", $ServiceName }
    if ($DoNotStartService) { $argsList += "-DoNotStartService" }
    Start-Process powershell.exe -ArgumentList $argsList -Verb RunAs
    exit 0
}

Invoke-SelfElevate | Out-Null

# ============================================================================
# Step UI: checkmarks and in-progress spinner
# ============================================================================

$script:StepNumber = 0
$script:StepTotal = 0

function Write-StepList {
    param([string[]]$StepNames)
    $script:StepTotal = $StepNames.Count
    Write-Host "  Steps:" -ForegroundColor DarkGray
    for ($i = 0; $i -lt $StepNames.Count; $i++) {
        Write-Host "    [  ] $($StepNames[$i])" -ForegroundColor DarkGray
    }
    Write-Host ""
}

function Start-Step {
    param([string]$Name)
    $script:StepNumber++
    $n = $script:StepNumber
    $total = $script:StepTotal
    $label = if ($total -gt 0) { "[$n/$total] $Name" } else { $Name }
    Write-Host "[  ] $label... " -NoNewline
    return $label
}

function Complete-Step {
    param([string]$Label, [bool]$Success = $true)
    $pad = " " * [Math]::Max(0, 60 - $Label.Length)
    if ($Success) {
        Write-Host "`r[OK] $Label$pad" -ForegroundColor Green
    } else {
        Write-Host "`r[!!] $Label$pad" -ForegroundColor Red
    }
}

function Invoke-Step {
    param([string]$Name, [scriptblock]$ScriptBlock)
    $label = Start-Step -Name $Name
    try {
        $result = & $ScriptBlock
        Complete-Step -Label $label -Success $true
        return $result
    } catch {
        Complete-Step -Label $label -Success $false
        throw
    }
}

function Invoke-StepWithSpinner {
    param([string]$Name, [scriptblock]$ScriptBlock, [array]$ArgumentList = @())
    $label = Start-Step -Name $Name
    $job = if ($ArgumentList.Count -gt 0) {
        Start-Job -ScriptBlock $ScriptBlock -ArgumentList $ArgumentList
    } else {
        Start-Job -ScriptBlock $ScriptBlock
    }
    $chars = @('|', '/', '-', '\')
    $i = 0
    while ($job.State -eq 'Running') {
        $c = $chars[$i % 4]
        Write-Host "`r[ $c ] $label... " -NoNewline -ForegroundColor Cyan
        Start-Sleep -Milliseconds 150
        $i++
    }
    $result = Receive-Job $job
    $failed = ($job.State -eq 'Failed') -or ($null -eq $result)
    Remove-Job $job -Force
    if ($failed) {
        Complete-Step -Label $label -Success $false
        return $null
    }
    Complete-Step -Label $label -Success $true
    return $result
}

# ============================================================================
# Helpers
# ============================================================================

function Write-Status {
    param([string]$Message, [string]$Type = "Info")
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $color = switch ($Type) { "Success" { "Green" } "Error" { "Red" } "Warning" { "Yellow" } default { "White" } }
    Write-Host "[$timestamp] [$Type] $Message" -ForegroundColor $color
}

function Get-ProjectRoot {
    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $parent = Split-Path -Parent $scriptDir
    if (Test-Path (Join-Path $parent "go.mod")) { return $parent }
    return $null
}

# ============================================================================
# Key generation (genkeys.exe or Go + repo)
# ============================================================================

function New-StonkAgentsKeypair {
    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $projectRoot = Get-ProjectRoot
    # Prefer genkeys.exe next to script or in .\bin\ (release zip)
    $genkeysExe = $null
    if (Test-Path (Join-Path $scriptDir "genkeys.exe")) { $genkeysExe = Join-Path $scriptDir "genkeys.exe" }
    elseif (Test-Path (Join-Path $scriptDir "bin\genkeys.exe")) { $genkeysExe = Join-Path $scriptDir "bin\genkeys.exe" }
    if ($genkeysExe) {
        try {
            $out = & $genkeysExe 2>$null
            if ($LASTEXITCODE -ne 0) { return $null }
            $lines = ($out -split "`n") | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }
            if ($lines.Count -lt 2) { return $null }
            return @{ PublicKey = $lines[0]; PrivateKey = $lines[1] }
        } catch { return $null }
    }
    # Fallback: Go + repo (go run keygen)
    if (-not $projectRoot) { return $null }
    $code = @"
package main
import (
  "encoding/base64"
  "fmt"
  crypto "github.com/stonkagents/agent/pkg/cryptography"
)
func main() {
  pub, priv, err := crypto.GenerateKeypair()
  if err != nil { panic(err) }
  fmt.Printf("%s`n%s`n",
    base64.StdEncoding.EncodeToString(pub),
    base64.StdEncoding.EncodeToString(priv),
  )
}
"@
    $tmp = Join-Path $env:TEMP "cs-gen-keys.go"
    Set-Content -Path $tmp -Value $code -Encoding ASCII
    Push-Location $projectRoot
    try {
        $out = & go run $tmp 2>$null
    } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { return $null }
    $lines = ($out -split "`n") | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }
    if ($lines.Count -lt 2) { return $null }
    return @{ PublicKey = $lines[0]; PrivateKey = $lines[1] }
}

# ============================================================================
# Main install flow
# ============================================================================

function Main {
    Write-Host ""
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host " StonkAgents - Windows Install" -ForegroundColor Cyan
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host ""

    # Resolve paths to absolute
    $InstallPath = [System.IO.Path]::GetFullPath($InstallPath)
    $SecretsPath = [System.IO.Path]::GetFullPath($SecretsPath)
    $DataPath = [System.IO.Path]::GetFullPath($DataPath)
    $SecretsDir = [System.IO.Path]::GetDirectoryName($SecretsPath)
    if (-not $SecretsDir) { $SecretsDir = $SecretsPath }
    $SecretsFile = if ([System.IO.Path]::GetFileName($SecretsPath)) { $SecretsPath } else { Join-Path $SecretsPath "secrets.env" }
    if (-not [System.IO.Path]::GetFileName($SecretsFile)) { $SecretsFile = Join-Path $SecretsFile "secrets.env" }
    $SecretsDir = [System.IO.Path]::GetDirectoryName($SecretsFile)


    # Show all steps upfront so user knows what will run
    $stepNames = @(
        "Checking Windows version",
        "Checking for Go (when building from source)",
        "Finding or building stonkagents.exe",
        "Creating install directory",
        "Copying binary",
        "Installing launcher script",
        "Writing daemon.env",
        "Creating secrets directory",
        "Generating keys / writing secrets",
        "Writing config.yaml",
        "Creating data directory",
        "Creating Windows service",
        "Starting service"
    )
    Write-StepList -StepNames $stepNames

    # 1. Check Windows version
    Invoke-Step -Name "Checking Windows version" -ScriptBlock {
        if ($SkipPrereqCheck) { return $true }
        $os = [System.Environment]::OSVersion.Version
        if ($os.Major -lt 10) { throw "Windows 10 or later is required." }
        return $true
    } | Out-Null

    # 2. Check for Go (when building from source)
    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $projectRoot = Get-ProjectRoot
    $binarySource = $null
    $needBuild = $false
    $nextTo = Join-Path $scriptDir $BinaryName
    $binPath = Join-Path $scriptDir "bin" $BinaryName
    if (Test-Path $nextTo) { $binarySource = $nextTo }
    elseif (Test-Path $binPath) { $binarySource = $binPath }
    elseif ($BuildFromSource -and $projectRoot) {
        Invoke-Step -Name "Checking for Go (when building from source)" -ScriptBlock {
            try { $v = & go version 2>&1; if ($LASTEXITCODE -ne 0) { throw "Go not found" }; return $true } catch { throw "Go is required for -BuildFromSource. Install from https://go.dev/dl/ or place stonkagents.exe next to this script." }
        } | Out-Null
        $needBuild = $true
    } else {
        $label = Start-Step -Name "Checking for Go (when building from source)"
        Complete-Step -Label $label -Success $true
    }

    # 3. Find or build binary (use spinner when building from source)
    if (-not $binarySource -and $needBuild) {
        $binarySource = Invoke-StepWithSpinner -Name "Finding or building stonkagents.exe" -ArgumentList @($projectRoot) -ScriptBlock {
            param($root)
            Push-Location $root
            try {
                $env:GOOS = "windows"; $env:GOARCH = "amd64"
                & go build -o "stonkagents.exe" -ldflags="-s -w" cmd/daemon/main.go
                if ($LASTEXITCODE -eq 0 -and (Test-Path "stonkagents.exe")) { return (Join-Path $root "stonkagents.exe") }
            } finally { Pop-Location }
            return $null
        }
        if (-not $binarySource) { $binarySource = $null }
    } elseif (-not $binarySource) {
        $label = Start-Step -Name "Finding or building stonkagents.exe"
        Complete-Step -Label $label -Success $false
        throw "stonkagents.exe not found. Place it next to this script, in .\bin\, or use -BuildFromSource from the repo with Go installed."
    } else {
        $label = Start-Step -Name "Finding or building stonkagents.exe"
        Complete-Step -Label $label -Success $true
    }

    # 4. Create install directory
    Invoke-Step -Name "Creating install directory" -ScriptBlock {
        if (-not (Test-Path $InstallPath)) {
            New-Item -ItemType Directory -Path $InstallPath -Force | Out-Null
        }
        return $true
    } | Out-Null

    # 5. Copy binary
    $binaryDest = Join-Path $InstallPath $BinaryName
    Invoke-Step -Name "Copying binary" -ScriptBlock {
        Copy-Item -Path $binarySource -Destination $binaryDest -Force
        return $true
    } | Out-Null

    # 6. Install launcher script
    $launcherSource = Join-Path $scriptDir "daemon" "start-daemon.ps1"
    Invoke-Step -Name "Installing launcher script" -ScriptBlock {
        if (Test-Path $launcherSource) {
            Copy-Item -Path $launcherSource -Destination (Join-Path $InstallPath "start-daemon.ps1") -Force
        }
        return (Test-Path (Join-Path $InstallPath "start-daemon.ps1"))
    } | Out-Null

    # 7. Write daemon.env
    $configPath = Join-Path $InstallPath "config.yaml"
    $daemonEnvPath = Join-Path $InstallPath "daemon.env"
    Invoke-Step -Name "Writing daemon.env" -ScriptBlock {
        $content = "STONKAGENTS_CONFIG_PATH=$configPath`nSTONKAGENTS_SECRETS_PATH=$SecretsFile`n"
        Set-Content -Path $daemonEnvPath -Value $content -Encoding ASCII -NoNewline
        return $true
    } | Out-Null

    # 8. Create secrets directory
    Invoke-Step -Name "Creating secrets directory" -ScriptBlock {
        if (-not (Test-Path $SecretsDir)) {
            New-Item -ItemType Directory -Path $SecretsDir -Force | Out-Null
        }
        return $true
    } | Out-Null

    # 9. Generate keys / write secrets
    $generatedPublicKey = $null
    Invoke-Step -Name "Generating keys / writing secrets" -ScriptBlock {
        if (Test-Path $SecretsFile) { return $true }
        $keys = New-StonkAgentsKeypair
        if ($keys) {
            Set-Content -Path $SecretsFile -Value "STONKAGENTS_PRIVATE_KEY=$($keys.PrivateKey)" -Encoding ASCII
            $script:generatedPublicKey = $keys.PublicKey
        } else {
            Set-Content -Path $SecretsFile -Value "# Add line: STONKAGENTS_PRIVATE_KEY=<base64-private-key>" -Encoding ASCII
        }
        return $true
    } | Out-Null
    $generatedPublicKey = $script:generatedPublicKey

    # 10. Write config.yaml
    $publicKey = if ($generatedPublicKey) { $generatedPublicKey } else { "" }
    Invoke-Step -Name "Writing config.yaml" -ScriptBlock {
        $configYaml = @"
peer_id: ""
public_key: "$publicKey"
daemon_host: "127.0.0.1"
daemon_port: 7841
bootstrap_peers: []
tracker_url: "http://localhost:7842"
data_dir: "$DataPath"
"@
        Set-Content -Path $configPath -Value $configYaml -Encoding ASCII
        return $true
    } | Out-Null

    # 11. Create data directory
    Invoke-Step -Name "Creating data directory" -ScriptBlock {
        if (-not (Test-Path $DataPath)) {
            New-Item -ItemType Directory -Path $DataPath -Force | Out-Null
        }
        return $true
    } | Out-Null

    # 12. Create Windows service
    $launcherPath = Join-Path $InstallPath "start-daemon.ps1"
    if (-not (Test-Path $launcherPath)) {
        throw "start-daemon.ps1 missing; cannot create service. Install launcher first."
    }
    $binPathVal = "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$launcherPath`""
    $DisplayName = "StonkAgents Daemon"
    $Description = "StonkAgents: P2P knowledge sync for AI agents"
    Invoke-Step -Name "Creating Windows service" -ScriptBlock {
        foreach ($svcName in @($ServiceName)) {
            $existingService = Get-Service -Name $svcName -ErrorAction SilentlyContinue
            if ($existingService) {
                if ($existingService.Status -eq 'Running') {
                    Stop-Service -Name $svcName -Force -ErrorAction SilentlyContinue
                    Start-Sleep -Seconds 2
                }
                & sc.exe delete $svcName
                Start-Sleep -Seconds 2
            }
        }
        & sc.exe create $ServiceName binPath= "`"$binPathVal`"" start= auto DisplayName= "`"$DisplayName`""
        if ($LASTEXITCODE -ne 0) { throw "sc create failed with exit code $LASTEXITCODE" }
        & sc.exe description $ServiceName $Description
        & sc.exe failure $ServiceName reset= 86400 actions= restart/60000/restart/60000/restart/60000
        return $true
    } | Out-Null

    # 13. Start service (optional)
    if (-not $DoNotStartService) {
        Invoke-Step -Name "Starting service" -ScriptBlock {
            & sc.exe start $ServiceName
            if ($LASTEXITCODE -eq 0) { Start-Sleep -Seconds 2 }
            return $true
        } | Out-Null
    } else {
        $label = Start-Step -Name "Starting service"
        Write-Host "`r[--] Starting service (skipped -DoNotStartService)     " -ForegroundColor DarkGray
    }

    Write-Host ""
    Write-Host "================================================" -ForegroundColor Green
    Write-Host " Installation Complete" -ForegroundColor Green
    Write-Host "================================================" -ForegroundColor Green
    Write-Host ""
    Write-Host "Install Dir:  $InstallPath" -ForegroundColor White
    Write-Host "Secrets:     $SecretsFile" -ForegroundColor White
    Write-Host "Data Dir:    $DataPath" -ForegroundColor White
    Write-Host ""
    Write-Host "Service: $ServiceName" -ForegroundColor Cyan
    Write-Host "  sc.exe query $ServiceName" -ForegroundColor White
    Write-Host "  sc.exe start $ServiceName" -ForegroundColor White
    Write-Host "  sc.exe stop $ServiceName" -ForegroundColor White
    Write-Host ""
    $controlScript = Join-Path $scriptDir "service_control_windows.ps1"
    if (Test-Path $controlScript) {
        Write-Host "Or use: $controlScript" -ForegroundColor Cyan
    }
    Write-Host ""
    return 0
}

try {
    exit (Main)
} catch {
    Write-Status "Installation failed: $_" "Error"
    exit 1
}
