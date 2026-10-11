import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const lang of ['en', 'tr'] as const) {
  test.describe(`Activity volume ${lang}`, () => {
    test.use({ lang, api: { authed: false }, viewport: lang === 'tr' ? { width: 390, height: 844 } : { width: 1280, height: 800 } });

    for (const mode of ['usd-only', 'mixed', 'unknown', 'empty'] as const) {
      test(`${mode} keeps the volume exclusively in NIM on a direct visit`, async ({ page }) => {
        const rate = mode === 'unknown' ? 0 : 0.002;
        await page.route('**/data/market/nim-rate.json', route => route.fulfill({ json: { usd_per_nim: rate } }));
        await page.route('**/api/market/nim-rate', route => route.fulfill({ json: { usd_per_nim: rate } }));
        const items = mode === 'empty' ? [] : [
          { id: 'q1', type: 'cryptorefills_purchase', title: 'Steam', usd: '6', status: 'fulfilled', time: new Date().toISOString(), anonymous: true },
          ...(mode === 'mixed' ? [{ id: 'q2', title: 'Steam', usd: '2', nim: '100', status: 'fulfilled', time: new Date().toISOString(), anonymous: true }] : []),
        ];
        await page.route('**/api/activity?*', route => route.fulfill({ json: { items, summary: {} } }));
        await open(page, path('/activity'));
        const volume = page.locator('.act-tile').filter({ has: page.locator('.act-tile-nim-mark') });
        const amount = mode === 'mixed' ? 3100 : 3000;
        await expect(volume.locator('.act-tile-num')).toHaveText(mode === 'unknown' ? '— NIM' : mode === 'empty' ? '0 NIM' : `${amount.toLocaleString(lang)} NIM`);
        await expect(volume).not.toContainText(/USD|Infinity|NaN/);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      });
    }
  });
}
