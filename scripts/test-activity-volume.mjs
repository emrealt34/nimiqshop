import { test } from 'node:test';
import assert from 'node:assert/strict';
import { activityVolumeNIM } from '../src/lib/activityVolume.ts';

test('USD-only volume becomes NIM and mixed rows contribute exactly once', () => {
  assert.equal(activityVolumeNIM([{ usd: '6' }], '0.002'), 3000);
  assert.equal(activityVolumeNIM([{ nim: '100', usd: 6 }, { usd: 6 }, { nim_estimate: 20, usd: 2 }], 0.002), 3120);
  assert.equal(activityVolumeNIM([{ nim: 100, usd: 6 }], 0.5), 100, 'purchase-time amount is preserved');
  assert.equal(activityVolumeNIM([], 0), 0);
});

test('missing or malformed rates never produce USD, a partial total or infinity', () => {
  for (const rate of [0, -1, undefined, NaN, Infinity, 'broken']) {
    assert.equal(activityVolumeNIM([{ nim: 100 }, { usd: 6 }], rate), null);
    assert.equal(activityVolumeNIM([{ nim: 100 }], rate), 100);
  }
  assert.equal(activityVolumeNIM([{ usd: 6 }], Number.MIN_VALUE), null);
  assert.equal(activityVolumeNIM([{ nim: Infinity, usd: 6 }], 0.002), 3000);
  assert.equal(activityVolumeNIM([{ nim: Number.MAX_VALUE }, { nim: Number.MAX_VALUE }], 1), null);
});
