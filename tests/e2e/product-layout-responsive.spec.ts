/** Exercise real denomination/range/stock layouts, not an empty product fixture. */
import { test, expect, open } from './support/fixtures';
import { DEVICES, LANGS, RAW_KEY_RE, path, type DeviceClass } from './support/data';
import { audit, AUDIT_DEFAULTS, formatIssues, shoot } from './support/scan';

for (const scenario of ['packages', 'range', 'sold-out'] as const) {
  for (const lang of LANGS) {
    test.describe(`product ${scenario} · ${lang}`, () => {
      test.use({ lang });
      test('responsive product controls', async ({ page }, testInfo) => {
        const cls = (testInfo.project.metadata?.device || 'desktop') as DeviceClass;
        const product = {
          family: 'Travel & Shopping Card', brand: 'Travel & Shopping Card', brand_id: 'layout-card',
          kind: 'giftcard', category: 'Shopping', country_code: 'TR',
          is_out_of_stock: scenario === 'sold-out',
          products: scenario === 'range' ? [{
            product_id: 'range-sku', delivery_type: 'by_email', is_dynamic: true,
            range: { min: 5, max: 500, step_size: 1, currency: 'USD' },
          }] : [5, 25, 50, 100, 250, 500].map((amount) => ({
            product_id: `sku-${amount}`, delivery_type: 'by_email', denomination: `${amount} USD`, coin: 'BTC', coin_amount: '0.0001',
            face_value: { currency_code: 'USD', amount: { type: 'fixed', price: String(amount) } },
          })),
        };
        await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: [product] }));
        await page.route('**/api/catalog/unavailable**', (route) => route.fulfill({ json: { country: 'TR', families: [] } }));
        await page.route('**/api/catalog/price**', (route) => route.fulfill({ json: {
          coin: 'BTC', coin_amount: '0.0001', price_expires_at: new Date(Date.now() + 60_000).toISOString(),
        } }));
        for (const size of DEVICES[cls]) {
          await page.setViewportSize(size);
          await open(page, path('/product?id=Travel%20%26%20Shopping%20Card&country=TR'));
          await expect(page.locator('.pd-top')).toBeVisible();
          const buy = page.locator('.btn.btn-gold.btn-block.btn-lg').first();
          if (scenario === 'sold-out') await expect(buy).toBeDisabled();
          else await expect(buy).toBeEnabled();
          if (scenario === 'packages') {
            await page.locator('.packages-grid button').last().click();
          }
          const fees = page.locator('details.checkout-details-min summary').first();
          if (await fees.count()) await fees.click();
          await page.waitForTimeout(150);
          const issues = await audit(page, AUDIT_DEFAULTS[cls], RAW_KEY_RE);
          await shoot(page, cls, `product-${scenario}`, lang, `${size.width}x${size.height}`);
          expect(issues, formatIssues(issues)).toEqual([]);
        }
      });
    });
  }
}
