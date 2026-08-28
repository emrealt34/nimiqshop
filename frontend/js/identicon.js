/* identicon.js — Nimiq Identicons for addresses (@nimiq/identicons, vendored).
 * Rendered via toDataUrl() into <img> src — never parsed as HTML.
 */

let Identicons = null;
let loadPromise = null;
const cache = new Map();

async function load() {
  if (Identicons) return Identicons;
  if (!loadPromise) {
    loadPromise = import('../vendor/identicons.module.js')
      .then((mod) => {
        const I = mod.default;
        // Resolve the sprite relative to THIS module so it works from any page.
        try { I.svgPath = new URL('../vendor/identicons.min.svg', import.meta.url).href; } catch { /* keep default */ }
        Identicons = I;
        return I;
      })
      .catch(() => { Identicons = null; return null; });
  }
  return loadPromise;
}

/** Deterministic fallback: gradient circle derived from the address. */
function fallbackDataUrl(address) {
  const s = String(address || '?').replace(/\s+/g, '');
  let h1 = 0, h2 = 0;
  for (let i = 0; i < s.length; i++) {
    h1 = (h1 * 31 + s.charCodeAt(i)) >>> 0;
    h2 = (h2 * 37 + s.charCodeAt(i)) >>> 0;
  }
  const hue1 = h1 % 360, hue2 = (h2 % 200) + 160;
  const initials = s.slice(0, 2).toUpperCase();
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
    `<stop offset="0" stop-color="hsl(${hue1},70%,55%)"/>` +
    `<stop offset="1" stop-color="hsl(${hue2},70%,40%)"/></linearGradient></defs>` +
    `<rect width="64" height="64" rx="32" fill="url(#g)"/>` +
    `<text x="32" y="41" font-family="sans-serif" font-size="22" font-weight="700" fill="#fff" text-anchor="middle">${initials.replace(/[<>&"']/g, '')}</text>` +
    `</svg>`;
  return 'data:image/svg+xml;utf8,' + encodeURIComponent(svg);
}

/** Returns a data: URL image for an address (cached). */
async function identiconUrl(address) {
  if (!address) return fallbackDataUrl(address);
  // Nimiq avatars are derived from the FRIENDLY address form ("NQ07 XL5G …",
  // groups of 4, spaces included) — that is exactly what the Nimiq wallet
  // hashes. Stripping the spaces (the old behavior here) changes the hash
  // and produces a DIFFERENT avatar than the user's wallet. Normalize to
  // the canonical spaced form before hashing.
  const key = canonicalIdenticonInput(address);
  if (cache.has(key)) return cache.get(key);
  const I = await load();
  let url;
  try {
    if (!I) throw new Error('identicons unavailable');
    url = await I.toDataUrl(key);
  } catch {
    url = fallbackDataUrl(key);
  }
  cache.set(key, url);
  return url;
}

/** Uppercased, space-grouped-by-4 friendly address (Nimiq canonical). */
function canonicalIdenticonInput(address) {
  const raw = String(address || '').replace(/[\s-]/g, '').toUpperCase();
  if (!raw) return String(address || '');
  return raw.replace(/(.{4})/g, '$1 ').trim();
}

/** <img> element showing an address identicon. */
export function identiconImg(address, cls = 'identicon') {
  const img = document.createElement('img');
  img.className = cls;
  img.alt = '';
  img.setAttribute('aria-hidden', 'true');
  // Do NOT pre-render the gradient fallback: it flashed before the real
  // identicon resolved, so users saw a WRONG avatar until the swap. The
  // real one arrives a few ms later; the empty slot beats a wrong face.
  const key = canonicalIdenticonInput(address);
  const cached = cache.get(key);
  if (cached) img.src = cached;
  identiconUrl(address).then((u) => { img.src = u; });
  return img;
}
