# Test Agent Chat APIs (daemon + optional tracker)
# Prereqs: Daemon on 7841, gateway_url set. For tracker tests: tracker on 7842, token registered.
# Usage: .\scripts\test-agent-chat.ps1 [-DaemonUrl "http://localhost:7841"] [-TrackerUrl "http://localhost:7842"] [-TokenContract "ADDR"]

param(
    [string]$DaemonUrl = "http://localhost:7841",
    [string]$TrackerUrl = "http://localhost:7842",
    [string]$TokenContract = "",
    [string]$UserId = "TestWallet"
)

$ErrorActionPreference = "Stop"

Write-Host "=== Agent Chat API Tests ===" -ForegroundColor Cyan
Write-Host "Daemon: $DaemonUrl | Tracker: $TrackerUrl"
Write-Host ""

# 1) Health
Write-Host "[1] GET $DaemonUrl/health" -ForegroundColor Yellow
try {
    $health = Invoke-RestMethod -Method Get -Uri "$DaemonUrl/health" -TimeoutSec 5
    Write-Host "    OK: $($health.status)" -ForegroundColor Green
} catch {
    Write-Host "    FAIL: $_" -ForegroundColor Red
    exit 1
}

# 2) Get API key (localhost-only endpoint)
Write-Host "[2] GET $DaemonUrl/api/v1/installer/peer-key" -ForegroundColor Yellow
try {
    $keyResp = Invoke-RestMethod -Method Get -Uri "$DaemonUrl/api/v1/installer/peer-key" -TimeoutSec 5
    $apiKey = $keyResp.api_key
    if (-not $apiKey) {
        Write-Host "    SKIP: Daemon not registered (no api_key). Register daemon with tracker first." -ForegroundColor DarkYellow
        $apiKey = $null
    } else {
        Write-Host "    OK: api_key present, tracker_url=$($keyResp.tracker_url)" -ForegroundColor Green
    }
} catch {
    Write-Host "    FAIL or localhost-only: $_" -ForegroundColor Red
    $apiKey = $null
}

# 3) Main agent chat
Write-Host "[3] POST $DaemonUrl/api/v1/agent/chat (main chat)" -ForegroundColor Yellow
$body = @{ messages = @(@{ role = "user"; content = "Reply with exactly: OK" }); user_id = $UserId } | ConvertTo-Json -Depth 5
try {
    $chatResp = Invoke-RestMethod -Method Post -Uri "$DaemonUrl/api/v1/agent/chat" -ContentType "application/json" -Body $body -TimeoutSec 60
    Write-Host "    OK: response=$($chatResp.response.Substring(0, [Math]::Min(60, $chatResp.response.Length)))..." -ForegroundColor Green
    Write-Host "    session_id=$($chatResp.session_id)" -ForegroundColor Gray
} catch {
    Write-Host "    FAIL: $_" -ForegroundColor Red
    if ($_.Exception.Response) {
        $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
        Write-Host "    Body: $($reader.ReadToEnd())" -ForegroundColor Gray
    }
}

# 4) List sessions
Write-Host "[4] GET $DaemonUrl/api/v1/agent/chat/sessions?user_id=$UserId" -ForegroundColor Yellow
try {
    $sessions = Invoke-RestMethod -Method Get -Uri "$DaemonUrl/api/v1/agent/chat/sessions?user_id=$UserId" -TimeoutSec 5
    $count = if ($sessions.sessions) { $sessions.sessions.Count } else { 0 }
    Write-Host "    OK: $count session(s)" -ForegroundColor Green
} catch {
    Write-Host "    FAIL: $_" -ForegroundColor Red
}

# 5) Tracker: owner lookup (if we have api_key and token)
if ($apiKey -and $TokenContract) {
    Write-Host "[5] GET $TrackerUrl/api/v1/agent/chat/owner?token_contract_address=$TokenContract" -ForegroundColor Yellow
    try {
        $owner = Invoke-RestMethod -Method Get -Uri "$TrackerUrl/api/v1/agent/chat/owner?token_contract_address=$TokenContract" -Headers @{ "X-API-Key" = $apiKey } -TimeoutSec 10
        Write-Host "    OK: owner_peer_id=$($owner.owner_peer_id) online=$($owner.online) multiaddrs=$($owner.multiaddrs.Count)" -ForegroundColor Green
    } catch {
        Write-Host "    FAIL: $_" -ForegroundColor Red
    }
} else {
    Write-Host "[5] SKIP: tracker owner (no api_key or -TokenContract)" -ForegroundColor DarkYellow
    if (-not $TokenContract) {
        Write-Host "    Hint: run with -TokenContract 'YOUR_TOKEN_CONTRACT_ADDRESS' to test owner lookup" -ForegroundColor Gray
    }
}

Write-Host ""
Write-Host "Done. For token chat (holder flow), use the portal or:" -ForegroundColor Cyan
Write-Host "  curl -X POST $DaemonUrl/api/v1/agent/chat -H 'Content-Type: application/json' -d '{\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"user_id\":\"HOLDER\",\"context_id\":\"TOKEN_CONTRACT\"}'" -ForegroundColor Gray
