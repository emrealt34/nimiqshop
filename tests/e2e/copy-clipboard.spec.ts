/**
 * The copy controls, on the real screens — the 2026-11-11 regression guard.
 *
 * Owner: "ödeme ekranında kopyala butonu var ya, çalışmıyor". Two separate
 * faults made a press look like a dead button:
 *
 *   1. `lib/clipboard.ts` had exactly ONE strategy (`execCommand('copy')` on a
 *      `contain: strict` textarea). Size containment collapses that field to
 *      0×0 and WebKit then builds no selection inside it — the command
 *      "succeeds" and the clipboard stays empty. Where a WebView refuses the
 *      command outright, there was no second chance at all.
 *   2. Nothing in the UI reacted when the copy failed. `CopyButton` (order id,
 *      refund wallet, tx hash, swap id, addresses) changed no pixel at all, so
 *      the only thing a buyer could conclude was that the button was broken.
 *
 * These tests pin the two rules that fix it:
 *   • a refused synchronous copy is retried through the async Clipboard API —
 *     the invoice still lands on the clipboard and the toast says so;
 *   • when EVERY strategy refuses, the press is answered in words (a warn
 *     toast on the pay screen, the failure text + `data-copy-state="failed"`
 *     on a `CopyButton`) instead of silence.
 *
 * The strategies are broken deliberately, by replacing `document.execCommand`
 * and `navigator.clipboard` in an init script. That is the only way to reach
 * the failure branches in a browser whose clipboard normally works.
 */
import type { Page, Route } from '@playwright/test';
import { test, expect } from './support/fixtures';

const QUOTES_POST = /\/api\/quotes(?:\/batch)?(?:\?|$)/;
const inMinutes = (m: number) => new Date(Date.now() + m * 60_000).toISOString();
/** Offline fixture: valid checksum and amount, zero signature; cannot be paid. */
const INVOICE = 'lnbc120000000p1qqqqqqqpp5qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqxqrpcgqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqkzf7d6';

function quoteBody(id: string) {
  return {
    quote_id: id, status: 'awaiting_payment', can_pay: true, test_mode: false, test_pay: false,
    lightning_invoice: INVOICE,
    payment_expires_at: inMinutes(14.5), expires_at: inMinutes(14.5), payment_handoff_at: null,
    coin: 'BTC', coin_amount: '0.00100000', network: '', payment_method: 'nimiq_pay',
    product_id: 'amazon-tr', country: 'TR', product_value: 25, product_currency: 'USD', denomination: '25 USD', quantity: 1,
    customer_email: 'buyer@example.com', phone_number: '', beneficiary_account: 'buyer@example.com',
    supplier_status: 'WaitingForPayment', supplier_order_id: 'sup-' + id, powered_by: 'cryptorefills',
    payment_observed: false, payment_blocked: false, cashback_bps: 0, cashback_percent: 0,
  };
}

/** Refuse the legacy command, the way a locked-down WebView does. */
const refuseExecCommand = () => { Document.prototype.execCommand = () => false; };
/** Remove the async API too: now NO strategy can work. */
const refuseEverything = () => {
  Document.prototype.execCommand = () => false;
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
};

const COPY_BUTTON = 'button:has-text("Copy Nimiq Pay payment request")';

function seedCart(page: Page) {
  return page.addInitScript(() => {
    localStorage.setItem('nimshop_cart', JSON.stringify([{
      id: 'amazon-tr', name: 'Amazon', type: 'giftcard', country: 'TR',
      currency: 'USD', pkg: 'USD25', value: 25, denomination: '25 USD', unitUSD: 25, qty: 1,
      brand: 'Amazon', category: 'giftcard', delivery_type: 'by_email',
    }]));
  });
}

/** Cart → delivery details → the pay screen. */
async function openPayScreen(page: Page) {
  await page.route(QUOTES_POST, async (route: Route) => {
    if (route.request().method() !== 'POST') return route.fallback();
    await route.fulfill({ status: 201, json: quoteBody('q-copy') });
  });
  await page.route(/\/api\/quotes\/[^/?]+(?:\?|$)/, async (route: Route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop() || '');
    await route.fulfill({ status: 200, json: { quote: quoteBody(id), can_pay: true, checkout_settled: false } });
  });
  await page.goto('/nimiqshop/cart');
  await page.locator('.btn-gold.btn-block.btn-lg').first().click();
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  const confirmEmail = page.locator('#cdp-my-email-again');
  if (await confirmEmail.count()) await confirmEmail.fill('buyer@example.com');
  const cont = page.getByRole('button', { name: /Continue to payment/i });
  if (await cont.count()) await cont.click();
  await expect(page.locator(COPY_BUTTON).first()).toBeVisible({ timeout: 15_000 });
}

test.describe('copy controls answer every press', () => {
  test('a refused legacy copy still reaches the clipboard through the async API @smoke', async ({ page, context, browserName }) => {
    // Reading the clipboard back needs the Chromium permission model; the
    // other engines cover this rule through the two tests below.
    test.skip(browserName !== 'chromium', 'clipboard read-back is Chromium-only');
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await seedCart(page);
    await page.addInitScript(refuseExecCommand);
    await openPayScreen(page);

    await page.evaluate(() => navigator.clipboard.writeText('__EMPTY__'));
    await page.locator(COPY_BUTTON).first().click();

    await expect(page.getByText(/Invoice copied/i).first()).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(INVOICE);
  });

  test('when every strategy refuses, the pay screen says so instead of going silent @smoke', async ({ page }) => {
    await seedCart(page);
    await page.addInitScript(refuseEverything);
    await openPayScreen(page);

    await page.locator(COPY_BUTTON).first().click();
    await expect(page.getByText(/Copy did not complete/i).first()).toBeVisible();
  });

  test('a CopyButton that cannot copy shows the failure, never a dead press @smoke', async ({ page }) => {
    await page.addInitScript(refuseEverything);
    await page.route('**/api/quotes', (r: Route) => r.fulfill({ json: [] }));
    await page.route('**/api/quotes/copy-test', (r: Route) => r.fulfill({
      json: {
        quote: {
          id: 'copy-test', quote_id: 'copy-test', type: 'cryptorefills_purchase', title: 'Carrefour',
          status: 'fulfilled', can_pay: false, coin: 'BTC', coin_amount: '0.00012', quantity: 1,
          lightning_swap_id: 'wallet-swap-private',
          lightning_receipt: { payment_hash: 'ab'.repeat(32), amount_btc: '0.00012', confirmed: true },
        },
      },
    }));
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/nimiqshop/order?id=copy-test&type=quote');

    const button = page.locator('.copy-btn').first();
    await expect(button).toBeVisible();
    await button.click();

    // Icon-only controls cannot grow a sentence without breaking the row, so
    // the state attribute and the title carry the message; a labelled control
    // shows the failure text in place of its label.
    await expect(button).toHaveAttribute('data-copy-state', 'failed');
    await expect(button).toHaveAttribute('title', /Copy did not complete/i);
  });
});
