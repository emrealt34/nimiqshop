/**
 * Buying the same cart twice — the storefront half of the 2026-10-18 rules
 * (the backend half lives in backend/cmd/server/rebuy_test.go):
 *
 *   • a handed-back LIVE, unpaid invoice of the same cart (`reused: true`)
 *     opens the "you already have an open order" question — continue with
 *     it, or open a new one (ack + fresh Idempotency-Key) — never the old
 *     pay screen by surprise;
 *   • a handed-back PAID order opens the "already paid" screen;
 *   • a genuinely new order goes straight to the pay screen;
 *   • the cart-derived checkout key is spent once its order is settled, so
 *     the next purchase of the same cart goes out under a FRESH key — while
 *     an unresolved order keeps its key (the retry contract).
 *
 * The API is mocked (see support/fixtures.ts); only POST /api/quotes[/batch]
 * and GET /api/quotes/{id} are answered here, per scenario.
 */
import type { Page, Route } from '@playwright/test';
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const QUOTES_POST = /\/api\/quotes(?:\/batch)?(?:\?|$)/;
const inMinutes = (m: number) => new Date(Date.now() + m * 60_000).toISOString();
// Offline fixture: valid checksum and amount, zero signature; cannot be paid.
const INVOICE = 'lnbc120000000p1qqqqqqqpp5qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqxqrpcgqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqkzf7d6';

/** A quote body as POST /api/quotes answers it (writeQuoteCreatedExtra). */
function quoteBody(id: string, extra: Record<string, unknown> = {}) {
  return {
    quote_id: id, status: 'awaiting_payment', can_pay: true, test_mode: false, test_pay: false,
    lightning_invoice: INVOICE,
    payment_expires_at: inMinutes(14.5), expires_at: inMinutes(14.5), payment_handoff_at: null,
    coin: 'BTC', coin_amount: '0.00100000', network: '', payment_method: 'nimiq_pay',
    product_id: 'amazon-tr', country: 'TR', product_value: 25, product_currency: 'USD', denomination: '25 USD', quantity: 1,
    customer_email: 'buyer@example.com', phone_number: '', beneficiary_account: 'buyer@example.com',
    supplier_status: 'WaitingForPayment', supplier_order_id: 'sup-' + id, powered_by: 'cryptorefills',
    payment_observed: false, payment_blocked: false, cashback_bps: 0, cashback_percent: 0,
    ...extra,
  };
}

type Call = { key: string; body: Record<string, any>; url: string };

/**
 * Answer every checkout POST from `answers` in order (the last one repeats)
 * and record what the storefront sent. GET /api/quotes/{id} answers with the
 * `lookups` body for that id (default: a live, unresolved order).
 */
async function mockCheckout(page: Page, answers: Record<string, unknown>[], lookups: Record<string, Record<string, unknown>> = {}) {
  const calls: Call[] = [];
  await page.route(QUOTES_POST, async (route: Route) => {
    if (route.request().method() !== 'POST') return route.fallback();
    calls.push({ key: route.request().headers()['idempotency-key'] || '', body: route.request().postDataJSON(), url: route.request().url() });
    const json = answers[Math.min(calls.length, answers.length) - 1];
    await route.fulfill({ status: 201, json });
  });
  await page.route(/\/api\/quotes\/[^/?]+(?:\?|$)/, async (route: Route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop() || '');
    const json = lookups[id] || { quote: quoteBody(id), can_pay: true, checkout_settled: false };
    await route.fulfill({ status: 200, json });
  });
  return calls;
}

function seedCart(page: Page, count: number) {
  return page.addInitScript((n: number) => {
    localStorage.setItem('nimshop_cart', JSON.stringify(Array.from({ length: n }, (_, i) => ({
      id: i ? 'steam-tr' : 'amazon-tr', name: i ? 'Steam' : 'Amazon', type: 'giftcard', country: 'TR',
      currency: 'USD', pkg: 'USD25', value: 25, denomination: '25 USD', unitUSD: 25, qty: 1,
      brand: i ? 'Steam' : 'Amazon', category: 'giftcard', delivery_type: 'by_email',
    }))));
  }, count);
}

/** Cart → delivery details → "Continue to payment". */
async function startCheckout(page: Page) {
  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();
}

const prompt = (page: Page) => page.getByTestId('live-duplicate');
const payScreen = (page: Page) => page.getByText(/Waiting for Nimiq Pay/i).first();

for (const count of [1, 2]) {
  const kind = count === 1 ? 'single item' : `${count}-item cart`;

  test(`${kind}: a live open order of the same cart is a question, and "continue" pays THAT order`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await seedCart(page, count);
    const calls = await mockCheckout(page, [quoteBody('q-live', { reused: true, payment_expires_at: inMinutes(14.5) })]);
    await startCheckout(page);

    await expect(prompt(page)).toBeVisible();
    await expect(prompt(page)).toContainText('You already have an open order for this');
    await expect(prompt(page)).toContainText('15 min');
    await expect(prompt(page)).toContainText('q-live');
    await expect(payScreen(page)).toBeHidden();
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toMatch(count === 1 ? /\/api\/quotes(?:\?|$)/ : /\/api\/quotes\/batch/);

    await page.getByRole('button', { name: /Continue with the open order/i }).click();
    await expect(payScreen(page)).toBeVisible();
    await expect(prompt(page)).toBeHidden();
    expect(calls, 'continuing with the open order creates nothing').toHaveLength(1);
  });

  test(`${kind}: "open a new order" re-creates under the buyer's ack and a FRESH key`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await seedCart(page, count);
    const calls = await mockCheckout(page, [quoteBody('q-live', { reused: true }), quoteBody('q-new')]);
    await startCheckout(page);

    await expect(prompt(page)).toBeVisible();
    await page.getByRole('button', { name: /Open a new order instead/i }).click();
    await expect(payScreen(page)).toBeVisible();
    await expect(prompt(page)).toBeHidden();

    expect(calls).toHaveLength(2);
    const [first, second] = calls;
    expect(first.body.ack_active_checkout ?? false, 'the first attempt is an ordinary checkout').toBe(false);
    expect(second.body.ack_active_checkout, 'the new order carries the buyer\'s acknowledgement').toBe(true);
    expect(second.key, 'the new order never reuses the key bound to the live order').not.toBe(first.key);
    expect(second.key.length).toBeGreaterThanOrEqual(16);
    const rows = count === 1 ? [second.body] : second.body.items;
    expect(rows).toHaveLength(count);
    expect(rows[0].product_id).toBe('amazon-tr');
  });

  test(`${kind}: a genuinely new order opens the pay screen without any question`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await seedCart(page, count);
    const calls = await mockCheckout(page, [quoteBody('q-fresh')]);
    await startCheckout(page);
    await expect(payScreen(page)).toBeVisible();
    await expect(prompt(page)).toHaveCount(0);
    expect(calls).toHaveLength(1);
  });
}

test('a handed-back order that is already paid shows the "already paid" screen, never a pay screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await seedCart(page, 1);
  const calls = await mockCheckout(page, [quoteBody('q-paid', { reused: true, status: 'payment_received', can_pay: false })]);
  await startCheckout(page);
  await expect(page.getByText('You already paid this order')).toBeVisible();
  await expect(page.getByRole('link', { name: /Open that order/i })).toHaveAttribute('href', /id=q-paid/);
  await expect(payScreen(page)).toHaveCount(0);
  await expect(prompt(page)).toHaveCount(0);
  expect(calls).toHaveLength(1);
});

test('the back-to-cart button on the question closes the checkout and creates nothing', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await seedCart(page, 1);
  const calls = await mockCheckout(page, [quoteBody('q-live', { reused: true })]);
  await startCheckout(page);
  await expect(prompt(page)).toBeVisible();
  await page.getByRole('button', { name: /Back to cart/i }).click();
  await expect(prompt(page)).toHaveCount(0);
  await expect(page.locator('.btn-gold.btn-block.btn-lg')).toBeVisible();
  expect(calls).toHaveLength(1);
});

test('the cart-derived checkout key is spent once its order is settled, and kept while it is open', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await seedCart(page, 1);
  // First purchase: a fresh order. The storefront stores key K1 → q-first.
  const calls = await mockCheckout(page, [quoteBody('q-first'), quoteBody('q-second'), quoteBody('q-third')], {
    'q-first': { quote: quoteBody('q-first', { status: 'fulfilled', can_pay: false }), can_pay: false, checkout_settled: true },
    'q-second': { quote: quoteBody('q-second'), can_pay: true, checkout_settled: false },
  });
  await startCheckout(page);
  await expect(payScreen(page)).toBeVisible();
  expect(calls).toHaveLength(1);
  const k1 = calls[0].key;
  expect(k1.length).toBeGreaterThanOrEqual(16);

  // The buyer comes back for the same cart after q-first was delivered
  // (GET says checkout_settled). The same cart must NOT go out under K1:
  // that is the key of a finished order, and replaying it is how a buyer
  // landed on a completed order page instead of a pay screen.
  await startCheckout(page);
  await expect(payScreen(page)).toBeVisible();
  expect(calls).toHaveLength(2);
  expect(calls[1].key).not.toBe(k1);
  const k2 = calls[1].key;

  // Same cart again while q-second is still OPEN: a retry, same key K2.
  await startCheckout(page);
  await expect(payScreen(page)).toBeVisible();
  expect(calls).toHaveLength(3);
  expect(calls[2].key, 'an unresolved order keeps its key — the retry contract').toBe(k2);
});
