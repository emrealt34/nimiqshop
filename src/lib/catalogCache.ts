/**
 * Instant shelf: last-good live catalog in localStorage so a revisit paints
 * immediately while the hourly snapshot/API refreshes.
 */
import type { Product } from './catalog';
import { excludedCatalogItem } from './catalogVisibility';

// v4 invalidates cached shelves built before the e-money/KYC exclusion.
const PREFIX = 'nimshop_cat_v4:';
const MAX_AGE_MS = 60 * 60 * 1000;

export type CatalogMap = Record<string, Product[]>;

export function readCachedCatalog(country: string): CatalogMap | null {
  if (typeof localStorage === 'undefined') return null;
  try {
    const raw = localStorage.getItem(PREFIX + country.toUpperCase());
    if (!raw) return null;
    const j = JSON.parse(raw);
    if (!j || !j.at || Date.now() - j.at > MAX_AGE_MS) return null;
    if (!j.gift_card && !j.phone_refill && !j.esim) return null;
    return {
      gift_card: Array.isArray(j.gift_card) ? j.gift_card.filter((p: Product) => !excludedCatalogItem(p)) : [],
      phone_refill: Array.isArray(j.phone_refill) ? j.phone_refill.filter((p: Product) => !excludedCatalogItem(p)) : [],
      esim: Array.isArray(j.esim) ? j.esim.filter((p: Product) => !excludedCatalogItem(p)) : [],
    };
  } catch {
    return null;
  }
}

export function writeCachedCatalog(country: string, catalogs: CatalogMap): void {
  if (typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(
      PREFIX + country.toUpperCase(),
      JSON.stringify({ at: Date.now(), ...catalogs })
    );
  } catch {
    /* quota */
  }
}

export function catalogHasItems(c: CatalogMap | null | undefined): boolean {
  if (!c) return false;
  return (c.gift_card?.length || 0) + (c.phone_refill?.length || 0) + (c.esim?.length || 0) > 0;
}
