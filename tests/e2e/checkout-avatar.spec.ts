import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const count of [1, 2]) {
  test(`${count}-item ordinary purchase waits for the wallet PNG before creating quotes`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.addInitScript((n) => {
      localStorage.setItem('nimshop_cart', JSON.stringify(Array.from({ length: n }, (_, i) => ({
        id: i ? 'steam-tr' : 'amazon-tr', name: i ? 'Steam' : 'Amazon', type: 'giftcard', country: 'TR',
        currency: 'USD', pkg: 'USD25', value: 25, denomination: '25 USD', unitUSD: 25, qty: 1,
        brand: i ? 'Steam' : 'Amazon', category: 'giftcard', delivery_type: 'by_email',
      }))));
    }, count);
    let release: (() => void) | undefined;
    const libraryReady = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/vendor/identicons.module.js', async (route) => {
      await libraryReady;
      await route.continue();
    });
    const received: Record<string, any>[] = [];
    await page.route(/\/api\/quotes(?:\/batch)?(?:\?|$)/, async (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      received.push(route.request().postDataJSON());
      // Stop before creating an invoice or launching a real wallet payment.
      await route.fulfill({ status: 503, json: { error: 'test checkout stop' } });
    });
    try {
      await open(page, path('/cart'));
      await page.locator('.btn-gold.btn-block.btn-lg').click();
      await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
      await page.locator('#cdp-my-email-again').fill('buyer@example.com');
      await page.getByRole('button', { name: /Continue to payment/i }).click();
      await expect(page.getByText(/Preparing/i).first()).toBeVisible();
      expect(received).toHaveLength(0);
      release?.();
      await expect.poll(() => received.length).toBe(1);
      const rows = count === 1 ? received : received[0].items;
      expect(rows).toHaveLength(count);
      expect(rows[0].gifter_identicon).toMatch(/^data:image\/png;base64,iVBOR/);
      expect(Buffer.from(rows[0].gifter_identicon.split(',')[1], 'base64').length).toBeLessThanOrEqual(48 * 1024);
      expect(rows[0].gift_channel).toBeUndefined();
      if (count === 2) expect(rows[1].gifter_identicon).toBeUndefined();
    } finally {
      release?.();
    }
  });
}
