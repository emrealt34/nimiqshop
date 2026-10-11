import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const purchase = {
  id: 'face-label-test', type: 'cryptorefills_purchase', title: 'Carrefour', product_id: 'Carrefour',
  product_country: 'FR', country: 'FR', denomination: 'range', face_label: '10',
  product_value: 10, product_currency: 'EUR', quantity: 1,
  local_amount: 10, local_currency: 'EUR', status: 'fulfilled',
  created_at: '2026-10-11T12:00:00Z', updated_at: '2026-10-11T12:00:00Z', time: '2026-10-11T12:00:00Z',
  product_usd: 10, usd: 10, coin: 'BTC', coin_amount: '0.00012', nim: 40000,
};

for (const width of [320, 1280]) {
  test.describe(`purchased units ${width}px`, () => {
    test.use({ viewport: { width, height: 844 }, lang: 'en' });
    for (const kind of ['order', 'track', 'orders', 'activity'] as const) {
      test(`${kind} shows the unit on a historical bare face label`, async ({ page }) => {
        await page.route('**/api/quotes', route => route.fulfill({ json: [purchase] }));
        await page.route('**/api/quotes/face-label-test', route => route.fulfill({ json: purchase }));
        await page.route('**/api/track/face-label-test', route => route.fulfill({ json: purchase }));
        await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [purchase], summary: {} } }));
        await open(page, path(kind === 'order' ? '/order?id=face-label-test&type=quote' : kind === 'track' ? '/track?order=face-label-test' : `/${kind}`));
        const content = kind === 'activity' ? page.locator('.feed-item') : kind === 'orders' ? page.locator('.order-card').filter({ hasText: 'Carrefour' }) : page.locator('main');
        await expect(content).toContainText('10 €');
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      });
    }

    test('cart keeps currency and nonmonetary package units', async ({ page }) => {
      await page.addInitScript(items => localStorage.setItem('nimshop_cart', JSON.stringify(items)), [
        { id: 'Carrefour', name: 'Carrefour', country: 'FR', type: 'giftcard', denomination: '10', value: 10, currency: 'EUR', qty: 1, unitUSD: 10 },
        { id: 'eSIM', name: 'eSIM', country: 'FR', type: 'esim', denomination: '50 GB 90 days', value: 16.16, currency: 'USD', qty: 1, unitUSD: 16.16 },
      ]);
      await open(page, path('/cart'));
      await expect(page.locator('.cart-row').filter({ hasText: 'Carrefour' })).toContainText('10 €');
      await expect(page.locator('.cart-row').filter({ hasText: 'eSIM' })).toContainText('50 GB 90 days');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });

    test('product package chooser completes a numeric supplier label with its own currency', async ({ page }) => {
      await page.route('**/api/catalog/products/**', route => route.fulfill({ json: {
        family: 'Carrefour', brand: 'Carrefour', kind: 'giftcard', category: 'retail', country_code: 'FR', is_out_of_stock: false,
        products: [{ product_id: 'carrefour-10', denomination: '10', localized_denomination: '10', delivery_type: 'by_email', coin: 'BTC', coin_amount: '0.00012', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '10' } } }],
      } }));
      await open(page, path('/product?id=Carrefour&country=FR'));
      await expect(page.locator('.pd-pkg').first()).toContainText('10 €');
    });
  });
}

test('mixed batch lines keep distinct currencies and data packages in the order and public track', async ({ page }) => {
  const lines = [
    { product_id: 'Carrefour', country: 'FR', denomination: '10', face_label: '10', product_value: 10, product_currency: 'EUR', quantity: 1, delivery_channel: 'email' },
    { product_id: 'Other card', country: 'FR', denomination: '10', face_label: '10', product_value: 10, product_currency: 'USD', quantity: 1, delivery_channel: 'email' },
    { product_id: 'eSIM', country: 'FR', denomination: '50 GB 90 days', face_label: '50 GB 90 days', product_value: 16.16, product_currency: 'USD', quantity: 1, delivery_channel: 'email' },
  ];
  const batch = { ...purchase, is_batch: true, quantity: 3, product_value: 0, product_currency: '', denomination: '', face_label: '', lines };
  await page.route('**/api/quotes/face-label-test', route => route.fulfill({ json: batch }));
  await page.route('**/api/track/face-label-test', route => route.fulfill({ json: batch }));
  for (const url of ['/order?id=face-label-test&type=quote', '/track?order=face-label-test']) {
    await open(page, path(url));
    await expect(page.locator('main')).toContainText('10 €');
    await expect(page.locator('main')).toContainText('10 USD');
    await expect(page.locator('main')).toContainText('50 GB 90 days');
  }
});
