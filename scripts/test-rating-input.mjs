import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';
import { ratingStars } from '../src/lib/ratingStars.ts';

const dir = mkdtempSync(join(tmpdir(), 'rating-input-'));
const entry = join(dir, 'entry.ts');
const bundle = join(dir, 'bundle.cjs');
writeFileSync(entry, `
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
globalThis.window = { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
globalThis.document = { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { ratingIntent, submitRating, ApiError, _setSessionGetter } from ${JSON.stringify(new URL('../src/lib/api.ts', import.meta.url).pathname)};
export { readPendingRating, rememberPendingRating } from ${JSON.stringify(new URL('../src/lib/ratingPay.ts', import.meta.url).pathname)};
`);
buildSync({ entryPoints: [entry], bundle: true, platform: 'node', format: 'cjs', outfile: bundle,
  define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, loader: { '.css': 'empty' }, logLevel: 'silent' });
const require = createRequire(import.meta.url);
const client = require(bundle);
client._setSessionGetter(() => 'buyer');
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));
const storage = new Map();
globalThis.sessionStorage = { getItem: (k) => storage.get(k) || null, setItem: (k, v) => storage.set(k, v), removeItem: (k) => storage.delete(k) };
const KEY = 'nimshop.pendingRating';

test('whole stars and form strings are normalized without rounding', () => {
  for (let n = 1; n <= 5; n++) {
    assert.equal(ratingStars(n), n);
    assert.equal(ratingStars(String(n)), n);
    assert.equal(ratingStars(` ${n} `), n);
  }
  for (const value of [0, 6, -1, 2.5, NaN, Infinity, null, undefined, true, {}, [], '', '0', '6', '2.5', '5stars', '1e0']) {
    assert.equal(ratingStars(value), null, String(value));
  }
});

test('intent and submission send integer JSON stars for orders and quotes', async () => {
  const savedFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, opts) => {
    calls.push({ url, body: JSON.parse(opts.body) });
    return Response.json({ status: 'ok' });
  };
  try {
    for (const kind of ['order', 'quote']) {
      await client.ratingIntent(kind, 'purchase', { stars: '5', comment: 'fast delivery' });
      await client.submitRating(kind, 'purchase', { stars: '4', tx_hash: 'a'.repeat(64) });
    }
    assert.deepEqual(calls.map((c) => c.body.stars), [5, 4, 5, 4]);
    assert.equal(calls[0].body.comment, 'fast delivery');
    assert.equal(calls[1].body.tx_hash, 'a'.repeat(64));
    assert.match(calls[0].url, /\/orders\/purchase\/rating\/intent$/);
    assert.match(calls[3].url, /\/quotes\/purchase\/rate$/);
    for (const fn of [client.ratingIntent, client.submitRating]) {
      for (const stars of [0, 6, 2.5, '2.5', null]) {
        await assert.rejects(fn('order', 'purchase', { stars }), (e) => e instanceof client.ApiError && e.code === 'INVALID_RATING_STARS');
      }
    }
    assert.equal(calls.length, 4, 'invalid stars are rejected before API or wallet work');
  } finally { globalThis.fetch = savedFetch; }
});

test('redirect recovery normalizes valid stars and discards malformed pending state', () => {
  client.rememberPendingRating({ kind: 'order', id: 'o1', stars: 5, comment: 'good' });
  const valid = JSON.parse(storage.get(KEY));
  storage.set(KEY, JSON.stringify({ ...valid, stars: '5' }));
  assert.equal(client.readPendingRating('order', 'o1').stars, 5);
  assert.equal(client.readPendingRating('quote', 'q1'), null, 'another purchase cannot resume this rating');
  assert.ok(storage.has(KEY), 'a valid rating for another purchase is preserved');
  for (const changes of [{ stars: 2.5 }, { stars: '5stars' }, { stars: 6 }, { comment: {} }, { ts: 'yesterday' }, { ts: Date.now() + 120_000 }, { ts: Date.now() - 16 * 60_000 }]) {
    storage.set(KEY, JSON.stringify({ ...valid, ...changes }));
    assert.equal(client.readPendingRating('order', 'o1'), null);
    assert.equal(storage.has(KEY), false, 'bad pending state must not trigger repeat invalid submissions');
  }
  storage.set(KEY, '{broken');
  assert.equal(client.readPendingRating('order', 'o1'), null);
  assert.equal(storage.has(KEY), false);
});
