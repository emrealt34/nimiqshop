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
 * Grace period after the invoice's payment window, mirrored EXACTLY by the
 * backend (`db.PaymentGrace`). It is the time the supplier gets to notice a
 * payment that landed right at the edge of the window.
 *
 * Until it has passed, "the timer ran out" is NOT proof that nothing was
 * charged — so no buyer is told "ödeme alınmadı" and no fresh invoice is
 * offered to sit next to a possibly-paid one. Both sides read the same field
 * (`payment_expiry`) and the same constant, so the button never appears
 * before the backend is willing to honour it.
 */
export const PAYMENT_VERIFY_GRACE_MS = 5 * 60 * 1000;

/** The quote's payment deadline in ms, or NaN when the shop cannot know it. */
export function paymentDeadline(q: any): number {
  return Date.parse(q?.payment_expiry || q?.payment_expires_at || q?.expires_at || '');
}

/** The supplier's single-use invoice can no longer be paid. */
export function paymentWindowOver(q: any, now = Date.now()): boolean {
  const expiry = paymentDeadline(q);
  return Number.isFinite(expiry) && expiry <= now;
}

/**
 * The window is over but the shop is still proving whether money arrived
 * (inside the grace buffer, or the deadline is unknown). The truthful state
 * here is "checking, you will be able to get a fresh invoice in a moment",
 * never "it failed" and never a second payment.
 */
export function paymentWindowVerifying(q: any, now = Date.now()): boolean {
  if (paymentInFlight(q) || q?.payment_observed || q?.payment_blocked) return false;
  const deadline = paymentDeadline(q);
  if (!Number.isFinite(deadline)) return false;
  return deadline <= now && now < deadline + PAYMENT_VERIFY_GRACE_MS;
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
