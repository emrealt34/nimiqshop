/**
 * pay.ts — one place to read a BOLT11 invoice from any quote/order shape
 * and to launch it in Nimiq Pay / a Lightning handler.
 */
import { inNimiqPay } from './miniapp';

const BOLT = /^ln(?:bc|tb|bcrt)[a-z0-9]+$/i;

function stripLightningPrefix(s: string): string {
  return s.toLowerCase().startsWith('lightning:') ? s.slice('lightning:'.length) : s;
}

/* ---- BOLT11 integrity: one gate, no silent garbage -----------------------
   A bolt11 string IS a bech32 string, so a truncated or corrupted invoice
   fails its checksum; and the human-readable part carries the amount
   (lnbc<digits>[munp]) — an amount-less invoice must never reach a buyer's
   clipboard or wallet. Both checks live here so EVERY consumer (the copy
   button, the pay hand-off, the lightning: URI, the order page) shares one
   definition of "this invoice is sane". */
const BECH32_CHARSET = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l';

function bech32Polymod(vals: number[]): number {
  const GEN = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];
  let chk = 1;
  for (const v of vals) {
    const b = chk >> 25;
    chk = ((chk & 0x1ffffff) << 5) ^ v;
    for (let i = 0; i < 5; i += 1) if ((b >> i) & 1) chk ^= GEN[i];
  }
  return chk;
}

/** True when the string's bech32 checksum verifies (BOLT11 uses constant 1). */
export function bolt11ChecksumOk(inv: string): boolean {
  const s = String(inv || '').toLowerCase();
  const sep = s.lastIndexOf('1');
  if (sep < 1 || s.length - sep - 1 < 6) return false;
  const hrp = s.slice(0, sep);
  const data = s.slice(sep + 1);
  const vals: number[] = [];
  for (const c of hrp) {
    const o = c.charCodeAt(0);
    if (o < 33 || o > 126) return false;
    vals.push(o >> 5);
  }
  vals.push(0);
  for (const c of hrp) vals.push(c.charCodeAt(0) & 31);
  for (const c of data) {
    const i = BECH32_CHARSET.indexOf(c);
    if (i < 0) return false;
    vals.push(i);
  }
  return bech32Polymod(vals) === 1;
}

/** True when the hrp carries an amount: lnbc<digits>[munp] (2500u, 25m, …). */
export function bolt11HasAmount(inv: string): boolean {
  const s = String(inv || '').toLowerCase();
  const hrp = s.slice(0, s.lastIndexOf('1'));
  return /^ln(?:bc|tb|bcrt)[0-9]+[munp]?$/.test(hrp);
}

/** The one sanity gate: shape + amount + checksum. */
export function saneBolt11(inv: string): boolean {
  const s = String(inv || '').trim();
  return BOLT.test(s) && bolt11HasAmount(s) && bolt11ChecksumOk(s);
}

/** First valid BOLT11 on a quote/order, regardless of which field the API used. */
export function quoteBolt11(q: unknown): string {
  if (!q || typeof q !== 'object') return '';
  const rec = q as Record<string, unknown>;
  const cands = [
    rec.lightning_invoice,
    rec.cryptorefills_payment_request,
    rec.wallet_address,
    rec.payment_request,
    rec.invoice,
  ];
  for (const c of cands) {
    const raw = stripLightningPrefix(String(c || '').trim());
    if (saneBolt11(raw)) return raw;
  }
  return '';
}

export function quoteIdOf(q: unknown): string {
  if (!q || typeof q !== 'object') return '';
  const rec = q as Record<string, unknown>;
  return String(rec.quote_id || rec.id || '');
}

export function isPayableStatus(st: unknown): boolean {
  return String(st || '') === 'awaiting_payment';
}

/** Supplier/backend statuses that mean the NIM payment already landed. */
export const PAID_STATUSES = new Set([
  'payment_received',
  'fulfilled',
  'delivering',
  'nim_confirmed',
  'supplier_invoice_created',
  'supplier_processing',
]);

/**
 * A checkout POST that answers with an order whose payment ALREADY landed did
 * not create anything: the shop handed back an earlier order of the same cart
 * that is still being delivered (its money-safety rule allows one unresolved
 * checkout per buyer). Returns that status, or '' for a genuinely new /
 * payable / unresolved-unpaid order.
 *
 * The pay screen must not open for such a handback: its poll would see the
 * paid status at once and play the success beat for a purchase that never
 * happened, which is the same "went straight to completed" surprise as the
 * 2026-10-18 rebuy bug, one step earlier. The caller shows the honest
 * "already paid, being prepared" screen instead.
 */
export function paidOrderHandedBack(created: unknown): string {
  if (!created || typeof created !== 'object') return '';
  const rec = created as Record<string, unknown>;
  const inner = rec.quote && typeof rec.quote === 'object' ? (rec.quote as Record<string, unknown>) : null;
  const st = String(rec.status || (inner && inner.status) || '').toLowerCase();
  return PAID_STATUSES.has(st) ? st : '';
}

/** What the shop handed back instead of creating: a LIVE, unpaid invoice. */
export type LiveOrderHandback = { quoteId: string; expiresAt: number; minutesLeft: number };

/**
 * A checkout POST that answers `reused: true` with an order that is still
 * AWAITING PAYMENT did not create anything either: the buyer already has an
 * open, unexpired, unpaid invoice for exactly this cart (they abandoned the
 * pay screen, or opened it on another device) and the shop handed it back.
 *
 * Owner (2026-10-18): that must not silently reopen the old pay screen. The
 * shop asks — "you already have an open order for this (expires in N min):
 * continue with it, or open a new one?" — and the caller renders that
 * prompt. Returns the handed-back order's id and how long it stays payable,
 * or null for a genuinely new order, a handback that is not payable (the
 * already-paid screen and the renew path own those), or an unknown deadline.
 */
export function liveOrderHandedBack(created: unknown, now = Date.now()): LiveOrderHandback | null {
  if (!created || typeof created !== 'object') return null;
  const rec = created as Record<string, unknown>;
  if (rec.reused !== true) return null;
  if (!isPayableStatus(rec.status) || rec.can_pay !== true) return null;
  if (rec.payment_observed || rec.payment_blocked) return null;
  const expiresAt = paymentDeadline(rec);
  if (!Number.isFinite(expiresAt) || expiresAt <= now) return null;
  const quoteId = quoteIdOf(rec);
  if (!quoteId) return null;
  return { quoteId, expiresAt, minutesLeft: Math.max(1, Math.ceil((expiresAt - now) / 60000)) };
}

export const FAIL_STATUSES = new Set([
  'failed',
  'manual_review',
  'expired',
  'refunded',
  'denied',
  'blocked',
]);

/* ------------------- payment window & safe renewal ----------------------- */

/**
 * Is the payment window over? Read from the quote ITSELF, never from a local
 * countdown: a page opened after the deadline (the common returning-buyer
 * case) never ran the timer, and treating that as "still open" is exactly how
 * the shop used to strand a buyer on "payment controls paused" with no way to
 * pay and no way to start a fresh invoice.
 */
/**
 * There is no grace buffer any more — on either side. This constant is 0 and
 * it mirrors the backend exactly (which no longer defines one at all); the
 * two must not drift.
 *
 * The shop used to hold a lapsed invoice against the buyer for five more
 * minutes in case a payment submitted at the very edge of the window had not
 * reached the supplier yet: a timer standing in for knowledge nobody had. On
 * the v6 PARTNER API that guess is unnecessary. The moment the window closes
 * the backend ASKS whether anything was paid and, when nothing was, cancels
 * the order — so the invoice can never be paid late. The release is a fact
 * from the supplier, not a wait, and the buyer is not parked in front of a
 * screen that says "we are checking" when nobody is.
 *
 * What still blocks is EVIDENCE, never a clock: money observed, a payment
 * settling, or an operator hold.
 */
export const PAYMENT_VERIFY_GRACE_MS = 0;

/** The quote's payment deadline in ms, or NaN when the shop cannot know it. */
export function paymentDeadline(q: any): number {
  return Date.parse(q?.payment_expiry || q?.payment_expires_at || q?.expires_at || '');
}

/** The supplier's single-use invoice can no longer be paid. */
export function paymentWindowOver(q: any, now = Date.now()): boolean {
  const expiry = paymentDeadline(q);
  return Number.isFinite(expiry) && expiry <= now;
}

/** Supplier states that mean money was seen or is being verified. */
const PAYMENT_IN_FLIGHT = new Set([
  'payment_started',
  'payment_received',
  'delivering',
  'fulfilled',
]);

/** True when the order itself says a payment landed and only settlement is left. */
export function paymentInFlight(q: any): boolean {
  const status = String(q?.status || '');
  if (q?.payment_observed) return true;
  if (PAYMENT_IN_FLIGHT.has(status)) return true;
  return /^(payments?started|partialpaymentstarted|paymentreceived|waitingfordelivery|done)$/i
    .test(String(q?.supplier_status || ''));
}

/**
 * True when the buyer's wallet has already been handed this invoice.
 *
 * Two independent stamps, both recorded by the shop itself:
 *   - `payment_handoff_at` — a pay button opened the invoice in a wallet;
 *   - `lightning_funding_hint` / `lightning_swap_id` — the wallet reported a
 *     SUBMITTED funding transaction for this very invoice (these fields are
 *     only ever written by that report, so their presence is real evidence
 *     the buyer pressed pay).
 *
 * This is deliberately NOT part of `paymentInFlight` and must never be: the
 * hint persists forever, so folding it in would make `canRenewQuote` refuse a
 * replacement invoice for the rest of that order's life and strand a buyer
 * whose swap failed. It means "something is moving", not "money landed" —
 * which is exactly what a waiting buyer has to be told instead of being left
 * in front of a QR code that looks frozen.
 */
export function paymentHandedOff(q: any): boolean {
  if (!q) return false;
  if (q.payment_handoff_at) return true;
  return Boolean(String(q.lightning_funding_hint || '').trim() || String(q.lightning_swap_id || '').trim());
}

/**
 * Is the checkout's payment watch OVER — should the client stop asking?
 *
 * This is the rule the pay poll uses to decide when to give up, and it is
 * deliberately about the INVOICE, never about a counter in the browser. A
 * fixed "N seconds and we call it a failure" clock used to fire 7.5 minutes
 * into a ~25-minute payment window and put "the supplier has not confirmed"
 * in front of a buyer who had paid minutes earlier and was still settling.
 *
 * Answering false is always the safe side: while money may exist (observed,
 * in flight, or on an operator hold) the watch NEVER ends, however old the
 * deadline is, because an order that is still settling is not a failure.
 */
export function paymentWatchOver(q: any, now = Date.now()): boolean {
  if (paymentInFlight(q) || q?.payment_observed || q?.payment_blocked) return false;
  const deadline = paymentDeadline(q);
  if (!Number.isFinite(deadline)) return false; // unreadable record: keep watching
  return now >= deadline + PAYMENT_VERIFY_GRACE_MS;
}

/**
 * May the buyer safely get a NEW invoice for the same order?
 *
 * Only when the old one can no longer be paid AND nothing was charged:
 * the window is over and neither the shop nor the supplier has seen money.
 * Anything else (observed payment, supplier hold, in-flight settlement) must
 * stay exactly where it is — a fresh invoice next to a possibly-paid one is
 * how a buyer pays twice.
 */
export function canRenewQuote(q: any, now = Date.now()): boolean {
  if (!q) return false;
  const status = String(q.status || '');
  if (status === 'order_creating') return false;
  if (q.payment_observed || q.payment_blocked) return false;
  if (paymentInFlight(q)) return false;
  if (['refunded', 'fulfilled', 'manual_review'].includes(status)) return false;
  // A settled local state (expired/failed) is the backend agreeing that this
  // quote is done, but the timer alone never proves it: the grace buffer has
  // to have elapsed on the real deadline before a fresh invoice is offered.
  const deadline = paymentDeadline(q);
  if (Number.isFinite(deadline)) {
    if (now < deadline + PAYMENT_VERIFY_GRACE_MS) return false;
    return true;
  }
  // No deadline on the record: only a terminal state that the shop itself set
  // can authorise a renewal (and never while money may exist — handled above).
  return status === 'expired' || status === 'failed';
}

/**
 * Rebuild "the same item" for a fresh invoice.
 *
 * The quote payload and the cart item do NOT share field names —
 * `product_country` vs `country`, `customer_email` vs `email`, and a
 * phone-delivered order keeps its number in `beneficiary_account`. Reading the
 * wrong one produced a live 400 ("product_id and country are required") on the
 * very button meant to unblock a stuck buyer, so the mapping lives here, in
 * one place, with tests against the real payload keys.
 */
export function renewItemFromQuote(q: any) {
  return {
    id: q?.product_id,
    qty: q?.quantity || 1,
    country: q?.product_country,
    denomination: q?.denomination || '',
    value: q?.product_value ?? 0,
    brand: q?.brand || '',
    brand_id: q?.brand_id || '',
    category: q?.category || '',
  };
}

export function renewInfoFromQuote(q: any) {
  return {
    email: q?.customer_email || q?.email || '',
    phone: q?.phone_number || q?.beneficiary_account || '',
    paymentMethod: q?.payment_method || 'nimiq_pay',
    cashbackDestination: q?.cashback_destination || 'cashback',
    anonymous: !!q?.anonymous,
  };
}

/**
 * A quote can only be re-quoted when the shop knows WHAT to buy and WHERE it
 * ships. Without both, the button must not be offered at all: a click that can
 * only fail with a raw backend sentence is worse than no button.
 */
export function canRebuildRequest(q: any): boolean {
  return Boolean(q?.product_id) && Boolean(q?.product_country || q?.country);
}

/** Fresh-invoice reasons that never prove the payment failed — for copy. */
export function renewalReason(q: any, localExpired = false, now = Date.now()): 'window' | 'expired' | 'none' {
  if (!canRenewQuote(q, now)) return 'none';
  if (localExpired && paymentWindowOver(q, now)) return 'window';
  return paymentWindowOver(q, now) ? 'window' : 'expired';
}


export function launchLightningUri(uri: string, onMiss: () => void): void {
  if (typeof window === 'undefined') return;
  let left = false;
  const mark = () => {
    if (typeof document !== 'undefined' && (document.hidden || document.visibilityState === 'hidden')) {
      left = true;
    }
  };
  document.addEventListener('visibilitychange', mark);
  window.addEventListener('pagehide', mark, { once: true });
  // Owner (2026-10-05): the attempt goes through a HIDDEN IFRAME, never
  // window.location.href — a phone with no wallet app used to navigate the
  // tab into a protocol error and kill this page (and the toast) with it.
  // The iframe hands the URI to the OS the same way; when nothing handles
  // it, the page lives and the miss callback always fires.
  let frame: HTMLIFrameElement | null = null;
  try {
    frame = document.createElement('iframe');
    frame.style.display = 'none';
    frame.setAttribute('aria-hidden', 'true');
    frame.setAttribute('tabindex', '-1');
    document.body.appendChild(frame);
    frame.src = uri;
  } catch {
    /* ignore */
  }
  const settle = () => {
    document.removeEventListener('visibilitychange', mark);
    try {
      if (frame) frame.remove();
    } catch {
      /* ignore */
    }
    if (!left && !inNimiqPay()) onMiss();
  };
  window.setTimeout(settle, 1500);
}

/** Fail closed on unknown state, absent server authorization or elapsed timer. */
export function isQuotePayable(q: any, now = Date.now()): boolean {
  if (!q || q.can_pay !== true || !isPayableStatus(q.status) || q.payment_observed || q.payment_blocked) return false;
  const expiry = Date.parse(q.payment_expiry || q.payment_expires_at || q.expires_at || '');
  if (!Number.isFinite(expiry) || expiry <= now) return false;
  // Legacy USDT-on-Polygon quotes (created before that rail was removed on
  // 2026-10-10) have no pay screen any more: they are never payable here.
  const method = String(q.payment_method || '').toLowerCase();
  const coin = String(q.coin || '').toUpperCase();
  if (method === 'usdt_polygon' || coin === 'USDT') return false;
  return !!quoteBolt11(q);
}
