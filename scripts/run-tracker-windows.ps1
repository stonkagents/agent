# Run the StonkAgents tracker on Windows.
# The real tracker lives under tracker/ (not cmd/tracker). This script builds and runs it.
# Mac: use your existing flow; this script is for Windows only.

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

$BinDir = "bin"
$TrackerBin = Join-Path $BinDir "cs-tracker.exe"

# Build from the correct path (tracker/cmd/tracker/main.go)
if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
Write-Host "Building tracker..."
go build -o $TrackerBin ./tracker/cmd/tracker
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Load .env from repo root so DATABASE_URL (and others) are set
if (Test-Path ".env") {
	Get-Content ".env" -Raw | ForEach-Object {
		$_ -split "`n" | ForEach-Object {
			if ($_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
				$key = $matches[1].Trim()
				$val = $matches[2].Trim() -replace '^["'']|["'']$'  # strip optional surrounding quotes
				[Environment]::SetEnvironmentVariable($key, $val, 'Process')
			}
		}
	}
	Write-Host "Loaded .env"
}
if ([string]::IsNullOrWhiteSpace($env:DATABASE_URL)) {
	Write-Error "DATABASE_URL is required. Set it in .env or in the environment (e.g. postgres://user:pass@host:5432/dbname)."
	exit 1
}

Write-Host "Starting tracker (default :7842). Use TRACKER_ADDR to override."
& $TrackerBin
