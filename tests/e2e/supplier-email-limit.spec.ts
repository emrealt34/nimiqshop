/**
 * The payment partner's per-EMAIL Lightning budgets — storefront half of the
 * 2026-10-20 Walmart US $104 live bug (backend half:
 * backend/cmd/server/email_limit_test.go).
 *
 * The first $104 order was created and left unpaid; the next attempt with
 * the same delivery email came back LIGHTNING_DAILY_LIMIT_EXCEEDED, a code
 * the storefront did not know, so the buyer read "Cryptorefills could not
 * approve this request. Review the order and contact support". Now:
 *
 *   • the refusal names the email, the budget, the numbers and the buyer's
 *     own unpaid order that is holding the budget;
 *   • the screen offers the two honest ways forward — pay the pending order,
 *     or use a different delivery email (back to the delivery step, with the
 *     reason on it) — and no "contact support";
 *   • the delivery step states the partner's rule before the last step;
 *   • a brand card never promises more than the per-order ceiling allows.
 *
 * The API is mocked (support/fixtures.ts); the checkout POST is answered
 * here per scenario.
 */
import type { Page, Route } from '@playwright/test';
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const QUOTES_POST = /\/api\/quotes(?:\/batch)?(?:\?|$)/;
const EMAIL = 'buyer@example.com';
const OTHER_EMAIL = 'second@example.com';

/** Verbatim live answer of 2026-10-20 (second Walmart $104 with the same
 *  email), plus the `shop` block the backend now appends from its own ledger. */
const LIVE_DAILY_REFUSAL = {
  error: 'The payment partner’s daily Lightning budget for this delivery email is used up.', code: 'SUPPLIER_PROBLEMS', supplier_error: true, upstream_status: 422,
  problems: [{ problem: 'LIGHTNING_DAILY_LIMIT_EXCEEDED', moreDetails: {
    kyc_level: 'NOT_REGISTERED',
    limits: { daily: { amount: 100, currency: 'EUR' }, monthly: { amount: 300, currency: 'EUR' }, per_order: { amount: 100, currency: 'EUR' } },
    next_level_limits: { daily: { amount: 250, currency: 'EUR' }, monthly: { amount: 1000, currency: 'EUR' }, per_order: { amount: 100, currency: 'EUR' } },
    order_total: { amount: 97.6957, currency: 'EUR' }, spent_today: { amount: 97.6957, currency: 'EUR' }, spent_this_month: { amount: 97.6957, currency: 'EUR' },
    usd: { limits: { daily: { amount: 112.434, currency: 'USD' }, monthly: { amount: 337.302, currency: 'USD' }, per_order: { amount: 112.434, currency: 'USD' } }, order_total: { amount: 109.8432, currency: 'USD' } },
    shop: { email: EMAIL, period: 'daily', pending_count: 1, counted_total_usd: 109.8432, orders: [{
      quote_id: '8d6b7a55-e921-482a-8003-bfa229e09a2c', product_id: 'Walmart', country: 'US', denomination: 'range',
      product_value: 104, product_currency: 'USD', quantity: 1, status: 'awaiting_payment',
      created_at: new Date(Date.now() - 5 * 60_000).toISOString(), payment_expiry: new Date(Date.now() + 10 * 60_000).toISOString(),
      total_usd: 109.8432, pending: true,
    }] },
  } }],
};

const LIMITS = { supplier_order_limit: {
  enabled: true, currency: 'EUR', total_eur: 100, daily_eur: 100, monthly_eur: 300, margin_pct: 5,
  face_caps: { USD: 104, EUR: 92, TRY: 4600 }, unpaid_orders_count: true, scope: 'delivery_email',
} };

function seedWalmart(page: Page) {
  return page.addInitScript(() => {
    localStorage.setItem('nimshop_cart', JSON.stringify([{
      id: 'Walmart', name: 'Walmart', type: 'giftcard', country: 'US', currency: 'USD', pkg: 'range', value: 104,
      denomination: 'range', unitUSD: 104, qty: 1, brand: 'Walmart', brand_id: 'b-walmart', category: 'giftcard', delivery_type: 'by_email',
    }]));
  });
}

async function mockLimits(page: Page) {
  await page.route('**/api/catalog/limits**', r => r.fulfill({ json: LIMITS }));
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

async function fillEmailAndContinue(page: Page, email: string) {
  await page.getByPlaceholder('you@gmail.com').first().fill(email);
  await page.locator('#cdp-my-email-again').fill(email);
  await page.getByRole('button', { name: /Continue to payment/i }).click();
}

async function startCheckout(page: Page, email = EMAIL) {
  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await fillEmailAndContinue(page, email);
}

test.describe('supplier per-email budget', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('the live daily refusal names the email, the numbers and the unpaid order — and offers a new email', async ({ page }) => {
    await seedWalmart(page);
    await mockLimits(page);
    const calls = await refuseCheckout(page, 409, LIVE_DAILY_REFUSAL);
    await startCheckout(page);

    const prompt = page.getByTestId('email-limit-prompt');
    await expect(prompt).toBeVisible();
    await expect(prompt).toContainText(/Checkout paused/i);
    // Owner (2026-10-20): open with the plain guidance, then the partner's
    // real reason — its numbers and its verbatim code.
    await expect(prompt).toContainText(/did not approve this payment/i);
    await expect(prompt).toContainText(/open order/i);
    await expect(prompt).toContainText(/Orders tab/i);
    await expect(page.getByTestId('supplier-reason-code')).toContainText('LIGHTNING_DAILY_LIMIT_EXCEEDED');
    await expect(prompt).toContainText(EMAIL);
    await expect(prompt).toContainText('€100');
    await expect(prompt).toContainText('€97.70');
    await expect(prompt).toContainText('Walmart');
    await expect(prompt).toContainText('$104');
    await expect(prompt).toContainText('$109.84');
    await expect(prompt).toContainText(/paid or not|whether or not/i);
    await expect(prompt).not.toContainText(/could not approve|contact support/i);
    await expect(page.getByText(/Waiting for Nimiq Pay/i)).toHaveCount(0);
    // Deterministic refusal: no reopen / retry / skip.
    await expect(page.getByRole('button', { name: /Reopen the existing|Retry/i })).toHaveCount(0);
    const payPending = page.getByTestId('email-limit-open-orders');
    await expect(payPending).toBeVisible();
    await expect(payPending).toHaveClass(/btn-gold/);
    expect(calls).toHaveLength(1);
    expect(calls[0].email).toBe(EMAIL);

    // "Use a different delivery email" goes back to the delivery step, says
    // why, and the next attempt carries the NEW address.
    await page.getByTestId('email-limit-change-email').click();
    const notice = page.getByTestId('delivery-email-notice');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText(EMAIL);
    await expect(page.getByTestId('email-limit-prompt')).toHaveCount(0);
    await expect(page.getByTestId('delivery-limits-hint')).toContainText('€300');
    await fillEmailAndContinue(page, OTHER_EMAIL);
    await expect(page.getByTestId('email-limit-prompt')).toBeVisible();
    expect(calls).toHaveLength(2);
    expect(calls[1].email).toBe(OTHER_EMAIL);
  });

  test('"pay the pending order" opens Orders', async ({ page }) => {
    await seedWalmart(page);
    await mockLimits(page);
    await refuseCheckout(page, 409, LIVE_DAILY_REFUSAL);
    await startCheckout(page);
    await page.getByTestId('email-limit-open-orders').click();
    await expect.poll(() => new URL(page.url()).pathname).toMatch(/\/orders\/?$/);
  });

  test('the daily code without any details still gets a specific sentence, not "contact support"', async ({ page }) => {
    await seedWalmart(page);
    await refuseCheckout(page, 409, { error: 'x', code: 'SUPPLIER_PROBLEMS', supplier_error: true, upstream_status: 422, problems: [{ problem: 'LIGHTNING_DAILY_LIMIT_EXCEEDED' }] });
    await startCheckout(page);
    const prompt = page.getByTestId('email-limit-prompt');
    await expect(prompt).toBeVisible();
    await expect(prompt).toContainText(/daily/i);
    await expect(prompt).toContainText(/delivery email/i);
    await expect(prompt).not.toContainText(/could not approve|contact support/i);
    // No pending order known: changing the email is the primary action.
    await expect(page.getByTestId('email-limit-open-orders')).toHaveCount(0);
    await expect(page.getByTestId('email-limit-change-email')).toHaveClass(/btn-gold/);
  });

  test('the delivery step states the rule per email before the last step', async ({ page }) => {
    await seedWalmart(page);
    await mockLimits(page);
    await open(page, path('/cart'));
    await page.locator('.btn-gold.btn-block.btn-lg').click();
    const hint = page.getByTestId('delivery-limits-hint');
    await expect(hint).toBeVisible();
    await expect(hint).toContainText('€100');
    await expect(hint).toContainText('€300');
    await expect(hint).toContainText(/unpaid/i);
  });

  test('brand cards never promise more than the per-order ceiling allows', async ({ page }) => {
    await mockLimits(page);
    // Force the API brand list (min "5", max "500") instead of the static files.
    await page.route('**/data/catalog/brands/**', r => r.fulfill({ status: 404, body: '' }));
    await open(page, path('/'));
    const prices = page.locator('.product-card .p-price');
    await expect(prices.first()).toBeVisible();
    const texts = await prices.allInnerTexts();
    expect(texts.length).toBeGreaterThan(0);
    expect(texts.some(t => /104/.test(t))).toBe(true);
    expect(texts.some(t => /500/.test(t))).toBe(false);
  });
});
