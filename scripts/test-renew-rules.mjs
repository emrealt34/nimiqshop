/**
 * Unit tests for the payment-window / renewal-safety rules — the 2026-10-04
 * regression guard, mirrored by the Go test `db/checkout_safety_test.go`.
 *
 *   node --test scripts/test-renew-rules.mjs
 *
 * Why this exists: the owner's live order sat on "Ödeme kontrolleri
 * duraklatıldı / Durum: WaitingForPayment" with no way forward, because a
 * local countdown running out was treated as proof about money. Two rules are
 * pinned here and they must hold in BOTH directions:
 *
 *   1. a timer is never proof of failure — inside the supplier's grace buffer
 *      the buyer is told the shop is still verifying, not that nothing was
 *      charged;
 *   2. a second payment is never offered while the first may exist — observed
 *      payments, holds, in-flight settlement and unreadable records all keep
 *      the renew button away.
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
const dir = mkdtempSync(path.join(tmpdir(), 'renew-rules-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.cjs');

writeFileSync(entry, `
(globalThis as any).localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };
(globalThis as any).window ??= { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
(globalThis as any).document ??= { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { canRenewQuote, paymentInFlight, paymentWindowOver, paymentWindowVerifying, PAYMENT_VERIFY_GRACE_MS } from ${JSON.stringify(path.join(root, 'src/lib/pay'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=cjs', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr || 'esbuild failed to bundle src/lib/pay.ts');
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const require = createRequire(import.meta.url);
const { canRenewQuote, paymentInFlight, paymentWindowOver, paymentWindowVerifying, PAYMENT_VERIFY_GRACE_MS } = require(bundle);

const NOW = Date.parse('2026-10-04T12:00:00Z');
const GRACE = PAYMENT_VERIFY_GRACE_MS;
assert.ok(GRACE > 0, 'the grace buffer must be positive');
const at = (ms) => new Date(NOW + ms).toISOString();

/** A shipped Lightning quote: open window unless told otherwise. */
const quote = (over = {}) => ({
  id: 'q1', status: 'awaiting_payment', supplier_order_id: 'sup-1',
  wallet_address: 'lnbc1invoice', payment_expiry: at(20 * 60_000),
  can_pay: true, ...over,
});

test('a still-open window is never renewed — the buyer pays the invoice they have', () => {
  const q = quote();
  assert.equal(canRenewQuote(q, NOW), false);
  assert.equal(paymentWindowOver(q, NOW), false);
  assert.equal(paymentWindowVerifying(q, NOW), false);
});

test('a just-lapsed invoice is "verifying", not "nothing was charged"', () => {
  const q = quote({ payment_expiry: at(-60_000), can_pay: false });
  assert.equal(paymentWindowOver(q, NOW), true);
  assert.equal(paymentWindowVerifying(q, NOW), true);
  assert.equal(canRenewQuote(q, NOW), false, 'a timer is not proof: no fresh invoice inside the grace buffer');
});

test('once the grace buffer has elapsed with nothing seen, a fresh invoice is safe', () => {
  const q = quote({ payment_expiry: at(-GRACE), can_pay: false });
  assert.equal(paymentWindowVerifying(q, NOW), false);
  assert.equal(canRenewQuote(q, NOW), true);
});

test('an observed payment is never renewed, however old the window is', () => {
  const q = quote({ payment_expiry: at(-48 * 3600_000), payment_observed: true, can_pay: false });
  assert.equal(paymentInFlight(q), true);
  assert.equal(paymentWindowVerifying(q, NOW), false);
  assert.equal(canRenewQuote(q, NOW), false);
});

test('an operator hold blocks renewal even with an ancient deadline', () => {
  const q = quote({ payment_expiry: at(-48 * 3600_000), payment_blocked: true, can_pay: false });
  assert.equal(canRenewQuote(q, NOW), false);
});

test('a supplier state beyond "waiting for payment" blocks renewal', () => {
  for (const supplier_status of ['PaymentStarted', 'PartialPaymentStarted', 'PaymentReceived', 'WaitingForDelivery', 'Done']) {
    const q = quote({ payment_expiry: at(-48 * 3600_000), supplier_status, can_pay: false });
    assert.equal(paymentInFlight(q), true, supplier_status);
    assert.equal(canRenewQuote(q, NOW), false, supplier_status);
  }
});

test('a supplier that only says "waiting for payment" is not a money claim', () => {
  const q = quote({ payment_expiry: at(-GRACE), supplier_status: 'WaitingForPayment', can_pay: false });
  assert.equal(paymentInFlight(q), false);
  assert.equal(canRenewQuote(q, NOW), true);
});

test('an unreadable record fails closed', () => {
  assert.equal(canRenewQuote({}, NOW), false);
  assert.equal(canRenewQuote(null, NOW), false);
  assert.equal(canRenewQuote(undefined, NOW), false);
  // No deadline anywhere: only a terminal state the shop itself set may renew.
  assert.equal(canRenewQuote({ status: 'awaiting_payment' }, NOW), false);
  assert.equal(canRenewQuote({ status: 'expired' }, NOW), true);
  assert.equal(canRenewQuote({ status: 'failed' }, NOW), true);
});

test('an order still being created, or under review, never renews', () => {
  assert.equal(canRenewQuote(quote({ status: 'order_creating', payment_expiry: at(-48 * 3600_000) }), NOW), false);
  assert.equal(canRenewQuote(quote({ status: 'manual_review', payment_expiry: at(-48 * 3600_000) }), NOW), false);
  assert.equal(canRenewQuote(quote({ status: 'refunded', payment_expiry: at(-48 * 3600_000) }), NOW), false);
  assert.equal(canRenewQuote(quote({ status: 'fulfilled', payment_expiry: at(-48 * 3600_000) }), NOW), false);
});

test('the grace boundary is inclusive and the shop field names all work', () => {
  assert.equal(canRenewQuote(quote({ payment_expiry: at(-GRACE), can_pay: false }), NOW), true);
  // payment_expires_at is the public API spelling; expires_at the local one.
  assert.equal(canRenewQuote({ status: 'awaiting_payment', can_pay: false, payment_expires_at: at(-GRACE) }, NOW), true);
  assert.equal(canRenewQuote({ status: 'awaiting_payment', can_pay: false, expires_at: at(-GRACE) }, NOW), true);
  assert.equal(canRenewQuote({ status: 'awaiting_payment', can_pay: false, expires_at: at(-GRACE + 1000) }, NOW), false);
});
