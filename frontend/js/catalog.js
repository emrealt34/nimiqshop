/* catalog.js — shared catalog normalization + product visuals.
 *
 * home.js (shop grid) and product.js (detail page) each used to normalize
 * the same supplier brand rows with slightly different copies (markdown
 * family cleanup, logo URL regex, kind→type mapping, logo fallback icon).
 * The brand→product shape, the dedup flatten and the thumb builders live
 * here once:
 *
 *   normalizeBrand(brand, country)   brand row → shop product object
 *   flattenBrands(data, country)     {categories:[…]} → unique products
 *   mapKind(kind, category)          supplier kind/category → shop type
 *   extractLogo(family)              raw logo_url/base_url → clean URL
 *   productThumb(p)                  <img> with error → brand-icon fallback
 *   brandIconImg(p)                  the shop's own mark fallback
 */
import { el, replaceChildren, cleanFamilyName, parseCurrencyValue } from './util.js';
import { brandMetaFor, brandMetaForTitle } from './catalog-meta.js';

/* Supplier kind/category → shop product type. "mobile_recharge" is a top-up
 * UNLESS the category says e-sim; everything else defaults to gift_card. */
export function mapKind(kind, category) {
  const k = (kind || '').toLowerCase();
  const c = (category || '').toLowerCase();
  if (k === 'giftcard' || k === 'gift_card') return 'gift_card';
  if (k === 'mobile_recharge') return c === 'e-sim' ? 'esim' : 'phone_refill';
  if (c === 'e-sim' || k === 'esim') return 'esim';
  return 'gift_card';
}

/* logo_url can arrive wrapped in markdown brackets and/or carry query noise —
 * the same extraction the catalog-meta lookup layer uses. */
export function extractLogo(family) {
  if (!family) return '';
  const raw = String(family.logo_url || family.logo_base_url || '');
  const urlMatch = raw.match(/https?:\/\/[^\s)\]]+/);
  return urlMatch ? urlMatch[0] : raw.replace(/[[\]]/g, '');
}

/* One supplier brand row → the shop's product object. The family is the
 * exact-match lookup id for the supplier API, so markdown decoration is
 * stripped but the name itself stays intact. */
function normalizeBrand(brand, country = 'US') {
  if (!brand) return null;
  const familyRaw = brand.family || brand.family_name || brand.name || brand.id || '';
  const family = cleanFamilyName(familyRaw);
  if (!family) return null;
  const countryCode = (brand.country_code || brand.country || country).toUpperCase();
  const minParsed = parseCurrencyValue(brand.min);
  const maxParsed = parseCurrencyValue(brand.max);
  const currency = minParsed.currency || maxParsed.currency || brand.currency || 'USD';
  const logo = extractLogo(brand);
  return {
    id: family,
    family,
    brand_id: brand.brand_id || '',
    name: family,
    type: mapKind(brand.kind, brand.category),
    country: countryCode,
    currency,
    in_stock: !brand.is_out_of_stock,
    is_out_of_stock: !!brand.is_out_of_stock,
    logo_url: logo,
    bg_color: brand.bg_color || '#FFFFFF',
    min_raw: brand.min || '',
    max_raw: brand.max || '',
    min_value: minParsed.value,
    max_value: maxParsed.value,
    range: (minParsed.value > 0 && maxParsed.value > 0) ? { min: minParsed.value, max: maxParsed.value, step: 1, currency } : null,
    images: logo ? { large: logo } : {},
    product_type: brand.product_type || 'digital',
  };
}

/* Flatten a catalog response ({categories:[{brands:[…]}]}, a bare array, or
 * an empty payload) into a deduplicated product list. The same family shows
 * up in several categories (e.g. A101 in retail + groceries + home) — keep
 * ONE per family|country, preferring in-stock. */
export function flattenBrands(data, country = 'US') {
  if (!data) return [];
  let brands = [];
  if (data.categories && Array.isArray(data.categories)) {
    for (const cat of data.categories) {
      if (cat.brands && Array.isArray(cat.brands)) brands.push(...cat.brands);
    }
  } else if (Array.isArray(data)) {
    brands = data;
  } else {
    return [];
  }
  const map = new Map();
  for (const b of brands) {
    const family = cleanFamilyName(b.family || '');
    if (!family) continue;
    const key = `${family.toLowerCase()}|${(b.country_code || country).toUpperCase()}`;
    const existing = map.get(key);
    if (!existing) {
      map.set(key, b);
    } else if (existing.is_out_of_stock && !b.is_out_of_stock) {
      map.set(key, b); // prefer in-stock over out-of-stock
    }
  }
  const out = [];
  for (const b of map.values()) {
    const p = normalizeBrand(b, country);
    if (p) out.push(p);
  }
  return out;
}

/* CUSTOM ICON: the shop's own mark replaces the old flag/SVG fallback on
 * cards and the product hero. */
function brandIconImg(p) {
  return el('img.product-img', { src: '/img/brand-icon.png', alt: p.name || 'Product', loading: 'lazy', decoding: 'async', style: { background: 'var(--surface-2)', objectFit: 'contain', padding: '18%' } });
}

/* Product thumb: the brand photo on its original background, with the shop
 * mark as the on-error fallback. NO inline width/height — the image is
 * absolutely positioned inside the fixed-aspect .thumb (see app.css), so a
 * large intrinsic logo can never inflate the grid cell before/after a
 * re-render (the "images appear big, then shrink" flash on sort change). */
export function productThumb(p) {
  const src = p.logo_url || '';
  if (src) {
    const img = el('img.product-img', { src, alt: p.name, loading: 'lazy', decoding: 'async', style: { background: p.bg_color || '#fff' } });
    img.addEventListener('error', () => { try { img.replaceWith(brandIconImg(p)); } catch {} });
    return img;
  }
  return brandIconImg(p);
}

/* Resolve a family's logo via the catalog-meta lookup and swap it into a
 * placeholder thumb once known (order detail hero, activity feed tile).
 * OrderThumb/feedThumb each used to carry their own copy of this
 * brandMetaFor-then-swap dance. Returns the tile for chaining. */
export async function resolveMetaLogo(tile, family, country, { alt = '', match = 'exact', onLogo } = {}) {
  if (!tile || !family) return tile;
  try {
    // exact family match (order hero) or longest-prefix match (feed titles)
    const m = match === 'prefix' ? await brandMetaForTitle(family, country) : await brandMetaFor(family, country);
    if (!m || !m.logo || !tile.isConnected) return tile;
    const img = el('img.product-img', { src: m.logo, alt: alt || family, loading: 'lazy', decoding: 'async', style: m.bg ? { background: m.bg } : {} });
    if (typeof onLogo === 'function') onLogo(img);
    replaceChildren(tile, img);
  } catch { /* meta lookup is best-effort */ }
  return tile;
}
