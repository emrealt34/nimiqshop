import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const width of [320, 1280]) {
  test.describe(`payment metadata recovery ${width}px`, () => {
    test.use({ viewport: { width, height: 900 } });
    test('reload recovers unsaved identifiers without another wallet or checkout request', async ({ page, requests }) => {
      const key = 'nimshop.payment-submission.v1:u1:old-payment';
      const other = 'nimshop.payment-submission.v1:u2:other-payment';
      await page.addInitScript(({ key, other }) => {
        if (localStorage.getItem('e2e.payment.seeded')) return;
        localStorage.setItem('e2e.payment.seeded', 'yes');
        localStorage.setItem(key, JSON.stringify({ owner: 'u1', id: 'old-payment', hash: 'ab'.repeat(32), swapId: 'retained-swap', recordedAt: Date.now() - 86400000 }));
        localStorage.setItem(other, JSON.stringify({ owner: 'u2', id: 'other-payment', hash: 'cd'.repeat(32), swapId: 'other-private-swap', recordedAt: Date.now() }));
      }, { key, other });
      let available = false;
      const writes: unknown[] = [];
      await page.route('**/api/quotes/old-payment/lightning-proof', route => {
        writes.push(route.request().postDataJSON());
        return route.fulfill({ status: available ? 200 : 503, json: available ? { saved: true } : { error: 'temporarily unavailable' } });
      });
      await open(page, path('/orders'));
      await expect.poll(() => writes.length).toBeGreaterThan(0);
      expect(await page.evaluate(key => !!localStorage.getItem(key), key)).toBe(true);
      available = true;
      await page.reload();
      await expect.poll(() => page.evaluate(key => localStorage.getItem(key), key)).toBe(null);
      expect(await page.evaluate(key => !!localStorage.getItem(key), other)).toBe(true);
      expect(writes.at(-1)).toEqual({ hash: 'ab'.repeat(32), swap_id: 'retained-swap' });
      expect(requests.filter(url => /payment-launch|quotes\/other-payment|quotes\/create/.test(url))).toHaveLength(0);
    });
  });
}

test('a submitted review survives a day and reload without asking the wallet to pay again', async ({ page, requests }) => {
  await page.addInitScript(() => {
    localStorage.setItem('nimshop.pendingRating:u1:order:o1', JSON.stringify({ kind: 'order', id: 'o1', owner_uid: 'u1', stars: 5, comment: 'excellent', submitted: true, hash: 'ef'.repeat(32), ts: Date.now() - 86400000 }));
  });
  await page.route('**/api/ratings/config', route => route.fulfill({ json: { enabled: true, comments_enabled: true } }));
  const bodies: any[] = [];
  await page.route('**/api/orders/o1/rate', route => {
    bodies.push(route.request().postDataJSON());
    return route.fulfill({ json: { rating: 5, comment: 'excellent', rating_tx: 'ef'.repeat(32) } });
  });
  await open(page, path('/order?id=o1'));
  await expect.poll(() => bodies.length).toBeGreaterThan(0);
  expect(bodies[0]).toEqual({ stars: 5, comment: 'excellent', tx_hash: 'ef'.repeat(32) });
  await expect.poll(() => page.evaluate(() => localStorage.getItem('nimshop.pendingRating:u1:order:o1'))).toBe(null);
  expect(requests.filter(url => url.endsWith('rating/intent'))).toHaveLength(0);
});
