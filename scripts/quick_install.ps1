param(
    [string]$RepoUrl = "https://github.com/stonkagents/agent.git",
    [string]$InstallDir = "$env:LOCALAPPDATA\StonkAgents\bin",
    [string]$ConfigDir = "$env:USERPROFILE.stonkagents",
    [string]$DataDir = "$env:USERPROFILE.stonkagents\data",
    [string]$SecretsPath = "$env:USERPROFILE.stonkagents\secrets.env"
)

$ErrorActionPreference = "Stop"

function Test-CommandAvailable {
    param([string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Missing dependency: $Name"
    }
}

Test-CommandAvailable git
Test-CommandAvailable go

$workDir = (Get-Location).Path
if (-not (Test-Path (Join-Path $workDir "go.mod"))) {
    $workDir = Join-Path $env:TEMP "stonkagents-install"
    if (Test-Path $workDir) { Remove-Item -Recurse -Force $workDir }
    git clone $RepoUrl $workDir
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

Push-Location $workDir
go build -o (Join-Path $InstallDir "sync-daemon.exe") .\cmd\daemon\main.go
Pop-Location

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
Push-Location $workDir
$out = go run $tmp
Pop-Location

$lines = $out -split "`n"
$pubKey = $lines[0].Trim()
$privKey = $lines[1].Trim()

if (-not (Test-Path (Join-Path $ConfigDir "config.yaml"))) {
@"
peer_id: ""
public_key: "$pubKey"
daemon_host: "127.0.0.1"
daemon_port: 7841
bootstrap_peers: []
tracker_url: "http://localhost:7842"
data_dir: "$DataDir"
"@ | Set-Content -Path (Join-Path $ConfigDir "config.yaml") -Encoding ASCII
}

@"
STONKAGENTS_PRIVATE_KEY=$privKey
"@ | Set-Content -Path $SecretsPath -Encoding ASCII

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
}

Write-Host "Installed: $InstallDir\sync-daemon.exe"
Write-Host "Config: $ConfigDir\config.yaml"
Write-Host "Secrets: $SecretsPath"
Write-Host "Start: sync-daemon.exe"
