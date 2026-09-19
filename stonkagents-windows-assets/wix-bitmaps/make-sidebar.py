#!/usr/bin/env python3
"""Generate a crisp StonkAgents Burn sidebar (234x392). SUPERSEDED.

Kept for reference only: the maintained generator is make-sidebar.mjs
(Node + @napi-rs/canvas), which renders at the theme's 165x400 with the
green wordmark. This version produces the old 234x392 orange layout and
is not run on the Windows build box (no Python there).

Key quality wins vs the previous version:

  * Supersample: render at 4x (936x1568), then downscale once with LANCZOS
    for sub-pixel-accurate anti-aliasing on text and logo edges.
  * No hard alpha threshold: the old script killed every pixel with
    alpha < 120, which destroyed the smooth edges that LANCZOS produced.
    Instead we replace the source logo's black-circle background with
    our navy sidebar background, so the character's anti-aliased edges
    blend naturally into the bg (no visible cutout).
  * Menlo Bold for the wordmark, matching the macOS DMG's monospace
    design style.
"""
from PIL import Image, ImageDraw, ImageFont
from pathlib import Path
import colorsys

# Final output size
W, H = 234, 392
# Render scale (higher = smoother, slower). 4x = 936x1568.
SCALE = 4
WS, HS = W * SCALE, H * SCALE

# Colors (navy bg, orange mark/text)
BG_TOP = (10, 13, 20, 255)
BG_BOTTOM = (4, 5, 9, 255)
ORANGE = (255, 138, 61, 255)

# stonkagents-windows-assets/ (this script lives in its wix-bitmaps/ subdir)
ASSETS = Path(__file__).resolve().parent.parent
SRC_LOGO = ASSETS / "icons/stonkagents-1024x1024.png"  # highest-res source
# Fall back to 512 if 1024 missing
if not SRC_LOGO.exists():
    SRC_LOGO = ASSETS / "icons/stonkagents-512x512.png"
OUT = ASSETS / "wix-bitmaps/wix-sidebar.png"


def vgradient(size, top, bot):
    w, h = size
    img = Image.new("RGBA", size, top)
    px = img.load()
    for y in range(h):
        t = y / max(h - 1, 1)
        r = int(top[0] + (bot[0] - top[0]) * t)
        g = int(top[1] + (bot[1] - top[1]) * t)
        b = int(top[2] + (bot[2] - top[2]) * t)
        for x in range(w):
            px[x, y] = (r, g, b, 255)
    return img


def bg_color_at_y(y_frac):
    """Sample the gradient at a given vertical fraction."""
    r = int(BG_TOP[0] + (BG_BOTTOM[0] - BG_TOP[0]) * y_frac)
    g = int(BG_TOP[1] + (BG_BOTTOM[1] - BG_TOP[1]) * y_frac)
    b = int(BG_TOP[2] + (BG_BOTTOM[2] - BG_TOP[2]) * y_frac)
    return (r, g, b, 255)


def prepare_logo(src_path, bg_color):
    """Recolor the logo's body from red/pink -> orange AND replace its
    black-circle background with `bg_color` (so edges blend instead of
    being hard-cut).

    Returns a fully opaque RGBA image of the same size as the source.
    """
    img = Image.open(src_path).convert("RGBA")
    w, h = img.size
    px = img.load()
    br, bg_, bb, ba = bg_color

    # Radial crop: the monster character only extends to ~radius 0.40 from
    # the center; everything beyond that is the black circle backdrop + the
    # pastel green halo ring baked into the source logo + the anti-aliased
    # green fringe around that ring. Replace the entire outer band with our
    # bg so NONE of those artifacts survive the downscale.
    cx, cy = w / 2.0, h / 2.0
    inner_radius = min(w, h) * 0.41  # keep pixels inside this radius untouched
    outer_radius = min(w, h) * 0.44  # fully bg beyond this
    # Soft feather between inner and outer so no hard edge appears

    for y in range(h):
        dy = y - cy
        for x in range(w):
            dx = x - cx
            dist = (dx * dx + dy * dy) ** 0.5
            r, g, b, a = px[x, y]

            # Fully-outer band: force bg regardless of color
            if dist >= outer_radius:
                px[x, y] = (br, bg_, bb, 255)
                continue
            # Feather band: blend toward bg by distance
            if dist > inner_radius:
                t = (dist - inner_radius) / (outer_radius - inner_radius)
                nr = int(r * (1 - t) + br * t)
                ng = int(g * (1 - t) + bg_ * t)
                nb = int(b * (1 - t) + bb * t)
                px[x, y] = (nr, ng, nb, 255)
                continue
            # Background absorption. We want every pixel that BELONGS to the
            # black backdrop (or its anti-aliased boundary into a colored
            # element) to become our navy bg. The trick: compute saturation
            # — backdrop pixels are pure greyscale (low sat), boundary AA
            # pixels are also pretty desaturated. Character body pixels are
            # highly saturated. Replace anything with low-sat AND moderate
            # darkness with our bg, fully — kills the ghost ring.
            mx = max(r, g, b)
            mn = min(r, g, b)
            sat = 0 if mx == 0 else (mx - mn) / mx
            if mx < 110 and sat < 0.30:
                px[x, y] = (br, bg_, bb, 255)
                continue
            # Recolor red/pink body to orange via HSV hue shift
            hh, ss, vv = colorsys.rgb_to_hsv(r / 255, g / 255, b / 255)
            deg = hh * 360
            if (deg <= 30 or deg >= 330) and ss > 0.25 and vv > 0.2:
                new_h = 0.062  # ~22deg orange
                new_s = min(1.0, ss * 1.05)
                nr, ng, nb = colorsys.hsv_to_rgb(new_h, new_s, vv)
                px[x, y] = (int(nr * 255), int(ng * 255), int(nb * 255), 255)
                continue
            # Everything else (green visor, whatever) keep as-is but force alpha 255
            px[x, y] = (r, g, b, 255)
    return img


def find_font(candidates, size):
    for name in candidates:
        try:
            return ImageFont.truetype(name, size)
        except Exception:
            continue
    return ImageFont.load_default()


def main():
    # 1. Big canvas with smooth vertical gradient
    canvas = vgradient((WS, HS), BG_TOP, BG_BOTTOM)

    # 2. Compute where the logo will land so we can bg-match its backdrop
    mark_size_final = 150  # final output pixels
    mark_size = mark_size_final * SCALE
    mark_y_final = 95
    mark_y = mark_y_final * SCALE
    mark_x = (WS - mark_size) // 2

    # Sample gradient at the vertical center of the logo -> that's the
    # color we replace the source logo's black backdrop with, so the
    # circle edge is invisible against the canvas.
    center_frac = (mark_y + mark_size / 2) / HS
    backdrop = bg_color_at_y(center_frac)

    # 3. Load + recolor + bg-replace the source logo at its native size
    logo_full = prepare_logo(SRC_LOGO, backdrop)
    # 4. Resize to the target mark size (LANCZOS downscale from 1024)
    logo_big = logo_full.resize((mark_size, mark_size), Image.LANCZOS)

    # 5. Paste opaque onto canvas (no alpha math — the logo now has our
    # bg color baked into its border pixels, so edges blend perfectly)
    canvas.paste(logo_big, (mark_x, mark_y))

    # 6. Wordmark — Menlo Bold for the DMG/monospace vibe the Mac uses
    draw = ImageDraw.Draw(canvas)
    title_size_final = 30
    title_size = title_size_final * SCALE
    mono_candidates = [
        "/System/Library/Fonts/Menlo.ttc",          # macOS Menlo (Bold variant picked by font index)
        "/System/Library/Fonts/Monaco.ttf",
        "/System/Library/Fonts/Courier.ttc",
    ]
    # PIL's truetype accepts an index= for TTC; Menlo.ttc index 1 is Bold
    try:
        title_font = ImageFont.truetype("/System/Library/Fonts/Menlo.ttc", title_size, index=1)
    except Exception:
        title_font = find_font(mono_candidates, title_size)

    title = "StonkAgents"
    bbox = draw.textbbox((0, 0), title, font=title_font)
    tw = bbox[2] - bbox[0]
    th = bbox[3] - bbox[1]
    tx = (WS - tw) // 2 - bbox[0]
    ty = mark_y + mark_size + 28 * SCALE
    draw.text((tx, ty), title, fill=ORANGE, font=title_font)

    # 7. Thin divider line above the wordmark (subtle accent)
    line_w = 60 * SCALE
    lx = (WS - line_w) // 2
    ly = ty - 14 * SCALE
    draw.line([(lx, ly), (lx + line_w, ly)], fill=(255, 138, 61, 110), width=2)

    # 8. Downscale the whole thing to final size with LANCZOS once.
    final = canvas.resize((W, H), Image.LANCZOS)

    OUT.parent.mkdir(parents=True, exist_ok=True)
    final.save(OUT, "PNG", optimize=True)
    print(f"wrote {OUT} ({W}x{H}), source {SRC_LOGO.name}")


if __name__ == "__main__":
    main()
