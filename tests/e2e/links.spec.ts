/**
 * Every link, asset and API call respects the deployment layout:
 *   • same-origin links stay under /nimiqshop/,
 *   • same-origin assets (JS, CSS, fonts, images, /vendor libs) never 404,
 *   • the API is called on API_BASE — never on the static host's /api/.
 */
import { test, expect, open } from './support/fixtures';
import { BASE, SCREENS, path } from './support/data';

test.describe('links & requests stay inside the deployment @smoke', () => {
  for (const screen of SCREENS.filter((s) => s.name !== 'not-found')) {
    test(`${screen.name}`, async ({ page, requests, baseURL }) => {
      // Only the deployment itself is under test: anything served from the
      // e2e origin (see playwright.config.ts) that fails is a broken asset.
      const ours = new URL(baseURL!).hostname;
      const broken: string[] = [];
      page.on('response', (r) => {
        const u = new URL(r.url());
        if (u.hostname === ours && !u.pathname.startsWith('/api/') && r.status() >= 400) broken.push(`${r.status()} ${u.pathname}`);
      });
      page.on('requestfailed', (r) => {
        const u = new URL(r.url());
        if (u.hostname === ours && !u.pathname.startsWith('/api/')) broken.push(`FAILED ${u.pathname} ${r.failure()?.errorText}`);
      });
      await open(page, path(screen.url));
      await page.waitForTimeout(3200); // idle-time vendor loads + route-chunk prefetch

      const origin = new URL(page.url()).origin;
      const hrefs = await page.locator('a[href]').evaluateAll((as) => as.map((a) => ({ raw: a.getAttribute('href') || '', abs: (a as HTMLAnchorElement).href })));
      const escaping = hrefs.filter(({ abs, raw }) => abs.startsWith(origin) && !raw.startsWith('#') && !raw.startsWith('blob:') && !new URL(abs).pathname.startsWith(BASE)).map((h) => h.raw);
      expect(escaping, 'same-origin links that drop the /nimiqshop/ base').toEqual([]);
      expect(broken.filter((b) => !/ERR_ABORTED|NS_BINDING_ABORTED|cancelled/i.test(b)), 'same-origin assets that failed to load').toEqual([]);
      expect(requests.filter((r) => /^\w+ http:\/\/127\.0\.0\.1:\d+\/api\//.test(r)), 'API calls sent to the static host instead of API_BASE').toEqual([]);
    });
  }
});
