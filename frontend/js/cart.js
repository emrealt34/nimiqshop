/* cart.js — fully LOCAL shopping cart (localStorage). Multi-product, adjustable quantity.
 * FIXED: No refund address, only email if not set. NIM logo vendored locally.
 */
import { el, icon, fmtMoney, fmtUSD, fmtNIM, clear, MAX_QTY } from './util.js';
import { toast, openSheet, closeSheet } from './ui.js';
import { createQuote, getFXRates, getNimRate, cachedNimRate, cachedFX } from './api.js'; /* TOTAL FIX: getNimRate was never imported — the cart total has been silently dying in its catch since day one */
import { isAuthed } from './session.js';
import { openLogin, navigate } from './shell.js';
import { nimIcon } from './nim.js';
import { askCartCheckout, buildOrderRequest } from './delivery.js';
import { waitForLightningPayment } from './quote-pay.js';

const KEY = 'nimshop_cart';

function read() { try { return JSON.parse(localStorage.getItem(KEY) || '[]'); } catch { return []; } }
function write(items) { localStorage.setItem(KEY, JSON.stringify(items)); notify(); }

const listeners = new Set();
function notify() { const n = cartCount(); listeners.forEach((fn) => { try { fn(n); } catch {} }); }
export function onCartChange(fn) { listeners.add(fn); }

export function cartCount() { return read().reduce((s, it) => s + (it.qty || 0), 0); }

function itemKey(it) { return it.id + '|' + (it.pkg ? 'pkg:' + it.pkg : 'val:' + it.value) + '|' + (it.country || ''); }

export function addToCart(p, { pkg, value, qty = 1, denomination = '' } = {}) {
  const items = read();
  const pkgData = pkg ? (p.packages || []).find((x) => x.package_id === pkg) || null : null;
  const entry = { id: p.id, type: p.type || '', name: p.name || p.id, image: p.logo_url || bestImage(p), country: p.country, currency: (pkgData && pkgData.currency) || p.currency, pkg: pkg || '', value: value || (pkgData ? pkgData.value || 0 : 0), denomination: denomination || (pkgData ? pkgData.denomination || '' : ''), coinAmount: pkgData && pkgData.coin_amount ? parseFloat(pkgData.coin_amount) || 0 : 0, unitUSD: unitUSD(p, { pkg, value }), qty }; /* CART-PRICE FIX: keep the package's own currency/value/BTC amount so the cart never shows a blind $0 — type is captured so the shared delivery step knows top-ups need a phone number */
  const k = itemKey(entry);
  const existing = items.find((x) => itemKey(x) === k);
  if (existing) {
    if (existing.qty + qty > MAX_QTY) {
      toast(`Max ${MAX_QTY} per product — you already have ${existing.qty}`, 'warn');
      if (existing.qty < MAX_QTY) existing.qty = MAX_QTY;
      write(items);
      return;
    }
    existing.qty = Math.min(MAX_QTY, existing.qty + qty);
  } else {
    if (qty > MAX_QTY) qty = MAX_QTY;
    items.push(entry);
  }
  write(items);
  toast(`Added ${entry.name} to cart`, 'success');
}

function bestImage(p) { const i = p.images || {}; return i.large || i.medium || i.small || (Object.keys(i).length ? Object.values(i)[0] : ''); }
function unitUSD(p, { pkg, value }) {
  if (pkg) { const k = (p.packages || []).find((x) => x.package_id === pkg); return k ? (k.value || 0) : 0; }
  return value || p.min_value || 0;
}

/* CART-PRICE FIX — synchronous reads of the session caches (same keys the
 * NIM/FX/product caches use, no network). Layers of truth for one row:
 *   1. parsed local face value (₺250 × qty)
 *   2. supplier BTC coin_amount × live usd_per_btc
 *   3. FX table: local value × usd_per_unit
 *   4. legacy unitUSD fallback
 * Never a blind "$0" again. */
// Sync rate reads come from api.js's shared cache helpers (cachedNimRate /
// cachedFX) — the sessionStorage keys live there, not duplicated here.
function sessionMarket() {
  const j = cachedNimRate();
  return j && Number(j.usd_per_btc) > 0 ? j : null;
}
function sessionFX() { return cachedFX(); }
function rowUSD(it) {
  const ccy = String(it.currency || 'USD').toUpperCase();
  const fx = sessionFX();
  if (it.value > 0 && fx && fx[ccy]) return it.value * it.qty * Number(fx[ccy]);
  const m = sessionMarket();
  if (it.coinAmount > 0 && m) return it.coinAmount * it.qty * Number(m.usd_per_btc);
  if (it.value > 0 && ccy === 'USD') return it.value * it.qty;
  return (it.unitUSD || 0) * it.qty; // legacy fallback (local-value heuristic)
}
function rowNIM(it) {
  const usd = rowUSD(it);
  const m = sessionMarket();
  if (usd > 0 && m && Number(m.usd_per_nim) > 0) return usd / Number(m.usd_per_nim);
  return 0;
}
function cartPriceLabel(it) {
  if (it.value > 0 && it.currency) return fmtMoney(it.value * it.qty, it.currency); // local currency first: ₺1.200
  // The buyer PAYS in NIM — the fallback price is a NIM estimate, never a
  // BTC/USD figure (BTC-derived "≈ $11" was confusing: you buy with NIM).
  const nim = rowNIM(it);
  if (nim > 0) {
    const dec = nim >= 1000 ? 0 : 2; // compact for thousands, exact cents-of-NIM below
    return '≈ ' + fmtNIM(nim, dec) + ' NIM';
  }
  const usd = rowUSD(it);
  return usd > 0 ? '≈ ' + fmtUSD(usd) : '—';
}
function cartSubtitle(it) {
  const denom = String(it.denomination || '').trim();
  const useful = denom && !/^package$/i.test(denom);
  const label = useful ? denom : (it.value > 0 ? fmtMoney(it.value, it.currency) : '');
  return [it.country || '', label || 'Package'].filter(Boolean).join(' ');
}

function setQty(k, qty) { const items = read(); const it = items.find((x) => itemKey(x) === k); if (it) it.qty = Math.max(0, Math.min(MAX_QTY, qty)); write(items.filter((x) => x.qty > 0)); }
export function removeItem(k) { write(read().filter((x) => itemKey(x) !== k)); }
function clearCart() { write([]); }

export function openCart() {
  const { body } = openSheet({ title: 'Your cart', wide: true });
  draw(body);
}


function draw(body) {
  // ALWAYS clear body first — her iki durumda da (dolu/boş) temizle
  clear(body);
  
  const items = read();
  if (!items.length) {
    body.appendChild(el('div.center', { style: { padding: '30px 10px' } },
      el('div', { style: { marginBottom: '12px', display: 'flex', justifyContent: 'center' } }, icon('bag', 34)),
      el('div.strong', { text: 'Your cart is empty' }),
      el('div.small.muted.mt-1', { text: 'Add products from the shop — your cart is saved on this device only.' }),
      el('a.btn.btn-gold.mt-2', { href: '/' }, icon('bag', 18), el('span', { text: 'Browse the shop' })),
    ));
    return;
  }
  const totalUSD = items.reduce((s, it) => s + it.unitUSD * it.qty, 0); // kept for legacy math only
  // Mixed-currency totals in "$" were nonsense (a 250 TRY item showed
  // $250). The total is now the honest USD-equivalent + NIM estimate,
  // computed with the backend's FX table.
  const totalEl = el('div.strong', { text: 'Total —' });
  (async () => {
    // TOTAL FIX: one endpoint failing (e.g. /market/fx 404) must NEVER freeze
    // the total at "Total —". allSettled + per-item layered fallbacks
    // (FX rate → BTC coin_amount × usd_per_btc → legacy) — the NIM estimate
    // is the headline number, exactly like the real payment page.
    try {
      const [fxR, nimR] = await Promise.allSettled([getFXRates(), getNimRate()]);
      const rates = fxR.status === 'fulfilled' && fxR.value ? fxR.value.usd_per_unit : null;
      const market = nimR.status === 'fulfilled' ? nimR.value : sessionMarket();
      let usd = 0;
      let known = false;
      for (const it of items) {
        const rate = rates && it.currency ? rates[String(it.currency).toUpperCase()] : null;
        if (it.value > 0 && rate) { usd += it.value * it.qty * Number(rate); known = true; }
        else { const u = rowUSD(it); if (u > 0) { usd += u; known = true; } }
      }
      const nimUsd = Number(market && market.usd_per_nim);
      let txt = 'Total —';
      if (nimUsd > 0 && known) {
        const nim = usd / nimUsd;
        txt = 'Total ≈ ' + fmtNIM(nim, nim >= 1000 ? 0 : 2) + ' NIM';
        if (usd > 0) txt += ' (≈ $' + usd.toLocaleString('en-US', { maximumFractionDigits: 2 }) + ')';
      }
      totalEl.textContent = txt;
    } catch { totalEl.textContent = 'Total —'; }
  })();
  const list = el('div.cart-list');
  for (const it of items) {
    const k = itemKey(it);
    list.appendChild(el('div.cart-row', {},
      el('div.cart-thumb', {}, it.image ? el('img', { src: it.image, alt: it.name, style: { width: '100%', height: '100%', objectFit: 'contain' } }) : el('span', { text: '🛒', style: { fontSize: '42px' } })),
      el('div.cart-main', {},
        el('div.strong', { text: it.name }),
        el('div.xs.faint', { text: cartSubtitle(it) }), /* CART-PRICE FIX: real denomination, not a blind "TR Package" */
        el('div.row.mt-1', { style: { gap: '8px', alignItems: 'center' } },
          el('button.cart-q', { type: 'button', 'aria-label': 'Decrease quantity', on: { click: () => { setQty(k, it.qty - 1); draw(body); } } }, '−'),
          el('span.cart-qty', { text: String(it.qty) }),
          el('button.cart-q', { type: 'button', disabled: it.qty >= MAX_QTY, style: it.qty >= MAX_QTY ? { opacity: 0.4, cursor: 'not-allowed' } : {}, 'aria-label': 'Increase quantity', on: { click: () => { if (it.qty < MAX_QTY) { setQty(k, it.qty + 1); draw(body); } } } }, '+'),
          el('button.cart-q.cart-x', { type: 'button', 'aria-label': `Remove ${it.name} from cart`, on: { click: () => { removeItem(k); draw(body); } } }, icon('x', 15)),
        ),
      ),
      el('div.cart-price.strong', { text: cartPriceLabel(it) }),
    ));
  }
  body.append(
    list,
    el('div.cart-total.row.between.mt-2', { style: { alignItems: 'baseline' } },
      el('div.small.faint', { text: `${items.length} item${items.length === 1 ? '' : 's'} · saved on this device` }),
      totalEl,
    ),
    el('button.btn.btn-gold.btn-block.btn-lg.mt-2', { on: { click: async () => {
      // Shared delivery steps: one email for card/eSIM items, a phone
      // number per top-up item — same flow as the product page.
      const info = await askCartCheckout(body, items);
      if (!info) return;
      checkout(body, items, info);
    } } },
      nimIcon(20), el('span.btn-label', { text: 'Checkout with NIM — Pay with Nimiq Pay' })),
    el('div.row.between.mt-1', {},
      el('button.btn.btn-ghost.btn-sm', { on: { click: () => { clearCart(); draw(body); } } }, 'Empty cart'),
    ),
  );
}

async function checkout(body, items, info) {
  if (!isAuthed()) { openLogin(() => checkout(body, read(), info)); return; }
  const done = [];
  // Shared payload builder (delivery.js) — gift extras come from the same
  // localStorage keys the delivery steps write. Gift extras / emails only
  // reach a top-up when it is THE single top-up gift purchase (its own
  // gift step wrote them); a plain top-up in a mixed cart carries nothing
  // but its phone number — never the card item's email or gift note.
  const singleTopUpCart = items.length === 1 && items[0].type === 'phone_refill';
  const buildReq = (it) => buildOrderRequest(it, {
    email: info.email,
    phone: info.phones.get(it) || '',
    gift: it.type === 'phone_refill' ? singleTopUpCart : true,
  });
  // PREPARE ALL QUOTES IN PARALLEL: the old code created each quote
  // sequentially right before its payment, so a 2-item cart waited twice.
  // The supplier queue still paces everything safely.
  let quotes = [];
  if (items.length > 1) {
    body.textContent = '';
    body.append(el('div.center', { style: { padding: '30px 10px' } },
      el('div.spinner', { style: { width: '28px', height: '28px', margin: '0 auto 12px' } }),
      el('div.strong', { text: `Preparing ${items.length} orders…` }),
      el('div.small.muted.mt-1', { text: 'Locking live prices for every item at once.' }),
    ));
    let finished = 0;
    try {
      quotes = await Promise.all(items.map((it) => createQuote(buildReq(it)).then((q) => { finished++; drawProgress(body, finished - 1, items.length, it.name, 'prepare'); return q; })));
    } catch (e) { toast(`Quote failed: ${e.message}`, 'error'); return; }
    try {
      const prev = parseInt(sessionStorage.getItem('nimshop_awaiting') || '0', 10) || 0;
      sessionStorage.setItem('nimshop_awaiting', String(prev + items.length));
      window.dispatchEvent(new Event('nimshop:awaiting'));
    } catch {}
  }
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    if (items.length === 1) {
      drawProgress(body, i, items.length, it.name, 'quote');
      try { quotes[i] = await createQuote(buildReq(it)); }
      catch (e) { toast(`Quote failed for ${it.name}: ${e.message}`, 'error'); return; }
    }
    const quote = quotes[i];
    if (!(await waitForLightningPayment(body, quote, it.name))) return;
    done.push(quote.quote_id);
  }
  clearCart();
  closeSheet();
  toast(`Paid ${done.length} order${done.length === 1 ? '' : 's'} — confirming…`, 'success');
  setTimeout(() => { navigate('/orders'); }, 900);
}

/* waitForLightningPayment lives in ../quote-pay.js (shared with product/home payment sheets). */

function drawProgress(body, i, total, name, phase) {
  clear(body);
  const label = phase === 'prepare' ? `Prices locked ✓` : phase === 'quote' ? `Pricing ${name}…` : `Approve ${name} in Nimiq Pay…`;
  body.append(el('div.center', { style: { padding: '30px 10px' } },
    el('div.spinner', { style: { width: '28px', height: '28px', margin: '0 auto 12px' } }),
    el('div.strong', { text: `Checkout ${i + 1} of ${total}` }),
    el('div.small.muted.mt-1', { text: label }),
  ));
}
