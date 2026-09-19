#!/usr/bin/env node
// Generate the StonkAgents Burn sidebar (wix-sidebar.png), 165x400.
//
// This is the maintained generator (make-sidebar.py is the earlier Python/PIL
// version, kept for reference; Python is not installed on the build box).
//
// The size matches the theme exactly: StonkAgentsSidebarTheme.xml draws
// logoside.png with an ImageControl of Width="165" Height="400", and thmutil
// stretches the bitmap to the control, so any other size looks squeezed.
//
// Composition (same as the Python version): dark navy gradient, the mascot
// centred with margins, a thin muted rule, the wordmark below it in the site's
// the site's green (#00FF00). Rendered at 4x and downscaled once
// so text and logo edges are crisp.
//
// Dependency: @napi-rs/canvas. It is deliberately NOT in this repo's
// package.json (this repo is Go); the script loads it from a sibling checkout
// (stonkagents CLI/node_modules) or from $CANVAS_DIR. Run:
//
//   node stonkagents-windows-assets/wix-bitmaps/make-sidebar.mjs
//
import { createRequire } from "node:module";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const ASSETS = path.resolve(here, "..");
const require = createRequire(import.meta.url);

function loadCanvas() {
  const candidates = [
    process.env.CANVAS_DIR,
    path.resolve(ASSETS, "../../stonkagents CLI/node_modules/@napi-rs/canvas"),
    "@napi-rs/canvas",
  ].filter(Boolean);
  for (const c of candidates) {
    try {
      return require(c);
    } catch {
      // try the next location
    }
  }
  throw new Error(
    "@napi-rs/canvas not found. Set CANVAS_DIR to a checkout's node_modules/@napi-rs/canvas " +
      "(for example D:\\Work\\StonkAgents\\stonkagents CLI\\node_modules\\@napi-rs\\canvas)."
  );
}

const { createCanvas, loadImage, GlobalFonts } = loadCanvas();

// Final output size: must equal the theme's ImageControl size.
const W = 165;
const H = 400;
// Render scale (4x = 660x1600), downscaled once at the end.
const SCALE = 4;
const WS = W * SCALE;
const HS = H * SCALE;

// Colours: navy gradient, orange mascot body, theme green wordmark.
const BG_TOP = [10, 13, 20];
const BG_BOTTOM = [4, 5, 9];
const GREEN = "#00FF00"; // the site's accent green (frontend-apps --color-accent-green)
const RULE = "rgba(0, 255, 0, 0.45)"; // muted version of the same green

const srcCandidates = ["icons/stonkagents-1024x1024.png", "icons/stonkagents-512x512.png"];
const SRC_LOGO = srcCandidates.map((p) => path.join(ASSETS, p)).find((p) => existsSync(p));
if (!SRC_LOGO) throw new Error("source logo not found under " + path.join(ASSETS, "icons"));
const OUT = path.join(ASSETS, "wix-bitmaps/wix-sidebar.png");

function lerp(a, b, t) {
  return a + (b - a) * t;
}

// Sample the background gradient at a vertical fraction (0 = top, 1 = bottom).
function bgColorAt(frac) {
  return BG_TOP.map((c, i) => Math.round(lerp(c, BG_BOTTOM[i], frac)));
}

function rgbToHsv(r, g, b) {
  r /= 255; g /= 255; b /= 255;
  const mx = Math.max(r, g, b);
  const mn = Math.min(r, g, b);
  const d = mx - mn;
  let h = 0;
  if (d !== 0) {
    if (mx === r) h = ((g - b) / d) % 6;
    else if (mx === g) h = (b - r) / d + 2;
    else h = (r - g) / d + 4;
    h /= 6;
    if (h < 0) h += 1;
  }
  return [h, mx === 0 ? 0 : d / mx, mx];
}

function hsvToRgb(h, s, v) {
  const i = Math.floor(h * 6);
  const f = h * 6 - i;
  const p = v * (1 - s);
  const q = v * (1 - f * s);
  const t = v * (1 - (1 - f) * s);
  let r, g, b;
  switch (i % 6) {
    case 0: [r, g, b] = [v, t, p]; break;
    case 1: [r, g, b] = [q, v, p]; break;
    case 2: [r, g, b] = [p, v, t]; break;
    case 3: [r, g, b] = [p, q, v]; break;
    case 4: [r, g, b] = [t, p, v]; break;
    default: [r, g, b] = [v, p, q]; break;
  }
  return [Math.round(r * 255), Math.round(g * 255), Math.round(b * 255)];
}

// Recolour the mascot's red body to orange and replace the source icon's
// black circle backdrop (plus the green halo ring around it) with the sidebar
// background colour, so the mascot's anti-aliased edges blend into the canvas
// instead of showing a cut-out circle. Returns an opaque canvas.
async function prepareLogo(srcPath, bgColor) {
  const img = await loadImage(srcPath);
  const w = img.width;
  const h = img.height;
  const cv = createCanvas(w, h);
  const ctx = cv.getContext("2d");
  ctx.drawImage(img, 0, 0);
  const data = ctx.getImageData(0, 0, w, h);
  const px = data.data;
  const [br, bgG, bb] = bgColor;

  // The character only reaches ~0.40 of the radius from the centre; everything
  // beyond is backdrop, halo ring and its fringe. Feather between inner and
  // outer so no hard edge appears.
  const cx = w / 2;
  const cy = h / 2;
  const innerRadius = Math.min(w, h) * 0.41;
  const outerRadius = Math.min(w, h) * 0.44;

  for (let y = 0; y < h; y++) {
    const dy = y - cy;
    for (let x = 0; x < w; x++) {
      const dx = x - cx;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const i = (y * w + x) * 4;
      const r = px[i];
      const g = px[i + 1];
      const b = px[i + 2];

      if (dist >= outerRadius) {
        px[i] = br; px[i + 1] = bgG; px[i + 2] = bb; px[i + 3] = 255;
        continue;
      }
      if (dist > innerRadius) {
        const t = (dist - innerRadius) / (outerRadius - innerRadius);
        px[i] = Math.round(lerp(r, br, t));
        px[i + 1] = Math.round(lerp(g, bgG, t));
        px[i + 2] = Math.round(lerp(b, bb, t));
        px[i + 3] = 255;
        continue;
      }
      // Backdrop absorption: dark, desaturated pixels are the black backdrop or
      // its anti-aliased boundary; make them the sidebar background.
      const mx = Math.max(r, g, b);
      const mn = Math.min(r, g, b);
      const sat = mx === 0 ? 0 : (mx - mn) / mx;
      if (mx < 110 && sat < 0.3) {
        px[i] = br; px[i + 1] = bgG; px[i + 2] = bb; px[i + 3] = 255;
        continue;
      }
      // The mascot keeps the site's colours (red body #FF4D4D, green glasses #00FF00).
      px[i + 3] = 255;
    }
  }
  ctx.putImageData(data, 0, 0);
  return cv;
}

// Monospace bold wordmark: Consolas Bold on Windows (what the other WiX
// bitmaps use), Cascadia or Courier New Bold as fallbacks.
function registerWordmarkFont() {
  const winFonts = path.join(process.env.WINDIR || "C:\\Windows", "Fonts");
  const candidates = [
    [path.join(winFonts, "consolab.ttf"), "SidebarMono"],
    [path.join(winFonts, "CascadiaCode.ttf"), "SidebarMono"],
    [path.join(winFonts, "courbd.ttf"), "SidebarMono"],
    ["/System/Library/Fonts/Menlo.ttc", "SidebarMono"],
  ];
  for (const [file, family] of candidates) {
    if (existsSync(file) && GlobalFonts.registerFromPath(file, family)) return family;
  }
  return "monospace";
}

async function main() {
  const canvas = createCanvas(WS, HS);
  const ctx = canvas.getContext("2d");

  // 1. Vertical gradient background.
  const grad = ctx.createLinearGradient(0, 0, 0, HS);
  grad.addColorStop(0, `rgb(${BG_TOP.join(",")})`);
  grad.addColorStop(1, `rgb(${BG_BOTTOM.join(",")})`);
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, WS, HS);

  // 2. Mascot placement (final pixels): 112 wide on a 165 canvas leaves
  // ~26px margins either side; vertically it sits in the upper half.
  const markSizeFinal = 112;
  const markSize = markSizeFinal * SCALE;
  const markYFinal = 104;
  const markY = markYFinal * SCALE;
  const markX = Math.round((WS - markSize) / 2);

  // The backdrop colour is the gradient sampled at the mascot's centre so the
  // replaced circle is invisible against the canvas.
  const backdrop = bgColorAt((markY + markSize / 2) / HS);

  // 3. Prepare the logo at its native size, then draw it scaled down (the
  // canvas uses high quality resampling for the 1024 -> 448 step).
  const logo = await prepareLogo(SRC_LOGO, backdrop);
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(logo, markX, markY, markSize, markSize);

  // 4. Wordmark in the theme green, centred under the mascot.
  const family = registerWordmarkFont();
  const titleSizeFinal = 22;
  ctx.font = `bold ${titleSizeFinal * SCALE}px "${family}"`;
  ctx.fillStyle = GREEN;
  ctx.textAlign = "center";
  ctx.textBaseline = "alphabetic";
  const title = "StonkAgents";
  const metrics = ctx.measureText(title);
  const ascent = metrics.actualBoundingBoxAscent;
  const titleTop = markY + markSize + 28 * SCALE;
  ctx.fillText(title, WS / 2, titleTop + ascent);

  // 5. Thin rule above the wordmark, muted green: exactly one final pixel
  // tall, on a final pixel boundary so the downscale does not smear it.
  const lineW = 52 * SCALE;
  const ly = titleTop - 14 * SCALE;
  ctx.fillStyle = RULE;
  ctx.fillRect(Math.round((WS - lineW) / 2), ly, lineW, SCALE);

  // 6. Downscale once to the final size.
  const final = createCanvas(W, H);
  const fctx = final.getContext("2d");
  fctx.imageSmoothingEnabled = true;
  fctx.imageSmoothingQuality = "high";
  fctx.drawImage(canvas, 0, 0, W, H);

  const { writeFile } = await import("node:fs/promises");
  await writeFile(OUT, await final.encode("png"));
  console.log(`wrote ${OUT} (${W}x${H}), source ${path.basename(SRC_LOGO)}, font ${family}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
