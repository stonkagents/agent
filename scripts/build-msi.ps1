# StonkAgents - MSI build script
# Builds stonkagents-daemon.exe, genkeys.exe, setuphelper.exe, stonkagents-svc.exe, stonkagents-controller-svc.exe,
# stonkagents-tools.exe and gateway-setup.exe; copies launcher; runs WiX to produce MSI.
# Supports: WiX 3.x (candle.exe, light.exe) or WiX 4/6 (dotnet build with .wixproj SDK project).
# For WiX 4/6: .NET SDK (dotnet) must be in PATH; extensions come from NuGet via the .wixproj.
# Usage: .\scripts\build-msi.ps1 [-Version "0.1.0"] [-OutDir "dist"] [-TrackerUrl "https://..."] [-PortalUrl "https://..."] [-Environment dev|stg|prd] [-Force] [-SkipBundle]
# -Environment selects the side-by-side install identity (product name, install and data folders, service names,
# ports, firewall rule, UpgradeCodes; see installer\wix\Environment.wxi and internal\installenv). Derived from the
# tracker URL when omitted: tracker.dev.* -> dev, tracker.stg.* -> stg, anything else -> prd (unchanged production layout).
# Tracker URL is read from .env (STONKAGENTS_TRACKER_URL) at build time and baked into the installer so the installed daemon uses the same URL.
# Optional -TrackerUrl overrides .env for this run.
# The Setup EXE finish screen opens the portal: -PortalUrl overrides it; otherwise it is derived from the environment
# on the primary portal domain, stonkagents.com (dev -> https://dev.stonkagents.com, stg -> https://stg.stonkagents.com,
# prd -> https://stonkagents.com), whichever domain the tracker URL is on (tracker.*.stonkagents.com stays live for installed clients).
# Passed to WiX as the PortalUrl preprocessor variable (-p:PortalUrl=...).
# -Force bypasses version tag collision, MSI collision, and tracker health checks.
# -SkipBundle: build only the MSI (no Node download, no Burn bundle); use when Node is managed separately or for CI.

param(
    [string]$Version = "0.1.0",
    [string]$OutDir = "dist",
    [string]$TrackerUrl = "",
    [string]$PortalUrl = "",
    [ValidateSet("", "dev", "stg", "prd")]
    [string]$Environment = "",
    [switch]$Force,
    [switch]$SkipBundle
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)

# Load .env from project root so STONKAGENTS_TRACKER_URL is available at build time (used when baking tracker URL into setuphelper).
$envFile = Join-Path $projectRoot ".env"
if (Test-Path $envFile) {
    Get-Content $envFile | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) { return }
        if ($line -match '^([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
            $val = $matches[2].Trim().Trim('"').Trim("'")
            Set-Item -Path "Env:$($matches[1])" -Value $val -ErrorAction SilentlyContinue
        }
    }
}
$buildDir = Join-Path $projectRoot "installer\wix\build"
$wixDir = Join-Path $projectRoot "installer\wix"
$wixProj = Join-Path $wixDir "StonkAgents.wixproj"
$bundleProj = Join-Path $wixDir "StonkAgents.Bundle.wixproj"
$msiOut = Join-Path $projectRoot $OutDir
$wixSrcV3 = Join-Path $wixDir "StonkAgents.v3.wxs"

# --- Pre-flight checks (same guards as macOS sign-release.sh) ---
Write-Host "[Pre-flight] Checking prerequisites..." -ForegroundColor Cyan

# 1. Git tag collision — prevent building a version that's already tagged
$existingTag = git tag -l "v$Version" 2>$null
if ($existingTag) {
    if ($Force) {
        Write-Host "  WARNING: Git tag v$Version already exists. -Force specified, continuing." -ForegroundColor Yellow
    } else {
        throw "Git tag v$Version already exists. Bump the version or use -Force."
    }
} else {
    Write-Host "  OK Version $Version not yet tagged" -ForegroundColor Green
}

# 2. MSI collision — prevent overwriting existing MSI
$msiPath_check = Join-Path $msiOut "StonkAgents-$Version.msi"
if (Test-Path $msiPath_check) {
    if ($Force) {
        Write-Host "  WARNING: $msiPath_check already exists. -Force specified, will overwrite." -ForegroundColor Yellow
    } else {
        throw "$msiPath_check already exists. Bump the version or use -Force."
    }
}

$archiveDir = Join-Path $msiOut "archive"
if (-not (Test-Path $archiveDir)) { New-Item -ItemType Directory -Path $archiveDir -Force | Out-Null }
$existingMsis = @(Get-ChildItem -Path $msiOut -Filter "StonkAgents-*.msi" -ErrorAction SilentlyContinue)
foreach ($msi in $existingMsis) {
    Write-Host "  Archiving $($msi.Name) -> archive/" -ForegroundColor DarkGray
    Move-Item -Path $msi.FullName -Destination (Join-Path $archiveDir $msi.Name) -Force
}

# 4. Tracker health check — fail early if tracker is unreachable
$trackerUrlCheck = $TrackerUrl
if ($trackerUrlCheck -eq "" -and $env:STONKAGENTS_TRACKER_URL) {
    $trackerUrlCheck = $env:STONKAGENTS_TRACKER_URL.Trim()
}
if ($trackerUrlCheck -eq "") {
    $trackerUrlCheck = "https://tracker.dev.stonkagents.com"
}
Write-Host "[Pre-flight] Checking tracker health..." -ForegroundColor Cyan
Write-Host "  Tracker URL: $trackerUrlCheck" -ForegroundColor DarkGray
try {
    $response = Invoke-WebRequest -Uri "$trackerUrlCheck/health" -TimeoutSec 10 -UseBasicParsing -ErrorAction Stop
    if ($response.StatusCode -eq 200) {
        Write-Host "  OK Tracker healthy (HTTP $($response.StatusCode))" -ForegroundColor Green
    } else {
        throw "HTTP $($response.StatusCode)"
    }
} catch {
    if ($Force) {
        Write-Host "  WARNING: Tracker at $trackerUrlCheck is not healthy ($_). -Force specified, continuing." -ForegroundColor Yellow
    } else {
        throw "Tracker at $trackerUrlCheck is not healthy ($_). Check: Invoke-WebRequest $trackerUrlCheck/health. Use -Force to bypass."
    }
}
Write-Host ""

# Detect WiX: prefer WiX 3 (candle/light), then WiX 4/6 (.wixproj + dotnet build)
$useWix6 = $false
$candle = $null
$light = $null

$candleCmd = Get-Command candle -ErrorAction SilentlyContinue
if ($candleCmd) {
    $candle = $candleCmd.Source
    $lightCmd = Get-Command light -ErrorAction SilentlyContinue
    if ($lightCmd) { $light = $lightCmd.Source }
}
if (-not $candle -and $env:WIX) {
    $candleExe = Join-Path $env:WIX "bin\candle.exe"
    $lightExe = Join-Path $env:WIX "bin\light.exe"
    if ((Test-Path $candleExe) -and (Test-Path $lightExe)) {
        $candle = $candleExe
        $light = $lightExe
    }
}
if (-not $candle -or -not $light) {
    if (Test-Path $wixProj) {
        $useWix6 = $true
    }
}

if (-not $useWix6 -and (-not $candle -or -not $light)) {
    throw "WiX Toolset not found. Install WiX 3.x (candle.exe/light.exe in PATH or set WIX) or use WiX 4/6 (installer\wix\StonkAgents.wixproj + dotnet build)."
}

Write-Host "Building StonkAgents MSI (version $Version)..." -ForegroundColor Cyan
if ($useWix6) { Write-Host "Using WiX 4/6 (dotnet build .wixproj)" -ForegroundColor DarkGray }
Write-Host ""

# Build dir
if (Test-Path $buildDir) { Remove-Item -Recurse -Force $buildDir }
New-Item -ItemType Directory -Path $buildDir -Force | Out-Null

Push-Location $projectRoot
try {
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"

    $versionLdflags = "-s -w -X main.Version=$Version"

    Write-Host "Building stonkagents-daemon.exe..." -ForegroundColor Yellow
    & go build -ldflags $versionLdflags -o (Join-Path $buildDir "stonkagents-daemon.exe") .\cmd\daemon
    if ($LASTEXITCODE -ne 0) { throw "go build daemon failed" }

    Write-Host "Building genkeys.exe..." -ForegroundColor Yellow
    & go build -ldflags="-s -w" -o (Join-Path $buildDir "genkeys.exe") .\cmd\genkeys
    if ($LASTEXITCODE -ne 0) { throw "go build genkeys failed" }

    Write-Host "Building setuphelper.exe..." -ForegroundColor Yellow
    $trackerUrlToBake = $TrackerUrl
    if ($trackerUrlToBake -eq "" -and $env:STONKAGENTS_TRACKER_URL) {
        $trackerUrlToBake = $env:STONKAGENTS_TRACKER_URL.Trim()
    }
    if ($trackerUrlToBake -eq "") {
        # Default to production. Override with -TrackerUrl or $env:STONKAGENTS_TRACKER_URL for dev/stg builds.
        # The tracker host is baked into the installed daemon. The stonkagents.com tracker hosts are live
        # (tracker.*.stonkagents.com stays up for installs that baked it).
        $trackerUrlToBake = "https://tracker.stonkagents.com"
    }
    # Live-agent download API key in daemon.env (replicator: REPLICATOR_LIVE_AGENT_API_KEY must match). Override with .env STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY at build time.
    $liveAgentKeyToBake = "live-agent-api-key-1234567890"
    if ($env:STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY) {
        $liveAgentKeyToBake = $env:STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY.Trim().Trim('"').Trim("'")
    }
    $ldflags = '-s -w'
    $ldflags += " -X main.defaultTrackerURL=$trackerUrlToBake"
    # Environment: explicit -Environment, else derived from the tracker host (dev/stg/prd).
    $environmentToBake = $Environment
    if ($environmentToBake -eq "") {
        $trackerHostForEnv = ([Uri]$trackerUrlToBake).Host.ToLowerInvariant()
        $environmentToBake = if ($trackerHostForEnv.StartsWith("tracker.dev.")) { "dev" } elseif ($trackerHostForEnv.StartsWith("tracker.stg.")) { "stg" } else { "prd" }
    }
    if (-not $useWix6 -and $environmentToBake -ne "prd") {
        throw "Environment $environmentToBake needs the WiX 4/6 project build (StonkAgents.v3.wxs only knows the production layout)."
    }
    $ldflags += " -X main.defaultEnvironment=$environmentToBake"
    $ldflags += " -X main.defaultLiveAgentDownloadAPIKey=$liveAgentKeyToBake"
    Write-Host "  Environment baked: $environmentToBake" -ForegroundColor DarkGray
    Write-Host "  Tracker URL baked: $trackerUrlToBake" -ForegroundColor DarkGray
    # Portal URL for the bundle finish screen: explicit -PortalUrl, else the environment on the primary
    # portal domain (dev -> https://dev.stonkagents.com, stg -> https://stg.stonkagents.com, prd -> https://stonkagents.com).
    $portalDomain = "stonkagents.com"
    $portalUrlToBake = $PortalUrl.Trim()
    if ($portalUrlToBake -eq "") {
        $portalUrlToBake = if ($environmentToBake -eq "prd") { "https://$portalDomain" } else { "https://$environmentToBake.$portalDomain" }
    }
    Write-Host "  Portal URL for finish screen: $portalUrlToBake" -ForegroundColor DarkGray
    Write-Host "  Live agent API key baked into daemon.env (set STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY in .env to override)" -ForegroundColor DarkGray
    & go build -ldflags $ldflags -o (Join-Path $buildDir "setuphelper.exe") .\installer\helper
    if ($LASTEXITCODE -ne 0) { throw "go build setuphelper failed" }

    Write-Host "Building stonkagents-svc.exe (Windows service wrapper)..." -ForegroundColor Yellow
    & go build -ldflags="-s -w" -o (Join-Path $buildDir "stonkagents-svc.exe") .\installer\svcwrap
    if ($LASTEXITCODE -ne 0) { throw "go build stonkagents-svc failed" }

    Write-Host "Building stonkagents-controller-svc.exe (controller service)..." -ForegroundColor Yellow
    & go build -ldflags $versionLdflags -o (Join-Path $buildDir "stonkagents-controller-svc.exe") .\installer\controllersvc
    if ($LASTEXITCODE -ne 0) { throw "go build stonkagents-controller-svc failed" }

    Copy-Item (Join-Path $projectRoot "scripts\daemon\start-daemon.ps1") -Destination (Join-Path $buildDir "start-daemon.ps1") -Force
    Write-Host "Copied start-daemon.ps1" -ForegroundColor Green
    Copy-Item (Join-Path $projectRoot "scripts\post-install.ps1") -Destination (Join-Path $buildDir "post-install.ps1") -Force
    Write-Host "Copied post-install.ps1" -ForegroundColor Green

    # Generate the CLI wrapper (stonkagents.cmd; stonkagents-dev.cmd / stonkagents-stg.cmd for those environments, so
    # three installs on one PATH do not shadow each other). Uses npx from user's PATH (nvm/system Node), falls back to bundled Node.
    # Dev and stg wrappers set OPENCLAW_PROFILE and OPENCLAW_GATEWAY_PORT so the shared npm CLI acts on that environment's
    # config, state dir and gateway (same table as internal/installenv); production keeps the CLI defaults.
    # Default is bare "stonkagents" (@latest).
    $cliNpmSpec = if ($env:STONKAGENTS_NPM_SPEC) { $env:STONKAGENTS_NPM_SPEC } else { "stonkagents" }
    Write-Host "  CLI npm spec baked into the CLI wrapper: $cliNpmSpec" -ForegroundColor DarkGray
    $cliEnvLines = switch ($environmentToBake) {
        "dev" { "set OPENCLAW_PROFILE=dev`r`nset OPENCLAW_GATEWAY_PORT=19001`r`nset STONKAGENTS_ENV=dev`r`n" }
        "stg" { "set OPENCLAW_PROFILE=stg`r`nset OPENCLAW_GATEWAY_PORT=19002`r`nset STONKAGENTS_ENV=stg`r`n" }
        default { "" }
    }
    $wrapperName = if ($environmentToBake -eq "prd") { "stonkagents.cmd" } else { "stonkagents-$environmentToBake.cmd" }
    $wrapperPath = Join-Path $buildDir $wrapperName
    $wrapperContent = "@echo off`r`nsetlocal`r`n${cliEnvLines}if not exist `"%APPDATA%\npm`" mkdir `"%APPDATA%\npm`"`r`nwhere npx >nul 2>nul`r`nif %errorlevel%==0 (`r`n  npx --yes $cliNpmSpec %*`r`n) else (`r`n  `"%ProgramFiles%\nodejs\npx.cmd`" --yes $cliNpmSpec %*`r`n)`r`nexit /b %errorlevel%"
    Set-Content -LiteralPath $wrapperPath -Value $wrapperContent -Encoding ASCII -Force
    Write-Host "Generated $wrapperName wrapper" -ForegroundColor Green

    # The two setup tools ship inside the MSI. setuphelper.exe registers the "<ProductName> command tools"
    # scheduled task that runs stonkagents-tools.exe --background as the installing user after the install
    # (installer\helper\commandtools.go); stonkagents-tools.exe runs gateway-setup.exe at the end. -H=windowsgui:
    # the task starts it on the user's desktop, where a console program would open a black window.
    Write-Host "Building stonkagents-tools.exe (command tools setup job)..." -ForegroundColor Yellow
    & go build -ldflags="-s -w -H=windowsgui" -o (Join-Path $buildDir "stonkagents-tools.exe") .\installer\stonkagentstools
    if ($LASTEXITCODE -ne 0) { throw "go build stonkagents-tools failed" }

    Write-Host "Building gateway-setup.exe (gateway setup tool)..." -ForegroundColor Yellow
    & go build -ldflags="-s -w" -o (Join-Path $buildDir "gateway-setup.exe") .\installer\gatewaysetup
    if ($LASTEXITCODE -ne 0) { throw "go build gateway-setup failed" }
}
finally {
    Pop-Location
}

if (-not (Test-Path $msiOut)) { New-Item -ItemType Directory -Path $msiOut -Force | Out-Null }
$msiPath = Join-Path $msiOut "StonkAgents-$Version.msi"

if ($useWix6) {
    # WiX 4/6: build via MSBuild SDK (.wixproj). Clean obj/bin so we don't pick up old MSI or cached path.
    $wixBinRelease = Join-Path $wixDir "bin\Release"
    $wixObjRelease = Join-Path $wixDir "obj\Release"
    if (Test-Path $wixBinRelease) { Remove-Item -Recurse -Force $wixBinRelease }
    if (Test-Path $wixObjRelease) { Remove-Item -Recurse -Force $wixObjRelease }
    Write-Host ""
    Write-Host "Running dotnet build (WiX SDK)..." -ForegroundColor Yellow
    & dotnet build $wixProj -c Release -p:BuildDir=$buildDir -p:Version=$Version -p:Environment=$environmentToBake -v minimal
    if ($LASTEXITCODE -ne 0) { throw "dotnet build failed" }
    # WiX SDK outputs StonkAgents.msi to bin\Release. Newer WiX 6 versions put it
    # in a culture subdir like bin\Release\en-us\ when any localization is in
    # play (even on sibling projects). Check both locations.
    $sdkMsi = Join-Path $wixBinRelease "StonkAgents.msi"
    if (-not (Test-Path $sdkMsi)) {
        $sdkMsi = Join-Path $wixBinRelease "en-us\StonkAgents.msi"
    }
    if (-not (Test-Path $sdkMsi)) {
        throw "MSI not found: checked bin\Release\StonkAgents.msi and bin\Release\en-us\StonkAgents.msi"
    }
    Copy-Item -Path $sdkMsi -Destination $msiPath -Force
    Write-Host "Copied MSI to $msiPath" -ForegroundColor DarkGray

    # Burn bundle (setup exe): Node LTS + StonkAgents MSI
    if (-not $SkipBundle) {
        $nodeVersion = "v22.22.0"
        $nodeMsiName = "node-$nodeVersion-x64.msi"
        $nodeUrl = "https://nodejs.org/dist/$nodeVersion/$nodeMsiName"
        $nodeDir = Join-Path $buildDir "node"
        $nodeMsiPath = Join-Path $nodeDir $nodeMsiName
        if (-not (Test-Path $nodeMsiPath)) {
            Write-Host ""
            Write-Host "Downloading Node.js LTS ($nodeVersion)..." -ForegroundColor Yellow
            if (-not (Test-Path $nodeDir)) { New-Item -ItemType Directory -Path $nodeDir -Force | Out-Null }
            try {
                Invoke-WebRequest -Uri $nodeUrl -OutFile $nodeMsiPath -UseBasicParsing
                Write-Host "  Saved to $nodeMsiPath" -ForegroundColor DarkGray
            } catch {
                throw "Failed to download Node LTS from $nodeUrl : $_"
            }
        } else {
            Write-Host ""
            Write-Host "Using cached Node LTS MSI: $nodeMsiPath" -ForegroundColor DarkGray
        }

        Write-Host ""
        Write-Host "Building Burn bundle (StonkAgents-Setup.exe)..." -ForegroundColor Yellow
        $bundleBinRelease = Join-Path $wixDir "bin\Release"
        $bundleObjRelease = Join-Path $wixDir "obj\Release"
        if (Test-Path $bundleBinRelease) { Remove-Item -Recurse -Force $bundleBinRelease }
        if (Test-Path $bundleObjRelease) { Remove-Item -Recurse -Force $bundleObjRelease }
        $msiPathAbs = (Resolve-Path -LiteralPath $msiPath).Path
        $nodeMsiPathAbs = (Resolve-Path -LiteralPath $nodeMsiPath).Path
        & dotnet build $bundleProj -c Release `
            -p:Version=$Version `
            -p:StonkAgentsMsiPath="$msiPathAbs" `
            -p:NodeMsiPath="$nodeMsiPathAbs" `
            -p:PortalUrl="$portalUrlToBake" `
            -p:Environment=$environmentToBake `
            -v minimal
        if ($LASTEXITCODE -ne 0) { throw "dotnet build bundle failed" }

        $bundleExe = Get-ChildItem -Path $bundleBinRelease -Filter "*.exe" -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
        if (-not $bundleExe) { throw "Bundle exe not found under installer\wix\bin\Release" }
        $setupExePath = Join-Path $msiOut "StonkAgents-Setup-$Version.exe"
        Copy-Item -Path $bundleExe.FullName -Destination $setupExePath -Force
        Write-Host "Copied bundle to $setupExePath" -ForegroundColor DarkGray
    }
} else {
    # WiX 3: candle then light
    $wixObj = Join-Path $buildDir "StonkAgents.wixobj"
    Write-Host ""
    Write-Host "Running candle..." -ForegroundColor Yellow
    & $candle -nologo -arch x64 "-dBuildDir=$buildDir" "-dVersion=$Version" "-out" $wixObj $wixSrcV3
    if ($LASTEXITCODE -ne 0) { throw "candle failed" }
    Write-Host "Running light..." -ForegroundColor Yellow
    & $light -nologo -ext WixUIExtension -ext WixUtilExtension "-out" $msiPath $wixObj
    if ($LASTEXITCODE -ne 0) { throw "light failed" }
}

Write-Host ""
Write-Host "MSI created: $msiPath" -ForegroundColor Green
if ($useWix6 -and -not $SkipBundle) {
    Write-Host "Setup exe created: $msiOut\StonkAgents-Setup-$Version.exe" -ForegroundColor Green
}
Write-Host ""
