/**
 * Backend failure modes: the shop must stay usable (shell + content, no
 * crash, no blank screen) when the API is down, 500s, returns HTML instead of
 * JSON, returns null, or is slow — and for signed-out visitors.
 */
import { test, expect, open, contentText } from './support/fixtures';
import { NOT_FOUND_MARK, path } from './support/data';

const UNDER_TEST = ['/', '/cart', '/product?id=p1', '/orders', '/order?id=o1', '/profile', '/activity', '/cashback', '/plant-trees', '/support', '/track?order=o1'];

for (const mode of ['down', 'error500', 'garbage', 'empty', 'slow'] as const) {
  test.describe(`API ${mode} @smoke`, () => {
    test.use({ api: { mode } });
    test.describe.configure({ timeout: mode === 'slow' ? 60_000 : 30_000 });
    for (const route of UNDER_TEST) {
      test(`${route} survives`, async ({ page }) => {
        await open(page, path(route));
        await expect(page.locator('.topbar').first()).toBeVisible();
        const text = await contentText(page);
        expect(text.length, 'not a blank page').toBeGreaterThan(10);
        expect(text).not.toMatch(NOT_FOUND_MARK);
      });
    }
  });
}

test.describe('signed-out visitor @smoke', () => {
  test.use({ api: { authed: false } });
  for (const route of UNDER_TEST) {
    test(`${route} renders the signed-out state`, async ({ page }) => {
      await open(page, path(route));
      expect(await contentText(page)).not.toMatch(NOT_FOUND_MARK);
    });
  }
});
