#!/usr/bin/env bash
# Feature: F-025 (Auto-Update)
# Story: US-025-10 (DMG v0.4.0 Release)
# Purpose: Build StonkAgents.app bundle with launcher script
#
# Build StonkAgents.app bundle (structure only — Go binaries added by build-installer.sh).
# Usage: ./scripts/macos/build-app-bundle.sh [version] [output_dir]
# Prereqs: Run build-icon.sh first to generate assets/StonkAgents.icns

set -euo pipefail

# Tracker URL — read from environment, fallback to production default.
# Used by sed to inject into the launcher's config.yaml template.
STONKAGENTS_TRACKER_URL="${STONKAGENTS_TRACKER_URL:-https://tracker.stonkagents.com}"

VERSION="${1:-0.1.0}"
OUT_DIR="${2:-dist}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
APP_DIR="$PROJECT_ROOT/$OUT_DIR/StonkAgents.app"
CONTENTS="$APP_DIR/Contents"
MACOS="$CONTENTS/MacOS"
RESOURCES="$CONTENTS/Resources"

echo "Building StonkAgents.app (v$VERSION)..."

# Clean previous
rm -rf "$APP_DIR"
mkdir -p "$MACOS" "$RESOURCES"

# --- Info.plist ---
cat > "$CONTENTS/Info.plist" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>StonkAgents</string>
    <key>CFBundleDisplayName</key>
    <string>StonkAgents</string>
    <key>CFBundleIdentifier</key>
    <string>com.stonkagents.daemon</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleExecutable</key>
    <string>stonkagents-launcher</string>
    <key>CFBundleIconFile</key>
    <string>StonkAgents</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

# --- Icon ---
ICNS_PATH="$PROJECT_ROOT/assets/StonkAgents.icns"
if [[ -f "$ICNS_PATH" ]]; then
  cp "$ICNS_PATH" "$RESOURCES/StonkAgents.icns"
else
  echo "WARN: StonkAgents.icns not found. Run scripts/build-icon.sh first." >&2
fi

# --- Launcher script (main executable) ---
cat > "$MACOS/stonkagents-launcher" << 'LAUNCHER'
#!/bin/bash
# StonkAgents launcher.
# - install mode: orchestrates full setup + onboarding
# - normal mode: verifies daemon health and restarts service if needed
set -euo pipefail

BUNDLE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RESOURCES="$BUNDLE_DIR/Resources"
INSTALL_DIR="$HOME/.local/bin"
CONFIG_DIR="$HOME/.stonkagents"
LOGS_DIR="$CONFIG_DIR/logs"
SECRETS_PATH="$CONFIG_DIR/secrets.env"
CONFIG_PATH="$CONFIG_DIR/config.yaml"
DAEMON_ENV_PATH="$CONFIG_DIR/daemon.env"
PLIST_NAME="com.stonkagents.daemon.plist"
PLIST_PATH="$HOME/Library/LaunchAgents/$PLIST_NAME"
CONTROLLER_PLIST_NAME="com.stonkagents.controller.plist"
CONTROLLER_PLIST_PATH="$HOME/Library/LaunchAgents/$CONTROLLER_PLIST_NAME"
DAEMON_BIN="$INSTALL_DIR/stonkagents"
GENKEYS_BIN="$INSTALL_DIR/genkeys"
CONTROLLER_BIN="$INSTALL_DIR/stonkagents-controller"

INSTALL_MODE=0
STATUS_FILE=""
GATEWAY_TOKEN=""
PEER_API_KEY=""
PEER_TRACKER_URL=""
# BAKED_TRACKER_URL holds the tracker URL this installer was built for. The
# build script (build-app-bundle.sh) rewrites this placeholder via a second
# sed pass immediately after it rewrites the tracker_url inside ensure_config.
# Used by ensure_config in install-mode to migrate stale dev URLs to prod.
BAKED_TRACKER_URL="http://localhost:7842"
NODE_BIN=""
NPM_BIN=""
CLI_BIN=""
NPM_GLOBAL_PREFIX="$CONFIG_DIR/npm-global"
NPM_GLOBAL_BIN="$NPM_GLOBAL_PREFIX/bin"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-mode)
      INSTALL_MODE=1
      shift
      ;;
    --status-file)
      STATUS_FILE="${2:-}"
      shift 2
      ;;
    --status-file=*)
      STATUS_FILE="${1#*=}"
      shift
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -n "$STATUS_FILE" ]]; then
  : > "$STATUS_FILE" 2>/dev/null || true
fi

status() {
  local phase="$1"
  shift
  local message="$*"
  echo "[stonkagents-launcher] ${phase}: ${message}"
  if [[ -n "$STATUS_FILE" ]]; then
    printf '%s\t%s\t%s\n' "$(/bin/date -u +"%Y-%m-%dT%H:%M:%SZ")" "$phase" "$message" >> "$STATUS_FILE" 2>/dev/null || true
  fi
}

notify() {
  local message="$1"
  local subtitle="$2"
  /usr/bin/osascript -e "display notification \"${message}\" with title \"StonkAgents\" subtitle \"${subtitle}\"" 2>/dev/null || true
}

fail() {
  local message="$1"
  status "error" "$message"
  local escaped="${message//\"/\\\"}"
  /usr/bin/osascript -e "display dialog \"${escaped}\" with title \"StonkAgents\" buttons {\"OK\"} with icon stop" 2>/dev/null || true
  exit 1
}

generate_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n'
    return
  fi
  /usr/bin/head -c 32 /dev/urandom | /usr/bin/base64 | tr '+/' '-_' | tr -d '=\n'
}

ensure_binary() {
  local bin_name="$1"
  local src="$RESOURCES/$bin_name"
  local dst="$INSTALL_DIR/$bin_name"
  [[ -f "$src" ]] || fail "$bin_name binary not found in app bundle."
  /bin/cp -f "$src" "$dst"
  /bin/chmod 755 "$dst"
}

ensure_secrets() {
  if [[ ! -f "$SECRETS_PATH" ]] || ! /usr/bin/grep -q 'STONKAGENTS_PRIVATE_KEY=.' "$SECRETS_PATH" 2>/dev/null; then
    if [[ -x "$GENKEYS_BIN" ]]; then
      "$GENKEYS_BIN" --out "$SECRETS_PATH" >/dev/null 2>&1 || echo "# Add: STONKAGENTS_PRIVATE_KEY=<base64>" > "$SECRETS_PATH"
    else
      echo "# Add: STONKAGENTS_PRIVATE_KEY=<base64>" > "$SECRETS_PATH"
    fi
    /bin/chmod 600 "$SECRETS_PATH"
  fi
}

ensure_config() {
  if [[ ! -f "$CONFIG_PATH" ]]; then
    cat > "$CONFIG_PATH" << CFGEOF
peer_id: ""
public_key: ""
daemon_host: "127.0.0.1"
daemon_port: 7841
bootstrap_peers: []
tracker_url: "http://localhost:7842"
data_dir: '$CONFIG_DIR/data'
gateway_url: "http://127.0.0.1:18789"
ask_use_tracker: true
CFGEOF
    return
  fi

  # Existing config: during --install-mode only, migrate a stale tracker_url
  # to the URL this installer was built for. Without this, users who had a
  # prior dev install keep talking to the dev tracker even after
  # installing a prod DMG (because the old config.yaml wins). We preserve
  # secrets.env (peer identity) — the daemon will auto-register the existing
  # peer_id on the new tracker (new account + 50 free credits granted by the
  # new tracker's registration flow; any paid balance on the prior tracker
  # stays recoverable there via the recovery flow).
  if [[ "$INSTALL_MODE" == "1" ]] && [[ -n "$BAKED_TRACKER_URL" ]]; then
    local current_url
    current_url=$(/usr/bin/grep '^tracker_url:' "$CONFIG_PATH" 2>/dev/null | /usr/bin/tail -n 1 \
                  | /usr/bin/sed 's/.*tracker_url:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}.*/\1/' \
                  | /usr/bin/tr -d '[:space:]')
    if [[ -z "$current_url" ]]; then
      echo "tracker_url: \"${BAKED_TRACKER_URL}\"" >> "$CONFIG_PATH"
      status "config" "added tracker_url: ${BAKED_TRACKER_URL}"
    elif [[ "$current_url" != "$BAKED_TRACKER_URL" ]]; then
      /usr/bin/sed -i '' "s|^tracker_url:.*|tracker_url: \"${BAKED_TRACKER_URL}\"|" "$CONFIG_PATH"
      status "config" "migrated tracker_url: ${current_url} -> ${BAKED_TRACKER_URL}"
    fi
  fi

  if ! /usr/bin/grep -q '^gateway_url:' "$CONFIG_PATH"; then
    echo 'gateway_url: "http://127.0.0.1:18789"' >> "$CONFIG_PATH"
  fi
  if ! /usr/bin/grep -q '^ask_use_tracker:' "$CONFIG_PATH"; then
    echo 'ask_use_tracker: true' >> "$CONFIG_PATH"
  fi
}

load_gateway_token() {
  if [[ -f "$DAEMON_ENV_PATH" ]]; then
    GATEWAY_TOKEN=$(/usr/bin/grep '^STONKAGENTS_GATEWAY_TOKEN=' "$DAEMON_ENV_PATH" | /usr/bin/tail -n 1 | /usr/bin/cut -d= -f2- | /usr/bin/tr -d '"' | /usr/bin/tr -d '[:space:]')
  fi
  if [[ -z "$GATEWAY_TOKEN" ]]; then
    GATEWAY_TOKEN="$(generate_token)"
  fi
}

write_daemon_env() {
  cat > "$DAEMON_ENV_PATH" << ENVEOF
STONKAGENTS_CONFIG_PATH=$CONFIG_PATH
STONKAGENTS_SECRETS_PATH=$SECRETS_PATH
STONKAGENTS_GATEWAY_TOKEN=$GATEWAY_TOKEN
STONKAGENTS_ASK_USE_TRACKER=true
ENVEOF
  /bin/chmod 600 "$DAEMON_ENV_PATH"
}

write_controller_plist() {
  cat > "$CONTROLLER_PLIST_PATH" << CTRLPLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.stonkagents.controller</string>
    <key>ProgramArguments</key>
    <array>
        <string>$CONTROLLER_BIN</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$LOGS_DIR/controller.stdout.log</string>
    <key>StandardErrorPath</key>
    <string>$LOGS_DIR/controller.stderr.log</string>
    <key>AssociatedBundleIdentifiers</key>
    <array>
        <string>com.stonkagents.daemon</string>
    </array>
</dict>
</plist>
CTRLPLIST
}

write_daemon_plist() {
  cat > "$PLIST_PATH" << PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.stonkagents.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>$DAEMON_BIN</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>STONKAGENTS_CONFIG_PATH</key>
        <string>$CONFIG_PATH</string>
        <key>STONKAGENTS_SECRETS_PATH</key>
        <string>$SECRETS_PATH</string>
        <key>STONKAGENTS_GATEWAY_TOKEN</key>
        <string>$GATEWAY_TOKEN</string>
        <key>STONKAGENTS_ASK_USE_TRACKER</key>
        <string>true</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$LOGS_DIR/daemon.stdout.log</string>
    <key>StandardErrorPath</key>
    <string>$LOGS_DIR/daemon.stderr.log</string>
    <key>AssociatedBundleIdentifiers</key>
    <array>
        <string>com.stonkagents.daemon</string>
    </array>
</dict>
</plist>
PLISTEOF
}

ensure_services() {
  local uid
  uid="$(id -u)"
  local domain="gui/$uid"
  /bin/mkdir -p "$HOME/Library/LaunchAgents"
  write_controller_plist
  write_daemon_plist
  /bin/launchctl bootout "$domain" "$CONTROLLER_PLIST_PATH" 2>/dev/null || true
  /bin/launchctl bootout "$domain" "$PLIST_PATH" 2>/dev/null || true
  /bin/launchctl bootstrap "$domain" "$CONTROLLER_PLIST_PATH" 2>/dev/null || true
  /bin/launchctl bootstrap "$domain" "$PLIST_PATH" 2>/dev/null || true
  /bin/launchctl kickstart -k "$domain/com.stonkagents.controller" 2>/dev/null || true
  /bin/launchctl kickstart -k "$domain/com.stonkagents.daemon" 2>/dev/null || true
}

wait_for_daemon_health() {
  local retries=60
  local sleep_secs=5
  local i=1
  while [[ $i -le $retries ]]; do
    if /usr/bin/curl -sf "http://127.0.0.1:7841/health" >/dev/null 2>&1; then
      return 0
    fi
    /bin/sleep "$sleep_secs"
    i=$((i + 1))
  done
  return 1
}

fetch_peer_credentials() {
  local retries=30
  local sleep_secs=5
  local response=""
  local i=1

  while [[ $i -le $retries ]]; do
    response=$(/usr/bin/curl -fsS --max-time 10 "http://127.0.0.1:7841/api/v1/installer/peer-key" 2>/dev/null || true)
    if [[ -n "$response" ]]; then
      PEER_API_KEY=$(printf '%s' "$response" | /usr/bin/sed -n 's/.*"api_key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
      PEER_TRACKER_URL=$(printf '%s' "$response" | /usr/bin/sed -n 's/.*"tracker_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
      if [[ -n "$PEER_API_KEY" ]]; then
        return 0
      fi
    fi
    /bin/sleep "$sleep_secs"
    i=$((i + 1))
  done
  return 1
}

ensure_node_and_npm() {
  # GUI apps (launched from Swift installer) get a minimal PATH that excludes
  # Homebrew, Volta, nvm, asdf, etc. Search common locations explicitly.
  local candidates=()
  local home="$HOME"

  # PATH-based candidates (works in terminal, usually empty in GUI context)
  if command -v node >/dev/null 2>&1; then
    candidates+=("$(command -v node)")
  fi

  # Hardcoded well-known locations (mirrors Swift installer's findBestNodeRuntime)
  candidates+=(
    "/opt/homebrew/bin/node"
    "/usr/local/bin/node"
    "$home/.volta/bin/node"
    "$home/.asdf/shims/node"
  )

  # nvm versions
  if [[ -d "$home/.nvm/versions/node" ]]; then
    for nvm_dir in "$home/.nvm/versions/node"/*/bin/node; do
      [[ -x "$nvm_dir" ]] && candidates+=("$nvm_dir")
    done
  fi

  # Find the best (highest version) compatible Node
  NODE_BIN=""
  local best_major=0 best_minor=0 best_patch=0
  for candidate in "${candidates[@]}"; do
    [[ -x "$candidate" ]] || continue
    local ver major minor patch
    ver=$("$candidate" -v 2>/dev/null | /usr/bin/sed 's/^v//') || continue
    major=$(printf '%s' "$ver" | /usr/bin/cut -d. -f1)
    minor=$(printf '%s' "$ver" | /usr/bin/cut -d. -f2)
    patch=$(printf '%s' "$ver" | /usr/bin/cut -d. -f3)
    [[ -n "$major" ]] || continue
    minor="${minor:-0}"; patch="${patch:-0}"
    # Pick highest version
    if [[ "$major" -gt "$best_major" ]] || \
       { [[ "$major" -eq "$best_major" ]] && [[ "$minor" -gt "$best_minor" ]]; } || \
       { [[ "$major" -eq "$best_major" ]] && [[ "$minor" -eq "$best_minor" ]] && [[ "$patch" -gt "$best_patch" ]]; }; then
      best_major="$major"; best_minor="$minor"; best_patch="$patch"
      NODE_BIN="$candidate"
    fi
  done

  [[ -n "$NODE_BIN" ]] || fail "Node.js v22.12+ is required but was not found."

  if [[ "$best_major" -lt 22 ]] || { [[ "$best_major" -eq 22 ]] && [[ "$best_minor" -lt 12 ]]; }; then
    fail "Node.js v22.12+ is required. Found: $("$NODE_BIN" -v 2>/dev/null || echo unknown)"
  fi

  # npm scripts use `env node`; guarantee node is resolvable in GUI/minimal PATH.
  local node_dir
  node_dir="$(dirname "$NODE_BIN")"
  case ":$PATH:" in
    *":$node_dir:"*) ;;
    *) PATH="$node_dir:$PATH"; export PATH ;;
  esac

  if command -v npm >/dev/null 2>&1; then
    NPM_BIN="$(command -v npm)"
  elif [[ -x "$(dirname "$NODE_BIN")/npm" ]]; then
    NPM_BIN="$(dirname "$NODE_BIN")/npm"
  else
    fail "npm was not found alongside Node.js."
  fi

  # Force npm globals into a user-writable location so installs work after
  # system Node.pkg installation (which defaults to root-owned /usr/local).
  /bin/mkdir -p "$NPM_GLOBAL_PREFIX" "$NPM_GLOBAL_BIN"
  export NPM_CONFIG_PREFIX="$NPM_GLOBAL_PREFIX"
  export npm_config_prefix="$NPM_GLOBAL_PREFIX"
  case ":$PATH:" in
    *":$NPM_GLOBAL_BIN:"*) ;;
    *) PATH="$NPM_GLOBAL_BIN:$PATH"; export PATH ;;
  esac
}

install_cli() {
  local npm_install_log="$LOGS_DIR/npm-install.log"

  # npm spec for the CLI. The package is published as "stonkagents" (renamed
  # for reproducible builds, or stonkagents@next to track a pre-release tag.
  local cli_npm_spec="${STONKAGENTS_NPM_SPEC:-${STONKAGENTS_NPM_SPEC:-stonkagents}}"

  # Install from npm registry — same as Windows post-install.ps1.
  if ! "$NPM_BIN" install -g "$cli_npm_spec" >"$npm_install_log" 2>&1; then
    /usr/bin/tail -n 40 "$npm_install_log" 2>/dev/null || true
    fail "npm install -g $cli_npm_spec failed. See $npm_install_log"
  fi

  # (existing installs may still carry the old global package).
  local npm_prefix=""
  npm_prefix=$("$NPM_BIN" config get prefix 2>/dev/null || true)
  local bin_dir bin_name
  CLI_BIN=""
  for bin_name in stonkagents stonkagents; do
    for bin_dir in "$NPM_GLOBAL_BIN" "${npm_prefix:+$npm_prefix/bin}"; do
      [[ -n "$bin_dir" ]] || continue
      if [[ -x "$bin_dir/$bin_name" ]]; then
        CLI_BIN="$bin_dir/$bin_name"
        break 2
      fi
    done
  done

  [[ -x "$CLI_BIN" ]] || fail "StonkAgents CLI (npm package stonkagents) was not found after npm install."
}

run_onboarding() {
  if [[ -z "$PEER_TRACKER_URL" ]]; then
    PEER_TRACKER_URL=$(/usr/bin/grep '^tracker_url:' "$CONFIG_PATH" 2>/dev/null | /usr/bin/sed 's/.*tracker_url:[[:space:]]*"\(.*\)".*/\1/' | /usr/bin/tail -n 1)
  fi
  [[ -n "$PEER_TRACKER_URL" ]] || PEER_TRACKER_URL="http://localhost:7842"
  local onboard_log="$LOGS_DIR/stonkagents-onboard.log"

  status "onboard" "StonkAgents: linking your account"
  # Best-effort: repair stale ~/.openclaw/openclaw.json before onboard. Users
  # upgrading from a pre-v1.0 CLI may have config entries for renamed or
  # removed channels/plugins (e.g. "stonkagents"), which cause onboard to abort
  # with a "Config invalid" error. `doctor --fix` is idempotent and harmless
  # on clean installs. We ignore its exit code so a missing/older CLI can't
  # block onboarding.
  "$CLI_BIN" doctor --fix >"$LOGS_DIR/stonkagents-doctor.log" 2>&1 || true

  # The --stonkagents-api-key flag auto-infers --auth-choice=stonkagents-ai via the plugin manifest.
  export STONKAGENTS_TRACKER_URL="$PEER_TRACKER_URL"
  "$CLI_BIN" onboard \
    --stonkagents-api-key "$PEER_API_KEY" \
    --gateway-token "$GATEWAY_TOKEN" \
    --accept-risk \
    --skip-health \
    --skip-channels \
    --skip-skills \
    --skip-ui \
    --non-interactive >"$onboard_log" 2>&1 || fail "StonkAgents CLI onboard failed. See $onboard_log"

  # Best-effort status pings from the onboard log. Each grep is wrapped with
  # `|| true` so a non-match never trips `set -e` and aborts the launcher —
  # these are informational milestones, not gating checks. The refactored
  # final "account setup complete" status as a fixed success marker after all
  # the probe greps have run.
  { /usr/bin/grep -q 'Updated ~/.openclaw/openclaw.json' "$onboard_log" 2>/dev/null && status "onboard" "StonkAgents: settings saved"; } || true
  { /usr/bin/grep -q '^Workspace OK:' "$onboard_log" 2>/dev/null && status "onboard" "StonkAgents: workspace ready"; } || true
  { /usr/bin/grep -q '^Sessions OK:' "$onboard_log" 2>/dev/null && status "onboard" "StonkAgents: session storage ready"; } || true
  status "onboard" "StonkAgents: account setup complete"
}

run_gateway_setup() {
  local gateway_log="$LOGS_DIR/stonkagents-gateway.log"
  status "gateway" "StonkAgents: installing gateway service"
  "$CLI_BIN" gateway install --force --token "$GATEWAY_TOKEN" >"$gateway_log" 2>&1 || fail "StonkAgents CLI gateway install failed. See $gateway_log"
  /usr/bin/grep -q '^Installed LaunchAgent:' "$gateway_log" 2>/dev/null && status "gateway" "StonkAgents: gateway service installed"
  "$CLI_BIN" gateway start >>"$gateway_log" 2>&1 || true
  /usr/bin/grep -q '^Restarted LaunchAgent:' "$gateway_log" 2>/dev/null && status "gateway" "StonkAgents: gateway started"
}

run_install_workflow() {
  status "install" "Checking for an older data folder"

  status "install" "Preparing folders"
  /bin/mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$CONFIG_DIR/data" "$LOGS_DIR"
  /bin/chmod 700 "$CONFIG_DIR" "$CONFIG_DIR/data" "$LOGS_DIR"

  status "install" "Removing older StonkAgents services"

  status "install" "Installing StonkAgents files"
  ensure_binary "stonkagents"
  ensure_binary "genkeys"
  ensure_binary "stonkagents-controller"

  status "config" "Creating secure keys and settings"
  ensure_secrets
  ensure_config
  load_gateway_token
  write_daemon_env

  status "services" "Starting background services"
  ensure_services

  status "daemon" "Starting StonkAgents"
  wait_for_daemon_health || fail "Daemon did not become healthy on localhost:7841."

  status "peer-key" "Connecting your account"
  fetch_peer_credentials || fail "Daemon peer key is unavailable at /api/v1/installer/peer-key."

  status "node" "Checking Node.js (can take 1-3 minutes if install is needed)"
  ensure_node_and_npm

  status "cli" "Installing StonkAgents tools (can take 1-2 minutes)"
  install_cli

  status "onboard" "Finalizing setup"
  run_onboarding

  status "gateway" "Starting StonkAgents gateway"
  run_gateway_setup

  status "complete" "Installation complete"
}

run_healthcheck_mode() {
  if /usr/bin/curl -sf "http://127.0.0.1:7841/health" >/dev/null 2>&1; then
    notify "Daemon is running on localhost:7841" "Already running"
    return
  fi

  /bin/launchctl unload "$PLIST_PATH" 2>/dev/null || true
  /bin/launchctl load "$PLIST_PATH" 2>/dev/null || true
  /bin/sleep 2

  if /usr/bin/curl -sf "http://127.0.0.1:7841/health" >/dev/null 2>&1; then
    notify "Daemon restarted on localhost:7841" "Service restarted"
  else
    notify "Daemon restart requested; check localhost:7841/health" "Service status"
  fi
}

if [[ "$INSTALL_MODE" -eq 1 ]]; then
  run_install_workflow
  notify "Daemon and StonkAgents setup complete" "Installation complete"
  exit 0
fi

if [[ ! -x "$DAEMON_BIN" ]] || [[ ! -f "$PLIST_PATH" ]]; then
  run_install_workflow
  notify "Daemon and StonkAgents setup complete" "First-time setup"
  exit 0
fi

run_healthcheck_mode
LAUNCHER

chmod 755 "$MACOS/stonkagents-launcher"

# Inject tracker URL into the launcher's config.yaml template.
# The heredoc above is single-quoted (no variable expansion), so we use sed post-hoc.
# This mirrors the Windows pattern: build-msi.ps1 bakes the URL via ldflags.
sed -i '' "s|tracker_url: \"http://localhost:7842\"|tracker_url: \"${STONKAGENTS_TRACKER_URL}\"|" "$MACOS/stonkagents-launcher"
# Also bake the URL into the BAKED_TRACKER_URL variable so ensure_config can
# detect and migrate stale URLs at install time (see ensure_config comment).
sed -i '' "s|^BAKED_TRACKER_URL=\"http://localhost:7842\"|BAKED_TRACKER_URL=\"${STONKAGENTS_TRACKER_URL}\"|" "$MACOS/stonkagents-launcher"
echo "  Tracker URL injected: ${STONKAGENTS_TRACKER_URL}"

echo "App bundle created: $APP_DIR"
echo "  Contents/Info.plist"
echo "  Contents/MacOS/stonkagents-launcher"
echo "  Contents/Resources/StonkAgents.icns"
echo ""
echo "Note: stonkagents, genkeys, stonkagents-controller, and stonkagents-cli.tgz are added by build-installer.sh"
