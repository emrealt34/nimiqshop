import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const purchase = { id: 'timing-test', title: 'Carrefour', product_id: 'Carrefour', product_country: 'FR', country: 'FR', denomination: 'range', product_value: 10, product_currency: 'EUR', quantity: 1,
  status: 'fulfilled', type: 'cryptorefills_purchase', created_at: '2026-10-11T16:14:36Z', paid_at: '2026-10-11T16:21:58Z', delivered_at: '2026-10-11T16:22:00Z', updated_at: '2026-10-11T21:00:00Z', rated_at: '2026-10-11T21:00:00Z', product_usd: 10000000, coin: 'BTC', coin_amount: '0.00012', usd: 10, nim: 40000 };

for (const width of [320, 1280]) {
  test.describe(`recorded delivery ${width}px`, () => {
    test.use({ viewport: { width, height: 844 }, lang: 'tr' });
    for (const [seconds, label] of [[0, '<1 sn'], [.25, '<1 sn'], [61, '1 dk 1 sn'], [3599, '59 dk 59 sn']] as const) {
      test(`average ${seconds} seconds is not empty or rounded to minutes`, async ({ page }) => {
        await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [purchase], summary: { count: 0, avg_delivery_seconds: seconds, delivered_count: 1 } } }));
        await open(page, path('/activity'));
        const tile = page.locator('.act-tile').filter({ hasText: 'ort. teslimat' });
        await expect(tile).toContainText(label);
        await expect(tile).toContainText('ödeme teyidi → teslim');
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      });
    }

    test('order history total stops at delivery and keeps seconds after a later rating', async ({ page }) => {
      await page.route('**/api/quotes', route => route.fulfill({ json: [purchase] }));
      await open(page, path('/orders'));
      const row = page.locator('.order-card').filter({ hasText: 'Carrefour' });
      await expect(row).toContainText('7 dk 24 sn');
      await expect(row).toContainText('10 €');
    });

    test('legacy order recap retains currency and quantity', async ({ page }) => {
      const legacy = { ...purchase, id: 'legacy-timing', status: 'delivered', kind: 'giftcard', quantity: 2, price_usd: 20, payload: { product_name: 'Carrefour', value: 10, currency: 'EUR', country: 'FR' } };
      await page.route('**/api/orders', route => route.fulfill({ json: [legacy] }));
      await page.route('**/api/orders/legacy-timing', route => route.fulfill({ json: legacy }));
      await open(page, path('/orders'));
      await expect(page.locator('.order-card').filter({ hasText: 'Carrefour' }).first()).toContainText('20 € (10 € ×2)');
      await open(page, path('/order?id=legacy-timing'));
      await expect(page.locator('main')).toContainText('20 € (10 € ×2)');
    });

    test('gift sender sees delivery location requirements before buying and in support', async ({ page }) => {
      await page.addInitScript(() => localStorage.setItem('nimshop_cart', JSON.stringify([{ id: 'Carrefour', name: 'Carrefour', type: 'giftcard', country: 'FR', currency: 'EUR', value: 10, denomination: '10', unitUSD: 10.87, qty: 1, delivery_type: 'by_email' }])));
      await open(page, path('/cart'));
      await page.locator('.btn-gold.btn-block.btn-lg').click();
      await page.locator('.gift-toggle').click();
      await expect(page.locator('.gift-toggle input[type=checkbox]')).toBeChecked();
      await expect(page.locator('.gift-support-notice')).toContainText('bulunduğu ülke veya bölgeyi');
      await expect(page.locator('.gift-support-notice')).toContainText('kodun gönderildiği e-posta adresinden');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await open(page, path('/support'));
      await page.locator('.faq-item').first().locator('summary').click();
      await expect(page.locator('.faq-item').first()).toContainText('bulunduğu ülke veya bölgeyi');
    });
  });
}

test('real checkout payment recap retains the selected monetary unit before payment', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('nimshop_cart', JSON.stringify([{ id: 'Carrefour', name: 'Carrefour', type: 'giftcard', country: 'FR', currency: 'EUR', value: 10, denomination: '10', unitUSD: 10.87, qty: 1, delivery_type: 'by_email' }])));
  // Offline fixture: valid checksum, zero signature; cannot be paid.
  const invoice = { ...purchase, status: 'awaiting_payment', can_pay: true, payment_method: 'nimiq_pay', paid_at: undefined, delivered_at: undefined, lightning_invoice: 'lnbc120000000p1qqqqqqqpp5qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqxqrpcgqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqkzf7d6', payment_expiry: new Date(Date.now() + 600000).toISOString() };
  await page.route(/\/api\/quotes(?:\/batch)?(?:\?|$)/, route => route.request().method() === 'POST' ? route.fulfill({ json: invoice }) : route.fallback());
  await page.route('**/api/quotes/timing-test', route => route.fulfill({ json: invoice }));
  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();
  await expect(page.locator('.pay-hero')).toContainText('10 €');
});
