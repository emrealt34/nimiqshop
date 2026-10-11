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

    test('legacy API rows cannot show the two fake Amazon purchases or inflate real volume', async ({ page }) => {
      const fakeIds = ['29760a02-9a43-40a2-8d5c-71277624df94', '0ac3ca80-a9cd-4524-a1fd-0c2fc95c583b'];
      const common = { type: 'cryptorefills_purchase', status: 'fulfilled', time: new Date().toISOString(), anonymous: true };
      await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [
        ...fakeIds.map(id => ({ ...common, id, title: 'Amazon', nim: 19000, country: 'TR' })),
        { ...common, id: 'future-test', title: 'Amazon', test_mode: true, nim: 5000 },
        { ...common, id: 'real-carrefour', title: 'Carrefour', country: 'FR', nim: 100 },
        { ...common, id: 'real-amazon', title: 'Amazon', country: 'TR', nim: 50 },
      ], summary: {} } }));
      await open(page, path('/activity'));
      await expect(page.locator('.feed-item')).toHaveCount(2);
      await expect(page.locator('.feed-item[data-order-id="real-carrefour"]')).toContainText('Carrefour');
      await expect(page.locator('.feed-item[data-order-id="real-amazon"]')).toContainText('Amazon');
      for (const id of [...fakeIds, 'future-test']) await expect(page.locator(`.feed-item[data-order-id="${id}"]`)).toHaveCount(0);
      await expect(page.locator('.act-tile-num').first()).toHaveText('2');
      await expect(page.locator('.act-tile').filter({ has: page.locator('.act-tile-nim-mark') }).locator('.act-tile-num')).toHaveText('150 NIM');
    });
  });
}
