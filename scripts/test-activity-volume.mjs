import { test } from 'node:test';
import assert from 'node:assert/strict';
import { activityVolumeNIM } from '../src/lib/activityVolume.ts';
import { publicActivityItems } from '../src/lib/activityVisibility.ts';

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

test('exact fake purchase IDs and simulations never inflate real activity volume', () => {
  const real = { id: 'real-carrefour', title: 'Carrefour', country: 'FR', nim: 100, address: 'same-buyer' };
  const otherReal = { id: 'real-amazon', title: 'Amazon', country: 'TR', nim: 50, address: 'same-buyer' };
  const input = [
    { id: '29760a02-9a43-40a2-8d5c-71277624df94', title: 'Amazon', nim: 4129 },
    { id: '0ac3ca80-a9cd-4524-a1fd-0c2fc95c583b', title: 'Amazon', nim: 19000 },
    { id: 'future-test', test_mode: true, nim: 5000 }, real, otherReal,
  ];
  const previous = JSON.stringify(input);
  const visible = publicActivityItems(input);
  assert.deepEqual(visible, [real, otherReal]);
  assert.equal(activityVolumeNIM(visible, 0), 150);
  assert.equal(JSON.stringify(input), previous, 'cached API response is never mutated');
});
