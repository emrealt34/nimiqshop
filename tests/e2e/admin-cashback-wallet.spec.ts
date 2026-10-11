import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const payoutAddress = 'NQ38 AU4H Y000 0000 0000 0000 0000 0000 0000';
const wallet = {
  address: payoutAddress.replaceAll(' ', '').toLowerCase(), configured: true,
  enabled: true, available: true, network: 'mainnet', balance_nim: 1234.56789,
  observed_at: '2026-10-11T18:00:00Z',
};

for (const width of [1280, 320]) {
  test.describe(`admin cashback wallet at ${width}px`, () => {
    test.use({ api: { authed: false }, lang: 'en', viewport: { width, height: 900 } });
    for (const state of ['funded', 'zero', 'unavailable', 'unconfigured'] as const) {
      test(`${state} payout wallet in Overview`, async ({ page }) => {
        const view = state === 'funded' ? wallet : state === 'zero' ? { ...wallet, enabled: false, balance_nim: 0 }
          : state === 'unavailable' ? { ...wallet, available: false, balance_nim: null, observed_at: null, reason: 'balance_unavailable' }
            : { ...wallet, address: '', configured: false, enabled: false, available: false, balance_nim: null, observed_at: null, reason: 'not_configured' };
        await page.route('**/api/admin/**', (route) => {
          const p = new URL(route.request().url()).pathname;
          return route.fulfill({ json: p.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
            : p.endsWith('/dashboard') ? { cashback_wallet: view, countries: { rows: [] }, players: {}, cashback_queue: {} }
              : {} });
        });
        await open(page, path('/admin?section=overview'));
        const card = page.getByRole('region', { name: 'Cashback wallet', exact: true });
        await expect(card).toBeVisible();
        if (state === 'unconfigured') {
          await expect(card).toContainText('not configured');
          await expect(card.locator('img.identicon')).toHaveCount(0);
          await expect(card.getByRole('button', { name: 'Copy payout address' })).toHaveCount(0);
        } else {
          await expect(card.locator('.admin-cashback-wallet-address')).toHaveText(payoutAddress);
          const avatar = card.locator('img.identicon');
          await expect(avatar).toBeVisible();
          await expect.poll(() => avatar.evaluate((img) => (img as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
          await expect(card.getByRole('button', { name: 'Copy payout address' })).toBeVisible();
        }
        const balance = card.locator('.astat').filter({ hasText: 'Available for payouts' });
        await expect(balance).toContainText(state === 'funded' ? '1,234.56789 NIM' : state === 'zero' ? '0.00000 NIM' : '— NIM');
        if (state === 'unavailable') await expect(card).toContainText('Balance could not be read');
        if (state === 'zero') await expect(card).toContainText('Disabled');
        expect(await card.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
        expect(await card.evaluate((el) => el.getBoundingClientRect().right <= innerWidth)).toBe(true);
      });
    }
  });
}

test.describe('cashback wallet polling', () => {
  test.use({ api: { authed: false }, lang: 'en' });
  test('a new balance appears without reloading Overview', async ({ page }) => {
    let balance = 1234.56789;
    await page.clock.install();
    await page.route('**/api/admin/**', (route) => {
      const p = new URL(route.request().url()).pathname;
      return route.fulfill({ json: p.endsWith('/auth/me') ? { admin: { id: 'operator', username: 'operator' } }
        : p.endsWith('/dashboard') ? { cashback_wallet: { ...wallet, balance_nim: balance }, countries: { rows: [] }, players: {}, cashback_queue: {} }
          : {} });
    });
    await open(page, path('/admin?section=overview'));
    const card = page.getByRole('region', { name: 'Cashback wallet', exact: true });
    await expect(card).toContainText('1,234.56789 NIM');
    balance = 987.65432;
    await page.clock.fastForward(15_100);
    await expect(card).toContainText('987.65432 NIM');
    await expect(card).not.toContainText('1,234.56789 NIM');
  });
});
