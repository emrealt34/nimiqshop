import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

// Public SHA-256 digest of the all-zero test preimage below, never a credential.
const hash = '66687aadf862bd776c8fc18b8e9f8e20089714856ee233b3902a591d0d5f2925'; // DevSkim: ignore DS173237
const purchase = { id: 'lightning-test', type: 'cryptorefills_purchase', title: 'Carrefour', product_id: 'Carrefour', country: 'FR', coin: 'BTC', network: 'Lightning', coin_amount: '0.00012', lightning_payment_hash: hash,
  status: 'fulfilled', created_at: '2026-10-11T12:00:00Z', updated_at: '2026-10-11T12:00:00Z', time: '2026-10-11T12:00:00Z', quantity: 1, product_usd: 10, usd: 10, nim: 40000 };
const receipt = { payment_hash: hash, amount_btc: '0.00012', confirmed: true, confirmed_at: '2026-10-11T12:00:00Z' };

for (const width of [320, 1280]) {
  test.describe(`Lightning receipt ${width}px`, () => {
    test.use({ viewport: { width, height: 844 }, lang: 'en' });

    test('activity opens payment details without navigating and verifies preimage locally', async ({ page, requests }) => {
      await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [{ ...purchase, lightning_receipt: receipt }], summary: {} } }));
      await open(page, path('/activity'));
      await page.getByRole('button', { name: /BTC Lightning/ }).click();
      await expect(page).toHaveURL(/\/activity\/?$/);
      const details = page.locator('.lightning-receipt-details');
      await expect(details).toBeVisible();
      await expect(details).toContainText('Payment confirmed by the supplier');
      await expect(details.locator('code')).toHaveText(hash);
      const external = details.getByRole('link', { name: 'External verifier' });
      await expect(external).toHaveAttribute('href', 'https://dev.blink.sv/decode');
      await expect(external).toHaveAttribute('referrerpolicy', 'no-referrer');
      await expect(external).toHaveAttribute('rel', 'noopener noreferrer');
      const input = details.getByLabel('Payment preimage');
      const verify = details.getByRole('button', { name: 'Verify in this browser' });
      await expect(verify).toBeDisabled();
      await input.fill('01'.repeat(32));
      await verify.click();
      await expect(details.locator('form').getByRole('status')).toHaveText('Receipt does not match this payment hash.');
      const before = requests.length;
      await input.fill('00'.repeat(32));
      await verify.click();
      await expect(details.locator('form').getByRole('status')).toHaveText('Receipt matches this payment hash.');
      expect(requests.length).toBe(before);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect(await page.locator('a a, a button').count()).toBe(0);
    });

    for (const kind of ['order', 'track', 'orders'] as const) {
      test(`${kind} summary shows the full Lightning hash and opens verifier`, async ({ page }) => {
        await page.route('**/api/quotes', route => route.fulfill({ json: [purchase] }));
        await page.route('**/api/quotes/lightning-test', route => route.fulfill({ json: purchase }));
        await page.route('**/api/track/lightning-test', route => route.fulfill({ json: { ...purchase, lightning_receipt: receipt } }));
        await open(page, path(kind === 'order' ? '/order?id=lightning-test&type=quote' : kind === 'track' ? '/track?order=lightning-test' : '/orders'));
        if (kind === 'orders') await page.getByRole('button', { name: /BTC Lightning/ }).click();
        else {
          await expect(page.locator('.lightning-receipt-hash')).toHaveText(hash);
          await expect(page.locator('.lightning-receipt')).toContainText('BTC Lightning');
          await expect(page.locator('.lightning-receipt').getByRole('link', { name: 'External verifier' })).toHaveCount(0);
          await page.getByRole('button', { name: 'Payment details' }).click();
        }
        await expect(page.locator('.lightning-receipt-details').getByRole('link', { name: 'External verifier' })).toBeVisible();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      });
    }

    test('matching public chain proof supplies real explorers and verifies without typed secrets', async ({ page }) => {
      await page.route('**/api/track/lightning-test', route => route.fulfill({ json: { ...purchase, lightning_receipt: receipt } }));
      await page.route('**/api/track/lightning-test/lightning-proof', route => route.fulfill({ json: { chain_proof: { funding_tx: 'a'.repeat(64), settlement_tx: 'b'.repeat(64), contract: 'NQ24 2XCD PK1C 7DR5 25U5 3PER U64V USR2 LPRK', preimage: '00'.repeat(32) } } }));
      await open(page, path('/track?order=lightning-test'));
      await expect(page.getByRole('link', { name: 'Nimiq funding transaction' })).toHaveAttribute('href', `https://nimiq.watch/#${'a'.repeat(64)}`);
      await expect(page.getByRole('link', { name: 'Nimiq settlement transaction' })).toHaveAttribute('href', `https://nimiq.watch/#${'b'.repeat(64)}`);
      await expect(page.locator('.lightning-chain-proof')).toContainText('Chain receipt verified against the invoice hash in your browser.');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });
    test('wrong preimage never shows settlement as verified', async ({ page }) => {
      await page.route('**/api/track/lightning-test', route => route.fulfill({ json: { ...purchase, lightning_receipt: receipt } }));
      await page.route('**/api/track/lightning-test/lightning-proof', route => route.fulfill({ json: { chain_proof: { funding_tx: 'a'.repeat(64), settlement_tx: 'b'.repeat(64), preimage: '01'.repeat(32) } } }));
      await open(page, path('/track?order=lightning-test'));
      await expect(page.locator('.lightning-chain-proof')).toContainText('Lightning settlement proof is not yet available.');
      await expect(page.getByRole('link', { name: 'Nimiq settlement transaction' })).toHaveCount(0);
    });
  });
}

test('public activity and track hide anonymous receipts even if an old API leaks metadata', async ({ page }) => {
  const privatePurchase = { ...purchase, anonymous: true, lightning_receipt: receipt };
  await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [privatePurchase], summary: {} } }));
  await page.route('**/api/track/lightning-test', route => route.fulfill({ json: privatePurchase }));
  await open(page, path('/activity'));
  await expect(page.locator('.feed-item')).toHaveCount(1);
  await expect(page.getByRole('button', { name: /BTC Lightning/ })).toHaveCount(0);
  await open(page, path('/track?order=lightning-test'));
  await expect(page.locator('.lightning-receipt')).toHaveCount(0);
  await expect(page.locator('body')).not.toContainText(hash);
});

test.describe('Turkish payment status', () => {
  test.use({ lang: 'tr' });
  test('unpaid invoice is labeled pending in Turkish', async ({ page }) => {
  await page.route('**/api/track/lightning-test', route => route.fulfill({ json: { ...purchase, status: 'awaiting_payment', lightning_receipt: { ...receipt, confirmed: false, confirmed_at: undefined } } }));
  await open(page, path('/track?order=lightning-test'));
  await page.locator('.lightning-receipt button').click();
  await expect(page.locator('.lightning-receipt-details')).not.toContainText('Payment confirmed by the supplier');
  await expect(page.locator('.lightning-receipt-details')).toContainText('Ödeme henüz onaylanmadı');
  await expect(page.locator('.lightning-receipt-details')).not.toContainText('Tedarikçi ödemeyi onayladı');
});

});

for (const tx of ['ab'.repeat(32), '0x' + 'cd'.repeat(32)]) {
  for (const surface of ['order', 'orders', 'activity']) {
    test(`${surface} displays the SDK submission separately: ${tx.slice(0, 4)}`, async ({ page }) => {
      const item = { ...purchase, status: 'awaiting_payment', lightning_funding_hint: tx,
        lightning_receipt: { ...receipt, confirmed: false, confirmed_at: undefined, submitted_tx: tx } };
      await page.route('**/api/quotes', route => route.fulfill({ json: [item] }));
      await page.route('**/api/quotes/lightning-test', route => route.fulfill({ json: item }));
      await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [item], summary: {} } }));
      await open(page, path(surface === 'order' ? '/order?id=lightning-test&type=quote' : '/' + surface));
      const submission = page.locator('[data-submitted-transaction]').first();
      await expect(submission).toBeVisible();
      await expect(submission.locator('a')).toHaveText(tx);
      await expect(submission.locator('a')).toHaveAttribute('href', tx.startsWith('0x') ? `https://polygonscan.com/tx/${tx}` : `https://nimiq.watch/#${tx}`);
      await page.reload();
      await expect(submission).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });
  }
}

for (const width of [320, 1280]) {
  test(`private order summary exposes swap id, public pages do not (${width}px)`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    const item = { ...purchase, lightning_swap_id: 'wallet-swap-private', lightning_receipt: receipt };
    await page.route('**/api/quotes/lightning-test', route => route.fulfill({ json: { quote: item } }));
    await page.route('**/api/activity?*', route => route.fulfill({ json: { items: [item], summary: {} } }));
    await page.route('**/api/track/lightning-test', route => route.fulfill({ json: item }));
    await open(page, path('/order?id=lightning-test&type=quote'));
    await expect(page.locator('body')).toContainText('Swap ID');
    await expect(page.locator('body')).toContainText('wallet-swap-private');
    await page.reload();
    await expect(page.locator('body')).toContainText('wallet-swap-private');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    for (const url of ['/activity', '/track?order=lightning-test']) {
      await open(page, path(url));
      await expect(page.locator('body')).not.toContainText('wallet-swap-private');
    }
  });
}
