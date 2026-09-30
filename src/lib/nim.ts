/**
 * nim.ts — the ONE place that turns a quote/order into "≈ X NIM" (ported
 * from nim.js). Pure functions so they're safe to use in React render.
 */
import { fmtNIM } from './format';
import { getNimRate, cachedNimRate } from './api';
import { inNimiqPay } from './miniapp';
import { t as tr } from '../i18n';
import { asset } from './asset';

export function nimAmountFor(q: any, m: any): number {
  // Locked snapshot wins: an amount captured when the quote/order was
  // created must never be re-derived from today's market. The Go backend
  // stores estimated_nim/required_nim on quotes and nim_usd_rate on orders
  // precisely so a purchase stays at the price the buyer saw.
  const explicit = Number(q && (q.estimated_nim ?? q.required_nim ?? q.nim_amount)) || 0;
  if (explicit > 0) return explicit;
  const micros = Number(q && q.product_usd) || 0;
  const snapRate = Number(q && q.nim_usd_rate) || 0;
  if (micros > 0 && snapRate > 0) return (micros / 1e6) / snapRate;
  // No snapshot anywhere: only a live conversion can help, and only when we
  // actually have a market quote to convert with.
  if (!m || !(Number(m.usd_per_nim) > 0)) return 0;
  const usdPerBtc = Number(m.usd_per_btc) || 0;
  const btc = parseFloat(q && (q.coin_amount ?? q.lightning_amount_btc)) || 0;
  if (btc > 0 && usdPerBtc > 0) return (btc * usdPerBtc) / Number(m.usd_per_nim);
  const usd = micros / 1e6;
  if (usd > 0) return usd / Number(m.usd_per_nim);
  return 0;
}

/**
 * True when a purchase row has a NIM price we can state without touching the
 * live market — either an explicit locked amount or a locked purchase-time
 * rate. Live-rate conversions are reserved for rows that are still being
 * priced (fresh quotes); history must not drift with the market.
 */
export function hasLockedNim(q: any): boolean {
  if (!q) return false;
  if (Number(q.estimated_nim ?? q.required_nim ?? q.nim_amount) > 0) return true;
  return Number(q.product_usd) > 0 && Number(q.nim_usd_rate) > 0;
}

export function nimAmountText(q: any): string | null {
  const m = cachedNimRate();
  const n = nimAmountFor(q, m);
  if (n > 0) return `≈ ${fmtNIM(n, 0)} NIM`;
  return null;
}

export function nimFallbackText(): string {
  return inNimiqPay() ? tr('nim.inInvoice') : tr('nim.shownInPay');
}

export const NIM_LOGO = asset('/img/nimiq-hexagon.png?v=128');
export { getNimRate };
