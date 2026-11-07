import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const vodafone = {
  id: 'vodafone-tr', name: 'Vodafone Türkiye', type: 'phone_refill', country: 'TR',
  currency: 'TRY', pkg: '100 TRY', value: 100, denomination: '100 TRY',
  unitUSD: 3, qty: 1, brand: 'Vodafone', category: 'mobile_credits', delivery_type: 'by_phone',
};
// Mocked quote endpoints below stop before any supplier order or payment.
test('single mobile top-up sends the entered receipt email with the E.164 number', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.addInitScript(item => localStorage.setItem('nimshop_cart', JSON.stringify([item])), vodafone);
  await page.route('**/api/catalog/check-phone*', route => route.fulfill({ json: { phone_number: '+905321234567', valid: true } }));
  const quoteRequests: Record<string, unknown>[] = [];
  await page.route(/\/api\/quotes(?:\?|$)/, async route => {
    if (route.request().method() !== 'POST') return route.fallback();
    quoteRequests.push(route.request().postDataJSON());
    await route.fulfill({ status: 400, json: { error: 'a valid email is required (your receipt and the order confirmation are sent to it)' } });
  });

  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.locator('#cdp-phone-0').fill('5321234567');
  await page.locator('#cdp-phone-0-again').fill('5321234567');
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();

  await expect.poll(() => quoteRequests.length).toBe(1);
  expect(quoteRequests[0]).toMatchObject({ product_id: 'vodafone-tr', email: 'buyer@example.com', phone_number: '+905321234567' });
});

test('mixed phone and email cart sends the buyer email on the batch and every line', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const amazon = {
    id: 'amazon-us', name: 'Amazon Gift Card', type: 'gift_card', country: 'US', currency: 'USD',
    pkg: 'USD25', value: 25, denomination: '25 USD', unitUSD: 25, qty: 1,
    brand: 'Amazon', category: 'giftcard', delivery_type: 'by_email',
  };
  const steam = {
    id: 'steam-us', name: 'Steam Gift Card', type: 'gift_card', country: 'US', currency: 'USD',
    pkg: 'USD20', value: 20, denomination: '20 USD', unitUSD: 20, qty: 1,
    brand: 'Steam', category: 'giftcard', delivery_type: 'by_email',
  };
  await page.addInitScript(items => localStorage.setItem('nimshop_cart', JSON.stringify(items)), [vodafone, amazon, steam]);
  await page.route('**/api/catalog/check-phone*', route => route.fulfill({ json: { phone_number: '+905321234567', valid: true } }));
  const requests: Record<string, any>[] = [];
  await page.route(/\/api\/quotes\/batch(?:\?|$)/, async route => {
    if (route.request().method() !== 'POST') return route.fallback();
    requests.push(route.request().postDataJSON());
    await route.fulfill({ status: 400, json: { error: 'test stops before supplier processing' } });
  });

  await open(page, path('/cart'));
  await page.locator('.btn-gold.btn-block.btn-lg').click();
  await page.locator('#cdp-phone-0').fill('5321234567');
  await page.locator('#cdp-phone-0-again').fill('5321234567');
  await page.getByPlaceholder('you@gmail.com').first().fill('buyer@example.com');
  await page.locator('#cdp-my-email-again').fill('buyer@example.com');
  await page.getByRole('button', { name: /Continue to payment/i }).click();

  await expect.poll(() => requests.length).toBe(1);
  expect(requests[0].email).toBe('buyer@example.com');
  expect(requests[0].items).toHaveLength(3);
  expect(requests[0].items.map((item: Record<string, unknown>) => item.email)).toEqual([
    'buyer@example.com', 'buyer@example.com', 'buyer@example.com',
  ]);
  expect(requests[0].items.map((item: Record<string, unknown>) => item.product_id)).toEqual([
    'vodafone-tr', 'amazon-us', 'steam-us',
  ]);
  expect(requests[0].items[0].phone_number).toBe('+905321234567');
});
