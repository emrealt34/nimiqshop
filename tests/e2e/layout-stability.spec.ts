/**
 * THE DEFERRED STYLESHEET MAY NOT MOVE ANYTHING.
 *
 * The build splits the shop's stylesheet in two: the rules a page needs before
 * its first paint stay inline, everything else (modals, sheets, hover states,
 * other breakpoints) goes into one shared `full.<hash>.css` that is applied a
 * moment later (see integrations/critical-css.mjs). The split is driven by
 * src/styles/critical.json — a capture of which rules matched at load — so a
 * capture that has gone stale silently turns first-paint layout into a late
 * rule, and the page visibly jumps the instant the sheet lands.
 *
 * That is not hypothetical. Before 2026-11-03 the capture predated the product
 * grid: `.product-card`, `.p-name`, `.p-meta .chip` and the whole 719px
 * `@media` block were "never matched" and therefore deferred, so on a real
 * load the hero's aside moved 13 px, every card changed height by 3 px and the
 * phone filter row jumped 21 px — the owner's "sayfa ilk açıldığında bazı
 * öğeler aşağı yukarı oynuyor". The build-time guards added with that fix
 * (selector-level cascade keep, shared-sheet pruning) are what this spec
 * protects: if either is weakened, or the capture goes stale again, this test
 * fails with the exact selectors that moved.
 *
 * HOW IT MEASURES
 *   Load the page twice — once with the deferred sheet allowed to apply, once
 *   with the request aborted — and diff the boxes of the elements that sit in
 *   the first viewport. Anything that differs by more than a pixel across the
 *   two runs moved because of that sheet, by definition.
 */
import { test, expect } from './support/fixtures';
import type { Page } from '@playwright/test';

/** Elements that are on screen at first paint on the pages under test. */
const WATCHED = [
  'header .topbar',
  '.hero h1',
  '.hero .lede',
  '.hero-actions',
  '.kf-pkg',
  '.how-strip',
  '#browse',
  '.toolbar',
  '.toolbar.filters',
  '.toolbar.filters .field',
  '.toolbar.filters .btn-country',
  '.seg',
  '.searchbox',
  '#gridWrap',
  '.grid.products',
  '.product-card',
  '.product-card .thumb',
  '.product-card .p-name',
  '.product-card .p-meta',
  '.come-back',
  '.wallet-balance',
  '.footer',
];

/** First-viewport boxes of everything in WATCHED. */
async function boxes(page: Page): Promise<Record<string, [number, number, number, number] | null>> {
  return page.evaluate((sels) => {
    const out: Record<string, [number, number, number, number] | null> = {};
    for (const s of sels) {
      const el = document.querySelector(s);
      if (!el) {
        out[s] = null;
        continue;
      }
      const r = el.getBoundingClientRect();
      out[s] = [r.x, r.y, r.width, r.height].map((n) => Math.round(n * 10) / 10) as [number, number, number, number];
    }
    return out;
  }, WATCHED);
}

/**
 * Load `url` with the deferred stylesheet either applied or blocked, and return
 * the settled boxes. Fonts are pinned to the document's own load event plus a
 * fixed settle window so both runs are compared at the same point of the
 * loading sequence.
 */
async function settledBoxes(page: Page, url: string, blockLateCss: boolean) {
  if (blockLateCss) {
    await page.route('**/full.*.css', (route) => route.abort());
  }
  await page.goto(url, { waitUntil: 'load' });
  // The sheet is fetched with `media="print"` and flipped by a tiny script, so
  // give both paths the same, generous window to reach their final state.
  await page.waitForTimeout(2500);
  const result = await boxes(page);
  await page.unroute('**/full.*.css');
  return result;
}

const VIEWPORTS = [
  { name: 'desktop', width: 1280, height: 800 },
  { name: 'phone', width: 390, height: 844 },
];

for (const vp of VIEWPORTS) {
  test(`home: the deferred stylesheet does not move anything (${vp.name}) @smoke`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const withLate = await settledBoxes(page, '/', false);
    const withoutLate = await settledBoxes(page, '/', true);

    const moved: string[] = [];
    for (const sel of WATCHED) {
      const a = withLate[sel];
      const b = withoutLate[sel];
      if (!a && !b) continue;
      // An element that only exists in one of the two runs is a bigger
      // difference than a moved box, and worth naming.
      if (!a || !b) {
        moved.push(`${sel}: ${a ? 'only WITH the deferred sheet' : 'only WITHOUT it'}`);
        continue;
      }
      const d = a.map((n, i) => Math.abs(n - b[i]));
      if (Math.max(...d) > 1) {
        moved.push(`${sel}: with=[${a.join(',')}] without=[${b.join(',')}]`);
      }
    }
    expect(
      moved,
      'elements moved when the deferred stylesheet applied — the critical-CSS capture is stale, or a cascade guard in integrations/critical-css.mjs was weakened. Re-run: node scripts/critical-css.mjs --capture <url> --country TR',
    ).toEqual([]);
  });
}

/**
 * The capture itself must have been taken with a market whose catalog renders.
 * A capture that never sees a product grid defers every card rule — the exact
 * regression the spec above then catches, but it is worth failing on the cause
 * rather than only on the symptom.
 */
test('capture was taken with product cards on screen @smoke', async () => {
  const fs = await import('node:fs/promises');
  const raw = await fs.readFile('src/styles/critical.json', 'utf8');
  const capture = JSON.parse(raw) as { neverMatched?: string[]; capturedAt?: string };
  const never = capture.neverMatched || [];
  // `.product-card` (the base rule, no pseudo-class) must have MATCHED.
  const deferredCard = never.filter((k) => k.endsWith('\u0000.product-card'));
  expect(
    deferredCard,
    `the capture (${capture.capturedAt}) deferred the base .product-card rule — it was taken without a filled grid. Re-run: node scripts/critical-css.mjs --capture <url> --country TR`,
  ).toEqual([]);
});
