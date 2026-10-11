import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const kind of ['order', 'quote'] as const) {
  for (const width of [390, 1280]) {
    test(`selecting all five star values sends integer ${kind} ratings at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 });
      await page.addInitScript(({ kind }) => localStorage.setItem(`nimshop.rate-later:${kind}:rating-test`, '1'), { kind });
      const purchase = { id: 'rating-test', status: kind === 'quote' ? 'fulfilled' : 'delivered', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), rating: 0,
        product_id: 'Amazon', kind: 'giftcard', quantity: 1, price_usd: 25, payload: { product_name: 'Amazon', country: 'TR', value: 25, currency: 'USD' } };
      await page.route(`**/api/${kind === 'quote' ? 'quotes' : 'orders'}/rating-test`, (route) => route.fulfill({ json: purchase }));
      await page.route('**/api/ratings/config', (route) => route.fulfill({ json: { enabled: true, comments_enabled: true, max_ratings: 5, max_comment: 62 } }));
      const received: number[] = [];
      let release: (() => void) | undefined;
      await page.route('**/rating/intent', async (route) => {
        received.push(route.request().postDataJSON().stars);
        await new Promise<void>((resolve) => { release = resolve; });
        // Stop before a real wallet transaction; exercise the request and retry.
        await route.fulfill({ status: 503, json: { error: 'star ratings are not available right now' } });
      });
      await open(page, path(`/order?id=rating-test${kind === 'quote' ? '&type=quote' : ''}`));
      const form = page.locator('.rating-flow');
      await expect(form).toBeVisible();
      const submit = form.locator('button.btn-gold');
      await expect(submit).toBeDisabled();
      for (let stars = 1; stars <= 5; stars++) {
        await form.locator('.star-btn').nth(stars - 1).click();
        await expect(form.locator('.stars.picker')).toHaveCount(1);
        await expect(form.locator('.stars:not(.picker)')).toHaveCount(0);
        await expect(submit).toBeEnabled();
        await submit.click();
        await expect.poll(() => received.length).toBe(stars);
        expect(received[stars - 1]).toBe(stars);
        await expect(submit).toBeDisabled();
        await expect(form.locator('.star-btn').first()).toBeDisabled();
        release?.();
        await expect(submit).toBeEnabled();
      }
    });
  }
}

for (const lang of ['en', 'tr'] as const) {
  test.describe(`rating recovery ${lang}`, () => {
    test.use({ lang });
    test('legacy pending strings resume with integer stars; star errors are localized', async ({ page }) => {
      await page.addInitScript(() => {
        localStorage.setItem('nimshop.rate-later:order:o1', '1');
        sessionStorage.setItem('nimshop.pendingRating', JSON.stringify({ kind: 'order', id: 'o1', stars: '5', comment: 'good', ts: Date.now() }));
      });
      await page.route('**/api/ratings/config', (route) => route.fulfill({ json: { enabled: true, comments_enabled: true } }));
      const received: unknown[] = [];
      await page.route('**/api/orders/o1/rate', (route) => {
        received.push(route.request().postDataJSON().stars);
        return route.fulfill({ status: 400, json: { error: 'star must be 1 to 5 integer' } });
      });
      await open(page, path('/order?id=o1'));
      await expect(page.locator('.rating-flow [role="alert"]')).toHaveText(lang === 'tr' ? 'Önce 1 ile 5 arasında yıldız seçin.' : 'Choose 1 to 5 stars first.');
      expect(received).toEqual([5]);
      expect(await page.evaluate(() => sessionStorage.getItem('nimshop.pendingRating'))).toBeNull();
    });
    test('invalid pending stars do not trigger an API submission', async ({ page, requests }) => {
      await page.addInitScript(() => {
        localStorage.setItem('nimshop.rate-later:order:o1', '1');
        sessionStorage.setItem('nimshop.pendingRating', JSON.stringify({ kind: 'order', id: 'o1', stars: 2.5, comment: '', ts: Date.now() }));
      });
      await page.route('**/api/ratings/config', (route) => route.fulfill({ json: { enabled: true, comments_enabled: true } }));
      await open(page, path('/order?id=o1'));
      await expect(page.locator('.rating-flow button.btn-gold')).toBeDisabled();
      expect(requests.filter((r) => /\/rate$|\/rating\/intent$/.test(r))).toEqual([]);
      expect(await page.evaluate(() => sessionStorage.getItem('nimshop.pendingRating'))).toBeNull();
    });
  });
}
