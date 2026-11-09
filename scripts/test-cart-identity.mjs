import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildSync } from 'esbuild';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
const dir = mkdtempSync(path.join(tmpdir(), 'cart-identity-'));
const output = path.join(dir, 'bundle.cjs');
buildSync({ entryPoints: ['src/lib/cartIdentity.ts'], bundle: true, platform: 'node', format: 'cjs', outfile: output });
const { sameCartSelection, cartItemKey, reconcileCart } = createRequire(import.meta.url)(output);
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));
const row = { id: 'Vodafone', country: 'TR', pkg: 'sku1', denomination: '100 TRY', value: 100, currency: 'TRY', delivery_type: 'by_phone', brand_id: 'a', qty: 1 };
test('identical lines and legacy rebuy merge preserving quantity and SKU', () => {
  const old = { ...row, pkg: '', brand_id: '', delivery_type: '' };
  for (const rows of [[row, old], [old, row]]) {
    const result = reconcileCart(rows, 100);
    assert.equal(result.length, 1);
    assert.equal(result[0].qty, 2);
    assert.equal(result[0].pkg, 'sku1');
    assert.equal(result[0].brand_id, 'a');
  }
});
test('different denominations, packages, countries, brands and channels stay separate', () => {
  for (const other of [{ denomination: '200 TRY' }, { pkg: 'sku2' }, { country: 'DE' }, { brand_id: 'b' }, { delivery_type: 'by_email' }, { currency: 'EUR' }]) {
    assert.equal(sameCartSelection(row, { ...row, ...other }), false, JSON.stringify(other));
    assert.equal(reconcileCart([row, { ...row, ...other }], 100).length, 2);
  }
  assert.notEqual(cartItemKey(row), cartItemKey({ ...row, denomination: '200 TRY' }));
});
test('range amounts stay separate, identical range amounts merge', () => {
  const range = { ...row, pkg: '', denomination: 'range' };
  assert.equal(reconcileCart([range, { ...range }], 100)[0].qty, 2);
  assert.equal(reconcileCart([range, { ...range, value: 200 }], 100).length, 2);
});
test('ambiguous missing identifiers never choose between distinct SKUs in any order', () => {
  const old = { ...row, pkg: '', brand_id: '' };
  const other = { ...row, pkg: 'sku2', brand_id: 'b' };
  for (const rows of [[old, row, other], [row, old, other], [row, other, old]]) {
    assert.equal(reconcileCart(rows, 100).length, 3);
  }
});
test('more than a row limit keeps every unit and gives distinct keys', () => {
  const result = reconcileCart([{ ...row, qty: 80 }, { ...row, qty: 50 }], 100);
  assert.deepEqual(result.map(r => r.qty), [100, 30]);
  assert.equal(new Set(result.map(cartItemKey)).size, 2);
  assert.deepEqual(reconcileCart(result, 100).map(r => r.qty), [100, 30]);
});

test('different named brands without brand IDs must not merge', () => {
  const a = { ...row, brand_id: '', brand: 'Credits' };
  const b = { ...row, brand_id: '', brand: 'Data' };
  assert.equal(sameCartSelection(a, b), false);
  assert.equal(reconcileCart([a, b], 100).length, 2);
});
