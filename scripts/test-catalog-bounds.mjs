import test from 'node:test';
import assert from 'node:assert/strict';
import {
  productMoney, parseMoneyLabel, formatMoneyLike, familyFaceBounds,
  applyFaceBoundsToBrand, faceCapIn, faceCapEur,
} from './catalog-bounds.mjs';

const FX = { USD: 1, EUR: 1.1, TRY: 0.03 };
const LIMITS = { totalEur: 100, marginPct: 5 }; // face cap ≈ 95.23 EUR

test('productMoney reads structured face_value like the product page', () => {
  assert.deepEqual(
    productMoney({ face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '5' } } }),
    { value: 5, currency: 'EUR' },
  );
  assert.deepEqual(productMoney({ amount: 25, currency_code: 'usd' }), { value: 25, currency: 'USD' });
  assert.deepEqual(productMoney({ denomination: '575 Points' }), { value: 0, currency: '' });
});

test('parseMoneyLabel keeps the label style', () => {
  const a = parseMoneyLabel('€2,000');
  assert.equal(a.value, 2000);
  assert.equal(a.currency, 'EUR');
  assert.equal(a.style.where, 'prefix');
  assert.equal(a.style.sep, ',');
  const b = parseMoneyLabel('50 USD');
  assert.equal(b.value, 50);
  assert.equal(b.currency, 'USD');
  assert.equal(b.style.where, 'suffix');
});

test('formatMoneyLike rewrites in the original style', () => {
  const style = parseMoneyLabel('€2,000').style;
  assert.equal(formatMoneyLike(106, 'EUR', style), '€106');
  assert.equal(formatMoneyLike(2500, 'EUR', style), '€2,500');
  const suffix = parseMoneyLabel('50 USD').style;
  assert.equal(formatMoneyLike(106, 'USD', suffix), '106 USD');
});

test('faceCapIn mirrors the backend face cap', () => {
  assert.equal(Math.round(faceCapEur(LIMITS) * 100) / 100, 95.24);
  assert.equal(faceCapIn(FX, 'EUR', LIMITS), 95);
  assert.equal(faceCapIn(FX, 'USD', LIMITS), 104); // floor(95.238 * 1.1)
  assert.equal(faceCapIn(FX, 'XXX', LIMITS), 0);
  assert.equal(faceCapIn(FX, 'EUR', { totalEur: 0 }), 0);
});

test('familyFaceBounds reads the Amazon.de SKU set exactly', () => {
  const products = [
    { product_id: 'a', denomination: '5 EUR', localized_denomination: '€5', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '5' } }, delivery_type: 'by_email' },
    { product_id: 'b', denomination: '10 EUR', localized_denomination: '€10', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '10' } }, delivery_type: 'by_email' },
    { product_id: 'c', denomination: '50 EUR', localized_denomination: '€50', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '50' } }, delivery_type: 'by_email' },
    { product_id: 'd', denomination: '75 EUR', localized_denomination: '€75', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '75' } }, delivery_type: 'by_email' },
    { product_id: 'e', denomination: '500 EUR', localized_denomination: '€500', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '500' } }, delivery_type: 'by_email', is_out_of_stock: true },
    { product_id: 'f', denomination: '900 EUR', delivery_type: 'by_account', face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: '900' } } },
  ];
  const bounds = familyFaceBounds(products);
  assert.equal(bounds.currency, 'EUR');
  assert.deepEqual(bounds.fixed.map((f) => f.v), [5, 10, 50, 75]);
  assert.equal(bounds.range, null);
  const b = { min: '€5', max: '€2,000' };
  assert.equal(applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS }), true);
  assert.equal(b.min, '€5');
  assert.equal(b.max, '€75');
});

test('range products clamp to the Lightning face cap and keep the label style', () => {
  const bounds = familyFaceBounds([
    { product_id: 'r', is_dynamic: true, range: { min: 50, max: 500, currency: 'USD', step_size: 1 } },
  ]);
  assert.deepEqual(bounds.range, { min: 50, max: 500 });
  const b = { min: '$50', max: '$500' };
  applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS });
  assert.equal(b.min, '$50');
  assert.equal(b.max, '$104'); // floor(95.238 EUR * 1.1)
});

test('fixed denominations above the cap are hidden from the range', () => {
  const bounds = familyFaceBounds([
    { product_id: 'a', denomination: '5 EUR', localized_denomination: '€5', face_value: { currency_code: 'EUR', amount: { price: '5' } } },
    { product_id: 'b', denomination: '120 EUR', localized_denomination: '€120', face_value: { currency_code: 'EUR', amount: { price: '120' } } },
    { product_id: 'c', denomination: '300 EUR', localized_denomination: '€300', face_value: { currency_code: 'EUR', amount: { price: '300' } } },
  ]);
  const b = { min: '€5', max: '€300' };
  applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS });
  assert.equal(b.max, '€5'); // only €5 still fits the 95 EUR face cap
});

test('mixed currencies and money-less families leave the labels alone', () => {
  assert.equal(familyFaceBounds([
    { product_id: 'a', denomination: '5 EUR', face_value: { currency_code: 'EUR', amount: { price: '5' } } },
    { product_id: 'b', denomination: '10 USD', face_value: { currency_code: 'USD', amount: { price: '10' } } },
  ]), null);
  assert.equal(familyFaceBounds([{ product_id: 'a', denomination: '575 Points' }]), null);
  assert.equal(familyFaceBounds([]), null);
  const b = { min: '€5', max: '€2,000' };
  assert.equal(applyFaceBoundsToBrand(b, null, { fx: FX, limits: LIMITS }), false);
  assert.equal(b.max, '€2,000');
});

test('label fallback parses SKU denominations when structured money is missing', () => {
  const bounds = familyFaceBounds([
    { product_id: 'a', denomination: '25 TRY' },
    { product_id: 'b', denomination: '250 TRY' },
  ]);
  assert.equal(bounds.currency, 'TRY');
  assert.deepEqual(bounds.fixed.map((f) => f.v), [25, 250]);
  const b = { min: '₺25', max: '₺2,500' };
  applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS });
  // The SKU's own display label is what the product page shows — the card
  // repeats it verbatim, exactly like the Amazon.de chips ("€5" … "€75").
  assert.equal(b.min, '25 TRY');
  assert.equal(b.max, '250 TRY');
});

test('the /v5/products family shape flattens to the same bounds', () => {
  const bounds = familyFaceBounds([
    { family: 'Amazon.de', brand: 'Amazon.de', products: [
      { product_id: 'a', denomination: '5 EUR', localized_denomination: '€5', face_value: { currency_code: 'EUR', amount: { price: '5' } } },
      { product_id: 'b', denomination: '75 EUR', localized_denomination: '€75', face_value: { currency_code: 'EUR', amount: { price: '75' } } },
    ] },
  ]);
  assert.equal(bounds.currency, 'EUR');
  assert.deepEqual(bounds.fixed.map((f) => f.v), [5, 75]);
  const b = { min: '€5', max: '€2,000' };
  applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS });
  assert.equal(b.max, '€75');
});

test('non-money display labels (eSIM data plans) keep the supplier wording', () => {
  const bounds = familyFaceBounds([
    { family: 'eSIM', products: [
      { product_id: 'a', denomination: '1 GB 3 Days', face_value: { currency_code: 'USD', amount: { price: '1.45' } } },
      { product_id: 'b', denomination: '10 GB 30 Days', face_value: { currency_code: 'USD', amount: { price: '12' } } },
    ] },
  ]);
  assert.equal(bounds.currency, 'USD');
  const b = { min: '1 GB 3 days', max: '100 GB 180 days' };
  assert.equal(applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS }), false);
  assert.equal(b.max, '100 GB 180 days');
});

test('mixed family keeps fixed SKUs even when the range is above the cap', () => {
  const bounds = familyFaceBounds([
    { product_id: 'a', denomination: '5 EUR', localized_denomination: '€5', face_value: { currency_code: 'EUR', amount: { price: '5' } } },
    { product_id: 'b', denomination: '75 EUR', localized_denomination: '€75', face_value: { currency_code: 'EUR', amount: { price: '75' } } },
    { product_id: 'r', is_dynamic: true, range: { min: 200, max: 500, currency: 'EUR' } },
  ]);
  const b = { min: '€5', max: '€2,000' };
  applyFaceBoundsToBrand(b, bounds, { fx: FX, limits: LIMITS });
  assert.equal(b.min, '€5');
  assert.equal(b.max, '€75');
});
