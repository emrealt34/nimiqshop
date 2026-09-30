// build-flag-geometry.mjs — measure every shipped flag and generate
// src/lib/flagGeometry.ts: the flag's aspect ratio plus the point inside the
// flag that FlagHex must land on the hexagon's centre.
//
// Run:  node scripts/build-flag-geometry.mjs     (needs the preview on :4600)
//
// WHY A FOCAL POINT AT ALL
//
// Flags are not all 3:2 and the hexagon is 20:18 (1.11:1), so filling the shape
// always crops something: a 3:2 flag loses ~16% off each side, a 2:1 flag ~24%,
// Qatar's 25:6 flag ~75%. Centring that crop is only right when the design is
// centred — and often it is not:
//
//   tr  the crescent and star sit at x = 0.388 of the flag. Crop both sides
//       equally and the emblem ends up visibly left of the hexagon's middle.
//   us  the canton is in the top-left corner of a 1.9:1 flag; a centred crop
//       cuts it off and leaves anonymous stripes.
//   qa  the whole design is a serrated band at the hoist of a 4.17:1 flag; a
//       centred crop shows blank maroon.
//
// So each flag carries a focal point, and the image is placed so that point
// lands on the hexagon's centre — the silhouette is always completely covered
// by flag artwork (the window is clamped so it can never slide off the flag).
//
// HOW THE FOCAL POINT IS CHOSEN
//
//   1. Plain field + localised design (Turkey, Japan, Switzerland, Qatar):
//      centre the design's bounding box. If the design is wider than the window
//      can show, anchor to whichever edge it hugs — that is the hoist for Qatar,
//      which is the only sensible reading of a flag that is 4× wider than tall.
//   2. Anything else (tricolours, stripes, hoist devices on busy flags): use the
//      centroid of the image gradient along that axis. Gradient energy sits on
//      edges, and edges are exactly what looks broken when they get cropped, so
//      the centroid pulls the window toward the canton / band / device.
//   3. No usable structure on that axis (horizontal stripes have no vertical
//      edges): leave it centred.
//
// Every result is clamped so the visible window stays inside the artwork.
import { chromium } from 'playwright';
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';

const DIR = new URL('../public/img/flags', import.meta.url).pathname;
const OUT = new URL('../src/lib/flagGeometry.ts', import.meta.url).pathname;
const REPORT = new URL('../flag-focus-report.json', import.meta.url).pathname;
const BASE = process.env.BASE || 'http://127.0.0.1:4600';

// hexagon + the reference image box, in user units (viewBox 0 0 20 18)
const HEX_W = 20, HEX_H = 18, IMG_W = 25.92, IMG_H = 19.44;

function headerRatio(code) {
  const src = readFileSync(`${DIR}/${code}.svg`, 'utf8').slice(0, 600);
  const vb = src.match(/viewBox\s*=\s*["']([^"']+)["']/);
  if (vb) {
    const b = vb[1].trim().split(/[\s,]+/).map(Number);
    if (b.length === 4 && b[3]) return b[2] / b[3];
  }
  const w = src.match(/\bwidth\s*=\s*["']([\d.]+)/);
  const h = src.match(/\bheight\s*=\s*["']([\d.]+)/);
  if (w && h && Number(h[1])) return Number(w[1]) / Number(h[1]);
  return 1.5;
}

/**
 * Flags the automatic rules get wrong, and why. Each entry gets the clamped
 * window bounds so it can anchor to an edge without hard-coding numbers.
 * Every entry here is a judgement call that a measurement cannot make:
 *
 *  au, nz, fj — British ensigns: a Union Flag canton at the hoist plus stars
 *      scattered across the rest of the field. The bounding box therefore
 *      covers nearly the whole flag, so "design-centred" would show the empty
 *      middle and crop the canton. The canton is the recognisable part.
 *  kz — the sun and eagle are centred on the flag; the vertical ornament at the
 *      hoist drags the bounding box left, and anchoring to it would crop the
 *      emblem in half. Centre the emblem instead.
 */
const OVERRIDES = {
  au: ({ loX, loY }) => ({ x: loX, y: loY }),
  nz: ({ loX, loY }) => ({ x: loX, y: loY }),
  fj: ({ loX, loY }) => ({ x: loX, y: loY }),
  kz: () => ({ x: 0.5, y: 0.5 }),
  // Benin: a green band covers the hoist two fifths, then yellow over red. The
  // band is the dominant colour, so the measurement only sees the other two
  // bands as "design" and would centre on them, cutting the green off.
  bj: ({ loX }) => ({ x: loX, y: 0.5 }),
};

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));
const snap = (v) => (Math.abs(v - 0.5) < 0.012 ? 0.5 : v);

(async () => {
  const codes = readdirSync(DIR).filter((f) => f.endsWith('.svg')).map((f) => f.replace('.svg', '')).sort();
  const browser = await chromium.launch();
  const page = await browser.newPage();
  await page.goto(BASE + '/');

  const measured = await page.evaluate(async (codes) => {
    const out = {};
    const W = 240, H = 180, M = 3; // margin: the outer edge is not real structure
    for (const cc of codes) {
      const img = new Image();
      const ok = await new Promise((r) => { img.onload = () => r(true); img.onerror = () => r(false); img.src = `/img/flags/${cc}.svg`; });
      if (!ok) { out[cc] = null; continue; }
      const c = document.createElement('canvas');
      c.width = W; c.height = H;
      const g = c.getContext('2d', { willReadFrequently: true });
      g.clearRect(0, 0, W, H);
      g.drawImage(img, 0, 0, W, H);
      const d = g.getImageData(0, 0, W, H).data;
      const lum = new Float32Array(W * H);
      for (let i = 0, p = 0; i < d.length; i += 4, p++) lum[p] = 0.299 * d[i] + 0.587 * d[i + 1] + 0.114 * d[i + 2];

      const freq = new Map();
      for (let y = M; y < H - M; y++) for (let x = M; x < W - M; x++) {
        const i = (y * W + x) * 4;
        if (d[i + 3] < 8) continue;
        const k = `${d[i]},${d[i + 1]},${d[i + 2]}`;
        freq.set(k, (freq.get(k) || 0) + 1);
      }
      let bg = null, best = -1;
      for (const [k, n] of freq) if (n > best) { best = n; bg = k; }
      const [br, bgc, bb] = bg.split(',').map(Number);
      let minX = W, maxX = -1, minY = H, maxY = -1, n = 0;
      for (let y = M; y < H - M; y++) for (let x = M; x < W - M; x++) {
        const i = (y * W + x) * 4;
        if (d[i + 3] < 8) continue;
        const diff = Math.abs(d[i] - br) + Math.abs(d[i + 1] - bgc) + Math.abs(d[i + 2] - bb);
        if (diff < 40) continue;
        if (x < minX) minX = x; if (x > maxX) maxX = x;
        if (y < minY) minY = y; if (y > maxY) maxY = y;
        n++;
      }
      const total = (W - 2 * M) * (H - 2 * M);

      let gxSum = 0, gxW = 0, gySum = 0, gyW = 0;
      for (let y = M; y < H - M; y++) for (let x = M; x < W - M; x++) {
        const p = y * W + x;
        const gx = Math.abs(lum[p + 1] - lum[p - 1]);
        const gy = Math.abs(lum[p + W] - lum[p - W]);
        gxSum += gx * x; gxW += gx;
        gySum += gy * y; gyW += gy;
      }
      let opaque = 0;
      for (let p = 0; p < W * H; p++) if (d[p * 4 + 3] > 8) opaque++;
      out[cc] = {
        // the box the browser will actually draw, not the viewBox: flagcdn's
        // qa.svg declares width/height 11:28 (the real ratio of Qatar's flag)
        // but a viewBox of 75:18, and the artwork fills the declared box.
        boxR: img.naturalWidth && img.naturalHeight ? img.naturalWidth / img.naturalHeight : null,
        opaqueShare: +(opaque / (W * H)).toFixed(3),
        gxC: gxW > 0 ? gxSum / gxW / W : 0.5,
        gyC: gyW > 0 ? gySum / gyW / H : 0.5,
        gxE: gxW, gyE: gyW,
        bboxCx: maxX >= 0 ? (minX + maxX) / 2 / W : 0.5,
        bboxCy: maxY >= 0 ? (minY + maxY) / 2 / H : 0.5,
        bboxW: maxX >= 0 ? (maxX - minX) / W : 1,
        bboxH: maxY >= 0 ? (maxY - minY) / H : 1,
        contentShare: +(n / total).toFixed(3),
        bgShare: +(best / total).toFixed(3),
      };
    }
    return out;
  }, codes);
  await browser.close();

  const rows = [];
  for (const cc of codes) {
    const m = measured[cc];
    const r = +(m && m.boxR ? m.boxR : headerRatio(cc)).toFixed(4);
    // how much of the flag the hexagon window shows, as a fraction of the flag
    const scale = Math.max(IMG_W / r, IMG_H);
    const rw = r * scale, rh = scale;
    const winX = HEX_W / rw, winY = HEX_H / rh;
    const loX = winX / 2, hiX = 1 - loX;
    const loY = winY / 2, hiY = 1 - loY;

    if (!m) { rows.push({ cc, r, x: 0.5, y: 0.5, why: 'unmeasurable' }); continue; }
    // Nepal's pennants leave the corners of the flag's own box empty, so the
    // hexagon would show the page through them: back it with white, the way
    // Nepal's flag is conventionally drawn.
    const needsBacking = m.opaqueShare < 0.995;
    const bg = needsBacking ? '#ffffff' : null;

    // A design that hugs one edge and is much narrower than the flag is a hoist
    // device (Qatar's serrated band, Bahrain's) even though it is full height —
    // the 0.9 height cap below would otherwise reject it.
    const hoistDevice = m.bboxW <= 0.55 && Math.abs(m.bboxCx - 0.5) > 0.12;

    // A plain field with a design on it: the bbox IS the design. The 0.9 cap on
    // bbox height matters — a vertical tricolour's "design" is its full height,
    // and treating that as an emblem to be centred (or anchored) is nonsense.
    const design = hoistDevice ||
      (m.bgShare >= 0.5 && m.contentShare <= 0.45 && m.bboxW <= 0.9 && m.bboxH <= 0.9);
    let fx, fy, why;
    if (OVERRIDES[cc]) {
      ({ x: fx, y: fy } = OVERRIDES[cc]({ loX, hiX, loY, hiY }));
      why = 'override';
    } else if (design) {
      // Centring is right when the design is already centred even if it does
      // not quite fit: cutting both edges of a centred emblem looks deliberate,
      // cutting one edge of it looks like a mistake. Only anchor when the
      // design genuinely hugs one edge AND is too wide to show whole.
      const cxCentred = Math.abs(m.bboxCx - 0.5) < 0.08;
      const cyCentred = Math.abs(m.bboxCy - 0.5) < 0.08;
      const fitX = m.bboxW <= winX * 1.15;
      const fitY = m.bboxH <= winY * 1.15;
      fx = cxCentred || fitX ? m.bboxCx : (m.bboxCx < 0.5 ? loX : hiX);
      fy = cyCentred || fitY ? m.bboxCy : (m.bboxCy < 0.5 ? loY : hiY);
      why = cxCentred || fitX ? 'design-centred' : 'design-anchored';
    } else {
      // Structure on this axis: some energy, and it is not sitting in the
      // middle. The energy test alone is not enough — a horizontally striped
      // flag has huge gradient energy in y and almost none in x, but a canton
      // still shows up clearly in x even though it is a minority of the total.
      const gxUseful = m.gxE > 0.05 * (m.gxE + m.gyE) && Math.abs(m.gxC - 0.5) > 0.04;
      const gyUseful = m.gyE > 0.05 * (m.gxE + m.gyE) && Math.abs(m.gyC - 0.5) > 0.04;
      fx = gxUseful ? m.gxC : 0.5;
      fy = gyUseful ? m.gyC : 0.5;
      why = gxUseful && gyUseful ? 'gradient' : gxUseful ? 'gradient-x' : gyUseful ? 'gradient-y' : 'centre';
    }
    fx = +snap(clamp(fx, loX, hiX)).toFixed(4);
    fy = +snap(clamp(fy, loY, hiY)).toFixed(4);

    // how much of the design actually survives the crop (QA on the output)
    const vis = (c, size, win) => {
      const a0 = c - size / 2, a1 = c + size / 2;
      const b0 = fx * 1 - 0, _unused = b0; // placeholder for clarity
      const w0 = fx - win / 2, w1 = fx + win / 2;
      const ov = Math.max(0, Math.min(a1, w1) - Math.max(a0, w0));
      return size > 0 ? +(ov / size).toFixed(3) : 1;
    };
    rows.push({
      cc, r, x: fx, y: fy, why, bg,
      designVisible: design ? Math.min(vis(m.bboxCx, m.bboxW, winX), vis(m.bboxCy, m.bboxH, winY)) : 1,
      winX: +winX.toFixed(3), winY: +winY.toFixed(3),
      raw: { gxC: +m.gxC.toFixed(3), gyC: +m.gyC.toFixed(3), bboxCx: +m.bboxCx.toFixed(3), bboxCy: +m.bboxCy.toFixed(3), bboxW: +m.bboxW.toFixed(3), bboxH: +m.bboxH.toFixed(3), bg: m.bgShare },
    });
  }

  // ---- emit flagGeometry.ts ---------------------------------------------
  const lines = [
    '/**',
    ' * flagGeometry.ts — GENERATED by scripts/build-flag-geometry.mjs. Do not',
    ' * hand-edit; re-run the generator (it writes this file and a JSON report).',
    ' *',
    ' * `r` — the flag\'s width/height ratio (default 1.5 when absent).',
    ' * `bg` — a backing colour painted under the flag, for the one flag whose own',
    ' *      artwork does not fill its box (the pennants of Nepal).',
    ' * `x`,`y` — the focal point in flag coordinates (0–1) that FlagHex centres',
    ' * in the hexagon (default 0.5/0.5 when absent). Only entries that differ',
    ' * from the defaults are emitted, so a missing code is never an error.',
    ' *',
    ' * Regenerate after adding flags:',
    ' *   npm run flags:fetch && node scripts/build-flag-geometry.mjs',
    ' */',
    'export type FlagGeometry = { r?: number; x?: number; y?: number; bg?: string };',
    '',
    'export const DEFAULT_FLAG_RATIO = 1.5;',
    '',
    'export const FLAG_GEOMETRY: Record<string, FlagGeometry> = {',
  ];
  for (const row of rows) {
    const parts = [];
    if (Math.abs(row.r - 1.5) > 0.005) parts.push(`r: ${row.r}`);
    if (Math.abs(row.x - 0.5) > 0.004) parts.push(`x: ${row.x}`);
    if (Math.abs(row.y - 0.5) > 0.004) parts.push(`y: ${row.y}`);
    if (row.bg) parts.push(`bg: '${row.bg}'`);
    if (parts.length) lines.push(`  ${row.cc}: { ${parts.join(', ')} },`);
  }
  lines.push('};', '');
  writeFileSync(OUT, lines.join('\n'), 'utf8');
  // the per-flag report is a dev aid: only written when asked for
  if (process.env.REPORT) writeFileSync(REPORT, JSON.stringify(rows, null, 1), 'utf8');

  const shifted = rows.filter((r) => Math.abs(r.x - 0.5) > 0.004 || Math.abs(r.y - 0.5) > 0.004);
  const cut = rows.filter((r) => r.designVisible < 0.75);
  console.log(`flags=${rows.length} offset=${shifted.length} designCut=${cut.length} → ${OUT}`);
  if (cut.length) console.log('design more than 25% cut:', cut.map((c) => `${c.cc}(${c.designVisible})`).join(' '));
})();
