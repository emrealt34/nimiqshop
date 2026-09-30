/**
 * Routing under the GitHub Pages base path (/nimiqshop/).
 * Regression guard for the client router ignoring Astro's base and showing
 * the React 404 screen on every page.
 */
import { test, expect, open, contentText } from './support/fixtures';
import { BASE, ROUTES, NAV_ROUTES, NOT_FOUND_MARK, path } from './support/data';

test.describe('routing @smoke', () => {
  for (const route of ROUTES) {
    test(`${route} renders its page, not the 404 screen`, async ({ page }) => {
      const res = await open(page, path(route));
      expect(res?.status(), 'HTTP status').toBe(200);
      const text = await contentText(page);
      expect(text.length).toBeGreaterThan(10);
      expect(text).not.toMatch(NOT_FOUND_MARK);
      await expect(page).toHaveTitle(/\S/);
      expect(await page.getAttribute('html', 'lang')).toBeTruthy();
    });
  }

  test('path without trailing slash redirects (like GitHub Pages) and renders', async ({ page }) => {
    await open(page, BASE + 'orders');
    expect(new URL(page.url()).pathname).toBe(BASE + 'orders/');
    expect(await contentText(page)).not.toMatch(NOT_FOUND_MARK);
  });

  test('unknown path gets the designed 404 page with base-aware links', async ({ page }) => {
    const res = await page.goto(BASE + 'this-page-does-not-exist', { waitUntil: 'load' });
    expect(res?.status()).toBe(404);
    await expect(page.locator('#page-content')).toContainText('404');
    const hrefs = await page.locator('#page-content a[href]').evaluateAll((as) => as.map((a) => a.getAttribute('href') || ''));
    expect(hrefs.length).toBeGreaterThan(0);
    for (const h of hrefs) expect(h.startsWith(BASE), `404 link ${h} keeps the base`).toBe(true);
  });

  test('query-string routes render', async ({ page }) => {
    for (const url of ['/product?id=p1&country=TR', '/order?id=o1', '/order?type=quote&id=q1', '/track?order=o1', '/support?ticket=t1']) {
      await open(page, path(url));
      expect(await contentText(page), url).not.toMatch(NOT_FOUND_MARK);
    }
  });
});

test.describe('client-side navigation @smoke', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('top-nav links navigate in-app (no reload), keep the base, never 404', async ({ page }) => {
    await open(page, path('/'));
    await page.evaluate(() => { (window as any).__noReload = true; });
    for (const route of NAV_ROUTES) {
      const link = page.locator(`nav.mainnav a[href^="${BASE}${route.slice(1)}"]`).first();
      await expect(link, `nav link for ${route}`).toBeVisible();
      await link.click();
      await expect(page).toHaveURL(new RegExp(`${BASE}${route.slice(1)}/?$`));
      await expect.poll(async () => (await contentText(page)).length).toBeGreaterThan(10);
      expect(await contentText(page), route).not.toMatch(NOT_FOUND_MARK);
      expect(await page.evaluate(() => (window as any).__noReload), 'SPA navigation, not a full reload').toBe(true);
    }
  });

  test('back / forward walk the history', async ({ page }) => {
    await open(page, path('/'));
    await page.locator(`nav.mainnav a[href^="${BASE}orders"]`).first().click();
    await expect(page).toHaveURL(/\/orders\/?$/);
    await page.locator(`nav.mainnav a[href^="${BASE}support"]`).first().click();
    await expect(page).toHaveURL(/\/support\/?$/);
    await page.goBack();
    await expect(page).toHaveURL(/\/orders\/?$/);
    expect(await contentText(page)).not.toMatch(NOT_FOUND_MARK);
    await page.goForward();
    await expect(page).toHaveURL(/\/support\/?$/);
    expect(await contentText(page)).not.toMatch(NOT_FOUND_MARK);
  });

  test('reloading a URL reached by client navigation still works', async ({ page }) => {
    await open(page, path('/'));
    await page.locator(`nav.mainnav a[href^="${BASE}cashback"]`).first().click();
    await expect(page).toHaveURL(/\/cashback\/?$/);
    await open(page, page.url());
    expect(await contentText(page)).not.toMatch(NOT_FOUND_MARK);
  });

  test('brand logo returns home', async ({ page }) => {
    await open(page, path('/support'));
    await page.locator('.topbar a.brand').first().click();
    await expect(page).toHaveURL(new RegExp(`${BASE}$`));
  });
});
