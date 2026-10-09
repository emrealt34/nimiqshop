import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

for (const lang of ['en', 'tr'] as const) {
  test.describe(`wallet hydration (${lang}) @smoke`, () => {
    test.use({ lang });
    test('a session-warmed balance does not replace the server-rendered home island', async ({ page }) => {
      // Session restoration can finish while the React renderer is still
      // loading. Exercise the early balance subscription before the beacon.
      await page.addInitScript(() => {
        const timer = setInterval(() => {
          if (document.documentElement.getAttribute('data-app-ready') === '1') { clearInterval(timer); return; }
          window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: true } }));
        }, 20);
      });
      await page.route('**/_assets/client.*.js', async (route) => {
        const response = await route.fetch();
        await new Promise((resolve) => setTimeout(resolve, 80));
        await route.fulfill({ response });
      });
      await open(page, path('/'));
      await expect(page.locator('.wal-card .wal-nim')).toContainText('124');
      await expect(page.locator('a.product-card').first()).toBeVisible();
      // The shared fixture fails this test on any React hydration error.
    });
  });
}
