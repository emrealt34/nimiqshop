/**
 * FlagHex.tsx — a country flag clipped to the Nimiq hexagon.
 *
 * WHY A HEXAGON AND NOT A RECTANGLE
 *
 * Every other surface in this shop — the identicons, the hub button, the
 * product art — is built on the Nimiq hexagon. Rectangular 4:3 flags next to
 * them read as stickers from a different design system: the corners fight the
 * rounded containers, and at 16–18 px (the size they actually ship at in the
 * country chips) a rectangle is mostly corner. Clipping the flag to the same
 * silhouette makes a row of countries read as one family of shapes.
 *
 * WHY THE FLAG IS AN <image> INSIDE AN SVG AND NOT A CSS clip-path
 *
 * `clip-path: polygon(…)` on an <img> would work, but it clips in CSS pixels:
 * the mask does not scale with the intrinsic flag art, so every size needs its
 * own polygon and the corners drift sub-pixel at small sizes. Clipping inside
 * the SVG keeps the mask in the same user-space as the artwork (viewBox
 * 0 0 20 18), so one path is exact at 16 px and at 64 px. It also lets the
 * outline be stroked on top of the clip, which is what gives the shape its
 * edge — a CSS mask cannot draw a border around itself.
 *
 * The silhouette is the one used by the Nimiq Hub country picker. The geometry
 * inside it is not: that picker hard-codes a 4:3 flag scaled to cover the
 * hexagon and centred — which crops every flag that is not 4:3 equally on both
 * sides. That looks wrong on a design that is not centred: the Turkish crescent
 * and star sit at 0.388 of the flag, so an even crop leaves them visibly left of
 * the hexagon's middle, and centring a 1.9:1 crop of the US flag cuts the canton
 * off entirely. Each flag therefore carries its own ratio and focal point
 * (src/lib/flagGeometry.ts, generated from the artwork), and the image is placed
 * so that point lands on the hexagon's centre.
 *
 * ASSET RESOLUTION (three stages, each strictly worse than the one before)
 *
 *   1. /img/flags/<cc>.svg — fetched by scripts/fetch-flags.mjs, so the shop
 *      works with no network at all.
 *   2. https://flagcdn.com/<cc>.svg — for a code that was added to
 *      src/lib/countries.ts after the last fetch.
 *   3. The regional-indicator emoji, drawn inside the hexagon so the row keeps
 *      its shape even with no artwork at all.
 *
 * `img-src 'self' data: blob: https:` in the CSP covers stages 1 and 2.
 */
import { useId, useState } from 'react';
import { flag } from '../../lib/format';
import { FLAG_GEOMETRY, DEFAULT_FLAG_RATIO } from '../../lib/flagGeometry';
import { asset } from '../../lib/asset';

/** The hexagon silhouette: 20 × 18 user units, 1.69 corner radius. */
const HEX_PATH =
  'M19.964 8.156 15.758.844A1.69 1.69 0 0014.299 0H5.887c-.6 0-1.156.32-1.456.844L.225 8.156c-.3.523-.3 1.165 0 1.688l4.206 7.312c.3.523.856.844 1.456.844h8.412c.6 0 1.156-.32 1.456-.844l4.206-7.312a1.69 1.69 0 00.003-1.688';

/** Hexagon and the reference image box, both in user units (viewBox 0 0 20 18). */
const HEX_W = 20;
const HEX_H = 18;
const IMG_W = 25.92;
const IMG_H = 19.44;

/**
 * Where to place a flag so the hexagon is filled and its focal point lands on
 * the hexagon's centre.
 *
 * A flag is `r` wide for 1 tall, so scaling it until it covers the 20 × 18
 * hexagon is `scale = max(IMG_W / r, IMG_H)`; the focal point (fx, fy) — the
 * emblem, canton or hoist device, measured by scripts/build-flag-geometry.mjs —
 * is then placed on (10, 9).
 *
 * Both axes are clamped so the visible window can never slide off the flag: the
 * hexagon is always completely covered by artwork, so a flag never shows the
 * page through its corners. For a 4:3 flag with a centred focal point this
 * reproduces the reference geometry exactly (x −2.96, y −0.72, 25.92 × 19.44).
 */
function placeFlag(code: string) {
  const g = FLAG_GEOMETRY[code] || {};
  const r = g.r || DEFAULT_FLAG_RATIO;
  const fx = g.x ?? 0.5;
  const fy = g.y ?? 0.5;
  void g; // (kept single-source: see backdropFlag below)
  const scale = Math.max(IMG_W / r, IMG_H);
  const w = r * scale;
  const h = scale;
  const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  const cx = clamp(fx, HEX_W / 2 / w, 1 - HEX_W / 2 / w);
  const cy = clamp(fy, HEX_H / 2 / h, 1 - HEX_H / 2 / h);
  return {
    x: +(HEX_W / 2 - cx * w).toFixed(3),
    y: +(HEX_H / 2 - cy * h).toFixed(3),
    width: +w.toFixed(3),
    height: +h.toFixed(3),
  };
}

type Stage = 'local' | 'cdn' | 'emoji';

function normalise(country?: string | null): string {
  if (!country) return '';
  const cc = String(country).trim().toLowerCase();
  return /^[a-z]{2}$/.test(cc) ? cc : '';
}

function srcFor(stage: Stage, cc: string): string {
  // The ?v= tag is what lets _headers serve /img/* as immutable: swapping
  // flag artwork means bumping it (see scripts/fetch-flags.mjs).
  return stage === 'local' ? asset(`/img/flags/${cc}.svg?v=nim-flags-1`) : `https://flagcdn.com/${cc}.svg`;
}

export interface FlagHexProps {
  country?: string | null;
  /** Rendered HEIGHT in px. Width follows from the hexagon (20:18 ≈ 1.11×). */
  size?: number;
  /** Accessible name. Omit when a neighbouring label already names the country. */
  label?: string;
  className?: string;
  style?: React.CSSProperties;
}

/** Backing colour for the handful of flags that do not fill their own box. */
function backdrop(code: string): string | undefined {
  return FLAG_GEOMETRY[code]?.bg;
}

export function FlagHex({ country, size = 18, label, className, style }: FlagHexProps) {
  const cc = normalise(country);
  const h = Math.max(10, Math.round(Number(size) || 18));
  const w = Math.round(h * (20 / 18) * 100) / 100;
  // Per-instance clip id: two flags on one page must not share a <clipPath>,
  // or the second definition wins and every flag wears the first one's mask.
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, '') || 'f';
  const clipId = `flaghex-${uid}`;
  const [stage, setStage] = useState<Stage>(cc ? 'local' : 'emoji');

  // Keep the outline ~1.1 px on screen whatever the flag is scaled to: the
  // stroke lives in user units, so it has to shrink as the flag grows.
  const strokeWidth = Math.min(1.4, Math.max(0.7, Math.round((22 / w) * 100) / 100));

  const title = label ?? (cc ? cc.toUpperCase() : '');

  return (
    <svg
      className={className}
      // The visual language of the reference picker; `overflow: visible` keeps
      // the stroked outline from being clipped by the SVG's own box.
      style={{ display: 'block', overflow: 'visible', flexShrink: 0, ...style }}
      viewBox="0 0 20 18"
      width={w}
      height={h}
      role={title ? 'img' : undefined}
      aria-label={title || undefined}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      {title ? <title>{title}</title> : null}
      <defs>
        <clipPath id={clipId}>
          <path d={HEX_PATH} />
        </clipPath>
      </defs>

      {/* Nepal: the pennants leave the corners of the flag's own box empty, so
          the hexagon would show the page through them. */}
      {stage !== 'emoji' && cc && backdrop(cc) ? (
        <rect x="0" y="0" width="20" height="18" fill={backdrop(cc)!} clipPath={`url(#${clipId})`} />
      ) : null}

      {stage === 'emoji' || !cc ? (
        <text
          x="10"
          y="13.4"
          textAnchor="middle"
          fontSize="10.5"
          fontFamily="system-ui, 'Apple Color Emoji', 'Segoe UI Emoji', 'Noto Color Emoji', sans-serif"
        >
          {flag(country) || '🌐'}
        </text>
      ) : (
        <image
          href={srcFor(stage, cc)}
          {...placeFlag(cc)}
          preserveAspectRatio="xMidYMid slice"
          clipPath={`url(#${clipId})`}
          onError={() => setStage(stage === 'local' ? 'cdn' : 'emoji')}
        />
      )}

      {/* Outline on top of the clip: the shape's edge, and what keeps a very
          light flag (Japan, Argentina) from dissolving into a light card. */}
      <path
        d={HEX_PATH}
        fill="none"
        stroke="var(--flag-hex-stroke, rgba(31, 35, 72, 0.38))"
        strokeWidth={strokeWidth}
        strokeLinejoin="round"
      />
    </svg>
  );
}

export default FlagHex;
