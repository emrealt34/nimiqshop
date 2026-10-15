import { test, expect, open, ADDRESS } from './support/fixtures';
import { path } from './support/data';

const hash = 'ab'.repeat(32);
const pendingKey = 'nimshop.pendingRating:u1:order:o1';

for (const status of [202, 503]) test(`Nimiq Pay hash survives delayed confirmation (${status})`, async ({ page }) => {
  await page.addInitScript(({ hash, ADDRESS }) => {
    (window as any).nimiq = {
      listAccounts: async () => [ADDRESS],
      sendBasicTransactionWithData: async () => {
        localStorage.setItem('e2e.rating.payments', String(Number(localStorage.getItem('e2e.rating.payments') || 0) + 1));
        return hash.toUpperCase();
      },
    };
  }, { hash, ADDRESS });
  await page.route('**/api/ratings/config', r => r.fulfill({ json: { enabled: true, comments_enabled: true } }));
  await page.route('**/rating/intent', r => r.fulfill({ json: { memo: '5 excellent', recipient: ADDRESS, value_luna: 1, fee_luna: 1 } }));
  const bodies: any[] = [];
  await page.route('**/api/orders/o1/rate', r => {
    bodies.push(r.request().postDataJSON());
    return r.fulfill(bodies.length <= 4 ? { status, json: status === 202 ? { status: 'pending' } : { error: 'temporarily unavailable' } }
      : { json: { rating: 5, comment: 'excellent', rating_tx: hash } });
  });
  await open(page, path('/order?id=o1'));
  const form = page.locator('.rating-flow');
  await form.locator('.star-btn').last().click();
  await form.locator('input').fill('excellent');
  await form.locator('button.btn-gold').click();
  await expect.poll(() => bodies.length).toBeGreaterThanOrEqual(1);
  expect(await page.evaluate(key => JSON.parse(localStorage.getItem(key)!).hash, pendingKey)).toBe(hash);
  await expect(form.locator('input')).toBeDisabled();
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), pendingKey), { timeout: 20_000 }).toBe(null);
  expect(bodies).toHaveLength(5);
  expect(bodies.every(body => body.tx_hash === hash && body.stars === 5 && body.comment === 'excellent')).toBe(true);
  expect(await page.evaluate(() => localStorage.getItem('e2e.rating.payments'))).toBe('1');
});

test('unconfirmed review offers confirmation-only retry and survives reload', async ({ page, requests }) => {
  test.setTimeout(90_000);
  await page.addInitScript(({ hash, pendingKey }) => {
    if (localStorage.getItem('e2e.rating.seeded')) return;
    localStorage.setItem('e2e.rating.seeded', '1');
    localStorage.setItem(pendingKey, JSON.stringify({ kind: 'order', id: 'o1', owner_uid: 'u1', stars: 5, comment: 'excellent', hash, submitted: true, ts: Date.now() }));
  }, { hash, pendingKey });
  await page.route('**/api/ratings/config', r => r.fulfill({ json: { enabled: true, comments_enabled: true } }));
  let confirmed = false;
  let calls = 0;
  await page.route('**/api/orders/o1/rate', r => {
    calls++;
    return r.fulfill({ status: confirmed ? 200 : 202, json: confirmed ? { rating: 5, comment: 'excellent', rating_tx: hash } : { status: 'pending' } });
  });
  await open(page, path('/order?id=o1'));
  const form = page.locator('.rating-flow');
  await expect(form.getByRole('button', { name: 'Check confirmation' })).toBeEnabled({ timeout: 75_000 });
  await expect(form.locator('input')).toBeDisabled();
  expect(calls).toBe(30);
  expect(await page.evaluate(key => !!localStorage.getItem(key), pendingKey)).toBe(true);
  confirmed = true;
  await page.reload();
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(null);
  expect(requests.filter(url => url.endsWith('/rating/intent'))).toHaveLength(0);
});

test('invalid acknowledgement retains review and retries without another wallet payment', async ({ page }) => {
  await page.addInitScript(({ hash, pendingKey }) => {
    localStorage.setItem(pendingKey, JSON.stringify({ kind: 'order', id: 'o1', owner_uid: 'u1', stars: 5, comment: 'excellent', hash, submitted: true, ts: Date.now() }));
  }, { hash, pendingKey });
  await page.route('**/api/ratings/config', r => r.fulfill({ json: { enabled: true, comments_enabled: true } }));
  let confirmed = false;
  await page.route('**/api/orders/o1/rate', r => r.fulfill({ json: confirmed ? { rating: 5, comment: 'excellent', rating_tx: hash } : { rating: 1, comment: 'wrong', rating_tx: hash } }));
  await open(page, path('/order?id=o1'));
  const retry = page.locator('.rating-flow').getByRole('button', { name: 'Check confirmation' });
  await expect(retry).toBeEnabled();
  expect(await page.evaluate(key => !!localStorage.getItem(key), pendingKey)).toBe(true);
  confirmed = true;
  await retry.click();
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(null);
});

test('uncertain wallet submission checks the known hash without requesting payment again', async ({ page }) => {
  await page.addInitScript(({ hash, ADDRESS }) => {
    (window as any).nimiq = {
      listAccounts: async () => [ADDRESS],
      sendBasicTransactionWithData: async () => {
        localStorage.setItem('e2e.rating.payments', String(Number(localStorage.getItem('e2e.rating.payments') || 0) + 1));
        throw { type: 'TRANSACTION_OUTCOME_UNKNOWN', data: { hash } };
      },
    };
  }, { hash, ADDRESS });
  await page.route('**/api/ratings/config', r => r.fulfill({ json: { enabled: true, comments_enabled: true } }));
  await page.route('**/rating/intent', r => r.fulfill({ json: { memo: '5 excellent', recipient: ADDRESS, value_luna: 1, fee_luna: 1 } }));
  const bodies: any[] = [];
  await page.route('**/api/orders/o1/rate', r => {
    bodies.push(r.request().postDataJSON());
    return r.fulfill({ json: { rating: 5, comment: 'excellent', rating_tx: hash } });
  });
  await open(page, path('/order?id=o1'));
  const form = page.locator('.rating-flow');
  await form.locator('.star-btn').last().click();
  await form.locator('input').fill('excellent');
  await form.locator('button.btn-gold').click();
  const retry = form.getByRole('button', { name: 'Check confirmation' });
  await expect(retry).toBeEnabled();
  expect(await page.evaluate(key => JSON.parse(localStorage.getItem(key)!).hash, pendingKey)).toBe(hash);
  await retry.click();
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(null);
  expect(bodies).toEqual([{ stars: 5, comment: 'excellent', tx_hash: hash }]);
  expect(await page.evaluate(() => localStorage.getItem('e2e.rating.payments'))).toBe('1');
});

test('explicit wallet cancellation releases only its server rating intent', async ({ page }) => {
  await page.addInitScript(({ ADDRESS }) => {
    (window as any).nimiq = { listAccounts: async () => [ADDRESS], sendBasicTransactionWithData: async () => { throw { type: 'PERMISSION_DENIED' }; } };
  }, { ADDRESS });
  await page.route('**/api/ratings/config', r => r.fulfill({ json: { enabled: true, comments_enabled: true } }));
  const cancelled: any[] = [];
  await page.route('**/rating/intent', r => {
    if (r.request().method() === 'DELETE') {
      cancelled.push(r.request().postDataJSON());
      return r.fulfill({ json: { cancelled: true } });
    }
    return r.fulfill({ json: { memo: '5 excellent', recipient: ADDRESS, value_luna: 1, fee_luna: 1 } });
  });
  await open(page, path('/order?id=o1'));
  const form = page.locator('.rating-flow');
  await form.locator('.star-btn').last().click();
  await form.locator('input').fill('excellent');
  await form.locator('button.btn-gold').click();
  await expect(form.getByRole('alert')).toHaveText('Cancelled in your wallet. Nothing was charged.');
  expect(cancelled).toEqual([{ stars: 5, comment: 'excellent' }]);
  expect(await page.evaluate(key => localStorage.getItem(key), pendingKey)).toBe(null);
});
