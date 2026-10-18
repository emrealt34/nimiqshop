/**
 * FULL-STACK rebuy scenarios — real storefront build, real browser, and the
 * REAL backend (handlers, Badger, idempotency gate, settlement tracker, the
 * in-process supplier mock, TEST_MODE off). No API mocks: every /api call the
 * page makes is forwarded to the backend with the buyer's bearer token.
 *
 * One buyer, one cart (test-airbnb 25 USD), six steps in order:
 *
 *   1. first purchase → a new, payable order A
 *   2. same cart while A is live → the question (reused A); "continue" pays A
 *   3. same cart again → "open a new order" → a second live order B beside A
 *   4. A paid and delivered (supplier mock) → same cart → the question again,
 *      now about B (a retry of B's own key, without the ack flag); the
 *      finished order is never replayed
 *   5. B paid and delivered → same cart → a brand-new order C, no question
 *   6. a second device (empty storage) while C is live → the question again,
 *      this time under a brand-new key (the same-cart branch of the gate)
 *
 * Skipped unless E2E_BACKEND_INFO points at the connection file written by
 * backend/cmd/server TestServeStackForBrowser. `npm run test:e2e:fullstack`
 * boots the stack, runs this file and shuts the stack down.
 */
import { existsSync, readFileSync } from 'node:fs';
import type { Page, Route } from '@playwright/test';
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

type StackInfo = { base_url: string; supplier_url: string; bearer: string; uid: string; address: string };
const INFO_PATH = process.env.E2E_BACKEND_INFO || '';
const info: StackInfo | null = INFO_PATH && existsSync(INFO_PATH) ? JSON.parse(readFileSync(INFO_PATH, 'utf8')) : null;

test.skip(!info, 'set E2E_BACKEND_INFO to the file written by TestServeStackForBrowser (npm run test:e2e:fullstack)');
test.describe.configure({ mode: 'serial' });

type Call = { url: string; key: string; body: Record<string, any>; status: number; json: Record<string, any> };

/** Forward every API request to the real backend as the signed-in buyer. */
async function forwardApi(page: Page, calls: Call[]) {
  await page.route(/\/api\//, async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const headers: Record<string, string> = { ...req.headers() };
    for (const h of ['origin', 'referer', 'cookie', 'host', 'content-length']) delete headers[h];
    headers.authorization = 'Bearer ' + info!.bearer;
    const res = await route.fetch({ url: info!.base_url + url.pathname + url.search, headers });
    if (req.method() === 'POST' && /\/api\/quotes(?:\/batch)?$/.test(url.pathname)) {
      let json: Record<string, any> = {};
      try { json = JSON.parse((await res.text()) || '{}'); } catch { /* keep {} */ }
      calls.push({ url: url.pathname, key: req.headers()['idempotency-key'] || '', body: req.postDataJSON(), status: res.status(), json });
      await route.fulfill({ status: res.status(), headers: res.headers(), json });
      return;
    }
    await route.fulfill({ response: res });
  });
}

// A cart line exactly as the storefront stores one for the supplier mock's
// test-airbnb family (brand == family, brand_id == "mock-" + family).
const CART = [{ id: 'test-airbnb', name: 'test-airbnb', type: 'giftcard', country: 'US', currency: 'USD', value: 25, denomination: 'range', unitUSD: 25, qty: 1, brand: 'test-airbnb', brand_id: 'mock-test-airbnb', category: 'travel_flights', delivery_type: 'by_email' }];

async function prepare(page: Page) {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.addInitScript(({ i, cart }) => {
    // The real session: the owner scope of the stored checkout key is the
    // backend's user id, exactly as in production after bootstrapSession.
    localStorage.setItem('nimshop.sess', JSON.stringify({ uid: i.uid, address: i.address, expiresAt: Math.floor(Date.now() / 1000) + 86400 }));
    localStorage.setItem('nimshop.addr', i.address);
    localStorage.setItem('nimshop_cart', JSON.stringify(cart));
  }, { i: info!, cart: CART });
  const calls: Call[] = [];
  await forwardApi(page, calls);
  return calls;
}

async function startCheckout(page: Page) {
  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();
}

const prompt = (page: Page) => page.getByTestId('live-duplicate');
const payScreen = (page: Page) => page.getByText(/Waiting for Nimiq Pay/i).first();

/* ---- direct backend access (Node side), as the same buyer ---- */
async function api(method: string, p: string, body?: unknown) {
  const res = await fetch(info!.base_url + '/api' + p, {
    method, headers: { authorization: 'Bearer ' + info!.bearer, 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json: any = {};
  try { json = JSON.parse(text || '{}'); } catch { json = { raw: text }; }
  return { status: res.status, json };
}
const quote = async (id: string) => (await api('GET', `/quotes/${encodeURIComponent(id)}`)).json;
const statusOf = (q: any) => String(q?.quote?.status || q?.status || '');

/** Pay and deliver an order through the supplier mock, then wait for the shop to settle it. */
async function payAndDeliver(id: string, supplierOrderId: string) {
  for (const action of ['pay', 'advance']) {
    const res = await fetch(`${info!.supplier_url}/mock/orders/${encodeURIComponent(supplierOrderId)}/${action}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
    expect(res.status, `supplier mock ${action}`).toBe(200);
  }
  await expect.poll(async () => {
    await api('POST', `/quotes/${encodeURIComponent(id)}/refresh`);
    return statusOf(await quote(id));
  }, { timeout: 30_000, intervals: [250, 500, 1000] }).toBe('fulfilled');
}

let liveC: { id: string; key: string } | null = null;

test('the rebuy lifecycle of one cart on the real backend (same device)', async ({ page }) => {
  test.setTimeout(180_000);
  const calls = await prepare(page);
  const ids = { A: '', B: '', C: '' };
  const sids = { A: '', B: '' };
  const keys = { first: '', ack: '', third: '' };
  const lastCall = () => calls[calls.length - 1];

  await test.step('1 · the first purchase opens a new, payable order', async () => {
    await startCheckout(page);
    await expect(payScreen(page)).toBeVisible({ timeout: 20_000 });
    await expect(prompt(page)).toHaveCount(0);
    expect(calls).toHaveLength(1);
    const c = lastCall();
    expect(c.status).toBe(201);
    expect(c.json.reused).toBeUndefined();
    expect(c.json.can_pay).toBe(true);
    expect(c.json.status).toBe('awaiting_payment');
    expect(c.body.product_id).toBe('test-airbnb');
    expect(c.body.ack_active_checkout ?? false).toBe(false);
    ids.A = c.json.quote_id;
    sids.A = c.json.supplier_order_id;
    keys.first = c.key;
    expect(ids.A).toBeTruthy();
    expect(sids.A).toBeTruthy();
    expect(keys.first.length).toBeGreaterThanOrEqual(16);
    const q = await quote(ids.A);
    expect(statusOf(q)).toBe('awaiting_payment');
    expect(q.checkout_settled).toBe(false);
  });

  await test.step('2 · the same cart while A is live is a question; "continue" pays A', async () => {
    await startCheckout(page);
    await expect(prompt(page)).toBeVisible({ timeout: 20_000 });
    await expect(prompt(page)).toContainText('You already have an open order for this');
    await expect(prompt(page)).toContainText(/\b([1-9]|[1-5][0-9]|60) min\b/);
    await expect(prompt(page)).toContainText(ids.A);
    expect(calls).toHaveLength(2);
    const c = lastCall();
    expect(c.status).toBe(201);
    expect(c.json.reused).toBe(true);
    expect(c.json.quote_id).toBe(ids.A);
    expect(c.key, 'same device: the stored key of the open order is sent again — the retry contract').toBe(keys.first);

    await page.getByRole('button', { name: /Continue with the open order/i }).click();
    await expect(payScreen(page)).toBeVisible();
    expect(calls, 'continuing creates nothing').toHaveLength(2);
    expect(statusOf(await quote(ids.A))).toBe('awaiting_payment');
  });

  await test.step('3 · "open a new order" creates a second live order B beside A', async () => {
    await startCheckout(page);
    await expect(prompt(page)).toBeVisible({ timeout: 20_000 });
    await page.getByRole('button', { name: /Open a new order instead/i }).click();
    await expect(payScreen(page)).toBeVisible({ timeout: 20_000 });
    await expect(prompt(page)).toBeHidden();
    expect(calls).toHaveLength(4);
    const [handback, created] = calls.slice(2);
    expect(handback.json.reused).toBe(true);
    expect(handback.json.quote_id).toBe(ids.A);
    expect(handback.key).toBe(keys.first);
    expect(created.status).toBe(201);
    expect(created.body.ack_active_checkout).toBe(true);
    expect(created.key).not.toBe(keys.first);
    expect(created.json.reused).toBeUndefined();
    expect(created.json.can_pay).toBe(true);
    expect(created.json.quote_id).not.toBe(ids.A);
    ids.B = created.json.quote_id;
    sids.B = created.json.supplier_order_id;
    keys.ack = created.key;
    // Both orders are live and payable; neither touched the other.
    for (const id of [ids.A, ids.B]) {
      const q = await quote(id);
      expect(statusOf(q)).toBe('awaiting_payment');
      expect(q.can_pay).toBe(true);
      expect(q.checkout_settled).toBe(false);
    }
  });

  await test.step('4 · A paid and delivered → the same cart asks about B, and never replays A', async () => {
    await payAndDeliver(ids.A, sids.A);
    expect((await quote(ids.A)).checkout_settled).toBe(true);

    await startCheckout(page);
    await expect(prompt(page)).toBeVisible({ timeout: 20_000 });
    await expect(prompt(page)).toContainText(ids.B);
    expect(calls).toHaveLength(5);
    const c = lastCall();
    expect(c.json.reused).toBe(true);
    expect(c.json.quote_id, 'the open order B is handed back, not the delivered A').toBe(ids.B);
    // Same device: the cart's scope holds the key of the order opened through
    // the prompt, so this is a retry of B's own key (without the ack flag),
    // never the delivered order's key.
    expect(c.key, "the open order's own key is retried").toBe(keys.ack);
    expect(c.key).not.toBe(keys.first);
    expect(c.body.ack_active_checkout ?? false, 'a plain checkout, no acknowledgement this time').toBe(false);
    keys.third = c.key;

    await page.getByRole('button', { name: /Continue with the open order/i }).click();
    await expect(payScreen(page)).toBeVisible();
    expect(calls).toHaveLength(5);
    expect(statusOf(await quote(ids.A))).toBe('fulfilled');
    expect(statusOf(await quote(ids.B))).toBe('awaiting_payment');
  });

  await test.step('5 · B paid and delivered → the same cart is a brand-new order C, no question', async () => {
    await payAndDeliver(ids.B, sids.B);
    expect((await quote(ids.B)).checkout_settled).toBe(true);

    await startCheckout(page);
    await expect(payScreen(page)).toBeVisible({ timeout: 20_000 });
    await expect(prompt(page)).toHaveCount(0);
    expect(calls).toHaveLength(6);
    const c = lastCall();
    expect(c.status).toBe(201);
    expect(c.json.reused).toBeUndefined();
    expect(c.json.can_pay).toBe(true);
    expect(c.json.quote_id).not.toBe(ids.A);
    expect(c.json.quote_id).not.toBe(ids.B);
    expect(c.key).not.toBe(keys.first);
    expect(c.key).not.toBe(keys.third);
    ids.C = c.json.quote_id;
    expect(statusOf(await quote(ids.A))).toBe('fulfilled');
    expect(statusOf(await quote(ids.B))).toBe('fulfilled');
    expect(statusOf(await quote(ids.C))).toBe('awaiting_payment');
  });
  liveC = { id: ids.C, key: lastCall().key };
});

test('another device (empty storage) while C is live: the question, under a brand-new key', async ({ page }) => {
  test.skip(!liveC, 'needs the lifecycle test to leave order C open');
  const calls = await prepare(page);
  await startCheckout(page);
  await expect(prompt(page)).toBeVisible({ timeout: 20_000 });
  await expect(prompt(page)).toContainText(liveC!.id);
  expect(calls).toHaveLength(1);
  expect(calls[0].status).toBe(201);
  expect(calls[0].json.reused).toBe(true);
  expect(calls[0].json.quote_id).toBe(liveC!.id);
  expect(calls[0].key, 'no stored intent on this device: a new key, same cart → still handed back').not.toBe(liveC!.key);
  await page.getByRole('button', { name: /Continue with the open order/i }).click();
  await expect(payScreen(page)).toBeVisible();
  expect(calls).toHaveLength(1);
  expect(statusOf(await quote(liveC!.id))).toBe('awaiting_payment');
});
