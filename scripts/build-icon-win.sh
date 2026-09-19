#!/usr/bin/env bash
# Build StonkAgents.ico for Windows installer from stonkagents-icon.svg.
# Uses macOS qlmanage + sips for PNG generation, Python Pillow for ICO packing.
# Usage: ./scripts/build-icon-win.sh [output_path]
# Prereqs: Python 3 with Pillow (pip3 install Pillow)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SVG_PATH="$PROJECT_ROOT/assets/stonkagents-icon.svg"
OUT_PATH="${1:-$PROJECT_ROOT/assets/StonkAgents.ico}"
WORK_DIR="$(mktemp -d)"

cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT

if [[ ! -f "$SVG_PATH" ]]; then
  echo "Error: $SVG_PATH not found. Run build-icon.sh first or create the SVG." >&2
  exit 1
fi

if ! python3 -c "from PIL import Image" 2>/dev/null; then
  echo "Error: Python Pillow not found. Install with: pip3 install Pillow" >&2
  exit 1
fi

echo "Building StonkAgents.ico..."

# Render SVG to large PNG via qlmanage
qlmanage -t -s 512 -o "$WORK_DIR" "$SVG_PATH" 2>/dev/null
SRC_PNG="$WORK_DIR/stonkagents-icon.svg.png"
if [[ ! -f "$SRC_PNG" ]]; then
  echo "Error: qlmanage failed to render SVG." >&2
  exit 1
fi

# Generate required ICO sizes
SIZES=(16 32 48 64 128 256)
PNG_PATHS=""
for size in "${SIZES[@]}"; do
  OUT_PNG="$WORK_DIR/icon-${size}.png"
  sips -z "$size" "$size" "$SRC_PNG" --out "$OUT_PNG" >/dev/null 2>&1
  PNG_PATHS="$PNG_PATHS $OUT_PNG"
done

# Pack into ICO using Pillow
python3 << PYEOF
from PIL import Image

sizes = [16, 32, 48, 64, 128, 256]
images = []
for s in sizes:
    img = Image.open(f"$WORK_DIR/icon-{s}.png")
    img = img.convert("RGBA")
    img = img.resize((s, s), Image.LANCZOS)
    images.append(img)

# Save as ICO — the 256x256 image is stored as PNG inside the ICO (standard)
# Pillow requires the base image to be the largest, append smaller ones
largest = images[-1]  # 256x256
rest = images[:-1]    # 16..128
largest.save(
    "$OUT_PATH",
    format="ICO",
    append_images=rest,
    sizes=[(s, s) for s in sizes],
)
print(f"ICO created: $OUT_PATH")
PYEOF

echo "Done: $OUT_PATH ($(du -h "$OUT_PATH" | awk '{print $1}'))"
