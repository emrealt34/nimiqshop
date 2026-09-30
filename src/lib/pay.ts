/**
 * pay.ts — one place to read a BOLT11 invoice from any quote/order shape
 * and to launch it in Nimiq Pay / a Lightning handler.
 */
import { inNimiqPay } from './miniapp';

const BOLT = /^ln(?:bc|tb|bcrt)[a-z0-9]+$/i;

function stripLightningPrefix(s: string): string {
  return s.toLowerCase().startsWith('lightning:') ? s.slice('lightning:'.length) : s;
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
    if (BOLT.test(raw)) return raw;
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
  try {
    window.location.href = uri;
  } catch {
    /* ignore */
  }
  const settle = () => {
    document.removeEventListener('visibilitychange', mark);
    if (!left && !inNimiqPay()) onMiss();
  };
  window.setTimeout(settle, 1500);
}

/** Fail closed on unknown state, absent server authorization or elapsed timer. */
export function isQuotePayable(q: any, now = Date.now()): boolean {
  if (!q || q.can_pay !== true || !isPayableStatus(q.status) || q.payment_observed || q.payment_blocked) return false;
  const expiry = Date.parse(q.payment_expiry || q.payment_expires_at || q.expires_at || '');
  if (!Number.isFinite(expiry) || expiry <= now) return false;
  // Lightning/Nimiq Pay needs a BOLT11 invoice; USDT on Polygon needs a
  // wallet address + coin amount (no Lightning invoice is generated for the
  // stablecoin rail).
  const method = String(q.payment_method || '').toLowerCase();
  const coin = String(q.coin || '').toUpperCase();
  if (method === 'usdt_polygon' || coin === 'USDT') {
    return !!q.wallet_address && !!q.coin_amount;
  }
  return !!quoteBolt11(q);
}
