/**
 * A family the backend saw sold out at checkout (GET /api/catalog/unavailable)
 * must read "Out of stock" on its product page and must not offer a buy button
 * that fails again. Other families stay purchasable.
 */
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const family = {
  family: 'Ay Yildiz',
  brand: 'Ay Yildiz Credits',
  kind: 'mobile_recharge',
  category: 'mobile_credits',
  country_code: 'DE',
  logo_url: 'https://logos.example.test/ay-yildiz.svg',
  bg_color: '#FFFFFF',
  is_out_of_stock: false,
  products: [
    {
      product_id: 'emailed-pin',
      delivery_type: 'by_email',
      denomination: '2.5 EUR',
      localized_denomination: '€2,50',
      coin: 'BTC',
      coin_amount: '0.0002',
      face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '2.5' } },
    },
  ],
};

test.describe('product page out-of-stock mark', () => {
  test.use({ lang: 'en', api: { authed: false }, viewport: { width: 1280, height: 800 } });

  test('a family refused at checkout reads Out of stock and cannot be bought', async ({ page }) => {
    await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: family }));
    await page.route('**/api/catalog/unavailable**', (route) => route.fulfill({ json: { country: 'DE', families: ['Ay Yildiz'] } }));
    await open(page, path('/product?id=Ay%20Yildiz&country=DE'));

    await expect(page.getByRole('status').filter({ hasText: 'Out of stock' })).toBeVisible();
    await expect(page.locator('.btn.btn-gold.btn-block.btn-lg').first()).toBeDisabled();
  });

  test('a family not on the list stays purchasable', async ({ page }) => {
    await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: family }));
    await page.route('**/api/catalog/unavailable**', (route) => route.fulfill({ json: { country: 'DE', families: [] } }));
    await open(page, path('/product?id=Ay%20Yildiz&country=DE'));

    await expect(page.locator('.btn.btn-gold.btn-block.btn-lg').first()).toBeEnabled();
    await expect(page.getByRole('status').filter({ hasText: 'Out of stock' })).toHaveCount(0);
  });

  test('an empty variant cannot make a sold-out variant purchasable', async ({ page }) => {
    await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: [
      { ...family, products: [], is_out_of_stock: false },
      { ...family, is_out_of_stock: true },
    ] }));
    await open(page, path('/product?id=Ay%20Yildiz&country=DE'));
    await expect(page.getByRole('status').filter({ hasText: 'Out of stock' })).toBeVisible();
    await expect(page.locator('.btn.btn-gold.btn-block.btn-lg').first()).toBeDisabled();
  });

  test('a live variant stays purchasable without offering a sold-out denomination', async ({ page }) => {
    await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: [
      { ...family, products: [{ ...family.products[0], product_id: 'sold-sku', denomination: '999 EUR' }], is_out_of_stock: true },
      family,
    ] }));
    await open(page, path('/product?id=Ay%20Yildiz&country=DE'));
    await expect(page.locator('.btn.btn-gold.btn-block.btn-lg').first()).toBeEnabled();
    await expect(page.getByText('999 EUR', { exact: true })).toHaveCount(0);
  });
});

test.describe('shelf availability reconciliation', () => {
  test.use({ lang: 'en', api: { authed: false } });
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('nimshop_country', 'TR'));
    const catalog = { generated_at: new Date().toISOString(), country_code: 'TR', categories: [{
      kind: 'giftcard', category: 'Shopping', brands: ['Live Card', 'Sold Card'].map((name) => ({
        family: name, brand_id: name, kind: 'giftcard', category: 'Shopping', country_code: 'TR', is_out_of_stock: false,
        min: '5 USD', max: '50 USD',
      })),
    }] };
    await page.route('**/data/catalog/brands/*.json', (route) => route.fulfill({ json: catalog }));
    await page.route('**/api/catalog/unavailable**', (route) => route.fulfill({ json: { country: 'TR', families: [] } }));
    await page.route('**/api/catalog/products/**', (route) => {
      const name = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop() || '');
      return route.fulfill({ json: [{ ...family, family: name, country_code: 'TR', kind: 'giftcard', is_out_of_stock: name === 'Sold Card' }] });
    });
    await page.route('**/api/catalog/search**', (route) => route.fulfill({ json: [{ family: 'Sold Card', kind: 'giftcard', country_code: 'TR' }] }));
  });

  test('prefetch updates stock before navigation and search keeps the stock flag', async ({ page }) => {
    await open(page, path('/'));
    await page.locator('#hideOOS').uncheck();
    const sold = page.locator('.product-card').filter({ hasText: 'Sold Card' });
    await sold.scrollIntoViewIfNeeded();
    // Hover also checks stock on engines that defer IntersectionObserver.
    await sold.hover();
    await expect(sold.locator('.oos')).toHaveText('Out of stock');
    await expect(sold).not.toHaveAttribute('href');
    await page.getByRole('searchbox', { name: 'Search products' }).fill('Sold');
    await expect(sold.locator('.oos')).toHaveText('Out of stock');
    await expect(sold).not.toHaveAttribute('href');
  });

  test('a click on inventory that just sold out stays on the shelf', async ({ page }) => {
    await open(page, path('/'));
    const live = page.locator('.product-card').filter({ hasText: 'Live Card' });
    await expect(live).toHaveAttribute('href', /id=Live%20Card/);
    // Invalidate an earlier warm result: click must perform a fresh check.
    await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: [{ ...family, is_out_of_stock: true }] }));
    await live.click();
    await expect(page).toHaveURL(/\/nimiqshop\/$/);
    await expect(page.getByText('This one is sold out right now.', { exact: true })).toBeVisible();
    await page.locator('#hideOOS').uncheck();
    await expect(page.locator('.product-card').filter({ hasText: 'Live Card' }).locator('.oos')).toBeVisible();
  });

  test('available inventory opens the product normally', async ({ page }) => {
    await open(page, path('/'));
    await page.locator('.product-card').filter({ hasText: 'Live Card' }).click();
    await expect(page).toHaveURL(/product.*id=Live%20Card/);
    await expect(page.locator('.btn.btn-gold.btn-block.btn-lg').first()).toBeEnabled();
  });

  test('a new checkout refusal blocks navigation even with products in the directory', async ({ page }) => {
    await open(page, path('/'));
    const live = page.locator('.product-card').filter({ hasText: 'Live Card' });
    await expect(live).toHaveAttribute('href', /id=Live%20Card/);
    await page.route('**/api/catalog/unavailable**', (route) => route.fulfill({ json: { country: 'TR', families: ['live card'] } }));
    await live.click();
    await expect(page).toHaveURL(/\/nimiqshop\/$/);
    await expect(live.locator('.oos')).toHaveText('Out of stock');
    await expect(live).not.toHaveAttribute('href');
  });
});
