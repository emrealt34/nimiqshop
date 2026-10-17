import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

/**
 * Two operator-console gaps that were both about the same stuck-order
 * conversation (owner, 2026-10-17):
 *
 *  1. "admin sayfasından amazon fr görüyom ne kadarlık almaya çalıştığını
 *     görmüyom" — the Orders panel named the product and the local status but
 *     never the money, so an operator could not tell a €10 abandoned cart from
 *     a €500 paid purchase waiting on a sync.
 *  2. "webhooku aç istiyorum … nası kurarsın bilmiyorum" — the webhook is
 *     account-managed (Cryptorefills publishes no self-registration field), so
 *     the operator has to hand a precise URL to the partner account. Nothing in
 *     the console showed that URL, or even whether the webhook was configured.
 */
test.describe('operator console: order money', () => {
  test.use({ api: { authed: false }, lang: 'en', viewport: { width: 1280, height: 900 } });

  test('a stuck order row states what the buyer was trying to spend', async ({ page }) => {
    await page.route('**/api/admin/**', route => {
      const url = new URL(route.request().url());
      return route.fulfill({ json: url.pathname.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
        : url.pathname.endsWith('/quotes') ? { quotes: [{
          id: 'amazon-fr-order',
          product_id: 'Amazon',
          product_country: 'FR',
          status: 'awaiting_payment',
          supplier_status: 'WaitingForPayment',
          // adminQuoteView renders money.Micros.String(): a decimal DOLLAR
          // string, unlike the customer endpoints' raw micro-USD integer.
          product_usd: '52.300000',
          quantity: 2,
          denomination: '25 EUR',
          product_value: 25,
          coin: 'BTC',
          coin_amount: '0.00048123',
          network: 'Lightning',
          estimated_nim: 1840,
          payment_expiry: new Date(Date.now() + 12 * 60_000).toISOString(),
          cashback: {},
        }] } : {} });
    });

    await open(page, path('/admin?section=orders'));
    const money = page.getByTestId('order-money');
    await expect(money).toBeVisible();
    await expect(money).toContainText('$52.30');
    await expect(money).toContainText('× 2');
    await expect(money).toContainText('25 EUR');
    await expect(money).toContainText('0.00048123 BTC');
    await expect(money).toContainText('Lightning');
    await expect(money).toContainText('1,840 NIM');
    await expect(money).toContainText('WaitingForPayment → awaiting_payment');
    await expect(money).toContainText('open');
    // The payable invoice itself must never reach an operator screen.
    await expect(money).not.toContainText('lnbc');
  });

  test('a row with no money fields degrades to dashes instead of NaN', async ({ page }) => {
    await page.route('**/api/admin/**', route => {
      const url = new URL(route.request().url());
      return route.fulfill({ json: url.pathname.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
        : url.pathname.endsWith('/quotes') ? { quotes: [{ id: 'bare', product_id: 'Carrefour', status: 'fulfilled', cashback: {} }] } : {} });
    });

    await open(page, path('/admin?section=orders'));
    const money = page.getByTestId('order-money');
    await expect(money).toContainText('Order value: —');
    await expect(money).not.toContainText('NaN');
    await expect(money).not.toContainText('$0.00');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
});

test.describe('operator console: supplier webhook', () => {
  test.use({ api: { authed: false }, lang: 'en', viewport: { width: 1280, height: 900 } });

  const mock = (page: any, webhook: any) => page.route('**/api/admin/**', (route: any) => {
    const url = new URL(route.request().url());
    return route.fulfill({ json: url.pathname.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
      : url.pathname.endsWith('/notification/status') ? { email: { enabled: true, sandbox: false, from: 'shop@example.com' }, gift_enabled: true, webhook } : {} });
  });

  test('when it is off, the card says so and gives the exact two variables', async ({ page }) => {
    await mock(page, { enabled: false, key_set: false, base_url: '', url: '' });
    await open(page, path('/admin?section=email'));
    await expect(page.locator('body')).toContainText('Supplier webhook OFF');
    await expect(page.locator('body')).toContainText('polling only');
    await expect(page.locator('body')).toContainText('CRYPTOREFILLS_WEBHOOK_KEY');
    await expect(page.locator('body')).toContainText('PUBLIC_WEBHOOK_BASE_URL');
    await expect(page.locator('body')).toContainText('/api/webhooks/cryptorefills?key=');
    await expect(page.getByTestId('webhook-url')).toHaveCount(0);
  });

  test('when it is on, the card hands over the exact URL to register', async ({ page }) => {
    const url = 'https://shopapi.nimiqbase.com/api/webhooks/cryptorefills?key=' + 'ab'.repeat(32);
    await mock(page, { enabled: true, key_set: true, base_url: 'https://shopapi.nimiqbase.com', url });
    await open(page, path('/admin?section=email'));
    await expect(page.locator('body')).toContainText('Supplier webhook READY');
    await expect(page.getByTestId('webhook-url')).toHaveText(url);
    await expect(page.getByRole('button', { name: 'Copy webhook URL' })).toBeVisible();
    await expect(page.locator('body')).not.toContainText('CRYPTOREFILLS_WEBHOOK_KEY is not configured');
  });

  test('a half-configured webhook is reported as the start-up refusal it is', async ({ page }) => {
    // config.go refuses to boot with a key and no base URL, so this state is
    // only reachable from a stale payload — but the card must not claim READY.
    await mock(page, { enabled: false, key_set: true, base_url: '', url: '' });
    await open(page, path('/admin?section=email'));
    await expect(page.locator('body')).toContainText('Supplier webhook OFF');
    await expect(page.locator('body')).toContainText('PUBLIC_WEBHOOK_BASE_URL is missing');
  });
});
