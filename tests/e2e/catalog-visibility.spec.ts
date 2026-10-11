import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

test.use({ lang: 'en', api: { authed: false } });

test('e-money duplicates and KYC flags never reach the shelf or prefetch', async ({ page }) => {
  const catalog = { generated_at: new Date().toISOString(), country_code: 'TR', categories: [
    { kind: 'giftcard', category: 'retail', brands: [
      { family: 'Normal Card', min: '5 USD', max: '50 USD' },
      { family: 'Mixed Financial Card', brand_id: 'same', min: '5 USD', max: '50 USD' },
      { family: 'KYC Card', requires_kyc: true, min: '5 USD', max: '50 USD' },
    ] },
    { kind: 'giftcard', category: 'e-money', brands: [{ family: 'Mixed Financial Card', brand_id: 'same' }] },
  ] };
  const prefetched: string[] = [];
  await page.route('**/data/catalog/brands/*.json', route => route.fulfill({ json: catalog }));
  await page.route('**/api/catalog/products/**', route => {
    const family = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop() || '');
    prefetched.push(family);
    return route.fulfill({ json: [{ family, country_code: 'TR', products: [{ product_id: 'normal', delivery_type: 'by_email' }] }] });
  });
  await open(page, path('/'));
  await expect(page.locator('.product-card').filter({ hasText: 'Normal Card' })).toBeVisible();
  await page.locator('#hideOOS').uncheck();
  await expect(page.locator('.product-card').filter({ hasText: 'Mixed Financial Card' })).toHaveCount(0);
  await expect(page.locator('.product-card').filter({ hasText: 'KYC Card' })).toHaveCount(0);
  await expect(page.locator('.product-card .kyc-badge')).toHaveCount(0);
  expect(prefetched.filter(family => /Financial|KYC/.test(family))).toEqual([]);
});

test('a direct product link cannot revive an e-money family hidden in additional categories', async ({ page }) => {
  await page.route('**/api/catalog/products/**', route => route.fulfill({ json: [{
    family: 'Mixed Financial Card', category: 'retail', additional_categories: ['e-money'], country_code: 'TR',
    products: [{ product_id: 'blocked', delivery_type: 'by_email', denomination: '10 USD' }],
  }] }));
  await open(page, path('/product?id=Mixed%20Financial%20Card&country=TR'));
  await expect(page.getByRole('heading', { name: 'Not available right now' })).toBeVisible();
  await expect(page.locator('.pd-info .btn-gold')).toHaveCount(0);
});
