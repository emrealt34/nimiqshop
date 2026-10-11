import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

test('delivery clocks use immutable observations and preserve minutes and seconds', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'delivery-timing-'));
  try {
    const file = join(dir, 'timing.cjs');
    buildSync({ stdin: { contents: `export { fmtDuration, purchaseFaceLabel } from './format'; export { deliverySeconds, purchaseElapsedSeconds } from './deliveryTiming'; export { quoteStages, shouldAskRating } from './orderTrack'; export { youGetText } from './deliveryCopy';`, resolveDir: new URL('../src/lib/', import.meta.url).pathname },
      bundle: true, platform: 'node', format: 'cjs', outfile: file, define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, logLevel: 'silent' });
    const { fmtDuration, deliverySeconds, purchaseElapsedSeconds, quoteStages, shouldAskRating, purchaseFaceLabel, youGetText } = createRequire(import.meta.url)(file);
    for (const [input, expected] of [[0, '<1s'], [.5, '<1s'], [59.8, '59s'], [60, '1 min 0s'], [61, '1 min 1s'], [119, '1 min 59s'], [3599, '59 min 59s'], [3600, '1h 0m 0s'], [3661, '1h 1m 1s'], [null, '—'], [-1, '—'], [NaN, '—'], [Infinity, '—']]) assert.equal(fmtDuration(input), expected);
    const row = { id: 'real', status: 'fulfilled', created_at: '2026-10-11T16:14:36Z', paid_at: '2026-10-11T16:21:58Z', delivered_at: '2026-10-11T16:22:00Z', updated_at: '2026-10-11T21:00:00Z', rated_at: '2026-10-11T21:00:00Z' };
    assert.equal(deliverySeconds(row), 2);
    assert.equal(purchaseElapsedSeconds(row), 444);
    assert.equal(deliverySeconds({ ...row, paid_at: row.delivered_at }), 0);
    assert.equal(deliverySeconds({ ...row, paid_at: undefined, payment_handoff_at: row.created_at }), null, 'opening a wallet is not proof of payment');
    assert.equal(deliverySeconds({ ...row, paid_at: '2026-10-11T17:00:00Z' }), null);
    assert.equal(deliverySeconds({ ...row, delivered_at: null }), null, 'a later rating cannot supply a missing fulfillment stamp');
    assert.equal(deliverySeconds({ ...row, delivered_at: null, supplier_updated_at: row.delivered_at }), 2);
    assert.equal(quoteStages(row)[1].timestamp, row.paid_at);
    assert.equal(quoteStages(row)[3].timestamp, row.delivered_at);
    assert.equal(shouldAskRating({ ...row, rating: 0 }, 'quote', { now: Date.parse(row.updated_at) }), false);
    const legacy = { payload: { value: 10, currency: 'EUR', country: 'FR' }, quantity: 2 };
    assert.equal(purchaseFaceLabel(legacy), '20 € (10 € ×2)');
    assert.equal(purchaseFaceLabel({ denomination: '50 GB 90 days', quantity: 2, product_value: 16.16, product_currency: 'USD' }), '50 GB 90 days ×2');
    assert.equal(youGetText({ lines: [{ product_id: 'Carrefour', face_label: '10', product_value: 10, product_currency: 'EUR', quantity: 2 }, { product_id: 'eSIM', denomination: '50 GB 90 days', quantity: 1 }] }), 'Carrefour · 20 € (10 € ×2) + eSIM · 50 GB 90 days');
  } finally { await rm(dir, { recursive: true, force: true }); }
});
