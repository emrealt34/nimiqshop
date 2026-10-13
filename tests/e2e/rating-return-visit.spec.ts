import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const purchase = { id: 'unrated-real-shape', product_id: 'Amazon', status: 'fulfilled', rating: 0, quantity: 1, product_country: 'TR', created_at: new Date(Date.now()-86400000).toISOString(), delivered_at: new Date(Date.now()-60000).toISOString() };

test.use({ lang: 'tr' });

test('session bootstrap cannot close or silence the returning-buyer reminder', async ({ page }) => {
  const expires = Math.floor(Date.now()/1000)+43200;
  let release!: () => void;
  const resumed = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/auth/session', async route => {
    await resumed;
    await route.fulfill({ json: { authed: true, user: { id: 'u1', nimiq_address: 'NQ12 3456 7890 ABCD EFGH JKLM NPQR STUV' }, expires_at: expires } });
  });
  await page.route('**/api/ratings/config', route => route.fulfill({ json: { enabled: true, comments_enabled: true } }));
  await page.route('**/api/orders', route => route.fulfill({ json: [] }));
  await page.route('**/api/quotes', route => route.fulfill({ json: [purchase] }));
  await open(page, path('/'));
  await expect(page.locator('.sheet')).toContainText('Henüz değerlendirmediğin alışverişlerin var');
  release();
  await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop.sess') || '{}').expiresAt)).toBe(expires);
  await expect(page.locator('.sheet')).toContainText('Henüz değerlendirmediğin alışverişlerin var');
});

test('reminder waits for an existing sheet and opens when it closes', async ({ page }) => {
  let release!: () => void;
  const resumed = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/ratings/config', route => route.fulfill({json:{enabled:true}}));
  await page.route('**/api/orders', route => route.fulfill({json:[]}));
  await page.route('**/api/quotes', async route => { await resumed; await route.fulfill({json:[purchase]}); });
  await open(page,path('/'));
  await expect(page.locator('.hero')).toBeVisible();
  await page.evaluate(() => (window as any).__nimshop.openSheet({title:'Existing sheet',render:()=> 'Existing dialog'}));
  await expect(page.locator('.sheet')).toContainText('Existing dialog');
  const history = page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/api/quotes'));
  release();
  await history;
  await expect(page.locator('.sheet')).toContainText('Existing dialog');
  await page.evaluate(() => (window as any).__nimshop.closeSheet());
  await expect(page.locator('.sheet')).toContainText('Henüz değerlendirmediğin alışverişlerin var');
});

test('a newly delivered purchase is not suppressed by an older purchase or legacy snooze', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('nimshop.rating-reminder.v1:u1', String(Date.now()+7*86400000));
    localStorage.setItem('nimshop.rating-reminder.v2:u1:quote:old-purchase', String(Date.now()+7*86400000));
  });
  await page.route('**/api/ratings/config', route => route.fulfill({ json: { enabled: true } }));
  await page.route('**/api/orders', route => route.fulfill({ json: [] }));
  await page.route('**/api/quotes', route => route.fulfill({ json: [{...purchase,id:'old-purchase'},purchase] }));
  await open(page, path('/'));
  await expect(page.locator('.sheet')).toContainText('Teslim edilmiş 1 alışverişini');
  await expect(page.locator('.sheet a').first()).toHaveAttribute('href', /unrated-real-shape/);
});

for (const width of [390,1280]) {
  test(`Orders stars open the purchase rating form at ${width}px`, async ({ page }) => {
    await page.setViewportSize({width,height:844});
    await page.route('**/api/ratings/config', route => route.fulfill({json:{enabled:true,comments_enabled:true}}));
    await page.route('**/api/orders', route => route.fulfill({json:[]}));
    await page.route('**/api/quotes', route => route.fulfill({json:[purchase]}));
    await page.route('**/api/quotes/unrated-real-shape', async route => {
      await new Promise(resolve=>setTimeout(resolve,250));
      await route.fulfill({json:purchase});
    });
    await open(page,path('/orders'));
    const stars=page.locator('.ord-rate');
    await expect(stars).toHaveAttribute('href', /order\?type=quote&id=unrated-real-shape#purchase-rating/);
    await stars.locator('svg').first().click();
    await expect(page).toHaveURL(/order\/?\?type=quote&id=unrated-real-shape#purchase-rating/);
    await expect(page.locator('.rating-flow .star-btn')).toHaveCount(5);
    await expect(page.locator('.rating-flow .stars.picker')).toBeInViewport();
  });
}
