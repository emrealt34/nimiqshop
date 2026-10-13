import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const lang of ['en', 'tr'] as const) {
  test.describe(`Pay stake without a shop session (${lang})`, () => {
    test.use({ lang, api: { authed: false } });
    for (const outage of [false, true]) {
      test(`shows liquid funds and both accounts' public stake; outage=${outage}`, async ({ page }) => {
        const accounts = ['NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082', 'NQ12CPDG4KKU70UBNC7U0Q82T8U7US17PYHU'];
        await page.addInitScript(({ accounts }) => {
          (window as any).nimiq = {
            connected: true,
            listAccounts: async () => [accounts[0], accounts[1], accounts[0].toLowerCase()],
            getBalance: async (address: string) => address === accounts[0] ? 70000000 : 3700000,
          };
        }, { accounts });
        const reads: string[] = [];
        let shopReads = 0;
        page.on('request', req => { if (new URL(req.url()).pathname.endsWith('/wallet/balance')) shopReads++; });
        await page.route('**/api/wallet/stake?**', route => {
          const address = new URL(route.request().url()).searchParams.get('address')!;
          reads.push(address);
          return outage ? route.fulfill({ status: 502, json: { error: 'staking unavailable' } }) : route.fulfill({ json: {
            address, staked_nim: address === accounts[0] ? 90 : 10, inactive_nim: 0, retired_nim: 0, network: 'mainnet',
          } });
        });
        await open(page, path('/'));
        await expect(page.locator('.wal-card .wal-nim')).toHaveText('737 NIM');
        if (outage) {
          await expect(page.locator('.wal-card .wal-staked')).toHaveCount(0);
          await expect(page.locator('.wal-card')).toContainText(lang === 'tr' ? 'Stake miktarı okunamadı' : 'Staking amounts could not be read');
        } else {
          await expect(page.locator('.wal-card .wal-staked')).toHaveText(lang === 'tr' ? '100 NIM stake edilmiş' : '100 NIM staked');
          expect(reads.sort()).toEqual([...accounts].sort());
        }
        expect(shopReads).toBe(0);
        await page.locator('.wal-card .wal-refresh').click();
        await expect(page.locator('.wal-card .wal-refresh')).toBeVisible();
        await expect.poll(() => reads.length).toBe(4);
        await expect(page.locator('.wal-card .wal-nim')).toHaveText('737 NIM');
      });
    }
  });
}
