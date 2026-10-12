import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const width of [320, 1280]) {
  test.describe(`stored payment inspection ${width}px`, () => {
    test.use({ api: { authed: false }, lang: 'en', viewport: { width, height: 900 } });
    test('operator reads legacy identifiers on demand and invoice is never labeled purchase tx', async ({ page }) => {
      let writes = 0;
      const invoice = 'ab'.repeat(32);
      const funding = 'cd'.repeat(32);
      await page.route('**/api/admin/**', route => {
        if (route.request().method() !== 'GET') writes++;
        const url = new URL(route.request().url());
        return route.fulfill({ json: url.pathname.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
          : url.pathname.endsWith('/quotes') ? { quotes: [{ id: 'old-order', product_id: 'Carrefour', status: 'fulfilled', lightning_payment_hash: invoice, cashback: {} }] }
            : url.pathname.endsWith('/orders/old-order') ? { payment_inspection: { records: ['quote:old-order'], identifiers: { 'quote.legacy.tx_hash': funding, 'quote.legacy.swap_id': 'historic-swap' } } } : {} });
      });
      await open(page, path('/admin?section=orders'));
      await expect(page.locator('body')).toContainText('Lightning invoice hash: ' + invoice);
      await expect(page.locator('body')).not.toContainText('purchase tx ' + invoice);
      await expect(page.getByTestId('payment-inspection')).toHaveCount(0);
      await page.getByRole('button', { name: 'Inspect stored payment identifiers' }).click();
      await expect(page.getByTestId('payment-inspection')).toContainText(funding);
      await expect(page.getByTestId('payment-inspection')).toContainText('historic-swap');
      expect(writes).toBe(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });
  });
}
