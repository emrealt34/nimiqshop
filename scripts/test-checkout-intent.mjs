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
export { checkoutSettled, releaseIntentForQuote, saveIntentQuote, forgetIntent, digestIntent, INTENT_PREFIX } from ${JSON.stringify(path.join(root, 'src/lib/checkoutIntent'))};
export { paidOrderHandedBack, liveOrderHandedBack } from ${JSON.stringify(path.join(root, 'src/lib/pay'))};
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
const { checkoutSettled, releaseIntentForQuote, saveIntentQuote, forgetIntent, digestIntent, INTENT_PREFIX, paidOrderHandedBack, liveOrderHandedBack } = require(bundle);

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

/* ---- live-duplicate prompt (owner, 2026-10-18): "önceki faturası hâlâ
   geçerliyken aynı ürünü tekrar almaya çalışırsa dükkân sormalı" ---- */

const NOW = Date.parse('2026-10-18T12:00:00Z');
const inMin = (m) => new Date(NOW + m * 60000).toISOString();
const liveReply = (extra = {}) => ({ quote_id: 'q-live', status: 'awaiting_payment', can_pay: true, payment_expires_at: inMin(14.2), reused: true, ...extra });

test('a reused, still-payable invoice of the same cart becomes the "open order" question', () => {
  const live = liveOrderHandedBack(liveReply(), NOW);
  assert.deepEqual(live, { quoteId: 'q-live', expiresAt: NOW + 14.2 * 60000, minutesLeft: 15 }, 'minutes are rounded UP so the prompt never promises less time than there is');
  assert.equal(liveOrderHandedBack(liveReply({ payment_expires_at: inMin(0.2) }), NOW)?.minutesLeft, 1, 'never "0 min" while it is still payable');
  assert.equal(liveOrderHandedBack({ ...liveReply(), payment_expires_at: undefined, payment_expiry: inMin(5) }, NOW)?.minutesLeft, 5, 'the alternative deadline field is read too');
});

test('a genuinely new order never triggers the question', () => {
  assert.equal(liveOrderHandedBack({ quote_id: 'q-new', status: 'awaiting_payment', can_pay: true, payment_expires_at: inMin(15) }, NOW), null, 'no reused marker: the shop created this one');
  assert.equal(liveOrderHandedBack({ quote_id: 'q-new', status: 'awaiting_payment', can_pay: true, payment_expires_at: inMin(15), reused: false }, NOW), null);
});

test('a handback that cannot be paid belongs to the other screens, not to the question', () => {
  for (const paid of ['payment_received', 'delivering', 'fulfilled']) {
    assert.equal(liveOrderHandedBack(liveReply({ status: paid }), NOW), null, `${paid}: the already-paid screen owns it`);
  }
  assert.equal(liveOrderHandedBack(liveReply({ status: 'order_creating', can_pay: false }), NOW), null);
  assert.equal(liveOrderHandedBack(liveReply({ can_pay: false }), NOW), null, 'the server did not authorise payment');
  assert.equal(liveOrderHandedBack(liveReply({ payment_expires_at: inMin(-1) }), NOW), null, 'lapsed: the renew path owns it');
  assert.equal(liveOrderHandedBack(liveReply({ payment_expires_at: 'garbage' }), NOW), null, 'unknown deadline: never claim "N minutes"');
  assert.equal(liveOrderHandedBack(liveReply({ payment_observed: true }), NOW), null, 'money was seen: no second order may be offered');
  assert.equal(liveOrderHandedBack(liveReply({ payment_blocked: true }), NOW), null);
  assert.equal(liveOrderHandedBack(liveReply({ quote_id: '' }), NOW), null, 'no id, nothing to continue with');
  assert.equal(liveOrderHandedBack(null, NOW), null);
  assert.equal(liveOrderHandedBack('reused', NOW), null);
});

test('"open a new order" forgets exactly that cart\'s key, so the next request cannot replay the live order', async () => {
  const store = new Map();
  const storage = {
    get length() { return store.size; },
    key: (i) => [...store.keys()][i] ?? null,
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => void store.set(k, String(v)),
    removeItem: (k) => void store.delete(k),
  };
  const scopeA = 'u1|/quotes|{"product_id":"a"}';
  const scopeB = 'u1|/quotes|{"product_id":"b"}';
  const keyA = INTENT_PREFIX + await digestIntent(scopeA);
  const keyB = INTENT_PREFIX + await digestIntent(scopeB);
  saveIntentQuote(storage, keyA, { key: 'k'.repeat(32), createdAt: 1 }, 'q-live');
  saveIntentQuote(storage, keyB, { key: 'j'.repeat(32), createdAt: 1 }, 'q-other');
  await forgetIntent(storage, scopeA);
  assert.equal(storage.getItem(keyA), null, 'the live order\'s key is gone: the next POST mints a fresh one');
  assert.ok(storage.getItem(keyB), 'another cart\'s checkout keeps its key');
  await forgetIntent(storage, 'u1|/quotes|never-stored');
  assert.equal(store.size, 1, 'forgetting an unknown scope touches nothing');
});
