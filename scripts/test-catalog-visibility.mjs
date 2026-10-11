import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';
import { excludedCatalogItem, filterPublicCatalog, filterPublicFamilies } from '../src/lib/catalogVisibility.ts';

const input = { country_code: 'TR', categories: [
  { kind: 'giftcard', category: 'retail', brands: [{ family: 'Mixed Card', brand_id: 'same' }, { family: 'Steam' }, { family: 'KYC card', requires_kyc: true }] },
  { kind: 'giftcard', category: ' E_MONEY ', brands: [{ family: 'mixed card', brand_id: 'same' }] },
  { kind: 'mobile_recharge', category: 'e-sim', brands: [{ family: 'eSIM' }] },
] };
test('whole e-money families disappear before category and duplicate filtering, leaving normal products', () => {
  const previous = JSON.stringify(input);
  const filtered = filterPublicCatalog(input);
  assert.deepEqual(filtered.categories.flatMap(c => c.brands.map(b => b.family)), ['Steam', 'eSIM']);
  assert.equal(JSON.stringify(input), previous, 'cached source remains untouched');
  for (const category of ['e-money', 'e_money', 'e money', 'EMONEY']) assert.equal(excludedCatalogItem({ category }), true);
  assert.equal(excludedCatalogItem({ requires_kyc: false, category: 'gaming' }), false);
  assert.deepEqual(filterPublicFamilies([{ family: 'Card', products: [], additional_categories: ['e-money'] }]), []);
  assert.deepEqual(filterPublicFamilies({ products: [{ product_id: 'blocked', kyc_required: true }] }), []);
});

test('normalization and warm browser caches cannot restore an excluded shelf', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'catalog-visibility-'));
  const require = createRequire(import.meta.url);
  try {
    const bundle = join(dir, 'catalog.cjs');
    buildSync({ stdin: { contents: `export { flattenBrands } from './catalog'; export { readCachedCatalog } from './catalogCache';`, resolveDir: new URL('../src/lib/', import.meta.url).pathname },
      bundle: true, platform: 'node', format: 'cjs', outfile: bundle, define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, logLevel: 'silent' });
    const { flattenBrands, readCachedCatalog } = require(bundle);
    assert.deepEqual(flattenBrands(input).map(p => p.family), ['Steam', 'eSIM']);
    const maps = new Map();
    globalThis.localStorage = { getItem: key => maps.get(key) || null };
    const cached = JSON.stringify({ at: Date.now(), gift_card: [{ family: 'Legacy money', is_e_money: true }, { family: 'Steam' }] });
    maps.set('nimshop_cat_v3:TR', cached);
    assert.equal(readCachedCatalog('TR'), null, 'old merged metadata cannot bypass the new policy');
    maps.set('nimshop_cat_v4:TR', cached);
    assert.deepEqual(readCachedCatalog('TR').gift_card.map(p => p.family), ['Steam']);
  } finally { delete globalThis.localStorage; await rm(dir, { recursive: true, force: true }); }
});
