#!/usr/bin/env bash
# Generate StonkAgents DMG background image (660x400 PNG).
# Usage: ./scripts/build-dmg-background.sh [output_path]
# Requires: macOS (qlmanage, sips)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUT_PATH="${1:-$PROJECT_ROOT/assets/dmg-background.png}"
WORK_DIR="$(mktemp -d)"

cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT

# Create the background as SVG for crisp rendering
cat > "$WORK_DIR/dmg-bg.svg" << 'SVGEOF'
<?xml version="1.0" encoding="UTF-8"?>
<svg viewBox="0 0 660 400" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="#080a0f"/>
      <stop offset="50%" stop-color="#0f1118"/>
      <stop offset="100%" stop-color="#080a0f"/>
    </linearGradient>
    <radialGradient id="glow" cx="50%" cy="40%" r="40%">
      <stop offset="0%" stop-color="#00FF00" stop-opacity="0.04"/>
      <stop offset="100%" stop-color="#00FF00" stop-opacity="0"/>
    </radialGradient>
  </defs>

  <!-- Background gradient -->
  <rect width="660" height="400" fill="url(#bg)"/>

  <!-- Subtle center glow -->
  <rect width="660" height="400" fill="url(#glow)"/>

  <!-- Top glow line -->
  <line x1="80" y1="0" x2="580" y2="0" stroke="#00FF00" stroke-width="1" opacity="0.3"/>

  <!-- Bottom glow line -->
  <line x1="80" y1="400" x2="580" y2="400" stroke="#00FF00" stroke-width="1" opacity="0.3"/>

  <!-- Drag arrow hint (subtle) -->
  <text x="330" y="205" text-anchor="middle" fill="#222222" font-family="SF Mono, Courier New, monospace" font-size="28" letter-spacing="12">→</text>

  <!-- Brand text -->
  <text x="330" y="355" text-anchor="middle" fill="#00FF00" font-family="SF Mono, Fira Code, Courier New, monospace" font-size="13" letter-spacing="4" opacity="0.5">STONKAGENTS</text>

  <!-- Tagline -->
  <text x="330" y="375" text-anchor="middle" fill="#555555" font-family="SF Mono, Fira Code, Courier New, monospace" font-size="9" letter-spacing="2">every agent deserves a network</text>
</svg>
SVGEOF

echo "Rendering DMG background..."

# Render SVG to PNG via qlmanage
qlmanage -t -s 660 -o "$WORK_DIR" "$WORK_DIR/dmg-bg.svg" 2>/dev/null
RENDERED="$WORK_DIR/dmg-bg.svg.png"

if [[ ! -f "$RENDERED" ]]; then
  echo "Error: qlmanage failed to render background SVG." >&2
  exit 1
fi

# Ensure exact 660x400
sips -z 400 660 "$RENDERED" --out "$OUT_PATH" >/dev/null 2>&1

echo "DMG background created: $OUT_PATH ($(du -h "$OUT_PATH" | awk '{print $1}'))"
