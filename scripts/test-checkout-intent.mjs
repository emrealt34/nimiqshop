/**
 * Unit tests for the checkout-key lifecycle — the 2026-10-18 rebuy regression
 * guard, mirrored by the Go tests in db/checkout_safety_test.go and
 * cmd/server/rebuy_test.go.
 *
 *   node --test scripts/test-checkout-intent.mjs
 *
 * Why this exists: the storefront's Idempotency-Key is derived from the cart,
 * so buying the same product twice sends the key of the FIRST purchase again.
 * The owner bought "UniPin 5 TRY", bought it again, and was thrown onto the
 * first, already-completed order page without ever seeing a pay screen. Two
 * rules are pinned here:
 *
 *   1. a key whose order is settled (delivered, refunded, or dead without
 *      money) is spent — the next purchase of the same cart mints a new one;
 *   2. an unresolved order keeps its key — a retry must still replay it and
 *      never open a second supplier order.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dir = mkdtempSync(path.join(tmpdir(), 'checkout-intent-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.cjs');

writeFileSync(entry, `
(globalThis as any).localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };
(globalThis as any).window ??= { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
(globalThis as any).document ??= { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { checkoutSettled, releaseIntentForQuote, saveIntentQuote, INTENT_PREFIX } from ${JSON.stringify(path.join(root, 'src/lib/checkoutIntent'))};
export { paidOrderHandedBack } from ${JSON.stringify(path.join(root, 'src/lib/pay'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=cjs', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr || 'esbuild failed to bundle src/lib/checkoutIntent.ts');
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const require = createRequire(import.meta.url);
const { checkoutSettled, releaseIntentForQuote, saveIntentQuote, INTENT_PREFIX, paidOrderHandedBack } = require(bundle);

/** A GET /api/quotes/{id} body as the backend shapes it. */
const lookup = (status, extra = {}) => ({ quote: { id: 'q1', status }, can_pay: status === 'awaiting_payment', ...extra });

test('the backend verdict wins: checkout_settled decides regardless of the status', () => {
  assert.equal(checkoutSettled(lookup('fulfilled', { checkout_settled: true })), true);
  assert.equal(checkoutSettled(lookup('expired', { checkout_settled: true })), true, 'lapsed unpaid past the grace buffer: the key is spent');
  assert.equal(checkoutSettled(lookup('failed', { checkout_settled: true })), true, 'rejected before any invoice: a retry may start over');
  assert.equal(checkoutSettled(lookup('fulfilled', { checkout_settled: false })), false, 'never second-guess the server when it says the checkout is open');
  assert.equal(checkoutSettled(lookup('expired', { checkout_settled: false })), false, 'inside the grace buffer money may still be in play');
});

test('without the flag only the two states that can never carry a pending payment release the key', () => {
  assert.equal(checkoutSettled(lookup('fulfilled')), true);
  assert.equal(checkoutSettled(lookup('refunded')), true);
  for (const open of ['order_creating', 'awaiting_payment', 'payment_started', 'payment_received', 'delivering', 'manual_review', 'expired', 'failed', '']) {
    assert.equal(checkoutSettled(lookup(open)), false, `${open || '(empty)'} keeps the key — the server decides`);
  }
  assert.equal(checkoutSettled({ status: 'fulfilled' }), true, 'a flat quote body is read the same way');
  assert.equal(checkoutSettled(null), false);
  assert.equal(checkoutSettled('fulfilled'), false);
});

test('a settled order releases exactly its own stored key and nothing else', () => {
  const store = new Map();
  const storage = {
    get length() { return store.size; },
    key: (i) => [...store.keys()][i] ?? null,
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => void store.set(k, String(v)),
    removeItem: (k) => void store.delete(k),
  };
  saveIntentQuote(storage, INTENT_PREFIX + 'cart-a', { key: 'k'.repeat(32), createdAt: 1 }, 'q-done');
  saveIntentQuote(storage, INTENT_PREFIX + 'cart-b', { key: 'j'.repeat(32), createdAt: 1 }, 'q-live');
  storage.setItem('nimshop.other', 'untouched');
  releaseIntentForQuote(storage, 'q-done');
  assert.equal(storage.getItem(INTENT_PREFIX + 'cart-a'), null, 'the finished order\'s key is gone, so the same cart gets a fresh one');
  assert.ok(storage.getItem(INTENT_PREFIX + 'cart-b'), 'an unrelated live checkout keeps its key');
  assert.equal(storage.getItem('nimshop.other'), 'untouched');
});

test('a checkout answer that is already paid is a handback, never a new purchase', () => {
  assert.equal(paidOrderHandedBack({ quote_id: 'q1', status: 'awaiting_payment', can_pay: true }), '', 'a fresh payable invoice opens the pay screen');
  assert.equal(paidOrderHandedBack({ quote_id: 'q1', status: 'order_creating' }), '');
  assert.equal(paidOrderHandedBack({ quote_id: 'q1', status: 'expired' }), '', 'unpaid-and-dead is handled by the renew path, not the already-paid screen');
  for (const paid of ['payment_received', 'delivering', 'fulfilled']) {
    assert.equal(paidOrderHandedBack({ quote_id: 'q1', status: paid, reused: true }), paid, `${paid}: the shop handed back an earlier paid order`);
  }
  assert.equal(paidOrderHandedBack({ quote: { id: 'q1', status: 'fulfilled' } }), 'fulfilled', 'nested quote bodies are read too');
  assert.equal(paidOrderHandedBack(null), '');
});
