/**
 * CART ROW LAYOUT — the artwork can never reach the −/+ cluster.
 *
 * Owner (2026-10-05), twice, about the cart sheet: "küçük ekranda görsel −
 * butonunun üstüne taşıyor" and then "sepet panelinde hâlâ taşıyor". The CSS
 * fix lives in src/styles/fixes.css (one responsive --cart-thumb track, the
 * tile and the cluster in separate grid rows, the wide-screen centring in a
 * min-width query) — easy to lose again, because the deep scan never visited a
 * POPULATED cart: no fixture seeds localStorage['nimshop_cart'], so every run
 * audited the empty-cart screen and the row could break unnoticed.
 *
 * This spec seeds one real row and opens /cart (CartView renders
 * CartSheetContent inline). At the narrow phone widths it asserts the geometry
 * directly, in the page: the tile box and the .cart-controls box must not
 * intersect on BOTH axes (>3px, the same tolerance support/scan.ts uses for
 * control overlap), the tile keeps the clamp floor's width, and nothing in the
 * row sticks out of the viewport. The generic audit runs too, but only overlap
 * findings (and anything naming the tile) are judged — this spec answers the
 * owner's screen, not the whole page's backlog.
 */
import { test, expect, open } from './support/fixtures';
import { path, RAW_KEY_RE } from './support/data';
import { audit, AUDIT_DEFAULTS, formatIssues, type Issue } from './support/scan';

/** One gift-card row, shaped like cartStore.readCart() expects. */
const ROW = [{
  id: 'amazon-tr', name: 'Amazon TR', type: 'giftcard', country: 'TR',
  image: 'https://logos.example.test/Amazon.svg', bgColor: '#1f2348',
  currency: 'TRY', pkg: 'TRY250', value: 250, denomination: 'TRY 250',
  coinAmount: 0, unitUSD: 7.4, qty: 2,
  brand: 'Amazon', brand_id: 'amazon', category: 'giftcard', delivery_type: 'by_email',
}];

/** The clamp floor in fixes.css — the tile may never be squeezed below this. */
const THUMB_FLOOR = 84;

const SIZES = [
  { width: 320, height: 640 }, // the narrowest phone the shop supports
  { width: 360, height: 780 },
  { width: 430, height: 932 },
];

/** Measurements taken inside the page, one per cart row. */
type Box = { l: number; r: number; t: number; b: number; w: number; h: number };
type RowGeo = {
  tile: Box; controls: Box;
  tileOverControls: boolean; tileOverMain: boolean;
  overflowRight: number; scrollW: number; vw: number;
};

for (const size of SIZES) {
  test(`cart row geometry · ${size.width}x${size.height}`, async ({ page }) => {
    await page.setViewportSize(size);
    // Seed BEFORE any app script runs: readCart() is called at mount.
    await page.addInitScript((row) => {
      try { localStorage.setItem('nimshop_cart', JSON.stringify(row)); } catch { /* storage disabled */ }
    }, ROW);

    await open(page, path('/cart'));
    await page.locator('.cart-row').first().waitFor({ state: 'visible', timeout: 10_000 });
    await page.waitForTimeout(250); // media queries + runtime fitText re-flow

    const rows = await page.evaluate((): RowGeo[] => {
      const box = (el: Element): Box => {
        const r = el.getBoundingClientRect();
        return { l: r.left, r: r.right, t: r.top, b: r.bottom, w: r.width, h: r.height };
      };
      return [...document.querySelectorAll('.cart-row')].map((row) => {
        const tile = box(row.firstElementChild as Element);
        const controls = box(row.querySelector('.cart-controls') as Element);
        const mainEl = row.querySelector('.cart-main');
        const main = mainEl ? box(mainEl) : null;
        const intersects = (a: Box, b: Box) =>
          Math.min(a.r, b.r) - Math.max(a.l, b.l) > 3 && Math.min(a.b, b.b) - Math.max(a.t, b.t) > 3;
        return {
          tile, controls,
          tileOverControls: intersects(tile, controls),
          tileOverMain: main ? intersects(tile, main) : false,
          overflowRight: Math.max(tile.r, controls.r, main ? main.r : 0) - document.documentElement.clientWidth,
          scrollW: document.documentElement.scrollWidth,
          vw: document.documentElement.clientWidth,
        };
      });
    });

    expect(rows.length, 'the seeded row renders in the cart').toBe(1);
    const geo = rows[0];
    const where = `${size.width}px: tile ${Math.round(geo.tile.l)}..${Math.round(geo.tile.r)}×${Math.round(geo.tile.t)}..${Math.round(geo.tile.b)},`
      + ` controls ${Math.round(geo.controls.l)}..${Math.round(geo.controls.r)}×${Math.round(geo.controls.t)}..${Math.round(geo.controls.b)}`;

    expect(geo.tileOverControls, `artwork overlaps the −/+ cluster — ${where}`).toBe(false);
    expect(geo.tileOverMain, `artwork overlaps the text column — ${where}`).toBe(false);
    expect(geo.tile.w, `tile keeps the clamp floor — ${where}`).toBeGreaterThanOrEqual(THUMB_FLOOR - 0.5);
    expect(geo.overflowRight, `row stays inside the viewport — ${where}`).toBeLessThanOrEqual(1);
    expect(geo.scrollW, 'the page does not scroll sideways').toBeLessThanOrEqual(geo.vw + 1);

    const issues: Issue[] = await audit(page, AUDIT_DEFAULTS.phone, RAW_KEY_RE);
    const overlaps = issues.filter((i) => i.kind === 'control-overlap' || i.kind === 'text-overlap' || /thumb/.test(i.detail));
    expect(overlaps.length, `\n${formatIssues(overlaps)}`).toBe(0);
  });
}
