/**
 * catalogMeta.ts — the one shared brand-lookup layer (ported from catalog-meta.js).
 * Caches ONE promise per country for the whole app. Live catalog only.
 */
import { listGiftCards, listTopups, listEsims } from './api';

type BrandMeta = { family: string; logo: string; bg: string };
const countryLists = new Map<string, Promise<Map<string, BrandMeta>>>();

function lookupKey(value: unknown): string {
  return String(value || '')
    .replace(/^\*+|\*+$/g, '')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/\s*\([^()]*\)\s*$/g, '')
    .replace(/\s+/g, ' ')
    .trim()
    .toLowerCase();
}

function addBrand(byFamily: Map<string, BrandMeta>, brand: any): void {
  const family = String(brand?.family || brand?.brand || '').trim();
  const key = lookupKey(family);
  if (!key) return;
  const raw = String(brand?.logo_url || brand?.logo_base_url || '');
  const urlMatch = raw.match(/https?:\/\/[^\s)\]]+/);
  const logo = urlMatch ? urlMatch[0] : raw.replace(/[\[\]]/g, '');
  const next: BrandMeta = {
    family,
    logo,
    bg: String(brand?.bg_color || '').trim(),
  };
  const previous = byFamily.get(key);
  // A live entry with no image must not block the static real-logo entry.
  if (!previous || (!previous.logo && next.logo)) byFamily.set(key, next);
}

function mergeCatalogResponse(byFamily: Map<string, BrandMeta>, data: any): void {
  for (const cat of data?.categories || []) {
    for (const brand of cat?.brands || []) addBrand(byFamily, brand);
  }
}

function familiesFor(country?: string | null): Promise<Map<string, BrandMeta>> {
  const cc = String(country || '').toUpperCase() || 'US';
  if (!countryLists.has(cc)) {
    const p = Promise.allSettled([listGiftCards(cc, false), listTopups(cc, false), listEsims(cc, false)]).then(
      (settled) => {
        const byFamily = new Map<string, BrandMeta>();
        for (const result of settled) {
          if (result.status === 'fulfilled') mergeCatalogResponse(byFamily, result.value);
        }
        return byFamily;
      }
    );
    p.catch(() => countryLists.delete(cc));
    countryLists.set(cc, p);
  }
  return countryLists.get(cc) as Promise<Map<string, BrandMeta>>;
}

const EMPTY = { logo: '', bg: '' };

export async function brandMetaFor(
  family?: string | null,
  country?: string | null
): Promise<{ logo: string; bg: string }> {
  const key = lookupKey(family);
  if (!key) return EMPTY;
  try {
    const byFamily = await familiesFor(country);
    const exact = byFamily.get(key);
    if (exact && (exact.logo || exact.bg)) return { logo: exact.logo, bg: exact.bg };

    // Product records sometimes use the short display brand while the catalog
    // family is country-specific (Amazon → Amazon.com.tr). Prefer the longest
    // matching catalog family so Google does not win over Google Play.
    let best: BrandMeta | null = null;
    for (const meta of byFamily.values()) {
      const f = lookupKey(meta.family);
      if (!f || !(f.startsWith(key) || key.startsWith(f))) continue;
      if (!best || f.length > lookupKey(best.family).length) best = meta;
    }
    return best ? { logo: best.logo, bg: best.bg } : EMPTY;
  } catch {
    return EMPTY;
  }
}

export async function brandMetaForTitle(
  title?: string | null,
  country?: string | null
): Promise<{ logo: string; bg: string }> {
  const t = lookupKey(title);
  if (!t) return EMPTY;
  try {
    const byFamily = await familiesFor(country);
    let best: BrandMeta | null = null;
    for (const meta of byFamily.values()) {
      const f = lookupKey(meta.family);
      if (!f || !(t.startsWith(f) || f.startsWith(t))) continue;
      if (!best || f.length > lookupKey(best.family).length) best = meta;
    }
    return best ? { logo: best.logo, bg: best.bg } : EMPTY;
  } catch {
    return EMPTY;
  }
}
