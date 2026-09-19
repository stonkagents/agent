param(
    [int]$Seeders = 2,
    [int]$Leechers = 1,
    [string]$BaseDir = "$(Join-Path $PSScriptRoot '..\\tmp\\multi-client')",
    [switch]$SkipHealthChecks
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$daemonExe = Join-Path $repoRoot "bin\\sync-daemon.exe"
$trackerExe = Join-Path $repoRoot "bin\\cs-tracker.exe"

if (-not (Test-Path $daemonExe)) {
    throw "Missing $daemonExe. Build with: go build -o bin\\sync-daemon.exe .\\cmd\\daemon\\main.go"
}
if (-not (Test-Path $trackerExe)) {
    throw "Missing $trackerExe. Build with: go build -o bin\\cs-tracker.exe .\\tracker\\cmd\\tracker\\main.go"
}

if (-not (Test-Path $BaseDir)) {
    New-Item -ItemType Directory -Path $BaseDir | Out-Null
}

function New-Keypair {
    $code = @'
package main
import (
  "encoding/base64"
  "fmt"
  crypto "github.com/stonkagents/agent/pkg/cryptography"
)
func main() {
  pub, priv, err := crypto.GenerateKeypair()
  if err != nil { panic(err) }
  fmt.Printf("%s\n%s\n", base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(priv))
}
'@
    $tmp = Join-Path $env:TEMP "cs-gen-keypair.go"
    Set-Content -Path $tmp -Value $code -Encoding ASCII
    Push-Location $repoRoot
    $out = go run $tmp
    Pop-Location
    if (-not $out) {
        throw "Failed to generate keypair"
    }
    $lines = $out -split "`n"
    return @{
        PublicKey = $lines[0].Trim()
        PrivateKey = $lines[1].Trim()
    }
}

function Write-Config {
    param(
        [string]$Path,
        [int]$Port,
        [string]$PublicKey,
        [string]$DataDir
    )

    $dataDir = $DataDir -replace '\\', '/'
    $yaml = @"
peer_id: ""
public_key: "$PublicKey"
daemon_host: "127.0.0.1"
daemon_port: $Port
bootstrap_peers: []
tracker_url: "http://localhost:7842"
data_dir: "$dataDir"
"@
    $dir = Split-Path -Parent $Path
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir | Out-Null
    }
    Set-Content -Path $Path -Value $yaml -Encoding ASCII
}

function Start-Daemon {
    param(
        [string]$Name,
        [int]$Port
    )

    $instanceDir = Join-Path $BaseDir $Name
    $dataDir = Join-Path $instanceDir "data"
    $configPath = Join-Path $instanceDir "config.yaml"
    $keys = New-Keypair

    Write-Config -Path $configPath -Port $Port -PublicKey $keys.PublicKey -DataDir $dataDir

    $runnerPath = Join-Path $instanceDir "run-daemon.ps1"
    $lines = @(
        "`$env:STONKAGENTS_CONFIG_PATH=`"$configPath`"",
        "`$env:STONKAGENTS_PRIVATE_KEY=`"$($keys.PrivateKey)`"",
        "& `"$daemonExe`""
    )
    Set-Content -Path $runnerPath -Value $lines -Encoding ASCII

    $stdoutPath = Join-Path $instanceDir "daemon.out.log"
    $stderrPath = Join-Path $instanceDir "daemon.err.log"
    $daemonLogPath = Join-Path $dataDir "logs\\daemon.log"
    Write-Host "Logs: $daemonLogPath"

    return Start-Process -FilePath "powershell" -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $runnerPath -WorkingDirectory $repoRoot -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru
}

function Wait-ForHealth {
    param(
        [string]$Url,
        [int]$TimeoutSeconds = 90
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = & curl.exe -s -o $null -w "%{http_code}" "$Url/health"
            if ($resp -eq "200") { return $true }
        } catch {}
        Start-Sleep -Seconds 1
    }
    return $false
}

Write-Host "Starting tracker..."
$trackerProc = Start-Process -FilePath $trackerExe -WorkingDirectory $repoRoot -PassThru
Start-Sleep -Seconds 2

$seederProcs = @()
for ($i = 0; $i -lt $Seeders; $i++) {
    $port = 7841 + $i
    if ($port -eq 7842) { $port = 7843 } # avoid tracker port
    Write-Host "Starting seeder $i on port $port..."
    $seederProcs += Start-Daemon -Name "seeder-$i" -Port $port
    Write-Host "Waiting for seeder $i health..."
    if (-not $SkipHealthChecks) {
        if (-not (Wait-ForHealth -Url "http://localhost:$port")) {
            Write-Warning "Seeder $i failed health check on port $port; continuing"
        }
    }
}

$leecherProcs = @()
for ($i = 0; $i -lt $Leechers; $i++) {
    $port = 7900 + $i
    Write-Host "Starting leecher $i on port $port..."
    $leecherProcs += Start-Daemon -Name "leecher-$i" -Port $port
    Write-Host "Waiting for leecher $i health..."
    if (-not $SkipHealthChecks) {
        if (-not (Wait-ForHealth -Url "http://localhost:$port")) {
            Write-Warning "Leecher $i failed health check on port $port; continuing"
        }
    }
}

Start-Sleep -Seconds 3

$seedFile = Join-Path $BaseDir "seed-file.txt"
Set-Content -Path $seedFile -Value "stonkagents test file" -Encoding ASCII

Write-Host "Sharing file from seeder 0..."
$shareJson = & curl.exe -s -X POST -F "file=@$seedFile" "http://localhost:7841/api/v1/share"
$share = $shareJson | ConvertFrom-Json
$cid = $share.cid
if (-not $cid) {
    throw "Share failed on seeder 0: $shareJson"
}
Write-Host "CID: $cid"

for ($i = 1; $i -lt $Seeders; $i++) {
    $port = 7841 + $i
    if ($port -eq 7842) { $port = 7843 }
    Write-Host "Sharing file from seeder $i..."
    $shareJson2 = & curl.exe -s -X POST -F "file=@$seedFile" "http://localhost:$port/api/v1/share"
    $share2 = $shareJson2 | ConvertFrom-Json
    if ($share2.cid -ne $cid) {
        Write-Host "Warning: CID mismatch from seeder $i ($($share2.cid))"
        Write-Host "Seeder $i response: $shareJson2"
    }
}

if ($Leechers -gt 0) {
    $leecherPort = 7900
    Write-Host "Requesting download on leecher 0..."
    $downloadResp = & curl.exe -s -X POST -H "Content-Type: application/json" -d "{`"cid`":`"$cid`"}" "http://localhost:$leecherPort/api/v1/download"
    if (-not $downloadResp) {
        throw "Download request failed on leecher 0"
    }
    Write-Host "Download response: $downloadResp"

    Write-Host "Polling leecher download status..."
    for ($i = 0; $i -lt 10; $i++) {
        $status = & curl.exe -s "http://localhost:$leecherPort/api/v1/downloads/status"
        Write-Host $status
        Start-Sleep -Seconds 2
    }

    Write-Host "Polling seeder 0 upload status..."
    $seederPort = 7841
    $uploadStatus = & curl.exe -s "http://localhost:$seederPort/api/v1/uploads/status"
    Write-Host $uploadStatus

    $downloadedPath = Join-Path (Join-Path (Join-Path $BaseDir "leecher-0\\data\\downloads\\assets") $cid) "seed-file.txt"
    $timeout = [DateTime]::UtcNow.AddMinutes(2)
    while ([DateTime]::UtcNow -lt $timeout) {
        if (Test-Path $downloadedPath) {
            Write-Host "Download complete: $downloadedPath"
            break
        }
        Start-Sleep -Seconds 2
    }
}

Write-Host "Tracker PID: $($trackerProc.Id)"
Write-Host "Seeder PIDs: $($seederProcs.Id -join ', ')"
Write-Host "Leecher PIDs: $($leecherProcs.Id -join ', ')"
Write-Host "Stop processes manually when done."
