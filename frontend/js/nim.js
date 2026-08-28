/* nim.js — the ONE place that turns a quote/order into "≈ X NIM".
 *
 * The supplier prices invoices in BTC, but the buyer only ever sees NIM.
 * An estimate must therefore resolve from WHICHEVER source exists:
 *   1. explicit estimate fields (estimated_nim / required_nim)
 *   2. invoice amount (coin_amount / lightning_amount_btc) × usd_per_btc ÷ usd_per_nim
 *   3. product_usd (micro-USD) ÷ usd_per_nim
 * Rates: session cache first (instant, no flash), then the live oracle with
 * retries — the oracle can take seconds when cold. The textual fallback
 * should be nearly unreachable: it needs a quote with NO amount fields at
 * all AND four failed rate fetches.
 */
import { el, fmtNIM } from './util.js';
import { getNimRate, cachedNimRate } from './api.js';
import { inNimiqPay } from './miniapp.js';

function nimAmountFor(q, m) {
  if (!m || !(Number(m.usd_per_nim) > 0)) return 0;
  const explicit = Number((q && (q.estimated_nim ?? q.required_nim)) ?? 0) || 0;
  if (explicit > 0) return explicit;
  const usdPerBtc = Number(m.usd_per_btc) || 0;
  const btc = parseFloat((q && (q.coin_amount ?? q.lightning_amount_btc)) ?? '') || 0;
  if (btc > 0 && usdPerBtc > 0) return (btc * usdPerBtc) / Number(m.usd_per_nim);
  const usd = (Number(q && q.product_usd) || 0) / 1e6;
  if (usd > 0) return usd / Number(m.usd_per_nim);
  return 0;
}

/* Returns a <span> that fills itself: cache-sync where possible, else "…"
 * while fetching, retried, and only then the (rare) fallback text. */
export function nimAmountNode(q, { fallback = null, retries = 4 } = {}) {
  if (!fallback) fallback = inNimiqPay() ? 'in the invoice below' : 'shown in Nimiq Pay'; // inside the app the old text pointed at itself
  const span = document.createElement('span');
  const render = (m) => {
    const n = nimAmountFor(q, m);
    if (n > 0) { span.textContent = `≈ ${fmtNIM(n, 0)} NIM`; return true; }
    return false;
  };
  if (render(cachedNimRate())) return span;
  span.textContent = '…';
  const attempt = (i) => {
    getNimRate()
      .then((m) => { if (!render(m) && i < retries) setTimeout(() => attempt(i + 1), 2000); })
      .catch(() => { if (i < retries) setTimeout(() => attempt(i + 1), 2000); else span.textContent = fallback; });
  };
  attempt(0);
  return span;
}

// One NIM logo + icon helper for every page (product, cart, admin used to
// each define their own copy with slightly different border radii).
const NIM_LOGO = '/img/nimiq-hexagon.png';
export function nimIcon(s = 18, { radius = '3px' } = {}) {
  return el('img', { src: NIM_LOGO, alt: 'NIM', width: s, height: s, style: { verticalAlign: 'middle', borderRadius: radius, display: 'inline-block' } });
}
