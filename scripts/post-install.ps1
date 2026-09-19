# Post-install: get daemon peer key and run stonkagents onboard (tied to same peer profile).
# Run this after installing the StonkAgents MSI to onboard the agent CLI with the daemon's peer API key.
# Installs the agent CLI globally (npm install -g stonkagents) so the gateway Scheduled Task has a stable module path.
# Uses the daemon's key so the agent shares one account with the portal; reinstall does not create a new account or grant extra credits.
# Reads STONKAGENTS_GATEWAY_TOKEN from daemon.env and passes it to onboard so the gateway and daemon share the same token (no manual paste).
# Requires: Node.js (in PATH or at default C:\Program Files\nodejs from bundle). Daemon must be running and already registered.
# Usage: .\post-install.ps1 [-DaemonUrl "http://127.0.0.1:7841"]

param(
    # Daemon base URL. Default: STONKAGENTS_DAEMON_URL (set by stonkagents-tools.exe), else the
    # port of the environment named in daemon.env next to this script, else production's 7841.
    [string]$DaemonUrl = "",
    # npm spec for the agent CLI package. Defaults to bare "stonkagents" (@latest); pin a
    # version (e.g. "stonkagents@2026.9.1") for reproducible installer builds, or point at
    [string]$CliNpmSpec = $(if ($env:STONKAGENTS_NPM_SPEC) { $env:STONKAGENTS_NPM_SPEC } else { "stonkagents" })
)
$ErrorActionPreference = "Continue"

# --- Environment (dev, stg, prd): side-by-side installs use different daemon ports and CLI profiles ---
# Same table as internal/installenv (Go). STONKAGENTS_ENV comes from stonkagents-tools.exe, or from the
# daemon.env setuphelper wrote next to this script when the Start menu "Configure" shortcut runs it.
$envTable = @{
    prd = @{ DaemonPort = 7841; CliProfile = "";    GatewayPort = 18789 }
    stg = @{ DaemonPort = 7851; CliProfile = "stg"; GatewayPort = 19002 }
    dev = @{ DaemonPort = 7861; CliProfile = "dev"; GatewayPort = 19001 }
}
$installEnv = "$env:STONKAGENTS_ENV".Trim().ToLowerInvariant()
if (-not $installEnv) {
    $envFileForEnv = Join-Path $PSScriptRoot "daemon.env"
    if (Test-Path -LiteralPath $envFileForEnv) {
        foreach ($line in (Get-Content -LiteralPath $envFileForEnv -ErrorAction SilentlyContinue)) {
            if ($line -match '^STONKAGENTS_ENV=(.*)$') { $installEnv = $matches[1].Trim().Trim('"').ToLowerInvariant(); break }
        }
    }
}
if (-not $envTable.ContainsKey($installEnv)) { $installEnv = "prd" }
$envProfile = $envTable[$installEnv]
$env:STONKAGENTS_ENV = $installEnv
if (-not $DaemonUrl) {
    $DaemonUrl = if ($env:STONKAGENTS_DAEMON_URL) { $env:STONKAGENTS_DAEMON_URL } else { "http://127.0.0.1:$($envProfile.DaemonPort)" }
}
# The npm CLI is shared by every environment; OPENCLAW_PROFILE gives each one its own config,
# state dir (~\.openclaw-<profile>) and gateway task, OPENCLAW_GATEWAY_PORT its own gateway port.
# Production keeps the CLI defaults (no profile, ~\.openclaw, 18789).
if ($envProfile.CliProfile) {
    if (-not $env:OPENCLAW_PROFILE) { $env:OPENCLAW_PROFILE = $envProfile.CliProfile }
    if (-not $env:OPENCLAW_GATEWAY_PORT) { $env:OPENCLAW_GATEWAY_PORT = "$($envProfile.GatewayPort)" }
}

$DaemonUrl = $DaemonUrl.TrimEnd("/")
$peerKeyUrl = "$DaemonUrl/api/v1/installer/peer-key"

# Step markers with wall clock and seconds since start. stonkagents-tools.exe copies this output to
# %TEMP%\stonkagents-tools.log; the markers show which phase (daemon wait, npm install, onboard) ate
# the minutes when the background command tools job runs long.
$script:stepStart = Get-Date
function Write-Step([string]$message, [string]$color = "Cyan") {
    $elapsed = [int]((Get-Date) - $script:stepStart).TotalSeconds
    Write-Host ("[{0:HH:mm:ss} +{1}s] {2}" -f (Get-Date), $elapsed, $message) -ForegroundColor $color
}
# Phase markers are the user-facing ones: stonkagents-tools.exe turns a "phase: ..." line into the
# text the portal's "Command tools" chip shows (with the elapsed time, and the npm download
# counter while npm runs), so keep them short (under 60 characters) and plain.
function Write-Phase([string]$message) {
    Write-Step "phase: $message"
}
Write-Step "post-install starting (environment: $installEnv, daemon: $DaemonUrl, cli profile: '$env:OPENCLAW_PROFILE', cli spec: $CliNpmSpec)"

# Package name = spec without a trailing @version/@tag (the leading @ of a scoped package is kept).
$CliPackageName = $CliNpmSpec
if ($CliPackageName.LastIndexOf("@") -gt 0) { $CliPackageName = $CliPackageName.Substring(0, $CliPackageName.LastIndexOf("@")) }
if (-not $CliPackageName) { $CliPackageName = "stonkagents" }
# Bin names to look for in the npm global prefix: the current package.
$CliBinNames = @("$CliPackageName.cmd", "stonkagents.cmd") | Select-Object -Unique

# --- Resolve Node.js so we can run npx $CliPackageName for onboarding/gateway ---
# Prefer Node from PATH (user's nvm/system Node) over hardcoded bundled path to avoid stale versions.
$nodeDir = $null
$nodeCmd = Get-Command node -ErrorAction SilentlyContinue
if ($nodeCmd) {
    $nodeDir = Split-Path -Parent $nodeCmd.Source
}
if (-not $nodeDir -or -not (Test-Path -LiteralPath "$nodeDir\node.exe")) {
    $nodeDir = "C:\Program Files\nodejs"
}
if (-not (Test-Path -LiteralPath "$nodeDir\node.exe")) {
    # A hard failure, not a skip: the command tools job reports this line to the portal with a
    # Retry, whereas "exit 0" once read as "ready" while nothing had been installed.
    Write-Host "Node.js was not found (looked in PATH and C:\Program Files\nodejs). Install Node.js LTS from nodejs.org, then retry the command tools from the portal." -ForegroundColor Yellow
    exit 1
}
$env:PATH = "$nodeDir;$env:PATH"
$npxCmd = Join-Path $nodeDir "npx.cmd"
if (-not (Test-Path -LiteralPath $npxCmd)) { $npxCmd = "npx" }

# --- Every external command runs with a hard timeout ---
# The command tools job (stonkagents-tools.exe) waits for this script, so nothing in it may wait
# forever: not npm, not the CLI. A command that overruns is killed with every descendant
# (npm spawns node -> nested npm -> node; the CLI can spawn the gateway) and the script
# goes on, non-fatal.

# Kill a process and every descendant.
function Stop-ProcessTree([int]$parentPid) {
    Get-CimInstance Win32_Process -Filter "ParentProcessId=$parentPid" -ErrorAction SilentlyContinue | ForEach-Object { Stop-ProcessTree $_.ProcessId }
    try { Stop-Process -Id $parentPid -Force -ErrorAction SilentlyContinue } catch {}
}

# Run a command with a hard timeout; returns its exit code (124 on timeout, after killing
# the process tree). Output goes to this script's own stdout/stderr (stonkagents-tools.exe
# logs it) unless -OutFile is given, which captures stdout into that file instead.
function Invoke-WithTimeout([string]$file, [string[]]$arguments, [int]$timeoutSec, [string]$label, [string]$outFile = "") {
    $startArgs = @{ FilePath = $file; NoNewWindow = $true; PassThru = $true }
    if ($arguments -and $arguments.Count -gt 0) { $startArgs.ArgumentList = $arguments }
    if ($outFile) { $startArgs.RedirectStandardOutput = $outFile }
    $proc = Start-Process @startArgs
    # Touch the handle now: without this, $proc.ExitCode is $null after WaitForExit (PowerShell
    # -PassThru quirk) and every successful command looked like a failure.
    $null = $proc.Handle
    if (-not $proc.WaitForExit($timeoutSec * 1000)) {
        Write-Host "$label did not finish within ${timeoutSec}s; killing it (non-fatal)." -ForegroundColor Yellow
        Stop-ProcessTree $proc.Id
        return 124
    }
    if ($null -eq $proc.ExitCode) { return 0 }
    return $proc.ExitCode
}

# Run a quick query command (npm config get, npm prefix -g) with a timeout and return its
# trimmed stdout, or an empty string when it fails or overruns.
function Invoke-Query([string]$file, [string[]]$arguments, [int]$timeoutSec = 60) {
    $outFile = Join-Path $env:TEMP ("post-install-" + [System.IO.Path]::GetRandomFileName() + ".txt")
    try {
        $exit = Invoke-WithTimeout $file $arguments $timeoutSec "$([System.IO.Path]::GetFileName($file)) $($arguments -join ' ')" $outFile
        if ($exit -ne 0 -or -not (Test-Path -LiteralPath $outFile)) { return "" }
        return ((Get-Content -LiteralPath $outFile -Raw -ErrorAction SilentlyContinue) -as [string]).Trim()
    } catch {
        return ""
    } finally {
        Remove-Item -LiteralPath $outFile -Force -ErrorAction SilentlyContinue
    }
}

# --- Wait for daemon to start and register, then get peer key for onboard ---
$maxRetries = 30
$retryDelaySec = 5
$apiKey = $null
$trackerUrl = $null

Write-Phase "Waiting for the StonkAgents service"
# Give daemon time to start (setuphelper just started it; cold start can be slow under SYSTEM)
Start-Sleep -Seconds 10

for ($i = 0; $i -lt $maxRetries; $i++) {
    $shouldRetry = $false
    try {
        $response = Invoke-RestMethod -Uri $peerKeyUrl -Method Get -TimeoutSec 10
        $apiKey = $response.api_key
        $trackerUrl = $response.tracker_url
        if ($apiKey) { break }
        $shouldRetry = $true
        $reason = "registered but no key yet"
    } catch {
        $statusCode = $_.Exception.Response.StatusCode.value__
        if ($statusCode -eq 403) {
            Write-Host "Peer-key endpoint only accepts localhost requests." -ForegroundColor Yellow
            exit 1
        }
        $shouldRetry = $true
        $reason = if ($null -eq $statusCode) { "not yet listening" } else { "HTTP $statusCode" }
    }
    if ($shouldRetry) {
        if ($i -lt $maxRetries - 1) {
            Write-Host "Daemon $reason. Retrying in ${retryDelaySec}s ($($i+1)/$maxRetries)..." -ForegroundColor Gray
            Start-Sleep -Seconds $retryDelaySec
        } else {
            Write-Host "Daemon not ready after $($maxRetries * $retryDelaySec)s." -ForegroundColor Yellow
            Write-Host "Start the StonkAgents daemon, wait for it to register, then run Configure StonkAgents AI again." -ForegroundColor Gray
            exit 1
        }
    }
}

if (-not $apiKey) {
    Write-Host "Could not get peer key from daemon." -ForegroundColor Yellow
    exit 1
}
Write-Step "daemon ready, peer key received"
if (-not $trackerUrl) {
    $trackerUrl = if ($env:STONKAGENTS_TRACKER_URL) { $env:STONKAGENTS_TRACKER_URL } else { "" }
}
if (-not $trackerUrl) {
    # Default to the production tracker (what new installs bake). Override at runtime with $env:STONKAGENTS_TRACKER_URL.
    $trackerUrl = "https://tracker.stonkagents.com"
}
$trackerUrl = $trackerUrl.TrimEnd("/")

# Read gateway token from daemon.env (same dir as script) so onboard uses the same token the daemon has
$gatewayToken = ""
$daemonEnvPath = Join-Path $PSScriptRoot "daemon.env"
if (Test-Path -LiteralPath $daemonEnvPath) {
    $lines = Get-Content -LiteralPath $daemonEnvPath -ErrorAction SilentlyContinue
    foreach ($line in $lines) {
        if ($line -match '^STONKAGENTS_GATEWAY_TOKEN=(.*)') {
            $gatewayToken = $matches[1].Trim().Trim('"')
            break
        }
    }
}

# --- Install the CLI globally so the gateway Scheduled Task has a stable module path ---
# The gateway.cmd (created by '<cli> gateway start') hardcodes the path to the package's index.js.
# If the package is only in the npx cache, that path is volatile and breaks when the cache is cleared.
$npmCmd = Join-Path $nodeDir "npm.cmd"
if (-not (Test-Path -LiteralPath $npmCmd)) { $npmCmd = "npm" }

# Where npm will fetch from. A user .npmrc pointing at a dead private registry or a proxy is the
# usual reason this step takes far longer than the estimate; the log shows it up front.
Write-Phase "Checking npm"
# One npm spawn (about 3 s each on Windows) answers all three questions.
$npmInfo = Invoke-Query $npmCmd @("config", "get", "registry", "https-proxy", "npm-version")
Write-Host "  npm at $npmCmd" -ForegroundColor DarkGray
Write-Host "  $($npmInfo -replace '\s+', ' ')" -ForegroundColor DarkGray

# No pre-emptive "npm cache verify" or npx cache wipe: verify walks the whole cache (a 7 GB cache
# took 160 s) and the wipe deletes tens of thousands of files (200 s behind an antivirus), both
# before a single package is fetched. A corrupt cache surfaces as an npm install failure
# (ECOMPROMISED, EINTEGRITY), and the retry below cleans the cache first, so only the machines that
# need it pay for it.

# The package's own postinstall runs a second, nested `npm install` for the runtime deps of every
# bundled channel extension (Discord voice, Teams, Twitch, OpenTelemetry, ...). The StonkAgents
# provider extension declares none, the nested resolve fails on a peer conflict in the published
# devDependencies anyway, and on the way it fetches several hundred more registry documents.
# The package honours this switch, so skip it.
$env:OPENCLAW_DISABLE_BUNDLED_PLUGIN_POSTINSTALL = "1"

# Ensure the global npm prefix directory exists (fresh Node installs may not have it yet).
$npmGlobalDir = Invoke-Query $npmCmd @("prefix", "-g")
if ($npmGlobalDir -and -not (Test-Path $npmGlobalDir)) {
    New-Item -ItemType Directory -Path $npmGlobalDir -Force | Out-Null
}

# Deleting a package tree (30,000 files) takes minutes behind an antivirus, and npm only needs the
# path to be free. Rename it beside itself (instant, same volume) and let a detached process delete
# it; if the rename fails the delete runs in the foreground as before.
function Remove-DirInBackground([string]$dir) {
    $gone = "$dir.gone-$([guid]::NewGuid().ToString('N').Substring(0, 8))"
    try {
        Rename-Item -LiteralPath $dir -NewName ([System.IO.Path]::GetFileName($gone)) -ErrorAction Stop
    } catch {
        Remove-Item -LiteralPath $dir -Recurse -Force -ErrorAction SilentlyContinue
        return
    }
    $cmd = "Remove-Item -LiteralPath '" + $gone.Replace("'", "''") + "' -Recurse -Force -ErrorAction SilentlyContinue"
    Start-Process -FilePath "powershell.exe" -ArgumentList @("-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", $cmd) -WindowStyle Hidden | Out-Null
}

# Remove leftovers of an interrupted earlier install. npm "retires" the old package to
# node_modules\.<pkg>-<hash> before replacing it; if that dir already exists (previous run
# was killed mid-reify) npm 10 on Windows hangs forever at "reify moves" instead of erroring.
# A package dir without package.json is the other half of the same broken state.
if ($npmGlobalDir) {
    $globalModules = Join-Path $npmGlobalDir "node_modules"
    if (Test-Path -LiteralPath $globalModules) {
        foreach ($pkg in @($CliPackageName)) {
            Get-ChildItem -LiteralPath $globalModules -Force -Directory -Filter ".$pkg-*" -ErrorAction SilentlyContinue | ForEach-Object {
                Write-Host "Moving stale npm retired dir out of the way: $($_.FullName)" -ForegroundColor DarkGray
                Remove-DirInBackground $_.FullName
            }
            $existingPkg = Join-Path $globalModules $pkg
            if ((Test-Path -LiteralPath $existingPkg) -and -not (Test-Path -LiteralPath (Join-Path $existingPkg "package.json"))) {
                Write-Host "Moving broken global $pkg install (no package.json) out of the way: $existingPkg" -ForegroundColor DarkGray
                Remove-DirInBackground $existingPkg
            }
        }
    }
}

# Run npm install with a hard timeout so the command tools job can never hang indefinitely.
$npmInstallTimeoutSec = 600

# Run an npm command with a hard timeout; returns the exit code (124 on timeout).
function Invoke-NpmWithTimeout([string[]]$npmArgs, [int]$timeoutSec) {
    return Invoke-WithTimeout $npmCmd $npmArgs $timeoutSec "npm $($npmArgs[0])"
}

Write-Step "Installing $CliNpmSpec globally (for gateway service, timeout ${npmInstallTimeoutSec}s)..."
Write-Phase "Downloading the command tools"
Write-Host "npm downloads about 660 packages, 600 MB in total. Each file it fetches is listed below; the portal shows the count and the elapsed time." -ForegroundColor Gray
# --loglevel=http: on a pipe npm shows no progress bar, so its per-request log lines are the only live
# signal. stonkagents-tools.exe counts the package files for the portal's status chip and logs them.
$npmExit = Invoke-NpmWithTimeout @("install", "-g", "--no-audit", "--no-fund", "--loglevel=http", $CliNpmSpec) $npmInstallTimeoutSec
if ($npmExit -ne 0 -and $npmExit -ne 124) {
    # A leftover bin shim from another package (e.g. a global openclaw) still collides: let npm
    # overwrite the shims rather than leave the machine without the CLI. --prefer-offline: the
    # first attempt already filled the cache, so the retry resolves from it instead of refetching
    # every package document.
    Write-Host "npm install exited $npmExit; cleaning the npm cache and retrying once with --force (overwrites conflicting bin shims)..." -ForegroundColor Yellow
    Write-Phase "Retrying the command tools download"
    # A corrupt cache (ECOMPROMISED, EINTEGRITY) is the usual reason; clean it before the retry, capped.
    $null = Invoke-NpmWithTimeout @("cache", "clean", "--force") 120
    $npmExit = Invoke-NpmWithTimeout @("install", "-g", "--no-audit", "--no-fund", "--loglevel=http", "--force", $CliNpmSpec) $npmInstallTimeoutSec
}
if ($npmExit -ne 0) {
    Write-Host "Global install of $CliNpmSpec failed (exit $npmExit, non-fatal). You can run 'npm install -g $CliNpmSpec' manually." -ForegroundColor Yellow
}
Write-Step "npm install step done (exit code: $npmExit)" "DarkGray"

# Resolve the globally-installed CLI command directly from npm prefix (avoids npx OOM).
$cliBin = $null
$npmGlobalBin = Invoke-Query $npmCmd @("prefix", "-g")
if ($npmGlobalBin) {
    foreach ($binName in $CliBinNames) {
        $candidate = Join-Path $npmGlobalBin $binName
        if (Test-Path -LiteralPath $candidate) {
            $cliBin = $candidate
            Write-Host "  Using global CLI: $cliBin" -ForegroundColor DarkGray
            break
        }
    }
}
if (-not $cliBin) {
    # Fallback: search PATH but skip the MSI npx wrapper in Program Files\StonkAgents
    foreach ($binName in $CliBinNames) {
        $cmdName = [System.IO.Path]::GetFileNameWithoutExtension($binName)
        $allBins = where.exe $cmdName 2>$null
        foreach ($c in $allBins) {
            if ($c -and $c -notlike "*Program Files\StonkAgents*") {
                $cliBin = $c
                Write-Host "  Using PATH CLI: $cliBin" -ForegroundColor DarkGray
                break
            }
        }
        if ($cliBin) { break }
    }
}
# npm failed and left no CLI behind: onboarding through npx would only fetch from the same
# registry again and fail with a less useful line. Stop here with the reason the portal can show.
if ($npmExit -ne 0 -and -not $cliBin) {
    $why = if ($npmExit -eq 124) { "npm did not finish within ${npmInstallTimeoutSec}s" } else { "npm install exited $npmExit" }
    Write-Host "Could not download the command tools ($why). Check the internet connection and any proxy or antivirus, then retry from the portal." -ForegroundColor Yellow
    exit 1
}

Write-Step "Onboarding StonkAgents AI with daemon peer key (same account as portal)..."
Write-Host "  api_key=$($apiKey.Substring(0, [Math]::Min(8, $apiKey.Length)))... tracker=$trackerUrl" -ForegroundColor DarkGray

# Best-effort: repair stale ~/.openclaw/openclaw.json before onboard. Users upgrading
# from an older CLI may have config entries for renamed or removed channels
# which cause onboard to abort with "Config invalid". `doctor --fix`
# is idempotent and harmless on clean installs. It MUST run without prompts: this script
# has no interactive stdin, so a prompt would hang the command tools job
# forever (--non-interactive --yes), and a hard timeout backs that up. Errors ignored so
# a missing/older CLI can't block onboarding.
#
# --fix also auto-approves the CLI's runtime repairs: "Install gateway service now?",
# "Start/Restart gateway service now?" and the gateway service config rewrite, each of
# which installs or (re)starts the gateway right here, before onboard has written the
# config and before gateway-setup.exe (run next by stonkagents-tools.exe) sets the task up. A gateway
# started that way inherited this script's output and kept the 2.4.0 installer waiting on
# the command tools step for good. OPENCLAW_UPDATE_IN_PROGRESS=1 is the CLI's own switch
# for exactly this (its updater runs doctor with it): config repairs still apply, the
# gateway is neither installed nor started nor restarted, its task script is only staged.
$doctorTimeoutSec = 180
Write-Phase "Checking the command tools configuration"
Write-Step "doctor --fix starting" "DarkGray"
$env:OPENCLAW_UPDATE_IN_PROGRESS = "1"
try {
    $doctorArgs = @("doctor", "--fix", "--non-interactive", "--yes")
    if ($cliBin) {
        $doctorExit = Invoke-WithTimeout $cliBin $doctorArgs $doctorTimeoutSec "doctor --fix"
    } else {
        $doctorExit = Invoke-WithTimeout $npxCmd (@($CliPackageName) + $doctorArgs) $doctorTimeoutSec "doctor --fix"
    }
    Write-Host "  doctor --fix exit code: $doctorExit" -ForegroundColor DarkGray
} catch {
    Write-Host "  openclaw doctor --fix skipped: $($_.Exception.Message)" -ForegroundColor DarkGray
}
Remove-Item Env:OPENCLAW_UPDATE_IN_PROGRESS -ErrorAction SilentlyContinue
Write-Step "doctor --fix done, onboard starting" "DarkGray"
Write-Phase "Connecting the command tools to your agent"

# Set the tracker URL and key for the provider plugin at config-write time (STONKAGENTS_*).
# onboard runs --non-interactive with an explicit --auth-choice (stonkagents-ai). Without
# --non-interactive the CLI shows its "Setup mode" menu, gets no stdin, and exits 0 having
# configured nothing.
#
# onboard writes the config only. --skip-daemon: the gateway task is gateway-setup.exe's
# job (stonkagents-tools.exe runs it right after this script); a gateway started from
# here would inherit this script's output and keep the job waiting on it. In
# non-interactive mode the CLI installs no service unless asked, and --skip-daemon wins
# over any default. --skip-health: nothing to probe, the gateway is not up yet. A hard
# timeout backs both up; on overrun the CLI and its children are killed, non-fatal.
$onboardTimeoutSec = 300
$env:STONKAGENTS_TRACKER_URL = $trackerUrl
$env:STONKAGENTS_API_KEY = $apiKey
$onboardArgs = @("onboard", "--auth-choice", "stonkagents-ai", "--stonkagents-api-key", $apiKey)
$onboardArgs += "--accept-risk", "--skip-daemon", "--skip-health", "--non-interactive"
if ($gatewayToken) {
    $onboardArgs += "--gateway-token", $gatewayToken
    Write-Host "  gateway_token provided" -ForegroundColor DarkGray
}

$onboardExit = 1
try {
    if ($cliBin) {
        Write-Host "  running: $cliBin $($onboardArgs -join ' ') (timeout ${onboardTimeoutSec}s)" -ForegroundColor DarkGray
        $onboardExit = Invoke-WithTimeout $cliBin $onboardArgs $onboardTimeoutSec "onboard"
    } else {
        Write-Host "  running (npx): $npxCmd $CliPackageName $($onboardArgs -join ' ') (timeout ${onboardTimeoutSec}s)" -ForegroundColor DarkGray
        $onboardExit = Invoke-WithTimeout $npxCmd (@($CliPackageName) + $onboardArgs) $onboardTimeoutSec "onboard"
    }
} catch {
    Write-Host "  onboard could not be started: $($_.Exception.Message)" -ForegroundColor Yellow
}
Write-Step "onboard exit code: $onboardExit" "DarkGray"
# Both are failures the portal shows with a Retry. A timed-out onboard used to exit 0, which
# let the job end "ready" with no provider config and a chat that failed for no visible reason.
if ($onboardExit -eq 124) {
    Write-Host "StonkAgents onboard did not finish within ${onboardTimeoutSec}s and was stopped. Retry from the portal, or run Configure StonkAgents AI from the Start menu." -ForegroundColor Yellow
    exit 1
}
if ($onboardExit -ne 0) {
    Write-Host "StonkAgents onboard failed (exit $onboardExit). Retry from the portal; the log has the CLI output." -ForegroundColor Yellow
    exit $onboardExit
}

Write-Host "StonkAgents AI onboarding complete." -ForegroundColor Green
# Gateway install + start is handled by gateway-setup.exe, which stonkagents-tools.exe runs
# right after this script (the same background job).
