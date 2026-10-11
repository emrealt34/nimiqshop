import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

test('purchased face values retain monetary and package units without changing supplier selection', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'face-label-'));
  try {
    const bundle = join(dir, 'face-label.cjs');
    buildSync({ stdin: { contents: `export { productFaceLabel, quoteFaceValue } from './format'; export { cartSubtitle } from './cartStore'; export { youGetText, linesOf } from './deliveryCopy'; export { selectionPayload } from './productMoney';`, resolveDir: new URL('../src/lib/', import.meta.url).pathname },
      bundle: true, platform: 'node', format: 'cjs', outfile: bundle, define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, logLevel: 'silent' });
    const { productFaceLabel, quoteFaceValue, cartSubtitle, youGetText, linesOf, selectionPayload } = createRequire(import.meta.url)(bundle);
    const eur = { product_value: 10, product_currency: 'EUR', product_country: 'FR' };
    for (const [source, expected] of [
      [{ ...eur, face_label: '10' }, '10 €'],
      [{ ...eur, denomination: 'range' }, '10 €'],
      [{ ...eur, denomination: 'RANGE', product_value: 10.5 }, '10.5 €'],
      [{ ...eur, face_label: '10', product_currency: 'USD' }, '10 USD'],
      [{ face_label: '10', denomination: '10 USD', country: 'FR' }, '10 USD'],
      [{ denomination: '10,5', country: 'FR' }, '10.5 €'],
      [{ denomination: '10', country: 'BE' }, '10 €'],
      [{ denomination: '10', country: 'ZZ' }, '10'],
      [{ ...eur, denomination: '10', product_currency: 'PTS' }, '10'],
      [{ ...eur, denomination: '50 GB 90 days' }, '50 GB 90 days'],
      [{ ...eur, denomination: '575 Points' }, '575 Points'],
      [{ ...eur, denomination: '1000 V-Bucks' }, '1000 V-Bucks'],
      [{ ...eur, face_label: '10 EUR' }, '10 EUR'],
      [{ ...eur, denomination: '€10' }, '€10'],
      [{ ...eur, face_label: '10 €' }, '10 €'],
      [{ ...eur, product_value: Infinity }, ''],
      [{ ...eur, product_value: NaN }, ''],
    ]) {
      const unchanged = JSON.stringify(source);
      assert.equal(productFaceLabel(source), expected, JSON.stringify(source));
      assert.equal(JSON.stringify(source), unchanged, 'display leaves the source untouched');
    }
    assert.deepEqual(quoteFaceValue({ denomination: '10', country: 'FR' }), { value: 0, currency: '', label: '10 €' }, 'legacy display fallback never becomes a pricing input');
    assert.deepEqual(selectionPayload('10', 10), { denomination: '10' });
    assert.deepEqual(selectionPayload('range', 10), { denomination: 'range', product_value: 10 });
    const cart = { id: 'Carrefour', country: 'FR', denomination: '10', value: 10, currency: 'EUR', qty: 1 };
    assert.match(cartSubtitle(cart), /10 €/);
    assert.match(cartSubtitle({ ...cart, currency: 'USD' }), /10 USD/);
    assert.equal(youGetText(eur), '10 €');
    assert.equal(youGetText({ ...eur, denomination: '50 GB 90 days' }), '50 GB 90 days');
    assert.equal(productFaceLabel(linesOf([cart])[0]), '10 €');
  } finally { await rm(dir, { recursive: true, force: true }); }
});
