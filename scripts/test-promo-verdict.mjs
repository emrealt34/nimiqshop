/**
 * Unit tests for the promo-code verdict — the 2026-10-19 regression guard.
 *
 *   node --test scripts/test-promo-verdict.mjs
 *
 * Why this exists: a promo code is proven ONCE, when it is typed in, and then
 * trusted for five minutes. In that window it can stop being true — it
 * expires, its uses run out, an admin retires it — and the cart kept showing
 * its cashback all the way to the order, where the code was quietly dropped.
 * The buyer found out when the cashback never arrived.
 *
 * The owner's rule: "promo kodu geçersizse o ilk şeyde söylemeli, ödemeye geç
 * butonuna basarken söylemeli" — say it at the first step, when the button is
 * pressed. So the checkout asks again, and only the shop's own refusal may
 * stop it:
 *
 *   1. a 4xx IS a refusal — the code is dead, name it and drop it;
 *   2. a 5xx or a dropped connection is NOT — blocking a buyer on our own
 *      outage would be punishing them for it, and the code must survive.
 */
import { test, before } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dir = mkdtempSync(path.join(tmpdir(), 'promo-verdict-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.cjs');

writeFileSync(entry, `
export { storeAppliedCashbackCode, readAppliedCashbackCode, clearAppliedCashbackCode, verifyAppliedCashbackCode } from ${JSON.stringify(path.join(root, 'src/lib/cashbackCode'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=cjs', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr || 'esbuild failed to bundle src/lib/cashbackCode.ts');
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

/* ---- The browser the module expects, in Node, BEFORE it is loaded. ---- */
const makeStore = () => {
  const map = new Map();
  return {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => void map.set(k, String(v)),
    removeItem: (k) => void map.delete(k),
    clear: () => map.clear(),
  };
};
const noop = () => {};
const fakeEl = { style: {}, classList: { add: noop, remove: noop, toggle: noop, contains: () => false }, setAttribute: noop, appendChild: noop, addEventListener: noop, removeEventListener: noop };
globalThis.localStorage = makeStore();
globalThis.sessionStorage = makeStore();
globalThis.window = {
  addEventListener: noop, removeEventListener: noop, dispatchEvent: noop,
  location: { href: 'https://shop.nimiqbase.com/cart', origin: 'https://shop.nimiqbase.com', pathname: '/cart', search: '', protocol: 'https:', host: 'shop.nimiqbase.com' },
  navigator: { language: 'en', languages: ['en'], userAgent: 'node' },
  matchMedia: () => ({ matches: false, addEventListener: noop, removeEventListener: noop }),
  setTimeout, clearTimeout, setInterval, clearInterval,
  localStorage: globalThis.localStorage, sessionStorage: globalThis.sessionStorage,
};
globalThis.document = {
  addEventListener: noop, removeEventListener: noop, cookie: '', visibilityState: 'visible',
  documentElement: { lang: 'en', style: {}, setAttribute: noop, classList: { add: noop, remove: noop, toggle: noop, contains: () => false } },
  createElement: () => fakeEl, querySelector: () => null, querySelectorAll: () => [], getElementById: () => null,
  body: fakeEl, head: fakeEl, title: '',
};
globalThis.navigator ??= globalThis.window.navigator;

/** What the shop answers, and what we send. */
let nextResponse = null;
let lastURL = '';
globalThis.fetch = async (url) => {
  lastURL = String(url);
  if (nextResponse === 'network') throw new TypeError('fetch failed');
  const { status = 200, body = {} } = nextResponse || {};
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(body),
    json: async () => body,
    headers: new Map(),
  };
};

const require = createRequire(import.meta.url);
const { storeAppliedCashbackCode, readAppliedCashbackCode, clearAppliedCashbackCode, verifyAppliedCashbackCode } = require(bundle);

/** A code applied ten minutes ago — old enough that no client cache is fresh. */
const apply = (code = 'ILOVENIM', bps = 750) => {
  clearAppliedCashbackCode();
  return storeAppliedCashbackCode({ code, cashback_bps: bps, fetched_at: Date.now() - 10 * 60_000 });
};
const respond = (status, body = {}) => { nextResponse = { status, body }; };
const okBody = { valid: true, code: 'ILOVENIM', cashback_bps: 750, cashback_percent: 7.5, cashback_source: 'code' };

before(() => { clearAppliedCashbackCode(); });

test('no code on the cart: nothing to prove, nothing to say', async () => {
  clearAppliedCashbackCode();
  assert.equal((await verifyAppliedCashbackCode(42)).state, 'none');
});

test('the shop says yes: the code is fresh, and the cart total is sent along', async () => {
  apply();
  respond(200, okBody);
  const verdict = await verifyAppliedCashbackCode(123.5);
  assert.equal(verdict.state, 'fresh');
  assert.equal(verdict.code.code, 'ILOVENIM');
  assert.equal(verdict.code.cashback_bps, 750);
  // The cap is decided against the order that is about to be created, so the
  // cart total has to travel with the question.
  assert.match(lastURL, /order_usd=123\.5$/);
  assert.ok(Date.now() - verdict.code.fetched_at < 60_000, 'the code is re-stamped as proven now');
});

test('the shop says 404 (invalid or expired): a refusal, named at the button', async () => {
  apply();
  respond(404, { error: 'cashback code "ILOVENIM" is invalid or expired' });
  const verdict = await verifyAppliedCashbackCode(0);
  assert.equal(verdict.state, 'rejected');
  assert.equal(verdict.status, 404);
  assert.equal(verdict.code, 'ILOVENIM', 'the message names the code that died');
});

test('the shop says 403 (not unlocked yet): a refusal, and a different sentence', async () => {
  apply('QUIZONLY');
  respond(403, { error: 'complete the quiz to unlock this cashback code for your account' });
  const verdict = await verifyAppliedCashbackCode(0);
  assert.equal(verdict.state, 'rejected');
  assert.equal(verdict.status, 403, '403 is what picks the "unlock it first" wording');
});

test('the shop says 400: a refusal like any other 4xx', async () => {
  apply();
  respond(400, { error: 'bad code' });
  assert.equal((await verifyAppliedCashbackCode(0)).state, 'rejected');
});

test('the shop is down (500): NOT a refusal — the buyer may still pay', async () => {
  apply();
  respond(500, { error: 'boom' });
  const verdict = await verifyAppliedCashbackCode(0);
  assert.equal(verdict.state, 'unknown');
  assert.equal(readAppliedCashbackCode()?.code, 'ILOVENIM', 'our outage must never strip a code that may still be good');
});

test('the connection drops: NOT a refusal either', async () => {
  apply();
  nextResponse = 'network';
  const verdict = await verifyAppliedCashbackCode(0);
  assert.equal(verdict.state, 'unknown');
  assert.equal(readAppliedCashbackCode()?.code, 'ILOVENIM');
});

test('a code that the shop still accepts keeps its cashback', async () => {
  apply('ILOVENIM', 750);
  respond(200, { ...okBody, cashback_bps: 400, cashback_percent: 4 });
  const verdict = await verifyAppliedCashbackCode(0);
  assert.equal(verdict.state, 'fresh');
  assert.equal(verdict.code.cashback_bps, 400, 'an admin can change the rate — the cart must show the new one');
  assert.equal(readAppliedCashbackCode()?.cashback_bps, 400);
});
