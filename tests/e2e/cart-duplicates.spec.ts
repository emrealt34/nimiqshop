import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const phone = { id: 'vodafone-tr', name: 'Vodafone Türkiye', type: 'phone_refill', country: 'TR', currency: 'TRY', pkg: 'sku100', value: 100, denomination: '100 TRY', unitUSD: 3, qty: 1, brand_id: 'vodafone', delivery_type: 'by_phone' };
const card = { id: 'amazon-tr', name: 'Amazon', type: 'gift_card', country: 'TR', currency: 'TRY', pkg: 'sku250', value: 250, denomination: '250 TRY', unitUSD: 7, qty: 1, brand_id: 'amazon', delivery_type: 'by_email' };

for (const [label, item] of [['phone', phone], ['email', card]] as const) {
  test(`legacy and current identical ${label} rows merge and quote both units`, async ({ page }) => {
    await page.addInitScript(rows => { if (!localStorage.getItem('nimshop_cart')) localStorage.setItem('nimshop_cart', JSON.stringify(rows)); }, [item, { ...item, pkg: '', brand_id: '', delivery_type: '' }]);
    await page.route('**/api/catalog/check-phone*', route => route.fulfill({ json: { phone_number: '+905321234567', valid: true } }));
    const requests: any[] = [];
    await page.route(/\/api\/quotes(?:\?|$)/, async route => {
      if (route.request().method() !== 'POST') return route.fallback();
      requests.push(route.request().postDataJSON());
      await route.fulfill({ status: 400, json: { error: 'test stops before payment' } });
    });
    await open(page, path('/cart'));
    await expect(page.locator('.cart-row')).toHaveCount(1);
    await page.locator('.btn-gold.btn-block.btn-lg').click();
    if (label === 'phone') {
      await expect(page.locator('#cdp-phone-0')).toBeVisible();
      await expect(page.locator('#cdp-phone-1')).toHaveCount(0);
      await page.locator('#cdp-phone-0').fill('5321234567');
      await page.locator('#cdp-phone-0-again').fill('5321234567');
    }
    await expect(page.getByPlaceholder('you@gmail.com')).toHaveCount(1);
    await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
    await page.locator('#cdp-my-email-again').fill('buyer@example.com');
    await page.getByRole('button', { name: /Continue to payment/i }).click();
    await expect.poll(() => requests.length).toBe(1);
    expect(requests[0]).toMatchObject({ product_id: item.id, quantity: 2, email: 'buyer@example.com' });
    if (label === 'phone') expect(requests[0].phone_number).toBe('+905321234567');
  });
}

test('different selections of the same family retain separate phone destinations', async ({ page }) => {
  await page.addInitScript(rows => localStorage.setItem('nimshop_cart', JSON.stringify(rows)), [phone, { ...phone, pkg: 'sku200', denomination: '200 TRY', value: 200 }]);
  await page.route('**/api/catalog/check-phone*', async route => {
    const input = route.request().url();
    await route.fulfill({ json: { phone_number: input.includes('5321234567') ? '+905321234567' : '+905331234567', valid: true } });
  });
  const requests: any[] = [];
  await page.route(/\/api\/quotes\/batch(?:\?|$)/, async route => {
    if (route.request().method() !== 'POST') return route.fallback();
    requests.push(route.request().postDataJSON());
    await route.fulfill({ status: 400, json: { error: 'test stops before payment' } });
  });
  await open(page, path('/cart'));
  await expect(page.locator('.cart-row')).toHaveCount(2);
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.locator('#cdp-phone-0').fill('5321234567');
  await page.locator('#cdp-phone-0-again').fill('5321234567');
  await page.locator('#cdp-phone-1').fill('5331234567');
  await page.locator('#cdp-phone-1-again').fill('5331234567');
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();
  await expect.poll(() => requests.length).toBe(1);
  expect(requests[0].items.map((it: any) => it.phone_number)).toEqual(['+905321234567', '+905331234567']);
});

test('batch keeps quantities above ten instead of silently truncating', async ({ page }) => {
  await page.addInitScript(rows => localStorage.setItem('nimshop_cart', JSON.stringify(rows)), [{ ...card, qty: 12 }, { ...card, id: 'steam', pkg: 'steam25', name: 'Steam' }]);
  const requests: any[] = [];
  await page.route(/\/api\/quotes\/batch(?:\?|$)/, async route => {
    if (route.request().method() !== 'POST') return route.fallback();
    requests.push(route.request().postDataJSON());
    await route.fulfill({ status: 400, json: { error: 'test stops before payment' } });
  });
  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();
  await expect.poll(() => requests.length).toBe(1);
  expect(requests[0].items.map((it: any) => it.quantity)).toEqual([12, 1]);
});

test('adding the same product twice on its product page increments one persistent row', async ({ page }) => {
  const family = {
    family: 'Vodafone Türkiye', brand: 'Vodafone', brand_id: 'vodafone', kind: 'mobile_recharge', category: 'mobile_credits', country_code: 'TR', is_out_of_stock: false,
    products: [{ product_id: 'sku100', delivery_type: 'by_phone', denomination: '100 TRY', face_value: { currency_code: 'TRY', amount: { type: 'fixed', price: '100' } } }],
  };
  await page.route('**/api/catalog/products/**', route => route.fulfill({ json: family }));
  await open(page, path('/product?id=Vodafone%20T%C3%BCrkiye&country=TR'));
  const add = page.locator('button').filter({ hasText: /add to cart/i });
  await expect(add).toBeEnabled();
  await add.click();
  await add.click();
  await expect.poll(async () => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'))).toMatchObject([{ id: 'Vodafone Türkiye', pkg: 'sku100', qty: 2 }]);
  await open(page, path('/cart'));
  await expect(page.locator('.cart-row')).toHaveCount(1);
  const saved = await page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'));
  expect(saved).toHaveLength(1);
  expect(saved[0].qty).toBe(2);
});

test('a reconciled legacy row can be decremented and removed without losing or resurrecting items', async ({ page }) => {
  await page.addInitScript(rows => { if (!localStorage.getItem('nimshop_cart')) localStorage.setItem('nimshop_cart', JSON.stringify(rows)); }, [card, { ...card, pkg: '', brand_id: '', delivery_type: '' }]);
  await open(page, path('/cart'));
  const row = page.locator('.cart-row');
  await expect(row).toHaveCount(1);
  await expect(row.locator('.cart-qty')).toHaveText('2');
  await row.locator('.cart-q').first().click();
  await expect(row.locator('.cart-qty')).toHaveText('1');
  await page.reload();
  await expect(page.locator('.cart-row .cart-qty')).toHaveText('1');
  await page.locator('.cart-row .cart-x').click();
  await expect(page.locator('.cart-row')).toHaveCount(0);
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'))).toEqual([]);
});

test('reused supplier product IDs keep the selected denomination and delivery channel', async ({ page }) => {
  const products = [
    { product_id: 'shared', delivery_type: 'by_email', denomination: '50 TRY', amount: 50, currency_code: 'TRY' },
    { product_id: 'shared', delivery_type: 'by_phone', denomination: '100 TRY', amount: 100, currency_code: 'TRY' },
  ];
  await page.route('**/api/catalog/products/**', route => route.fulfill({ json: { family: 'Test Mobile', brand: 'Test Mobile', brand_id: 'test-mobile', kind: 'mobile_recharge', category: 'mobile_credits', country_code: 'TR', products, is_out_of_stock: false } }));
  await open(page, path('/product?id=Test%20Mobile&country=TR'));
  const choices = page.locator('.pd-pkg');
  await expect(choices).toHaveCount(2);
  await choices.nth(1).click();
  await expect(choices.nth(1)).toHaveClass(/active/);
  await expect(choices.nth(0)).not.toHaveClass(/active/);
  await page.locator('button').filter({ hasText: /add to cart/i }).click();
  await choices.nth(0).click();
  await page.locator('button').filter({ hasText: /add to cart/i }).click();
  await expect.poll(async () => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'))).toMatchObject([
    { pkg: 'shared', denomination: '100 TRY', value: 100, delivery_type: 'by_phone' },
    { pkg: 'shared', denomination: '50 TRY', value: 50, delivery_type: 'by_email' },
  ]);
  await open(page, path('/cart'));
  await expect(page.locator('.cart-row')).toHaveCount(2);
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await expect(page.locator('#cdp-phone-0')).toBeVisible();
  await expect(page.locator('#cdp-phone-1')).toHaveCount(0);
  await expect(page.getByPlaceholder('you@gmail.com')).toHaveCount(1);
});

test('same label and reused ID from different brands remain distinct in the cart', async ({ page }) => {
  const p = { product_id: 'shared', delivery_type: 'by_phone', denomination: '100 TRY', amount: 100, currency_code: 'TRY' };
  const common = { family: 'Test Mobile', kind: 'mobile_recharge', category: 'mobile_credits', country_code: 'TR', products: [p], is_out_of_stock: false };
  await page.route('**/api/catalog/products/**', route => route.fulfill({ json: [
    { ...common, brand: 'Mobile Credits', brand_id: 'credits' },
    { ...common, brand: 'Mobile Data', brand_id: 'data' },
  ] }));
  await open(page, path('/product?id=Test%20Mobile&country=TR'));
  const choices = page.locator('.pd-pkg');
  await expect(choices).toHaveCount(2);
  await choices.nth(0).click();
  await page.locator('button').filter({ hasText: /add to cart/i }).click();
  await choices.nth(1).click();
  await page.locator('button').filter({ hasText: /add to cart/i }).click();
  await expect.poll(async () => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'))).toMatchObject([
    { brand: 'Mobile Credits', brand_id: 'credits', qty: 1 },
    { brand: 'Mobile Data', brand_id: 'data', qty: 1 },
  ]);
  await open(page, path('/cart'));
  await expect(page.locator('.cart-row')).toHaveCount(2);
});

test('different supplier brands are visible before selecting uniquely priced top-ups', async ({ page }) => {
  const common = { family: 'Vodafone', kind: 'mobile_recharge', category: 'mobile_credits', country_code: 'TR', is_out_of_stock: false };
  await page.route('**/api/catalog/products/**', route => route.fulfill({ json: [
    { ...common, brand: 'Vodafone Cyprus Credits', brand_id: 'cyprus', products: [{ product_id: 'cyprus12', delivery_type: 'by_phone', denomination: '12 TRY', amount: 12, currency_code: 'TRY' }] },
    { ...common, brand: 'Vodafone Credits', brand_id: 'turkey', products: [{ product_id: 'turkey250', delivery_type: 'by_phone', denomination: '250 TRY', amount: 250, currency_code: 'TRY' }] },
  ] }));
  await open(page, path('/product?id=Vodafone&country=TR'));
  const choices = page.locator('.pd-pkg');
  await expect(choices).toHaveCount(2);
  await expect(choices.nth(0)).toContainText('Vodafone Cyprus Credits');
  await expect(choices.nth(1)).toContainText('Vodafone Credits');
  await choices.nth(1).click();
  await page.getByRole('button', { name: /add to cart/i }).click();
  await expect.poll(async () => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'))).toMatchObject([{ brand_id: 'turkey', pkg: 'turkey250', value: 250 }]);
});

test('missing FX never treats a TRY face value as dollars', async ({ page }) => {
  await page.route('**/data/market/fx.json', r => r.fulfill({ json: {} }));
  await page.route('**/api/market/fx', r => r.fulfill({ json: {} }));
  await page.route('**/api/catalog/limits', r => r.fulfill({ json: { supplier_order_limit: { enabled: true, face_caps: { USD: 104 } } } }));
  await page.route('**/api/catalog/products/**', r => r.fulfill({ json: {
    family: 'Vodafone', brand: 'Vodafone Credits', brand_id: 'turkey', kind: 'mobile_recharge', category: 'mobile_credits', country_code: 'TR',
    products: [{ product_id: 'turkey250', delivery_type: 'by_phone', denomination: '250 TRY', amount: 250, currency_code: 'TRY' }],
  } }));
  await open(page, path('/product?id=Vodafone&country=TR'));
  const add = page.getByRole('button', { name: /add to cart/i });
  await expect(add).toBeEnabled();
  // Allow the limits request to resolve, but never install any FX table.
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('nim_fx'))).toBeNull();
  await add.click();
  await expect.poll(async () => page.evaluate(() => JSON.parse(localStorage.getItem('nimshop_cart') || '[]'))).toMatchObject([{ pkg: 'turkey250', value: 250, qty: 1 }]);
});
