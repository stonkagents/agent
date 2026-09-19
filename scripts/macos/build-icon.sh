#!/usr/bin/env bash
# Build StonkAgents.icns from the mascot SVG.
# Usage: ./scripts/build-icon.sh [output_path]
# Requires: macOS (qlmanage, sips, iconutil)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SVG_PATH="$PROJECT_ROOT/assets/stonkagents-icon.svg"
OUT_ICNS="${1:-$PROJECT_ROOT/assets/StonkAgents.icns}"
WORK_DIR="$(mktemp -d)"
ICONSET_DIR="$WORK_DIR/StonkAgents.iconset"

cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT

if [[ ! -f "$SVG_PATH" ]]; then
  echo "Error: SVG not found: $SVG_PATH" >&2
  exit 1
fi

echo "Rendering SVG to PNG..."

# Render SVG to 1024x1024 PNG using qlmanage (built-in macOS)
qlmanage -t -s 1024 -o "$WORK_DIR" "$SVG_PATH" 2>/dev/null
RENDERED="$WORK_DIR/stonkagents-icon.svg.png"

if [[ ! -f "$RENDERED" ]]; then
  echo "Error: qlmanage failed to render SVG." >&2
  exit 1
fi

# Ensure exact 1024x1024
MASTER_PNG="$WORK_DIR/master-1024.png"
sips -z 1024 1024 "$RENDERED" --out "$MASTER_PNG" >/dev/null 2>&1

echo "Generating iconset sizes..."
mkdir -p "$ICONSET_DIR"

# Standard sizes
for SIZE in 16 32 128 256 512; do
  sips -z $SIZE $SIZE "$MASTER_PNG" --out "$ICONSET_DIR/icon_${SIZE}x${SIZE}.png" >/dev/null 2>&1
done

# Retina @2x variants
sips -z 32 32 "$MASTER_PNG" --out "$ICONSET_DIR/icon_16x16@2x.png" >/dev/null 2>&1
sips -z 64 64 "$MASTER_PNG" --out "$ICONSET_DIR/icon_32x32@2x.png" >/dev/null 2>&1
sips -z 256 256 "$MASTER_PNG" --out "$ICONSET_DIR/icon_128x128@2x.png" >/dev/null 2>&1
sips -z 512 512 "$MASTER_PNG" --out "$ICONSET_DIR/icon_256x256@2x.png" >/dev/null 2>&1
sips -z 1024 1024 "$MASTER_PNG" --out "$ICONSET_DIR/icon_512x512@2x.png" >/dev/null 2>&1

echo "Creating .icns..."
iconutil -c icns "$ICONSET_DIR" -o "$OUT_ICNS"

echo "Icon created: $OUT_ICNS ($(du -h "$OUT_ICNS" | awk '{print $1}'))"
