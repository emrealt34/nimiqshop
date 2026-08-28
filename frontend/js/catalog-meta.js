/* catalog-meta.js — THE one shared brand-lookup layer.
 *
 * Four pages (orders, order detail, product, activity) resolve real brand
 * photos + original background colors from the catalog brand lists. They
 * used to each keep a private cache and fetch giftcards/topups/esims
 * separately — the same country lists were downloaded up to 4 times per
 * session and could drift out of sync. This module caches ONE promise per
 * country for the whole app.
 *
 *   brandMetaFor(family, country)        → exact family match ({logo, bg})
 *   brandMetaForTitle(title, country)    → longest-prefix match (feed titles
 *                                          like "Amazon.com.tr TRY500")
 */
import { listGiftCards, listTopups, listEsims } from './api.js';

// country → Promise<Map<lowercasedFamily, {family, logo, bg}>>
const countryLists = new Map();

function familiesFor(country) {
  const cc = String(country || '').toUpperCase() || 'US';
  if (!countryLists.has(cc)) {
    countryLists.set(cc, Promise.allSettled([listGiftCards(cc, false), listTopups(cc, false), listEsims(cc, false)]).then((settled) => {
      const byFamily = new Map();
      for (const r of settled) {
        if (r.status !== 'fulfilled' || !r.value || !Array.isArray(r.value.categories)) continue;
        for (const cat of r.value.categories) {
          for (const b of cat.brands || []) {
            const key = String(b.family || '').toLowerCase();
            if (!key || byFamily.has(key)) continue; // first listing wins — deterministic
            const raw = String(b.logo_url || b.logo_base_url || '');
            const urlMatch = raw.match(/https?:\/\/[^\s)\]]+/);
            byFamily.set(key, {
              family: String(b.family || ''),
              logo: urlMatch ? urlMatch[0] : raw.replace(/[[\]]/g, ''),
              bg: String(b.bg_color || '').trim(),
            });
          }
        }
      }
      return byFamily;
    }));
    // A rejected promise must not poison the cache forever: drop it so the
    // next caller retries the fetch.
    countryLists.get(cc).catch(() => countryLists.delete(cc));
  }
  return countryLists.get(cc);
}

const EMPTY = { logo: '', bg: '' };

export async function brandMetaFor(family, country) {
  const key = String(family || '').toLowerCase();
  if (!key) return EMPTY;
  try {
    const byFamily = await familiesFor(country);
    return byFamily.get(key) || EMPTY;
  } catch {
    return EMPTY;
  }
}

export async function brandMetaForTitle(title, country) {
  const t = String(title || '').toLowerCase();
  if (!t) return EMPTY;
  try {
    const byFamily = await familiesFor(country);
    let best = null;
    for (const meta of byFamily.values()) {
      const f = meta.family.toLowerCase();
      if (!f || !t.startsWith(f)) continue;
      if (!best || f.length > best.family.length) best = meta;
    }
    return best || EMPTY;
  } catch {
    return EMPTY;
  }
}
