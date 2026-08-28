/* pages/track.js — PUBLIC live order tracking. Anyone with an order id can see
 * WHERE a purchase is in its lifecycle (the anti-fraud transparency proof), but
 * the delivery itself (codes / PIN / claim link) is NEVER shown here — only the
 * owner sees that, via the authenticated order/quote pages.
 */
import { bootShell, openLogin } from '../shell.js';
import { el, icon, $, replaceChildren, fmtUSD, fmtNIM, fmtDate, queryParam, flag, countryName, shortAddr, pageInterval } from '../util.js';
import { trackOrder } from '../api.js';
import { isAuthed } from '../session.js';
import { toast, statusBadge, stageTimeline, kv, emptyState, errorState, skeletonLines, alertBox, copyButton , kvCard } from '../ui.js';

bootShell('');

const id = queryParam('order');
const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on
// top of the first (the duplicate locked-card bug).
replaceChildren(main);

main.appendChild(el('div.container', {},
  el('div.row.mb-2', { style: { gap: '10px' } },
    el('a.btn.btn-ghost.btn-sm', { href: '/activity' }, icon('back', 16), el('span.btn-label', { text: 'Activity' })),
  ),
  el('h1', { style: { display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' } }, icon('pulse', 24), 'Track order'),
  el('p.lede', { text: 'A public, real-time view of any order. Everyone can verify it is being fulfilled — delivery codes stay private to the owner.' }),
  el('div#detail.mt-2', {}, el('div.card', {}, skeletonLines(5))),
));

const detail = $('#detail');
let timer = null;

async function load() {
  if (!id) {
    replaceChildren(detail, emptyState({ iconName: 'search', title: 'No order id', text: 'Open an order from the Activity feed to track it publicly.' }));
    return;
  }
  try {
    const t = await trackOrder(id);
    render(t);
  } catch (err) {
    if (err.status === 404) {
      replaceChildren(detail, emptyState({ iconName: 'search', title: 'Order not found', text: 'This order id does not exist.' }));
    } else {
      replaceChildren(detail, errorState(err.message, () => load()));
    }
  }
}

function render(t) {
  const title = t.title || (t.type === 'direct_payment' ? 'Direct CryptoRefills Lightning purchase' : 'Purchase');
  const left = el('div.card', {},
    el('div.card-title', { text: 'Live tracking' }),
    stageTimeline(t.stages || []),
  );

  const rows = [
    ['Status', statusBadge(t.status)],
    ['Type', t.type === 'direct_payment' ? 'NIM → Lightning → CryptoRefills' : 'Purchase'],
    ['Amount', Number(t.usd) > 0 ? fmtUSD(t.usd) : '—'],
  ];
  if (t.nim) rows.push(['NIM estimate', '≈ ' + fmtNIM(t.nim) + ' NIM']);
  if (t.country) rows.push(['Country', `${flag(t.country)} ${countryName(t.country)}`]);
  if (t.quantity && t.quantity > 1) rows.push(['Quantity', '×' + t.quantity]);
  if (t.created_at) rows.push(['Created', fmtDate(t.created_at)]);
  if (t.updated_at) rows.push(['Updated', fmtDate(t.updated_at)]);

  const right = el('div.card', {}, el('div.card-title', { text: 'Summary' }), kv(rows));

  // Every on-chain transaction is public: the buyer's NIM payment, the USDC we
  // sent the supplier, and the supplier invoice — all verifiable by anyone.
  const txPairs = (t.transactions || []).map((tx) => [tx.label + ' · ' + tx.network,
    el('span.row', { style: { gap: '6px', justifyContent: 'flex-end' } },
      el('span.mono.small', { text: shortAddr(tx.hash, 8, 6), title: tx.network + ' · ' + tx.hash }),
      copyButton(tx.hash, ''))]);
  const txCard = txPairs.length ? kvCard('Transactions (public)', txPairs) : null;

  const privacy = alertBox('info',
    'This is a public view — anyone can see this order\'s stage, which is exactly how we prove orders are really fulfilled. The delivery (codes / PIN / claim link) is shown ONLY to the order\'s owner.');

  // Owner CTA: if the visitor owns it (authed) they can open the private view;
  // otherwise prompt sign-in.
  const ownerBar = isAuthed()
    ? el('a.btn.btn-gold.btn-block', { href: t.type === 'direct_payment' ? `/order?type=quote&id=${encodeURIComponent(t.id)}` : `/order?id=${encodeURIComponent(t.id)}` },
        icon('eye', 18), el('span.btn-label', { text: 'Open my order (view delivery)' }))
    : el('button.btn.btn-outline.btn-block', { on: { click: () => openLogin(() => load()) } },
        icon('wallet', 18), el('span.btn-label', { text: 'Is this yours? Connect wallet to see delivery' }));

  replaceChildren(detail, el('div.fade-in', {},
    el('div.row.between.mb-2', { style: { flexWrap: 'wrap', gap: '12px', alignItems: 'center' } },
      el('h2', { style: { margin: 0 }, text: title }),
      statusBadge(t.status),
    ),
    el('div.detail-grid.cols', {}, left, el('div.col', {}, right, txCard ? el('div.mt-2', {}, txCard) : null, el('div.mt-2', {}, ownerBar))),
    el('div.mt-2', {}, privacy),
  ));
}

load();
timer = pageInterval(() => { if (id) load(); }, 15000);
window.addEventListener('beforeunload', () => clearInterval(timer));
