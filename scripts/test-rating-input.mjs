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
export { ratingIntent, submitRating, ApiError, _setSessionGetter, saveLightningFunding, flushPaymentSubmissions } from ${JSON.stringify(new URL('../src/lib/api.ts', import.meta.url).pathname)};
export { pendingSubmissions } from ${JSON.stringify(new URL('../src/lib/paymentSubmissionQueue.ts', import.meta.url).pathname)};
export { readPendingRating, rememberPendingRating, markPendingRatingSubmitted, awaitRatingSaved } from ${JSON.stringify(new URL('../src/lib/ratingPay.ts', import.meta.url).pathname)};
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

test('SDK identifiers remain queued offline and only the same buyer retries the metadata write', async () => {
  const savedFetch = globalThis.fetch;
  const hash = 'ab'.repeat(32);
  const calls = [];
  globalThis.fetch = async () => { throw new TypeError('offline'); };
  try {
    await client.saveLightningFunding('durable-payment', hash, 'wallet-swap');
    assert.equal(client.pendingSubmissions('buyer')[0].hash, hash);
    client._setSessionGetter(() => 'other');
    globalThis.fetch = async (url, options) => { calls.push({ url, body: JSON.parse(options.body) }); return Response.json({ saved: true }); };
    await client.flushPaymentSubmissions();
    assert.equal(calls.length, 0);
    client._setSessionGetter(() => 'buyer');
    await client.flushPaymentSubmissions();
    assert.equal(calls.length, 1);
    assert.deepEqual(calls[0].body, { hash, swap_id: 'wallet-swap' });
    assert.match(calls[0].url, /lightning-proof$/);
    assert.equal(client.pendingSubmissions('buyer').length, 0);
  } finally { globalThis.fetch = savedFetch; client._setSessionGetter(() => 'buyer'); }
});

test('submitted review retains comment and transaction hash beyond the old 15-minute timeout', () => {
  client.rememberPendingRating({ kind: 'order', id: 'durable-review', stars: 5, comment: 'excellent' });
  client.markPendingRatingSubmitted('order', 'durable-review', 'ef'.repeat(32));
  const pending = JSON.parse(storage.get(KEY));
  storage.set(KEY, JSON.stringify({ ...pending, ts: Date.now() - 24 * 60 * 60 * 1000 }));
  const recovered = client.readPendingRating('order', 'durable-review');
  assert.equal(recovered.comment, 'excellent');
  assert.equal(recovered.hash, 'ef'.repeat(32));
});

test('automatic metadata writes have a per-page budget and keep exhausted records', async () => {
  const savedFetch = globalThis.fetch;
  let calls = 0;
  client._setSessionGetter(() => 'bounded-buyer');
  globalThis.fetch = async () => { calls++; throw new TypeError('offline'); };
  try {
    await client.saveLightningFunding('bounded-payment', 'cd'.repeat(32), 'pending-swap');
    for (let index = 0; index < 10; index++) await client.flushPaymentSubmissions();
    assert.equal(calls, 3);
    assert.equal(client.pendingSubmissions('bounded-buyer').length, 1);
  } finally { globalThis.fetch = savedFetch; client._setSessionGetter(() => 'buyer'); }
});

test('review confirmation cannot poll more than three times or resubmit a wallet transaction', async () => {
  const savedFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => { calls++; return Response.json({ status: 'pending' }); };
  try {
    await assert.rejects(client.awaitRatingSaved('order', 'pending-review', { stars: 5, comment: 'excellent' }, { attempts: 1000, delayMs: 0 }), error => error.code === 'pending');
    assert.equal(calls, 3);
  } finally { globalThis.fetch = savedFetch; }
});
