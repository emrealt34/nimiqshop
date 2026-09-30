/**
 * identicon.ts — Nimiq Identicons for addresses (@nimiq/identicons, vendored
 * in /public/vendor). Rendered via toDataUrl() into <img> src — never parsed
 * as HTML. Ported from identicon.js; the vendored ESM is dynamically imported
 * at runtime and the sprite is at its public path.
 */

import { asset } from './asset';
import { svgDataUrl } from './svg';

const cache = new Map<string, string>();
let Identicons: any = null;
let loadPromise: Promise<any> | null = null;

function resolveSprite(I: any): Promise<boolean> {
  return (async () => {
    try {
      I.svgPath = asset('/vendor/identicons.min.svg');
      try {
        (self as any).NIMIQ_IDENTICONS_SVG_PATH = asset('/vendor/identicons.min.svg');
      } catch {
        /* ignore */
      }
      try {
        I._assetsPromise = null;
      } catch {
        /* older builds */
      }
      const test = await I.toDataUrl('NQ07 0000 0000 0000 0000 0000 0000 0000 0000');
      const svg = atob(String(test).split(',')[1] || '');
      if (test && test.startsWith('data:image/svg+xml;base64,') && svg.length > 1200) return true;
    } catch {
      /* candidate unreachable */
    }
    return false;
  })();
}

async function load(): Promise<any> {
  if (Identicons) return Identicons;
  if (!loadPromise) {
    // Dynamic path (variable) so Rollup leaves it for the browser to fetch at
    // runtime from the static /vendor/ location.
    const modUrl = asset('/vendor/identicons.module.js');
    loadPromise = import(/* @vite-ignore */ modUrl)
      .then(async (mod: { default: any }) => {
        const I = mod.default;
        await resolveSprite(I);
        Identicons = I;
        return I;
      })
      .catch(() => {
        Identicons = null;
        return null;
      });
  }
  return loadPromise;
}

function fallbackDataUrl(address?: string | null): string {
  const s = String(address || '?').replace(/\s+/g, '');
  let h1 = 0;
  let h2 = 0;
  for (let i = 0; i < s.length; i++) {
    h1 = (h1 * 31 + s.charCodeAt(i)) >>> 0;
    h2 = (h2 * 37 + s.charCodeAt(i)) >>> 0;
  }
  const hue1 = h1 % 360;
  const hue2 = (h2 % 200) + 160;
  const initials = s.slice(0, 2).toUpperCase();
  const body =
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
    `<stop offset="0" stop-color="hsl(${hue1},70%,55%)"/>` +
    `<stop offset="1" stop-color="hsl(${hue2},70%,40%)"/></linearGradient></defs>` +
    `<rect width="64" height="64" rx="32" fill="url(#g)"/>` +
    `<text x="32" y="41" font-family="sans-serif" font-size="22" font-weight="700" fill="#fff" text-anchor="middle">${initials.replace(
      /[<>&"']/g,
      ''
    )}</text>`;
  return svgDataUrl(64, 64, body);
}

function canonicalIdenticonInput(address?: string | null): string {
  const raw = String(address || '').replace(/[\s-]/g, '').toUpperCase();
  if (!raw) return String(address || '');
  return raw.replace(/(.{4})/g, '$1 ').trim();
}

async function identiconUrl(address?: string | null): Promise<string> {
  if (!address) return fallbackDataUrl(address);
  const key = canonicalIdenticonInput(address);
  if (cache.has(key)) return cache.get(key) as string;
  const I = await load();
  let url: string;
  try {
    if (!I) throw new Error('identicons unavailable');
    url = await I.toDataUrl(key);
  } catch {
    url = fallbackDataUrl(key);
  }
  cache.set(key, url);
  return url;
}

/** React-friendly: returns the resolved URL, or null until ready. */
export async function resolveIdenticonUrl(address?: string | null): Promise<string> {
  return identiconUrl(address);
}

/** Synchronous, deterministic placeholder (initials face) so an avatar slot is
 *  never blank — the nicer @nimiq/identicons face replaces it once loaded. */
export function identiconPlaceholder(address?: string | null): string {
  return fallbackDataUrl(address);
}

/**
 * The buyer's identicon as a PNG data URI — for the gift email. The email
 * cannot carry an <img data:…> (Gmail blocks data: images), so the backend
 * re-draws the avatar as bgcolor table cells from a PNG. This rasterizes the
 * EXACT face the site shows (same @nimiq/identicons module, same address) via
 * a canvas at the SVG's native 160px, which is also the sampling sweet spot
 * for the 32x32 email mosaic. Returns '' when anything fails — the note then
 * falls back to its placeholder avatar, never to an error.
 */
const pngCache = new Map<string, string>();

export async function identiconPngDataUrl(address?: string | null, size = 160): Promise<string> {
  const key = canonicalIdenticonInput(address || '');
  if (!key) return '';
  if (pngCache.has(key)) return pngCache.get(key) as string;
  try {
    const svgUrl = await resolveIdenticonUrl(key);
    // resolveIdenticonUrl may hand back the initials fallback (an SVG too) —
    // still fine: whatever the site would show, rasterized.
    const png = await new Promise<string>((resolve) => {
      const img = new Image();
      img.onload = () => {
        try {
          const c = document.createElement('canvas');
          c.width = size;
          c.height = size;
          const ctx = c.getContext('2d');
          if (!ctx) return resolve('');
          ctx.imageSmoothingEnabled = true;
          ctx.drawImage(img, 0, 0, size, size);
          resolve(c.toDataURL('image/png'));
        } catch {
          resolve('');
        }
      };
      img.onerror = () => resolve('');
      img.src = svgUrl;
    });
    if (png) pngCache.set(key, png);
    return png;
  } catch {
    return '';
  }
}

export { canonicalIdenticonInput };
