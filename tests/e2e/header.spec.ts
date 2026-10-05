/**
 * Header / navigation legibility. Regression guard for nav labels clamped to
 * 9.6px and the wordmark showing the API host ("shopapi.nimiqbase.com").
 */
import { test, expect, open, hasHorizontalScroll } from './support/fixtures';
import { LANGS, path } from './support/data';

const overlap = (a: any, b: any) => a && b && a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1;

// The desktop link row starts at 1200px (below it the bottom tab bar is used).
for (const width of [1200, 1280, 1366, 1440, 1600, 1920]) {
  for (const lang of LANGS) {
    test.describe(`desktop header ${width}px ${lang}`, () => {
      test.use({ viewport: { width, height: 800 }, lang });
      test('nav labels readable, unclipped, not colliding', async ({ page }) => {
        await open(page, path('/'));
        const info = await page.evaluate(() => {
          const links = [...document.querySelectorAll<HTMLElement>('.topbar nav.mainnav a')];
          const r = (el: Element | null) => (el ? el.getBoundingClientRect().toJSON() : null);
          return {
            shown: links.length > 0 && getComputedStyle(links[0].parentElement!).display !== 'none',
            sizes: links.map((a) => parseFloat(getComputedStyle(a).fontSize)),
            clipped: links.filter((a) => a.scrollWidth > a.clientWidth + 1).map((a) => a.textContent),
            links: links.map(r), brand: r(document.querySelector('.topbar .brand')),
            wmClipped: [...document.querySelectorAll<HTMLElement>('#site-wordmark, #site-wordmark *')].some((e) => e.scrollWidth > e.clientWidth + 1 && getComputedStyle(e).overflowX !== 'visible')
              || (() => { const w = document.querySelector('#site-wordmark'); const t = w?.querySelector('.wm-tail'); return !!(w && t && t.getBoundingClientRect().right > w.getBoundingClientRect().right + 1); })(),
            tabbarShown: [...document.querySelectorAll<HTMLElement>('.tabbar')].some((e) => getComputedStyle(e).display !== 'none'),
            actions: [...document.querySelectorAll('.topbar .cart-btn, .topbar .acct-wrap, .topbar-inner > .btn')].map(r),
            vw: innerWidth,
          };
        });
        expect(info.shown, 'desktop nav shown ≥1200px').toBe(true);
        expect(info.tabbarShown, 'bottom tab bar hidden when the desktop nav is shown').toBe(false);
        expect(info.wmClipped, 'wordmark "nim.shop" cut off').toBe(false);
        for (const s of info.sizes) expect(s, 'nav font-size px').toBeGreaterThanOrEqual(10.5);
        expect(info.clipped, 'ellipsized nav labels').toEqual([]);
        for (const b of info.links) {
          expect(b!.right).toBeLessThanOrEqual(info.vw);
          expect(overlap(b, info.brand), 'nav link overlaps brand').toBeFalsy();
          for (const a of info.actions) expect(overlap(b, a), 'nav link overlaps a header control').toBeFalsy();
        }
        expect(await hasHorizontalScroll(page)).toBe(false);
      });
    });
  }
}

test('wordmark shows the product brand, never the API host @smoke', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await open(page, path('/'));
  const wm = page.locator('#site-wordmark').first();
  // The navbar wordmark is the product name (owner, 2026-10-05) — the live
  // hostname still lives in titles, share links and copy.
  await expect(wm).toHaveText(/nim\.shop/i);
  await expect(wm).not.toHaveText(/shopapi/i);
});

for (const width of [320, 360, 390, 430, 768, 1024, 1199]) {
  for (const lang of LANGS) {
    test.describe(`phone/tablet header ${width}px ${lang}`, () => {
      test.use({ viewport: { width, height: 800 }, lang, hasTouch: true });
      test('controls fit, wordmark + tab labels readable', async ({ page }) => {
        await open(page, path('/'));
        const info = await page.evaluate(() => {
          const wm = document.querySelector<HTMLElement>('#site-wordmark');
          const tabs = [...document.querySelectorAll<HTMLElement>('.tabbar a > span:last-child')];
          return {
            wmSize: wm && wm.getClientRects().length ? parseFloat(getComputedStyle(wm).fontSize) : 99,
            wmText: wm?.textContent || '',
            tabSizes: tabs.map((s) => parseFloat(getComputedStyle(s).fontSize)),
            navShown: [...document.querySelectorAll<HTMLElement>('.topbar nav.mainnav')].some((e) => getComputedStyle(e).display !== 'none'),
            tabbarShown: [...document.querySelectorAll<HTMLElement>('.tabbar')].some((e) => getComputedStyle(e).display !== 'none'),
            tabClipped: tabs.filter((s) => s.scrollWidth > s.clientWidth + 1).map((s) => s.textContent),
            outside: [...document.querySelectorAll<HTMLElement>('.topbar-inner > *')].filter((e) => e.getBoundingClientRect().width > 0 && e.getBoundingClientRect().right > innerWidth + 1).map((e) => e.className),
          };
        });
        expect(info.wmText).not.toMatch(/shopapi/i);
        expect(info.wmSize).toBeGreaterThanOrEqual(10);
        expect(info.tabbarShown, 'bottom tab bar shown below 1200px').toBe(true);
        expect(info.navShown, 'desktop link row hidden below 1200px').toBe(false);
        for (const s of info.tabSizes) expect(s, 'tab label px').toBeGreaterThanOrEqual(10);
        expect(info.tabClipped).toEqual([]);
        expect(info.outside, 'header controls pushed off-screen').toEqual([]);
        expect(await hasHorizontalScroll(page)).toBe(false);
      });
    });
  }
}
