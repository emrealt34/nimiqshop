import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const width of [320, 768, 1280]) {
  for (const theme of ['light', 'dark']) {
    test.describe(`cashback receipt ${width}px ${theme}`, () => {
      test.use({ viewport: { width, height: 900 }, lang: 'tr' });
      test('hero fills its row and sharing actions leave no vacant columns', async ({ page }) => {
        await page.addInitScript(theme => { localStorage.setItem('nimshop.theme', theme); sessionStorage.setItem('e2e.theme', theme); }, theme);
        await open(page, path('/cashback'));
        const card = page.locator('.pt-card.pt-impact').first();
        await expect(card.locator('.pt-actions .btn')).toHaveCount(2);
        expect(await card.locator('.pt-impact-metrics').count()).toBe(0);
        await expect.poll(() => card.evaluate(card => {
          const row = card.querySelector('.pt-impact-summary')!.getBoundingClientRect();
          const hero = card.querySelector('.pt-impact-total-copy')!.getBoundingClientRect();
          return hero.width / row.width;
        })).toBeGreaterThan(0.8);
        const actions = await card.locator('.pt-actions .btn').evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().width));
        expect(Math.abs(actions[0] - actions[1])).toBeLessThan(2);
        const unusedFooter = await card.evaluate(card => card.getBoundingClientRect().bottom - card.querySelector('.pt-actions')!.getBoundingClientRect().bottom);
        expect(unusedFooter).toBeLessThan(40);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
        await card.screenshot({ path: test.info().outputPath('cashback-card.png') });
      });
    });
  }
}
