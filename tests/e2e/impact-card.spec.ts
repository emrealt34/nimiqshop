/**
 * "Your cashback card" on /cashback — responsive layout across widths ×
 * languages × preferences. Regression guard for buttons overflowing on
 * 320px, rows drifting out of alignment, clipped labels.
 */
import { test, expect, open, overflowingChildren, clippedText, hasHorizontalScroll } from './support/fixtures';
import { LANGS, path } from './support/data';

const CARD = 'section.pt-impact';
const ROWS = ['.pt-impact-bar', '.pt-impact-summary', '.pt-impact-share-head', '.pt-actions'];

for (const preference of ['cashback', 'burn'] as const) {
  for (const width of [320, 390, 768, 1280]) {
    for (const lang of LANGS) {
      test.describe(`cashback card · ${preference} · ${width}px · ${lang}`, () => {
        test.use({ viewport: { width, height: 900 }, lang, api: { preference } });
        test('no overflow, clipping or misalignment', async ({ page }) => {
          await open(page, path('/cashback'));
          const card = page.locator(CARD).first();
          await expect(card).toBeVisible();
          await card.scrollIntoViewIfNeeded();
          expect(await overflowingChildren(page, CARD), 'sticking out of the card').toEqual([]);
          expect(await clippedText(page, CARD), 'text cut off').toEqual([]);
          expect(await hasHorizontalScroll(page)).toBe(false);
          const edges = await page.evaluate(({ card, sels }) => sels.map((s) => {
            const el = document.querySelector(card)!.querySelector(s); if (!el) return null;
            // Compare content edges: the header/summary use padding; share/actions use margins.
            const r = el.getBoundingClientRect(), css = getComputedStyle(el);
            return { s, left: Math.round(r.left + parseFloat(css.paddingLeft)), right: Math.round(r.right - parseFloat(css.paddingRight)) };
          }).filter(Boolean) as { s: string; left: number; right: number }[], { card: CARD, sels: ROWS });
          expect(edges.map((edge) => edge.s)).toEqual(ROWS);
          for (const e of edges) expect(Math.abs(e.left - edges[0].left), `${e.s} left edge`).toBeLessThanOrEqual(1);
          const right = edges.find((e) => e.s === '.pt-actions')!.right;
          for (const e of edges.filter((x) => x.s !== '.pt-impact-share-head')) expect(Math.abs(e.right - right), `${e.s} right edge`).toBeLessThanOrEqual(1);
          const heights = await page.locator(`${CARD} .pt-actions .btn`).evaluateAll((bs) => bs.map((b) => b.getBoundingClientRect().height));
          // The card exposes PNG download and the native share action.
          expect(heights.length).toBe(2);
          for (const h of heights) expect(h, 'tap target height').toBeGreaterThanOrEqual(36);
        });
      });
    }
  }
}

test.describe('cashback card · signed out @smoke', () => {
  test.use({ api: { authed: false } });
  for (const width of [320, 1280]) {
    test(`${width}px shows the connect prompt without overflow`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await open(page, path('/cashback'));
      await expect(page.locator(`${CARD} .pt-impact-empty`)).toBeVisible();
      expect(await overflowingChildren(page, CARD)).toEqual([]);
    });
  }
});
