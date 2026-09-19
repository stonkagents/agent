#!/usr/bin/env bash
# StonkAgents macOS Installer v2 — Spotify-style Swift UI installer
# Builds: Go daemon → StonkAgents.app payload → Swift installer → embeds payload
# Output: dist/Install StonkAgents.app/
#
# Usage: ./scripts/build-installer-v2.sh [VERSION]
# Example: ./scripts/build-installer-v2.sh 0.3.0
#
# NOTE: This is separate from the v1 drag-and-drop DMG builder.
#       Does NOT modify any Windows build files.

set -euo pipefail

VERSION="${1:-0.3.0}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
INSTALLER_SRC="$PROJECT_ROOT/installer/macos"
DIST_DIR="$PROJECT_ROOT/dist"
STAGE_DIR="$(mktemp -d)"
INSTALLER_APP="$DIST_DIR/Install StonkAgents.app"

# Load .env for build-time config (e.g. STONKAGENTS_TRACKER_URL)
if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$PROJECT_ROOT/.env"
  set +a
fi

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

build_arch() {
    local goarch="$1" suffix="$2"
    echo "  Building stonkagents for darwin/$goarch..."
    cd "$PROJECT_ROOT"
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags="-s -w" -o "$STAGE_DIR/stonkagents-$suffix" ./cmd/daemon/main.go
    echo "  Building genkeys for darwin/$goarch..."
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags="-s -w" -o "$STAGE_DIR/genkeys-$suffix" ./cmd/genkeys/main.go
    echo "  Building stonkagents-controller for darwin/$goarch..."
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags="-s -w" -o "$STAGE_DIR/stonkagents-controller-$suffix" ./cmd/controller/main.go
}

build_arch arm64 arm64
build_arch amd64 amd64

echo "  Creating universal binaries..."
PAYLOAD_RESOURCES="$DIST_DIR/StonkAgents.app/Contents/Resources"
lipo -create -output "$PAYLOAD_RESOURCES/stonkagents" "$STAGE_DIR/stonkagents-arm64" "$STAGE_DIR/stonkagents-amd64"
lipo -create -output "$PAYLOAD_RESOURCES/genkeys" "$STAGE_DIR/genkeys-arm64" "$STAGE_DIR/genkeys-amd64"
lipo -create -output "$PAYLOAD_RESOURCES/stonkagents-controller" "$STAGE_DIR/stonkagents-controller-arm64" "$STAGE_DIR/stonkagents-controller-amd64"
chmod 755 "$PAYLOAD_RESOURCES/stonkagents" "$PAYLOAD_RESOURCES/genkeys" "$PAYLOAD_RESOURCES/stonkagents-controller"

echo "  stonkagents: $(du -h "$PAYLOAD_RESOURCES/stonkagents" | awk '{print $1}') (universal)"
echo "  genkeys: $(du -h "$PAYLOAD_RESOURCES/genkeys" | awk '{print $1}') (universal)"
echo "  controller: $(du -h "$PAYLOAD_RESOURCES/stonkagents-controller" | awk '{print $1}') (universal)"

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
