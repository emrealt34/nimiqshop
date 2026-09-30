/**
 * PNG preview dialog on phones, tablets and desktops: covers the screen,
 * actions reachable without scrolling, title clear of the close button,
 * page scroll locked, closes with Esc / backdrop / close button.
 */
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const inside = (b: any, vw: number, vh: number) => !!b && b.x >= -1 && b.y >= -1 && b.x + b.width <= vw + 1 && b.y + b.height <= vh + 1;

for (const [width, height] of [[320, 640], [390, 844], [768, 1024], [1280, 800]] as const) {
  for (const lang of ['en', 'de', 'tr', 'pt', 'fr', 'es'] as const) {
    test.describe(`share preview · ${width}×${height} · ${lang}`, () => {
      test.use({ viewport: { width, height }, lang });
      test('fits, actions reachable, title clear of close', async ({ page }) => {
        await open(page, path('/plant-trees'));
        await page.locator('section.pt-impact .pt-actions .btn').first().click();
        await expect(page.locator('.pt-overlay img')).toBeVisible();
        // measure the rendered PNG, not the empty <img> box before decode
        await expect.poll(() => page.locator('.pt-overlay img').evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0)).toBe(true);
        const ob = await page.locator('.pt-overlay').boundingBox();
        expect(ob!.width).toBeGreaterThanOrEqual(width - 1);
        expect(ob!.height).toBeGreaterThanOrEqual(height - 1);
        expect(inside(await page.locator('.pt-overlay-box').boundingBox(), width, height)).toBe(true);
        for (const b of await page.locator('.pt-overlay-actions .btn').all()) {
          const bb = await b.boundingBox();
          expect(inside(bb, width, height), 'action visible without scrolling').toBe(true);
          expect(bb!.height, 'action label at most two lines').toBeLessThanOrEqual(72);
        }
        const t = (await page.locator('.pt-overlay-head .strong').boundingBox())!, c = (await page.locator('.pt-overlay-head .btn').boundingBox())!;
        expect(t.x < c.x + c.width && c.x < t.x + t.width && t.y < c.y + c.height && c.y < t.y + t.height, 'title overlaps close').toBe(false);
        // The PNG is 4:5 and is shown undistorted: either the <img> box has that
        // ratio, or the image is letter-boxed into it (object-fit contain/scale-down).
        const img = await page.locator('.pt-overlay img').evaluate((i: HTMLImageElement) => {
          const r = i.getBoundingClientRect();
          return { natural: i.naturalWidth / i.naturalHeight, box: r.width / r.height, fit: getComputedStyle(i).objectFit, w: r.width };
        });
        expect(Math.abs(img.natural - 0.8), 'impact PNG is 4:5').toBeLessThan(0.02);
        if (!['contain', 'scale-down'].includes(img.fit)) expect(Math.abs(img.box - 0.8), 'preview not stretched').toBeLessThan(0.02);
        expect(img.w, 'preview has a visible size').toBeGreaterThan(60);
        expect(await page.evaluate(() => getComputedStyle(document.documentElement).overflow)).toBe('hidden');
      });
    });
  }
}

test('share preview closes via Esc, backdrop, close button @smoke', async ({ page }) => {
  await open(page, path('/plant-trees'));
  const openIt = async () => { await page.locator('section.pt-impact .pt-actions .btn').first().click(); await expect(page.locator('.pt-overlay')).toBeVisible(); };
  const closed = async () => { await expect(page.locator('.pt-overlay')).toHaveCount(0); expect(await page.evaluate(() => getComputedStyle(document.documentElement).overflow)).not.toBe('hidden'); };
  await openIt(); await page.keyboard.press('Escape'); await closed();
  await openIt(); await page.locator('.pt-overlay').click({ position: { x: 4, y: 4 } }); await closed();
  await openIt(); await page.locator('.pt-overlay-head .btn').click(); await closed();
});
