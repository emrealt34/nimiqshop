/* pages/orders.js — purchase history: legacy order rows and direct
 * CryptoRefills-Lightning quotes, with local-status refresh.
 */
import { bootShell, openLogin } from '../shell.js';
import { el, icon, $, replaceChildren, fmtUSD, fmtNum, fmtDate, flag, pageInterval, cleanFamilyName, quoteFaceValue, fmtDuration } from '../util.js';
import { listOrders, listQuotes, rateOrder, rateQuote } from '../api.js';
import { brandMetaFor } from '../catalog-meta.js';
import { isAuthed } from '../session.js';
import { statusBadge, kindMeta, miniProgress, emptyState, errorState, skeletonCards, starsDisplay, starPicker, toast, lockedSignInCard } from '../ui.js';
import { quoteStages, isTerminalStatus, isDeliveredStatus, isIssueStatus } from '../order-track.js';

bootShell('orders');
// Client-side navigation mounts this module more than once; always restore the
// initial Orders view instead of carrying a previous filter/render state over.
let filter = 'all';

const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on top
// of the first (the duplicate locked-card bug).
replaceChildren(main);
main.appendChild(el('div.container.orders-page', {},
  el('div.section-head', {},
    el('h2', {}, icon('receipt', 26), ' ', 'Orders', el('span#awaitPill')),
    el('div.row', { style: { gap: '8px' } },
      el('span.xs.faint#lastRefresh'),
      el('button.btn.btn-ghost.btn-sm#refreshBtn', {}, icon('refresh', 16), el('span.btn-label', { text: 'Refresh' })),
    ),
  ),
  el('div.seg.mb-2#filters'),
  el('div#list'),
));

const list = $('#list');
list.classList.add('orders-surface');
const awaitPill = $('#awaitPill');
if (awaitPill) { awaitPill.className = 'await-pill'; awaitPill.style.display = 'none'; }

function publishAwaiting(count) {
  try { sessionStorage.setItem('nimshop_awaiting', String(count)); } catch {}
  window.dispatchEvent(new Event('nimshop:awaiting'));
  if (awaitPill) {
    if (count > 0) { awaitPill.textContent = `🔴 ${count} awaiting payment`; awaitPill.style.display = ''; }
    else awaitPill.style.display = 'none';
  }
}
function countAwaiting(rows) {
  return (rows || []).filter((r) => String(r.status).toLowerCase() === 'awaiting_payment').length;
}
const FILTERS = [
  { key: 'all', label: 'All' },
  { key: 'active', label: 'Active' },
  { key: 'delivered', label: 'Delivered' },
  { key: 'issues', label: 'Issues' },
  { key: 'support', label: 'With support' },
];
let rows = [];
let refreshTimer = null;

const filtersBox = $('#filters');
for (const f of FILTERS) {
  filtersBox.appendChild(el('button' + (f.key === 'all' ? '.active' : ''), {
    text: f.label,
    on: { click: (e) => { filter = f.key; [...filtersBox.children].forEach((b) => b.classList.toggle('active', b === e.currentTarget)); render(); } },
  }));
}

$('#refreshBtn').addEventListener('click', () => load(true));


async function load(manual = false) {
  if (!isAuthed()) {
    replaceChildren(list, lockedSignInCard({
      title: 'Track everything you buy',
      text: 'Connect your wallet to see live order tracking, delivery codes and your full purchase history.',
      onConnect: () => openLogin(() => load()),
      lg: true, iconSize: 20,
    }));
    return;
  }

  if (!rows.length) replaceChildren(list, el('div.grid.products', {}, skeletonCards(6)));

  try {
    const [orders, quotes] = await Promise.all([listOrders(), listQuotes().catch(() => [])]);
    rows = [...(await normalizeOrders(orders)), ...(await normalizeQuotes(quotes))];
    publishAwaiting(countAwaiting(rows));
    rows.sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    $('#lastRefresh').textContent = 'Updated ' + new Date().toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    render();
    scheduleAutoRefresh();
  } catch (err) {
    replaceChildren(list, errorState(err.message, () => load()));
  }
}

async function normalizeOrders(orders) {
  const out = [];
  for (const o of (orders || [])) {
    // Real product photo: payload image first, else the brand's own logo
    // resolved from the catalog; bgColor is the brand's ORIGINAL background.
    const family = (o.payload && (o.payload.product_name || o.product_id)) || o.product_id;
    const meta = await brandMetaFor(family, o.payload && o.payload.country);
    out.push({
      rowKind: 'order',
      id: o.id,
      kind: o.kind,
      name: (o.payload && o.payload.product_name) || o.product_id,
      country: o.payload && o.payload.country,
      qty: o.quantity,
      priceLabel: Number(o.price_usd) > 0 ? fmtUSD(o.price_usd) : '—',
      status: o.status,
      created_at: o.created_at,
      updated_at: o.updated_at,
      stages: o.stages,
      current_stage: o.current_stage,
      rating: o.rating || 0,
      rated_at: o.rated_at,
      hasTicket: !!o.has_ticket,
      ticketStatus: o.ticket_status,
      image: (o.payload && (o.payload.product_image || o.payload.logo_url || o.payload.image)) || meta.logo,
      bgColor: (o.payload && (o.payload.product_bg || o.payload.bg_color)) || meta.bg,
    });
  }
  return out;
}

/* ---------------- quote rows: real product identity ---------------- */

// cleanFamilyName + quoteFaceValue live in ../util.js (shared canonical
// copies used by orders, order detail, product and home).

function quotePrice(q) {
  // product_usd is micro-USD (the USD-equivalent of the cart); the local
  // face value is the shared quoteFaceValue (parsed denomination label or
  // product_value) + the currency.
  const micros = Number(q.product_usd);
  const usd = isFinite(micros) && micros > 0 ? micros / 1e6 : 0;
  const { value: localVal, currency: ccy, label } = quoteFaceValue(q);
  const local = (localVal > 0 && ccy)
    ? `${fmtNum(localVal)} ${ccy}`
    : (label ? label : '');
  if (local && ccy !== 'USD' && usd > 0) return { priceLabel: local, usdLabel: fmtUSD(usd) };
  if (local && ccy === 'USD' && usd > 0) return { priceLabel: fmtUSD(usd), usdLabel: '' };
  if (local) return { priceLabel: local, usdLabel: '' };
  if (usd > 0) return { priceLabel: fmtUSD(usd), usdLabel: '' };
  return { priceLabel: 'NIM · Lightning', usdLabel: '' }; // rail only (label-only quotes)
}

async function normalizeQuotes(quotes) {
  const out = [];
  for (const q of (quotes || [])) {
    const stages = quoteStages(q);
    const family = cleanFamilyName(q.product_id) || 'item';
    const { priceLabel, usdLabel } = quotePrice(q);
    const meta = await brandMetaFor(family, q.product_country || q.country);
    out.push({
      rowKind: 'quote',
      id: q.id || q.quote_id || q.ID,
      kind: 'quote',
      name: 'Lightning purchase · ' + family,
      country: q.product_country || q.country,
      qty: q.quantity,
      priceLabel,
      usdLabel,
      status: q.status,
      created_at: q.created_at,
      updated_at: q.updated_at || q.created_at,
      stages,
      current_stage: stages.findIndex((s) => s.status === 'in_progress'),
      rating: q.rating || 0,
      rated_at: q.rated_at,
      hasTicket: false,
      image: meta.logo,
      bgColor: meta.bg,
    });
  }
  return out;
}

/* quoteStages + status classifiers live in ../order-track.js (shared with order detail). */

function render() {
  const filtered = rows.filter((r) => {
    if (filter === 'active') return !isTerminalStatus(r.status);
    if (filter === 'delivered') return isDeliveredStatus(r.status);
    if (filter === 'issues') return isIssueStatus(r.status);
    if (filter === 'support') return r.hasTicket;
    return true;
  });

  if (!rows.length) {
    replaceChildren(list, emptyState({
      iconName: 'bag',
      title: 'No orders yet',
      text: 'Everything you buy appears here with live tracking — from payment to delivery.',
      action: el('a.btn.btn-gold', { href: '/' }, icon('bag', 18), el('span', { text: 'Browse the shop' })),
    }));
    return;
  }
  if (!filtered.length) {
    replaceChildren(list, emptyState({ iconName: 'search', title: 'Nothing in this view', text: 'Try another filter.' }));
    return;
  }

  const box = el('div.order-list.fade-in');
  for (const r of filtered) box.appendChild(orderRow(r));
  replaceChildren(list, box);
}

function durationNode(r) {
  if (!isTerminalStatus(r.status) || !r.updated_at || !r.created_at) return null;
  const secs = Math.round((new Date(r.updated_at) - new Date(r.created_at)) / 1000);
  if (!isFinite(secs) || secs < 0) return null;
  // fmtDuration (util.js) is the shared seconds→label formatter.
  return el('span.xs.faint', {}, icon('clock', 12), ' Total ' + fmtDuration(secs));
}

// Inline rating: delivered items can be rated right from the list (now or later).
function ratingLine(r) {
  if (!isDeliveredStatus(r.status)) return null;
  if (r.rating > 0) {
    return el('span.row', { style: { gap: '6px', alignItems: 'center' } },
      starsDisplay(r.rating, { size: 14 }), el('span.xs.faint', { text: 'Rated' }));
  }
  const picker = starPicker({
    size: 16,
    onSelect: async (val) => {
      picker._disable();
      try {
        await (r.rowKind === 'quote' ? rateQuote(r.id, val) : rateOrder(r.id, val));
        toast('Thanks for your rating!', 'success');
        load();
      } catch (e) {
        toast(e.message || 'Could not save rating', 'error');
      }
    },
  });
  return el('span.row', { style: { gap: '8px', alignItems: 'center' } }, el('span.xs.faint', { text: 'Rate:' }), picker);
}

function orderRow(r) {
  const meta = kindMeta(r.kind);
  const href = r.rowKind === 'quote'
    ? `/order?type=quote&id=${encodeURIComponent(r.id)}`
    : `/order?id=${encodeURIComponent(r.id)}`;

  const sub = el('div.o-sub', {},
    el('span', { text: fmtDate(r.created_at) }),
    r.qty > 1 ? el('span', { text: `× ${r.qty}` }) : null,
    r.country ? el('span', { text: `${flag(r.country)} ${r.country}` }) : null,
    r.hasTicket ? el('span', { style: { color: 'var(--orange)' }, text: '• support ticket' }) : null,
  );

  // This foot row captures clicks so interacting with the star picker never
  // navigates away (the card is otherwise a link to the order detail).
  const foot = el('div.row.mt-1', { style: { gap: '14px', alignItems: 'center', flexWrap: 'wrap' },
    on: { click: (e) => { e.stopPropagation(); e.preventDefault(); } } },
    durationNode(r),
    ratingLine(r),
  );

  // Real product photo on its ORIGINAL brand background (bg_color from the
  // supplier catalog); no photo → category icon on the kraft tile.
  // thumb-bp thumbs render at double size — scale the fallback icon too.
  const thumb = el('div.thumb.' + meta.thumb, {},
    r.image
      ? el('img.product-img', { src: r.image, alt: r.name || r.id, loading: 'lazy', decoding: 'async', style: r.bgColor ? { background: r.bgColor } : {} })
      : icon(meta.icon, meta.thumb === 'thumb-bp' ? 44 : 24),
  );

  const card = el('a.order-card', { href },
    thumb,
    el('div.o-main', {},
      el('div.o-name', { text: r.name || r.id }),
      sub,
      el('div.mt-1', {}, miniProgress(r)),
      foot,
    ),
    el('div.o-side', {},
      statusBadge(r.status),
      el('div.o-price', {}, r.priceLabel, r.usdLabel ? el('span.xs.faint', { text: ' · ' + r.usdLabel }) : null),
    ),
  );
  return card;
}

function scheduleAutoRefresh() {
  clearInterval(refreshTimer);
  if (rows.some((r) => !isTerminalStatus(r.status))) {
    refreshTimer = pageInterval(() => load(false), 20000);
  }
}

window.addEventListener('nimshop:session', () => load());
load();
