import { test, expect } from './support/fixtures';
import { path } from './support/data';
import type { Page } from '@playwright/test';

async function openLoading(page: Page, url: string) {
  await page.goto(url);
  await page.waitForFunction(() => document.documentElement.dataset.appReady === '1');
  await page.evaluate(() => document.fonts.ready);
}

async function checkBars(page: Page, selector: string) {
  const bars = page.locator(selector);
  await expect(bars.first()).toBeVisible();
  await expect.poll(async () => bars.evaluateAll(nodes => nodes.map(node => {
    const rect = node.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0 && getComputedStyle(node).animationName === 'none';
  }).every(Boolean))).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
}

for (const width of [320, 768, 1280]) {
  for (const theme of ['light', 'dark']) {
    test.describe(`loading twins ${width}px ${theme}`, () => {
      test.use({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
      test.beforeEach(async ({ page }) => {
        await page.addInitScript(theme => { sessionStorage.setItem('e2e.theme', theme); localStorage.setItem('nimshop.theme', theme); }, theme);
      });
      test('orders stay full-width rows until data arrives', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/orders', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/orders'));
          await checkBars(page, '.order-list [aria-hidden] .skel');
          expect(await page.locator('.orders-page .grid.products').count()).toBe(0);
          await expect.poll(() => page.locator('.order-list').evaluate(list => Math.abs(list.getBoundingClientRect().width - list.querySelector('.order-card')!.getBoundingClientRect().width))).toBeLessThan(2);
          await page.screenshot({ path: test.info().outputPath('orders-loading.png'), fullPage: true });
          release();
          await expect(page.locator('.order-list .skel')).toHaveCount(0);
          await expect(page.locator('.order-card').first()).toBeVisible();
        } finally { release(); }
      });
      test('tracking key/value bars have visible widths', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/track/**', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/track?order=o1'));
          await checkBars(page, '.kv-skeleton .skel');
          await page.screenshot({ path: test.info().outputPath('track-loading.png'), fullPage: true });
        } finally { release(); }
      });
      test('activity reserves actual tiles and feed row layouts', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/activity**', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/activity'));
          await checkBars(page, '#stats .skel, #feed .skel, #summary .skel');
          await expect(page.locator('#stats .act-tile')).toHaveCount(4);
          await expect(page.locator('#summary .rs-dist')).toHaveCount(1);
          await page.screenshot({ path: test.info().outputPath('activity-loading.png'), fullPage: true });
          release();
          await expect(page.locator('#feed .skel')).toHaveCount(0);
        } finally { release(); }
      });
      test('cashback leaderboard does not claim empty while loading', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/cashback/leaderboard**', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/cashback'));
          await checkBars(page, '.pt-leader .skel');
          await page.screenshot({ path: test.info().outputPath('cashback-loading.png'), fullPage: true });
          release();
          await expect(page.locator('.pt-leader .skel')).toHaveCount(0);
          await expect(page.locator('.pt-leader li').first()).toContainText('#1');
        } finally { release(); }
      });
      test('storefront skeleton matches product card structure', async ({ page }) => {
        await page.addInitScript(() => localStorage.setItem('nimshop_country', 'US'));
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/data/catalog/brands/US.json', async route => { await gate; await route.fallback(); });
        await page.route('**/api/catalog/brands**', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/'));
          await checkBars(page, '.product-card-skeleton .skel');
          await expect(page.locator('.product-card-skeleton .p-name')).toHaveCount(4);
          await expect(page.locator('.product-card-skeleton .p-meta')).toHaveCount(4);
          await page.screenshot({ path: test.info().outputPath('shop-loading.png'), fullPage: true });
          release();
          await expect(page.locator('.product-card-skeleton')).toHaveCount(0);
        } finally { release(); }
      });
      test('product image follows the real hero layout', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/catalog/products/**', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/product?id=Amazon&country=TR'));
          await checkBars(page, '.pd-skeleton .skel, .pd-skeleton .skeleton-box');
          await expect(page.locator('.skeleton-row')).toHaveCount(0);
          await page.screenshot({ path: test.info().outputPath('product-loading.png'), fullPage: true });
          release();
          await expect(page.locator('.pd-skeleton')).toHaveCount(0);
        } finally { release(); }
      });
      test('quiz keeps its heading and accessible loading group', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/quiz', async route => { await gate; await route.fulfill({ json: { items: [] } }); });
        try {
          await openLoading(page, path('/quiz'));
          await checkBars(page, '.quiz-page .skel');
          await expect(page.locator('.quiz-heading h1')).toBeVisible();
          await page.screenshot({ path: test.info().outputPath('quiz-loading.png'), fullPage: true });
          release();
          await expect(page.locator('.quiz-page .skel')).toHaveCount(0);
        } finally { release(); }
      });
      test('profile summary uses noncollapsed loading lines', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/account/limits', async route => { await gate; await route.fallback(); });
        try {
          await openLoading(page, path('/profile'));
          await checkBars(page, '.kv-skeleton .skel');
          release();
          await expect(page.locator('.kv-skeleton')).toHaveCount(0);
        } finally { release(); }
      });
      test('admin overview has visible bounded loading rows', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/api/admin/**', async route => {
          if (new URL(route.request().url()).pathname.endsWith('/auth/me')) return route.fulfill({ json: { admin: { id: 'operator', username: 'operator' } } });
          await gate; await route.fulfill({ json: {} });
        });
        try {
          await openLoading(page, path('/admin?section=overview'));
          await checkBars(page, '.loading-rows:visible .skel');
        } finally { release(); }
      });
      test('refresh report reserves rows then clears skeleton', async ({ page }) => {
        let release!: () => void;
        const gate = new Promise<void>(resolve => { release = resolve; });
        await page.route('**/data/market/refresh.json', async route => {
          await gate;
          await route.fulfill({ json: { catalog: { generated_at: new Date().toISOString(), countries: 1, stock: {} }, market: {} } });
        });
        try {
          await openLoading(page, path('/status'));
          await checkBars(page, '.refresh-status-list .skel');
          release();
          await expect(page.locator('.refresh-status-list .skel')).toHaveCount(0);
        } finally { release(); }
      });
    });
  }
}

test('cashback loading ends on failure and a manual retry recovers', async ({ page }) => {
  let failed = true;
  let calls = 0;
  await page.route('**/api/cashback/leaderboard**', route => {
    calls++;
    return failed ? route.fulfill({ status: 500, json: { error: 'internal' } }) : route.fallback();
  });
  await openLoading(page, path('/cashback'));
  await expect(page.locator('.pt-page .empty .btn')).toBeVisible();
  await expect(page.locator('.pt-leader .skel')).toHaveCount(0);
  const before = calls;
  failed = false;
  await page.locator('.pt-page .empty .btn').click();
  await expect(page.locator('.pt-leader li').first()).toContainText('#1');
  expect(calls).toBe(before + 1);
});
