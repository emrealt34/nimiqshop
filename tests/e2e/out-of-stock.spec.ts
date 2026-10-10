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
});
