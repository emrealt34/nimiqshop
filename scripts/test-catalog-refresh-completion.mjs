import { test } from 'node:test';
import assert from 'node:assert/strict';
import { refreshCompletion } from './catalog-refresh-completion.mjs';

const now = Date.parse('2026-10-11T18:00:00Z');
const at = '2026-10-11T17:59:00Z';
const catalog = { complete: true, completed_at: at, failed: [],
  verified: { mode: 'full', completed: true, checked: 8681, skipped: 0, unresolved: 0 } };
const market = { generated_at: at,
  nim: { observed_at: at, usd_per_nim: 0.0003 },
  btc: { observed_at: at, usd_per_btc: 80000 },
  fx: { observed_at: '2026-10-11T00:00:00Z', currencies: 166 } };

test('full stock scan and fresh crypto rates accept daily provider fiat timestamps', () => {
  assert.deepEqual(refreshCompletion(catalog, market, now), { complete: true, ratesComplete: true });
});
test('pending, unresolved, retained countries and reused scans cannot pass completion', () => {
  for (const bad of [
    { ...catalog, complete: false },
    { ...catalog, failed: [{ country: 'DE' }] },
    { ...catalog, completed_at: '2026-10-10T18:00:00Z' },
    ...[{ skipped: 1 }, { unresolved: 1 }, { mode: 'incremental' }, { completed: false }]
      .map(fields => ({ ...catalog, verified: { ...catalog.verified, ...fields } })),
  ]) assert.equal(refreshCompletion(bad, market, now).complete, false);
});
test('a successful catalog cannot mask stale, missing or invalid rate legs', () => {
  for (const bad of [null, { ...market, nim: 'kept-previous-file' },
    { ...market, btc: { ...market.btc, observed_at: '2026-10-11T16:00:00Z' } },
    { ...market, btc: { ...market.btc, usd_per_btc: 0 } },
    { ...market, nim: { ...market.nim, observed_at: '2026-10-12T00:00:00Z' } },
    { ...market, fx: 'kept-previous-file' },
    { ...market, fx: { ...market.fx, observed_at: '2026-10-01T00:00:00Z' } },
  ]) assert.deepEqual(refreshCompletion(catalog, bad, now), { complete: false, ratesComplete: false });
});
test('a previous complete snapshot cannot mask a crashed refresh step', () => {
  assert.equal(refreshCompletion(catalog, market, now, { catalog: 'failure', market: 'success' }).complete, false);
  assert.equal(refreshCompletion(catalog, market, now, { catalog: 'success', market: 'failure' }).ratesComplete, false);
  assert.equal(refreshCompletion(catalog, market, now, { catalog: 'skipped', market: 'skipped' }).complete, false);
});
