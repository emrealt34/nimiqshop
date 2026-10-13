import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const failSecond of [false, true]) {
  test(`reads session and selected account without duplicate counting; partial=${failSecond}`, async ({ page }) => {
    await page.addInitScript(({ failSecond }) => {
      const session = 'NQ0700000000000000000000000000000000';
      const other = 'NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082';
      (window as any).__balanceCalls = [];
      (window as any).nimiq = {
        connected: true,
        listAccounts: async () => [other, other.toLowerCase()],
        getBalance: async (address: string) => {
          (window as any).__balanceCalls.push(address);
          if (address === other && failSecond) throw new Error('Balance unavailable');
          return address === session ? 12400000 : 5000000;
        },
      };
    }, { failSecond });
    await open(page, path('/'));
    await expect(page.locator('.wal-card .wal-nim')).toContainText(failSecond ? '124' : '174');
    // The authenticated fixture also owns 800 staked NIM. The shopping
    // balance must stay liquid-only, like the host-only view.
    await expect(page.locator('.wal-card .wal-total')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => Array.from(new Set((window as any).__balanceCalls)))).toEqual([
      'NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082', 'NQ0700000000000000000000000000000000',
    ]);
    if (failSecond) await expect(page.locator('.wal-card')).toContainText('may be incomplete');
  });
}
for (const width of [320, 768, 1280]) {
  test(`footer is centered at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await open(page, path('/'));
    const footer = page.locator('footer.footer');
    await expect(footer.locator('a[href*="exchangerate-api"], a[href$="/status"]')).toHaveCount(0);
    await expect(footer.locator('.bb-gh')).toBeVisible();
    const container = await footer.locator('.container').boundingBox();
    const barcode = await footer.locator('.footer-barcode').boundingBox();
    expect(Math.abs(container!.x + container!.width / 2 - barcode!.x - barcode!.width / 2)).toBeLessThan(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}
