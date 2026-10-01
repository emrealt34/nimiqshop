/**
 * shelfSeed.ts — the default-country shelf, baked into the HTML at build time.
 *
 * WHY: the shelf used to appear only after hydration → effect → fetch, so on
 * a cold visit the grid area was a skeleton for the whole round trip even
 * though the catalog snapshot is a static file the build already has. The
 * build (Base.astro frontmatter) reads public/data/catalog/brands/TR.json,
 * flattens it with the same flattenBrands() the live path uses, and hands it
 * here: Base inlines it as window.__SHELF_SEED and HomePage seeds its initial
 * state from it — server render and first client render agree, so hydration
 * is clean and the cards are in the FIRST paint, logos lazy-loading on top.
 *
 * The seed obeys the same 12 h freshness rule as the runtime static path: a
 * snapshot older than that (stalled cron/deploys) is ignored instead of
 * showing half-day-old stock.
 */
export type ShelfSeed = {
  country: string;
  at: string;
  maps: { gift_card: unknown[]; phone_refill: unknown[]; esim: unknown[] };
};

export const SHELF_SEED_MAX_AGE_MS = 12 * 60 * 60 * 1000;

let seed: ShelfSeed | null = null;

/** Called once per build/render by Base.astro (server side only). */
export function setShelfSeed(s: ShelfSeed | null): void {
  seed = s;
}

/** SSR/build side read (the client reads window.__SHELF_SEED instead). */
export function getShelfSeed(): ShelfSeed | null {
  return seed;
}

export function shelfSeedFresh(s: ShelfSeed | null | undefined): boolean {
  if (!s || !s.maps) return false;
  const at = Date.parse(String(s.at || ''));
  if (!Number.isFinite(at)) return false;
  const age = Date.now() - at;
  if (age < 0) return -age <= 60_000;
  return age <= SHELF_SEED_MAX_AGE_MS;
}
