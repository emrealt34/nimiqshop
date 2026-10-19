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
export { canRenewQuote, paymentInFlight, paymentHandedOff, paymentWindowOver, paymentWindowVerifying, paymentWatchOver, paymentDeadline, PAYMENT_VERIFY_GRACE_MS, renewItemFromQuote, renewInfoFromQuote, canRebuildRequest } from ${JSON.stringify(path.join(root, 'src/lib/pay'))};
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
const { canRenewQuote, paymentInFlight, paymentHandedOff, paymentWindowOver, paymentWindowVerifying, paymentWatchOver, paymentDeadline, PAYMENT_VERIFY_GRACE_MS, renewItemFromQuote, renewInfoFromQuote, canRebuildRequest } = require(bundle);

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

/* ------------------------------------------------------------------ *
 * The buyer who already pressed pay (2026-10-19).
 *
 * A buyer paid and saw nothing change: the pay card kept showing the same QR
 * and the same countdown until, 7.5 minutes later, the checkout declared
 * "the supplier has not confirmed" — on an invoice that was still perfectly
 * payable. Two rules are pinned here and they pull in opposite directions:
 *
 *   1. a wallet that reported a SUBMITTED funding transaction is real
 *      evidence the buyer pressed pay, and the page must say so instead of
 *      looking frozen (paymentHandedOff);
 *   2. a submission is NOT a confirmation — it must never be allowed into
 *      `paymentInFlight`, or the hint (which never clears) would block a
 *      fresh invoice for the rest of the order's life and strand every
 *      buyer whose swap failed.
 * ------------------------------------------------------------------ */
test('a wallet that reported a submitted payment counts as handed off', () => {
  assert.equal(paymentHandedOff(quote()), false, 'a plain open invoice is not a handoff');
  assert.equal(paymentHandedOff(quote({ payment_handoff_at: at(-60_000) })), true);
  assert.equal(paymentHandedOff(quote({ lightning_funding_hint: '0xfeed' })), true);
  assert.equal(paymentHandedOff(quote({ lightning_swap_id: 'swap-1' })), true);
  assert.equal(paymentHandedOff(quote({ lightning_funding_hint: '   ', lightning_swap_id: '' })), false);
  assert.equal(paymentHandedOff(null), false);
  assert.equal(paymentHandedOff(undefined), false);
});

test('a handed-off payment is not yet money: it never blocks a fresh invoice', () => {
  const q = quote({ payment_expiry: at(-GRACE), can_pay: false, lightning_funding_hint: '0xfeed', lightning_swap_id: 'swap-1' });
  assert.equal(paymentHandedOff(q), true);
  assert.equal(paymentInFlight(q), false, 'a submission is not a confirmation');
  assert.equal(canRenewQuote(q, NOW), true, 'a buyer whose swap failed must still be able to pay');
});

test('the invoice deadline, not a client counter, decides when a payment can no longer land', () => {
  const q = quote({ payment_expiry: at(20 * 60_000) });
  assert.equal(paymentDeadline(q), NOW + 20 * 60_000);
  // 7.5 minutes of waiting (the old hard give-up) is nowhere near the end.
  assert.ok(paymentDeadline(q) + GRACE > NOW + 7.5 * 60_000, 'the window plus grace outlives any short counter');
  assert.equal(Number.isFinite(paymentDeadline({ status: 'awaiting_payment' })), false);
});

test('the payment watch outlives the window — it never ends on a client counter', () => {
  // 7.5 minutes of waiting: the OLD hard give-up, and the reason a buyer who
  // had just paid was told "the supplier has not confirmed".
  const q = quote({ payment_expiry: at(20 * 60_000) });
  assert.equal(paymentWatchOver(q, NOW + 7.5 * 60_000), false);
  assert.equal(paymentWatchOver(q, NOW + 24 * 60_000), false, 'still inside the verification buffer');
  // Inside the verification buffer the shop is still proving whether money
  // arrived, so the watch stays on.
  assert.equal(paymentWatchOver(quote({ payment_expiry: at(-60_000), can_pay: false }), NOW), false);
  // Only the deadline PLUS the grace ends it.
  assert.equal(paymentWatchOver(quote({ payment_expiry: at(-GRACE), can_pay: false }), NOW), true);
});

test('the payment watch never ends while money may exist', () => {
  const ancient = { payment_expiry: at(-48 * 3600_000), can_pay: false };
  for (const over of [
    { payment_observed: true },
    { payment_blocked: true },
    { status: 'payment_started', supplier_status: 'PaymentStarted' },
    { status: 'payment_received' },
    { lightning_funding_hint: '0xfeed', status: 'payment_started' },
  ]) {
    assert.equal(paymentWatchOver(quote({ ...ancient, ...over }), NOW), false, JSON.stringify(over));
  }
});

test('the payment watch keeps going when the record cannot be read', () => {
  assert.equal(paymentWatchOver({}, NOW), false);
  assert.equal(paymentWatchOver(null, NOW), false);
  assert.equal(paymentWatchOver({ status: 'awaiting_payment' }, NOW), false, 'an unreadable deadline is not an expiry');
});

/* ------------------------------------------------------------------ *
 * The renewal REQUEST shape.
 *
 * A live click on "Yeni fatura oluştur" answered 400 "product_id and country
 * are required", because the quote payload calls it `product_country` and the
 * cart item calls it `country`. These cases are written against the keys the
 * live API actually returns (captured 2026-10-04):
 *   id, product_id, product_country, denomination, product_value, quantity,
 *   customer_email, beneficiary_account, payment_method, cashback_destination
 * ------------------------------------------------------------------ */
const liveQuote = {
  id: '5fc9efdf-cc03-4906-95b7-07bf3c6df5a2',
  product_id: 'Amazon.com.tr', product_country: 'TR', denomination: 'range',
  product_value: 100, quantity: 1, customer_email: 'canli.test.nimshop@gmail.com',
  payment_method: 'nimiq_pay', cashback_destination: 'cashback', status: 'expired',
  payment_expiry: '2026-10-04T11:53:04Z', expires_at: '2026-10-04T11:53:04Z', coin: 'BTC',
};

test('the renewal request is built from the QUOTE field names, not the cart names', () => {
  const item = renewItemFromQuote(liveQuote);
  assert.equal(item.id, 'Amazon.com.tr');
  assert.equal(item.country, 'TR', 'product_country must map to country — the live 400 came from exactly this');
  assert.equal(item.denomination, 'range');
  assert.equal(item.value, 100);
  assert.equal(item.qty, 1);
  const info = renewInfoFromQuote(liveQuote);
  assert.equal(info.email, 'canli.test.nimshop@gmail.com');
  assert.equal(info.paymentMethod, 'nimiq_pay');
  assert.equal(info.cashbackDestination, 'cashback');
});

test('a phone-delivered quote keeps the number that lives in beneficiary_account', () => {
  const topup = { product_id: 'Vodafone TR', product_country: 'TR', denomination: 'range', product_value: 50,
                  quantity: 1, customer_email: '', beneficiary_account: '+905321112233', payment_method: 'nimiq_pay' };
  assert.equal(renewInfoFromQuote(topup).phone, '+905321112233');
  assert.equal(renewInfoFromQuote({ phone_number: '+905321112233' }).phone, '+905321112233');
});

test('renewal is refused when the shop does not know what to buy', () => {
  assert.equal(canRebuildRequest(liveQuote), true);
  assert.equal(canRebuildRequest({ product_id: 'Amazon.com.tr' }), false);
  assert.equal(canRebuildRequest({ product_country: 'TR' }), false);
  assert.equal(canRebuildRequest(null), false);
  assert.equal(renewItemFromQuote(null).country, undefined);
});
