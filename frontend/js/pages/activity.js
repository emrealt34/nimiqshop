/* pages/activity.js — PUBLIC ACTIVITY, reworked.
 *
 * A live, transparent feed of everything bought on nim.shop:
 *   • stat tiles (payments in feed, ≈ NIM volume, shoppers online, avg delivery)
 *   • community rating card with clickable distribution filter
 *   • feed cards with the product's REAL brand thumb, the buyer's local
 *     amount first (then USD, then NIM), wallet chip, stars and status
 *
 * Shape-robust: accepts the real backend's {items, summary} AND older/legacy
 * {activity:[…]} payloads; missing summaries are derived client-side.
 */
import { bootShell, navigate } from '../shell.js';
import { el, icon, $, replaceChildren, fmtUSD, fmtNIM, timeAgo, flag, countryName, copyText, pageInterval, shortAddr, fmtDuration } from '../util.js';
import { getActivity, cachedNimRate } from '../api.js';

import { toast, emptyState, errorState, skeletonLines, starsDisplay, statusBadge } from '../ui.js';
import { identiconImg } from '../identicon.js';

bootShell('activity');

const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on
// top of the first (the duplicate locked-card bug).
replaceChildren(main);

main.appendChild(el('div.container.activity-page', {},
  el('div.act-hero', {},
    el('div', {},
      el('h1.act-title', {}, icon('pulse', 26), ' ', 'Live activity'),
      el('div.xs.faint.mt-1', { text: 'Every payment is public — products, amounts and ratings. Nothing hidden, nothing fabricated.' }),
    ),
    el('div.activity-actions', {},
      el('button.btn.btn-ghost.btn-sm#refreshBtn', {}, icon('refresh', 16), el('span.btn-label', { text: 'Refresh' })),
    ),
  ),
  el('div#stats.act-stats.mt-2', {}, el('div.card', { style: { padding: '18px' } }, skeletonLines(1))),
  el('div#summary.mt-2', {}, el('div.card', {}, skeletonLines(3))),
  el('div#feed.mt-2'),
));

const statsEl = $('#stats');
const summaryEl = $('#summary');
const feedEl = $('#feed');
let timer = null;
let hbTimer = null;
let allItems = [];
let starFilter = 0; // 0 = all; 1..5 = only that rating
let lastSummary = {};
let knownIds = new Set(); // for the "new payment" flash

/* ---------------- presence ---------------- */
const presenceId = (() => { try { let id = localStorage.getItem('nimshop_pid'); if (!id) { id = 'p-' + Math.random().toString(36).slice(2) + Date.now().toString(36); localStorage.setItem('nimshop_pid', id); } return id; } catch { return ''; } })();
function heartbeat() {
  try { fetch((window.APP_CONFIG?.API_BASE || '/api') + '/presence', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: presenceId }), keepalive: true }); } catch { /* ignore */ }
}

/* ---------------- normalization ----------------
 * Accepts real-backend items AND legacy/mock rows (product/created_at/kind). */
function normItem(it) {
  return {
    ...it,
    type: it.type || it.kind || 'purchase',
    title: it.title || it.product || it.product_name || 'Purchase',
    time: it.time || it.created_at || it.updated_at || '',
    address: it.address || '',
    usd: Number(it.usd) > 0 ? Number(it.usd) : 0,
    nim: Number(it.nim) > 0 ? Number(it.nim) : (Number(it.nim_estimate) > 0 ? Number(it.nim_estimate) : 0),
    country: it.country || '',
    rating: Number(it.rating) || 0,
  };
}

/* ---------------- brand thumbs (real product photos) ---------------- */
function feedThumb(it) {
  const tile = el('div.feed-thumb', {}, it.country ? el('span.feed-flag', { text: flag(it.country), title: countryName(it.country) }) : icon('bag', 22));
  resolveMetaLogo(tile, it.title, it.country, { alt: it.title, match: 'prefix' });
  return tile;
}

/* ---------------- formatting ---------------- */
// fmtDuration lives in ../util.js (shared with orders/profile).
function compactNIM(n) {
  if (!(n > 0)) return '—';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e4) return Math.round(n / 1e3) + 'k';
  return fmtNIM(n, 0);
}
// shortAddr lives in ../util.js (shared).

/* ---------------- stat tiles ---------------- */
function renderStats(items, s) {
  const activeNow = Number(s.active_users) || 0;
  const avgDeliv = Number(s.avg_delivery_seconds) || 0;
  const volUSD = items.reduce((acc, it) => acc + (it.usd > 0 ? it.usd : 0), 0);
  const volNIM = items.reduce((acc, it) => acc + (it.nim > 0 ? it.nim : 0), 0)
    || (volUSD > 0 ? volUSD / (Number(cachedNimRate()?.usd_per_nim) || 0) : 0);

  const tile = (ico, label, value, sub, cls = '') => el('div.act-tile' + (cls ? '.' + cls : ''), {},
    el('div.act-tile-ico', {}, icon(ico, 18)),
    el('div.act-tile-body', {},
      el('div.act-tile-num', { text: value }),
      el('div.act-tile-label', { text: label }),
      sub ? el('div.act-tile-sub.xs.faint', { text: sub }) : null,
    ),
  );

  replaceChildren(statsEl, el('div.act-stats-grid.fade-in', {},
    tile('bag', 'payments in feed', String(items.length), 'most recent ' + items.length),
    tile('nimiq', '≈ NIM volume', compactNIM(volNIM), volUSD > 0 ? '≈ ' + fmtUSD(volUSD, { compact: true }) : ''),
    tile('user', 'shopping now', String(activeNow), activeNow === 1 ? 'live visitor' : 'live visitors', activeNow > 0 ? 'hot' : ''),
    tile('clock', 'avg delivery', avgDeliv > 0 ? fmtDuration(avgDeliv) : '—', 'code to wallet'),
  ));
}

/* ---------------- rating summary ---------------- */
function renderSummary(s, items) {
  // Derive the summary client-side when the payload has none (legacy mock).
  if (!s || (!s.count && !items.length)) {
    const rated = items.filter((it) => it.rating > 0);
    const dist = {};
    for (const it of rated) dist[String(it.rating)] = (Number(dist[String(it.rating)]) || 0) + 1;
    s = { count: rated.length, average: rated.length ? rated.reduce((a, b) => a + b.rating, 0) / rated.length : 0, dist, active_users: s?.active_users || 0 };
  }
  lastSummary = s;
  const count = s.count || 0;
  const avg = Number(s.average) || 0;
  const dist = s.dist || {};

  const rows = [];
  for (let star = 5; star >= 1; star--) {
    const c = Number(dist[String(star)] || 0);
    const pct = count ? Math.round((c / count) * 100) : 0;
    const active = starFilter === star;
    rows.push(el('button.dist-row' + (active ? '.active' : ''), {
      type: 'button',
      title: c ? `Show the ${c} buyer${c === 1 ? '' : 's'} who rated ${star} star${star === 1 ? '' : 's'}` : `No ${star}-star ratings yet`,
      on: { click: () => { starFilter = starFilter === star ? 0 : star; renderSummary(lastSummary, allItems); renderFeed(); } },
    },
      el('span.dist-star', {}, starsDisplay(star, { size: 13 }), el('span.xs.faint', { text: String(star) })),
      el('div.dist-track', {}, el('div.dist-fill', { style: { width: pct + '%' } })),
      el('span.dist-count.xs.mono.faint', { text: String(c) }),
    ));
  }

  replaceChildren(summaryEl, el('div.card.rating-summary.fade-in', {},
    el('div.rs-body', {},
      el('div.rs-left', {},
        el('div.rs-avg', {}, starsDisplay(avg, { size: 30 }), el('span.rs-num', { text: avg ? avg.toFixed(1) : '—' })),
        el('div.small.faint', { text: count ? `${count} buyer rating${count === 1 ? '' : 's'}` : 'No ratings yet' }),
      ),
      el('div.rs-dist', {}, rows),
    ),
  ));
}

/* ---------------- feed ---------------- */
function amountBlock(it) {
  const primary = (it.local_amount && it.local_currency) ? `${it.local_amount} ${it.local_currency}` : (it.usd > 0 ? fmtUSD(it.usd) : '');
  const chips = [];
  if (primary && it.usd > 0 && primary !== fmtUSD(it.usd)) chips.push(fmtUSD(it.usd));
  if (it.nim > 0) chips.push('≈ ' + compactNIM(it.nim) + ' NIM');
  return el('div.feed-amounts', {},
    primary ? el('span.feed-amt-main', { text: primary }) : el('span.feed-amt-main.muted', { text: 'amount in wallet' }),
    chips.length ? el('span.feed-amt-chips', {}, ...chips.map((c) => el('span.chip.xs', { text: c }))) : null,
  );
}

function ratingCell(it) {
  if (it.rating > 0) return el('div.cell-rate', {}, starsDisplay(it.rating, { size: 14 }));
  return null;
}

function feedItem(it, isNew) {
  const addr = it.address || 'unknown wallet';
  const sub = [
    it.quantity && it.quantity > 1 ? `×${it.quantity}` : null,
    it.country ? countryName(it.country) : null,
  ].filter(Boolean).join(' · ');

  return el('div.feed-item' + (isNew ? '.feed-new' : ''), { on: { click: () => { navigate('/track?order=' + encodeURIComponent(it.id)); } } },
    feedThumb(it),
    el('div.feed-main', {},
      el('div.feed-row-1', {},
        el('div.feed-title-wrap', {},
          el('span.feed-flag', { text: it.country ? flag(it.country) : '🌐', title: it.country ? countryName(it.country) : '' }),
          el('span.strong.truncate.feed-title', { text: it.title }),
        ),
        el('div.feed-side', {},
          it.status ? statusBadge(it.status) : null,
          el('span.xs.faint.feed-time', { text: timeAgo(it.time) }),
        ),
      ),
      el('div.feed-row-2', {},
        amountBlock(it),
        ratingCell(it),
      ),
      el('div.feed-row-3', {},
        el('button.feed-wallet.chip', {
          type: 'button',
          title: addr + ' — click to copy (public on-chain anyway)',
          on: { click: (e) => { e.stopPropagation(); copyText(addr); toast('Wallet address copied', 'success'); } },
        }, identiconImg(addr, 'feed-id'), el('span.mono', { text: shortAddr(addr) }), icon('copy', 12)),
        sub ? el('span.xs.faint', { text: sub }) : null,
        isNew ? el('span.chip.feed-new-chip', {}, icon('pulse', 11), 'new') : null,
      ),
    ),
  );
}

function renderFeed() {
  const items = starFilter > 0 ? allItems.filter((it) => it.rating === starFilter) : allItems;
  if (!items.length) {
    replaceChildren(feedEl, emptyState({
      iconName: starFilter ? 'star' : 'pulse',
      title: starFilter ? `No ${starFilter}-star ratings yet` : 'No payments yet',
      text: starFilter ? 'No buyer has left this rating so far. Click the same row again to clear the filter.'
        : 'Completed purchases appear here in real time — fully public, nothing hidden.',
    }));
    return;
  }
  replaceChildren(feedEl, el('div.feed-list.fade-in', {}, ...items.map((it) => feedItem(it, !knownIds.has(it.id)))));
  for (const it of items) knownIds.add(it.id);
  if (knownIds.size > 400) knownIds = new Set([...knownIds].slice(-200));
}

/* ---------------- load ---------------- */
async function load() {
  try {
    const res = await getActivity(50);
    const raw = res.items || res.activity || res || [];
    allItems = (Array.isArray(raw) ? raw : []).map(normItem).sort((a, b) => new Date(b.time) - new Date(a.time));
    const summary = res.summary || {};
    renderStats(allItems, summary);
    renderSummary(summary, allItems);
    renderFeed();
  } catch (err) {
    replaceChildren(feedEl, errorState(err.message, () => load()));
  }
}

$('#refreshBtn').addEventListener('click', () => { toast('Refreshing feed…', 'info', 1500); load(); });
load();
heartbeat();
timer = pageInterval(load, 15000);
hbTimer = pageInterval(heartbeat, 30000);
window.addEventListener('beforeunload', () => { clearInterval(timer); clearInterval(hbTimer); });
