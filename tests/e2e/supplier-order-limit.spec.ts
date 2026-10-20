/**
 * The supplier's per-order ceiling on Lightning orders — storefront half of
 * the 2026-10-20 live bug (backend half: backend/cmd/server/order_limit_test.go).
 *
 * Amazon.fr from France: the page offered €5–€445, the buyer chose €200,
 * typed the email, and the LAST step answered "Cryptorefills could not
 * approve this request" — the payment partner caps the order total at
 * €100 per order (LIGHTNING_ORDER_LIMIT_EXCEEDED). Now:
 *
 *   • the range stops at the real maximum and SAYS why (cap note);
 *   • the shop's own refusal (403 SUPPLIER_ORDER_LIMIT) names the ceiling
 *     and the product maximum;
 *   • the supplier's own refusal, should it still arrive, is phrased with
 *     ITS numbers instead of the generic "contact support" sentence.
 *
 * The API is mocked (support/fixtures.ts); the checkout POST is answered
 * here per scenario.
 */
import type { Page, Route } from '@playwright/test';
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const QUOTES_POST = /\/api\/quotes(?:\/batch)?(?:\?|$)/;

/** GET /api/catalog/products/amazon-fr: one range product, capped by the backend. */
async function mockAmazonFr(page: Page, cap: boolean) {
  const range: Record<string, unknown> = { min: 5, max: cap ? 95 : 445, step_size: 1, currency: 'EUR' };
  if (cap) range.cap = { reason: 'supplier_order_limit', total_eur: 100, supplier_max: 2000 };
  await page.route('**/api/catalog/products/**', r => r.fulfill({ json: [{
    family: 'Amazon.fr', brand: 'Amazon.fr', brand_id: 'b-amz-fr', kind: 'giftcard', country_code: 'FR', product_type: 'giftcard',
    products: [{ product_id: 'amz-fr-range', is_dynamic: true, coin: 'BTC', coin_amount: '0.0028', delivery_type: 'by_email', range, face_value: { currency_code: 'EUR', amount: { type: 'range', min: '5', max: String(range.max), step: '1' } } }],
  }] }));
  await page.route('**/api/catalog/unavailable**', r => r.fulfill({ json: { country: 'FR', families: [] } }));
  await page.route('**/api/catalog/price**', r => r.fulfill({ json: { coin: 'BTC', coin_amount: '0.0028', price_expires_at: new Date(Date.now() + 60_000).toISOString() } }));
}

/** Verbatim live answer of 2026-10-20 (EUR block) for Amazon.fr / €200. */
const LIVE_SUPPLIER_REFUSAL = {
  error: 'Cryptorefills could not approve this request.', code: 'SUPPLIER_PROBLEMS', supplier_error: true, upstream_status: 422,
  problems: [{ problem: 'LIGHTNING_ORDER_LIMIT_EXCEEDED', moreDetails: {
    kyc_level: 'NOT_REGISTERED',
    limits: { daily: { amount: 100, currency: 'EUR' }, monthly: { amount: 300, currency: 'EUR' }, per_order: { amount: 100, currency: 'EUR' } },
    next_level_limits: { daily: { amount: 250, currency: 'EUR' }, monthly: { amount: 1000, currency: 'EUR' }, per_order: { amount: 100, currency: 'EUR' } },
    order_total: { amount: 208.0707, currency: 'EUR' }, spent_this_month: { amount: 0, currency: 'EUR' }, spent_today: { amount: 0, currency: 'EUR' },
  } }],
};

const SHOP_REFUSAL = {
  error: 'This product can be ordered for at most 95 EUR per order: the payment partner accepts at most 100 EUR in total per order, including the supplier fee. Choose a lower amount or lower the quantity.',
  code: 'SUPPLIER_ORDER_LIMIT', order_limit: { total_eur: 100, max_face_value: 95, currency: 'EUR' },
};

function seedCart(page: Page, value: number) {
  return page.addInitScript((v: number) => {
    localStorage.setItem('nimshop_cart', JSON.stringify([{
      id: 'Amazon.fr', name: 'Amazon.fr', type: 'giftcard', country: 'FR', currency: 'EUR', pkg: 'range', value: v,
      denomination: 'range', unitUSD: v * 1.08, qty: 1, brand: 'Amazon.fr', brand_id: 'b-amz-fr', category: 'giftcard', delivery_type: 'by_email',
    }]));
  }, value);
}

async function refuseCheckout(page: Page, status: number, json: Record<string, unknown>) {
  const calls: Record<string, any>[] = [];
  await page.route(QUOTES_POST, async (route: Route) => {
    if (route.request().method() !== 'POST') return route.fallback();
    calls.push(route.request().postDataJSON());
    await route.fulfill({ status, json });
  });
  return calls;
}

async function startCheckout(page: Page) {
  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();
}

test.describe('supplier per-order ceiling', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('the product page stops the range at the real maximum and explains it', async ({ page }) => {
    await mockAmazonFr(page, true);
    await open(page, path('/product?id=Amazon.fr&country=FR'));
    const note = page.getByTestId('range-cap-note');
    await expect(note).toBeVisible();
    await expect(note).toContainText('€95');
    await expect(note).toContainText('€100');
    await expect(page.locator('input[type="range"]')).toHaveAttribute('max', '95');
    await expect(page.getByText(/between €5 and €95/)).toBeVisible();
  });

  test('without a cap the range is the supplier range and there is no note', async ({ page }) => {
    await mockAmazonFr(page, false);
    await open(page, path('/product?id=Amazon.fr&country=FR'));
    await expect(page.locator('input[type="range"]')).toHaveAttribute('max', '445');
    await expect(page.getByTestId('range-cap-note')).toHaveCount(0);
  });

  test('a stale page submitting €200 reads the ceiling and the product maximum, not "contact support"', async ({ page }) => {
    await seedCart(page, 200);
    const calls = await refuseCheckout(page, 403, SHOP_REFUSAL);
    await startCheckout(page);
    const paused = page.getByText(/Checkout paused/i).first();
    await expect(paused).toBeVisible();
    const screen = page.locator('.center', { has: paused });
    await expect(screen).toContainText('€100');
    await expect(screen).toContainText('€95');
    await expect(screen).not.toContainText(/could not approve/i);
    await expect(page.getByText(/Waiting for Nimiq Pay/i)).toHaveCount(0);
    expect(calls).toHaveLength(1);
    expect(calls[0].product_value).toBe(200);
    // A per-order ceiling is deterministic: no "reopen / retry" button, the
    // primary action is the cart with a lower amount, and the sentence ends once.
    await expect(page.getByRole('button', { name: /Reopen the existing/i })).toHaveCount(0);
    const back = page.getByTestId('order-limit-back-to-cart');
    await expect(back).toBeVisible();
    await expect(back).toHaveClass(/btn-gold/);
    await expect(back).toContainText(/lower the amount/i);
    await expect(screen).toContainText(/nothing was charged/i);
    expect((await screen.innerText()).includes('..')).toBe(false);
    await back.click();
    await expect(page.getByText(/Checkout paused/i)).toHaveCount(0);
  });

  test("the supplier's own refusal is phrased with its numbers", async ({ page }) => {
    await seedCart(page, 200);
    await refuseCheckout(page, 409, LIVE_SUPPLIER_REFUSAL);
    await startCheckout(page);
    const notice = page.getByRole('alert');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText('€100');
    await expect(notice).toContainText('€208.07');
    await expect(notice).toContainText('€300');
    await expect(notice).toContainText(/did not approve this payment/i);
    await expect(page.getByTestId('supplier-reason-code')).toContainText('LIGHTNING_ORDER_LIMIT_EXCEEDED');
    await expect(notice).not.toContainText(/could not approve|contact support before/i);
    await expect(page.getByText(/Waiting for Nimiq Pay/i)).toHaveCount(0);
  });
});
