#!/usr/bin/env bash
# StonkAgents macOS Installer v2 — Spotify-style Swift UI installer
# Builds: Go daemon → StonkAgents.app payload → Swift installer → embeds payload
# Output: dist/Install StonkAgents.app/
#
# Feature: F-025 (Auto-Update)
# Story: US-025-10 (DMG v0.4.0 Release)
# Purpose: Build Go binaries (universal), Swift installer, and assemble Install StonkAgents.app
#
# Usage: ./scripts/macos/build-installer.sh [VERSION]
# Example: ./scripts/macos/build-installer.sh 0.4.0

set -euo pipefail

VERSION="${1:-0.3.0}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
INSTALLER_SRC="$PROJECT_ROOT/installer/macos"
DIST_DIR="$PROJECT_ROOT/dist"
STAGE_DIR="$(mktemp -d)"
INSTALLER_APP="$DIST_DIR/Install StonkAgents.app"

# Load only required build-time config from .env — never source entire file.
read_env_value() {
    local key="$1"
    local file="$2"
    grep "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- | tr -d '"' | tr -d "'"
}

if [[ -f "$PROJECT_ROOT/.env" ]]; then
    if [[ -z "${STONKAGENTS_TRACKER_URL:-}" ]]; then
        STONKAGENTS_TRACKER_URL="$(read_env_value STONKAGENTS_TRACKER_URL "$PROJECT_ROOT/.env")"
    fi
fi
export STONKAGENTS_TRACKER_URL="${STONKAGENTS_TRACKER_URL:-https://tracker.stonkagents.com}"

cleanup() { rm -rf "$STAGE_DIR"; }
trap cleanup EXIT

echo "================================================"
echo " StonkAgents Installer v2 Builder (v${VERSION})"
echo "================================================"
echo ""

if [[ "$(uname)" != "Darwin" ]]; then
    echo "Error: Requires macOS." >&2
    exit 1
fi

mkdir -p "$DIST_DIR"

# --- Step 1: Build StonkAgents.app payload (daemon + launcher) ---
echo "[1/4] Building StonkAgents.app payload..."
"$SCRIPT_DIR/build-app-bundle.sh" "$VERSION" "dist"

# --- Step 2: Build Go binaries (universal) and embed in payload ---
echo "[2/4] Compiling Go binaries (universal)..."
SKIP_GO_BUILD="${SKIP_GO_BUILD:-0}"
PREBUILT_BIN_DIR="${PREBUILT_BIN_DIR:-}"
PAYLOAD_RESOURCES="$DIST_DIR/StonkAgents.app/Contents/Resources"

build_arch() {
    local goarch="$1" suffix="$2"
    echo "  Building stonkagents for darwin/$goarch..."
    cd "$PROJECT_ROOT"
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${VERSION}" -o "$STAGE_DIR/stonkagents-$suffix" ./cmd/daemon/main.go
    echo "  Building genkeys for darwin/$goarch..."
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${VERSION}" -o "$STAGE_DIR/genkeys-$suffix" ./cmd/genkeys/main.go
    echo "  Building stonkagents-controller for darwin/$goarch..."
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${VERSION}" -o "$STAGE_DIR/stonkagents-controller-$suffix" ./cmd/controller/main.go
}

if [[ "$SKIP_GO_BUILD" == "1" ]]; then
    if [[ -z "$PREBUILT_BIN_DIR" ]]; then
        echo "Error: SKIP_GO_BUILD=1 requires PREBUILT_BIN_DIR." >&2
        exit 1
    fi
    for bin in stonkagents genkeys stonkagents-controller; do
        if [[ ! -f "$PREBUILT_BIN_DIR/$bin" ]]; then
            echo "Error: missing prebuilt binary: $PREBUILT_BIN_DIR/$bin" >&2
            exit 1
        fi
        cp "$PREBUILT_BIN_DIR/$bin" "$PAYLOAD_RESOURCES/$bin"
    done
    chmod 755 "$PAYLOAD_RESOURCES/stonkagents" "$PAYLOAD_RESOURCES/genkeys" "$PAYLOAD_RESOURCES/stonkagents-controller"
    echo "  Using prebuilt binaries from: $PREBUILT_BIN_DIR"
else
    build_arch arm64 arm64
    build_arch amd64 amd64

    echo "  Creating universal binaries..."
    lipo -create -output "$PAYLOAD_RESOURCES/stonkagents" "$STAGE_DIR/stonkagents-arm64" "$STAGE_DIR/stonkagents-amd64"
    lipo -create -output "$PAYLOAD_RESOURCES/genkeys" "$STAGE_DIR/genkeys-arm64" "$STAGE_DIR/genkeys-amd64"
    lipo -create -output "$PAYLOAD_RESOURCES/stonkagents-controller" "$STAGE_DIR/stonkagents-controller-arm64" "$STAGE_DIR/stonkagents-controller-amd64"
    chmod 755 "$PAYLOAD_RESOURCES/stonkagents" "$PAYLOAD_RESOURCES/genkeys" "$PAYLOAD_RESOURCES/stonkagents-controller"
fi

echo "  stonkagents: $(du -h "$PAYLOAD_RESOURCES/stonkagents" | awk '{print $1}') (universal)"
echo "  genkeys: $(du -h "$PAYLOAD_RESOURCES/genkeys" | awk '{print $1}') (universal)"
echo "  controller: $(du -h "$PAYLOAD_RESOURCES/stonkagents-controller" | awk '{print $1}') (universal)"

# The CLI (npm package "stonkagents") is installed from the npm registry at
# runtime (same as Windows), no bundled tarball needed.

# --- Step 3: Build Swift installer ---
echo "[3/4] Compiling Swift installer..."
cd "$INSTALLER_SRC"
swift build -c release 2>&1 | tail -5

SWIFT_BINARY="$INSTALLER_SRC/.build/release/InstallStonkAgents"
if [[ ! -f "$SWIFT_BINARY" ]]; then
    echo "Error: Swift build failed; binary not found at $SWIFT_BINARY" >&2
    exit 1
fi

# --- Step 4: Assemble Install StonkAgents.app ---
echo "[4/4] Assembling Install StonkAgents.app..."

rm -rf "$INSTALLER_APP"
mkdir -p "$INSTALLER_APP/Contents/MacOS"
mkdir -p "$INSTALLER_APP/Contents/Resources/payload"

# Copy Swift binary as main executable
cp "$SWIFT_BINARY" "$INSTALLER_APP/Contents/MacOS/InstallStonkAgents"

# Copy icon
ICNS_PATH="$PROJECT_ROOT/assets/StonkAgents.icns"
if [[ -f "$ICNS_PATH" ]]; then
    cp "$ICNS_PATH" "$INSTALLER_APP/Contents/Resources/StonkAgents.icns"
fi

# Embed payload (the entire StonkAgents.app)
cp -R "$DIST_DIR/StonkAgents.app" "$INSTALLER_APP/Contents/Resources/payload/StonkAgents.app"

# Embed Node installer package for automatic Node v22+ installation.
# The stonkagents npm package requires Node >=22.12.0 (see the CLI package.json engines).
# Search order:
#   1) NODE_PKG_PATH env var (explicit)
#   2) common project locations
# Use ALLOW_MISSING_NODE_PKG=1 to build without bundling Node.pkg.
ALLOW_MISSING_NODE_PKG="${ALLOW_MISSING_NODE_PKG:-0}"
NODE_PKG_PATH="${NODE_PKG_PATH:-}"
if [[ -z "$NODE_PKG_PATH" ]]; then
    shopt -s nullglob
    NODE_CANDIDATES=(
        "$PROJECT_ROOT/assets/node/Node.pkg"
        "$PROJECT_ROOT/assets/Node.pkg"
        "$PROJECT_ROOT/installer/macos/Node.pkg"
        "$PROJECT_ROOT/assets/node/"*.pkg
    )
    shopt -u nullglob
    for candidate in "${NODE_CANDIDATES[@]}"; do
        if [[ -f "$candidate" ]]; then
            NODE_PKG_PATH="$candidate"
            break
        fi
    done
fi

if [[ -n "$NODE_PKG_PATH" ]] && [[ -f "$NODE_PKG_PATH" ]]; then
    cp "$NODE_PKG_PATH" "$INSTALLER_APP/Contents/Resources/Node.pkg"
    echo "  Node.pkg: $(du -h "$INSTALLER_APP/Contents/Resources/Node.pkg" | awk '{print $1}')"
elif [[ "$ALLOW_MISSING_NODE_PKG" == "1" ]]; then
    echo "  WARN: Node.pkg not bundled (ALLOW_MISSING_NODE_PKG=1)"
else
    echo "Error: Node.pkg not found for bundling." >&2
    echo "  Provide NODE_PKG_PATH=/path/to/node-v22.x.pkg or place Node.pkg in assets/node/." >&2
    echo "  To bypass temporarily: ALLOW_MISSING_NODE_PKG=1" >&2
    exit 1
fi

# Write Info.plist for installer
cat > "$INSTALLER_APP/Contents/Info.plist" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>Install StonkAgents</string>
    <key>CFBundleDisplayName</key>
    <string>Install StonkAgents</string>
    <key>CFBundleIdentifier</key>
    <string>com.stonkagents.installer</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleExecutable</key>
    <string>InstallStonkAgents</string>
    <key>CFBundleIconFile</key>
    <string>StonkAgents</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSMinimumSystemVersion</key>
    <string>13.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

INSTALLER_SIZE=$(du -sh "$INSTALLER_APP" | awk '{print $1}')
echo ""
echo "================================================"
echo " Installer built: $INSTALLER_APP"
echo " Size: $INSTALLER_SIZE"
echo "================================================"
echo ""
