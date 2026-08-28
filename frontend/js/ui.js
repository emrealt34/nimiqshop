/* ui.js — shared UI components: toasts, sheets/modals, badges, skeletons,
 * empty states, copy buttons, countdowns, order timelines (English copy).
 */
import { el, icon, copyText, fmtDate } from './util.js';
import { identiconImg } from './identicon.js';
import { inNimiqPay, NIMIQ_PAY_IOS_URL, NIMIQ_PAY_ANDROID_URL, detectMobilePlatform } from './miniapp.js';
import { ensureLib } from './vendor-load.js';
import { nimAmountNode, nimIcon } from './nim.js';

/* ---------------- Toasts ---------------- */

let toastRoot = null;
function toasts() {
  if (!toastRoot) {
    toastRoot = el('div.toasts', { role: 'status', 'aria-live': 'polite' });
    document.body.appendChild(toastRoot);
  }
  return toastRoot;
}

export function toast(message, type = 'info', ms = 3800) {
  const ico = type === 'success' ? 'check' : type === 'error' ? 'alert' : 'info';
  const t = el('div.toast.' + type, {}, icon(ico, 18), el('span', { text: message }));
  toasts().appendChild(t);
  while (toasts().children.length > 4) toasts().firstChild.remove();
  setTimeout(() => {
    t.classList.add('out');
    setTimeout(() => t.remove(), 260);
  }, ms);
  return t;
}

/* ---------------- Sheet / modal ---------------- */

let overlay = null;

export function openSheet({ title, wide = false, onClose, locked = false } = {}) {
  closeSheet();
  overlay = el('div.overlay', { role: 'dialog', 'aria-modal': 'true' });
  const sheet = el('div.sheet' + (wide ? '.wide' : ''));
  sheet.appendChild(el('div.sheet-handle'));
  const head = el('div.sheet-head', {},
    el('h3', { text: title || '' }),
    el('button.sheet-close', { 'aria-label': 'Close', on: { click: () => closeSheet() } }, icon('x', 18)),
  );
  const body = el('div.sheet-body');
  sheet.append(head, body);
  overlay.appendChild(sheet);

  overlay.addEventListener('mousedown', (e) => { if (e.target === overlay && !locked) closeSheet(); });
  const onKey = (e) => { if (e.key === 'Escape' && !locked) closeSheet(); };
  document.addEventListener('keydown', onKey);
  overlay._onKey = onKey;
  overlay._onClose = onClose;

  document.body.appendChild(overlay);
  requestAnimationFrame(() => overlay.classList.add('open'));
  document.body.style.overflow = 'hidden';
  return { body, sheet, close: closeSheet };
}

export function closeSheet() {
  if (!overlay) return;
  document.removeEventListener('keydown', overlay._onKey);
  const cb = overlay._onClose;
  overlay.remove();
  overlay = null;
  document.body.style.overflow = '';
  if (typeof cb === 'function') { try { cb(); } catch { /* ignore */ } }
}

/* ---------------- Buttons in loading state ---------------- */

/* ---------------- Status → English badge ---------------- */

const STATUS_MAP = {
  // orders
  pending: ['Pending', 'gold', true],
  created: ['Created', 'blue', true],
  payment_detected: ['Payment detected', 'blue', true],
  processing: ['Processing', 'orange', true],
  delivered: ['Delivered', 'teal'],
  complete: ['Delivered', 'teal'],
  fulfilled: ['Delivered', 'teal'],
  failed: ['Failed', 'red'],
  refunded: ['Refunded', 'purple'],
  blocked: ['Blocked', 'red'],
  denied: ['Denied', 'red'],
  payment_error: ['Payment error', 'red'],
  expired: ['Expired', 'gray'],
  // quotes
  quoted: ['Awaiting payment', 'gold', true],
  invoice_creating: ['Preparing Lightning invoice', 'blue', true],
  lightning_invoice_created: ['Awaiting Lightning payment', 'gold', true],
  order_creating: ['Preparing order', 'blue', true],
  awaiting_payment: ['Awaiting payment', 'gold', true],
  payment_started: ['Payment detected', 'blue', true],
  payment_received: ['Payment confirmed', 'teal', true],
  delivering: ['Delivering', 'orange', true],
  nim_payment_submitted: ['Payment submitted', 'blue', true],
  nim_confirmed: ['NIM confirmed', 'teal'],
  supplier_invoice_created: ['Settling', 'orange', true],
  polygon_tx_submitted: ['Settling', 'orange', true],
  polygon_confirmed: ['Settling', 'orange', true],
  failed_supplier: ['Supplier failed', 'red', true],
  refunding: ['Refund in progress', 'purple', true],
  manual_review: ['Manual review', 'orange'],
  // tickets
  open: ['Open', 'gold', true],
  waiting_user: ['Waiting for you', 'orange', true],
  waiting_admin: ['Support replying', 'blue', true],
  resolved: ['Resolved', 'teal'],
  closed: ['Closed', 'gray'],
};

export function statusBadge(status) {
  const s = String(status || '').toLowerCase();
  const [label, color, pulsing] = STATUS_MAP[s] || [status || 'Unknown', 'gray', false];
  return el('span.badge.' + color + (pulsing ? '.pulse' : ''), { text: label });
}

const KIND_META = {
  gift_card: { icon: 'gift', thumb: 'thumb-gc', label: 'Gift card' },
  topup: { icon: 'phone', thumb: 'thumb-tu', label: 'Top-up' },
  esim: { icon: 'globe', thumb: 'thumb-es', label: 'eSIM' },
  quote: { icon: 'bolt', thumb: 'thumb-bp', label: 'Direct payment' },
  bill_payment: { icon: 'card', thumb: 'thumb-bp', label: 'Bill payment' },
};

export function kindMeta(kind) {
  return KIND_META[kind] || { icon: 'bag', thumb: 'thumb-gc', label: kind || 'Item' };
}

/* ---------------- Order stages timeline (English copy) ---------------- */
/* The backend emits localized stage text; this client intentionally renders
 * its own English copy keyed by stage id so the UI language is deterministic. */

/* STAGE_COPY — the ONE English text source for every stage id. Bare backend
 * stages (id + status + timestamp only, no text) and client-built stages
 * (order-track.js) both resolve their copy here; order-track's dynamic
 * descriptions are deliberately not duplicated into the timeline. */
const STAGE_COPY = {
  order_placed: {
    title: 'Order placed',
    desc: 'Your order was received. Nothing is charged yet — you pay the supplier’s invoice directly from your own wallet.',
    failedDesc: 'Your order was received. Nothing is charged yet — you pay the supplier’s invoice directly from your own wallet.',
  },
  payment_settled: {
    title: 'Lightning payment (NIM)',
    desc: 'Nimiq Pay converts your NIM and pays the CryptoRefills invoice directly.',
  },
  supplier_processing: {
    title: 'Supplier processing',
    desc: 'The supplier network is producing and activating your item.',
    failedDesc: 'The supplier could not complete this transaction.',
  },
  delivery_complete: {
    title: 'Delivery complete',
    desc: 'Your code, PIN and redemption instructions are ready below.',
    failedTitle: 'Refunded',
    failedDesc: 'The supplier refunded the full amount back to your own wallet.',
  },
};

export function stageTimeline(stages) {
  const wrap = el('div.timeline');
  const arr = Array.isArray(stages) ? stages : [];
  if (!arr.length) {
    return el('div.empty.small', {}, 'No tracking information yet.');
  }
  for (const st of arr) {
    const copy = STAGE_COPY[st.id] || { title: st.id, desc: '' };
    const status = st.status || 'pending';
    const failed = status === 'failed';
    const title = failed && copy.failedTitle ? copy.failedTitle : copy.title;
    const desc = failed && copy.failedDesc ? copy.failedDesc : copy.desc;

    const dotIcon = status === 'completed' ? 'check' : failed ? 'x' : 'clock';
    const item = el('div.tl-item.' + status, {},
      el('div.tl-dot', {}, icon(dotIcon, 14)),
      el('div.tl-title', { text: title }),
      desc ? el('div.tl-desc', { text: desc }) : null,
      st.timestamp ? el('div.tl-time', { text: fmtDate(st.timestamp) }) : null,
    );
    wrap.appendChild(item);
  }
  return wrap;
}

export function miniProgress(order) {
  const bar = el('div.mini-progress');
  const cur = Number(order.current_stage ?? 1);
  const bad = ['failed', 'refunded'].includes(String(order.status).toLowerCase());
  const stages = Array.isArray(order.stages) ? order.stages : new Array(4);
  for (let i = 0; i < Math.max(stages.length, 4); i++) {
    let cls = '';
    const st = stages[i];
    if (st) {
      if (st.status === 'completed') cls = 'done';
      else if (st.status === 'in_progress') cls = bad ? 'fail' : 'working';
      else if (st.status === 'failed') cls = 'fail';
    } else if (i < cur) cls = 'done';
    bar.appendChild(el('i' + (cls ? '.' + cls : '')));
  }
  return bar;
}

/* ---------------- Copy button ---------------- */

export function copyButton(getText, label = 'Copy') {
  const btn = el('button.copy-btn', { type: 'button' }, icon('copy', 14), el('span', { text: label }));
  btn.addEventListener('click', async () => {
    const text = typeof getText === 'function' ? getText() : getText;
    const ok = await copyText(String(text ?? ''));
    toast(ok ? 'Copied to clipboard' : 'Could not copy — please copy manually', ok ? 'success' : 'error');
    if (ok) {
      const span = btn.querySelector('span');
      span.textContent = 'Copied';
      setTimeout(() => { span.textContent = label; }, 1400);
    }
  });
  return btn;
}

/* ---------------- Skeletons & empty states ---------------- */

export function skeletonCards(n = 8, cls = 'skel.card') {
  const out = [];
  for (let i = 0; i < n; i++) out.push(el('div.' + cls));
  return out;
}

export function skeletonLines(n = 3) {
  const out = [];
  for (let i = 0; i < n; i++) out.push(el('div.skel.line', { style: { width: (90 - i * 18) + '%' } }));
  return out;
}

export function emptyState({ iconName = 'bag', title, text, action }) {
  return el('div.empty.fade-in', {},
    el('div.empty-ico', {}, icon(iconName, 32)),
    el('h3', { text: title }),
    text ? el('p', { text: text }) : null,
    action ? el('div', {}, action) : null,
  );
}

export function errorState(message, retryFn) {
  const box = el('div.empty.fade-in', {},
    el('div.empty-ico', { style: { background: 'var(--red-soft)', borderColor: 'rgba(240,97,97,.3)', color: 'var(--red)' } }, icon('alert', 30)),
    el('h3', { text: 'Something went wrong' }),
    el('p', { text: message || 'Please try again.' }),
  );
  if (retryFn) {
    box.appendChild(el('button.btn.btn-ghost', { on: { click: retryFn } }, icon('refresh', 18), 'Try again'));
  }
  return box;
}

/* ---------------- QR (vendored qrcode-generator) ---------------- */

function qrSvgNode(text, modules = 0) {
  const lib = window.qrcode;
  if (!lib) return el('div.small.muted', { text: 'QR unavailable' });
  const qr = lib(0, 'M');
  qr.addData(String(text));
  qr.make();
  const count = qr.getModuleCount();
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  const size = modules || 29;
  svg.setAttribute('viewBox', `0 0 ${count} ${count}`);
  svg.setAttribute('width', size * 6);
  svg.setAttribute('height', size * 6);
  svg.setAttribute('shape-rendering', 'crispEdges');
  const bg = document.createElementNS(svgNS, 'rect');
  bg.setAttribute('width', count); bg.setAttribute('height', count); bg.setAttribute('fill', '#ffffff');
  svg.appendChild(bg);
  let d = '';
  for (let r = 0; r < count; r++) {
    for (let c = 0; c < count; c++) {
      if (qr.isDark(r, c)) d += `M${c} ${r}h1v1h-1z`;
    }
  }
  const path = document.createElementNS(svgNS, 'path');
  path.setAttribute('d', d || 'M0 0');
  path.setAttribute('fill', '#042133');
  svg.appendChild(path);
  return svg;
}

/* ---------------- Misc ---------------- */

/* Sign-in gate card — orders/order/profile used to each build the same
 * "Connect wallet" locked card with slightly different wording/button size. */
export function lockedSignInCard({ title, text, onConnect, lg = false, iconSize = 19 }) {
  return el('div.card.locked.fade-in', {},
    el('div.lock-ico', {}, icon('lock', 34)),
    el('h2', { text: title }),
    el('p', { text: text }),
    el('button.btn.btn-gold' + (lg ? '.btn-lg' : ''), { on: { click: onConnect } }, icon('nimiq', iconSize), el('span.btn-label', { text: 'Connect wallet' })),
  );
}

/* Card wrapper around the key/value list (orders detail + track page both
 * render the public transactions card with this exact shape). */
export function kvCard(title, rows) {
  return el('div.card', {}, el('div.card-title', { text: title }), kv(rows));
}

export function kv(pairs) {
  const box = el('dl.kv');
  for (const [k, v] of pairs) {
    box.appendChild(el('div', {}, el('dt', { text: k }), el('dd', {}, v)));
  }
  return box;
}

export function alertBox(type, text) {
  const ico = type === 'success' ? 'check' : type === 'error' ? 'alert' : type === 'warn' ? 'alert' : 'info';
  return el('div.alert.' + (type === 'warn' ? 'warn' : type), {}, icon(ico, 19), el('div', { text }));
}

/* ---------------- Star ratings ---------------- */

// Genuine Lucide "star" path (lucide-static v1.34.0, ISC) — no hand-drawn icons.
const STAR_D = 'M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z';
let starGradSeq = 0;

// starSVG renders one star filled to `ratio` (0..1) using a horizontal gradient
// so fractional ratings (4.2) show a genuinely partial fill, not a rounded one.
function starSVG(ratio, size = 16) {
	const r = Math.max(0, Math.min(1, ratio));
	const ns = 'http://www.w3.org/2000/svg';
	const svg = document.createElementNS(ns, 'svg');
	svg.setAttribute('viewBox', '0 0 24 24');
	svg.setAttribute('width', size);
	svg.setAttribute('height', size);
	svg.setAttribute('aria-hidden', 'true');
	const id = 'sg' + (starGradSeq++);
	const defs = document.createElementNS(ns, 'defs');
	const grad = document.createElementNS(ns, 'linearGradient');
	grad.setAttribute('id', id);
	grad.setAttribute('x1', '0'); grad.setAttribute('y1', '0');
	grad.setAttribute('x2', '1'); grad.setAttribute('y2', '0');
	const s1 = document.createElementNS(ns, 'stop');
	s1.setAttribute('offset', (r * 100) + '%'); s1.setAttribute('stop-color', '#F7C948');
	const s2 = document.createElementNS(ns, 'stop');
	s2.setAttribute('offset', (r * 100) + '%'); s2.setAttribute('stop-color', 'rgba(226,166,43,0.18)');
	grad.appendChild(s1); grad.appendChild(s2); defs.appendChild(grad); svg.appendChild(defs);
	const p = document.createElementNS(ns, 'path');
	p.setAttribute('d', STAR_D);
	p.setAttribute('fill', 'url(#' + id + ')');
	p.setAttribute('stroke', '#E2A62B');
	p.setAttribute('stroke-width', '1.3');
	p.setAttribute('stroke-linejoin', 'round');
	svg.appendChild(p);
	return svg;
}

// Read-only row of 5 stars with genuine partial fill. Pass a fractional rating
// (e.g. 4.2) and an optional numeric `label` to show alongside it.
export function starsDisplay(rating, { size = 16, label = '' } = {}) {
	const v = Math.max(0, Math.min(5, Number(rating) || 0));
	const row = el('span.stars', { role: 'img', 'aria-label': v.toFixed(1) + ' out of 5 stars' });
	for (let i = 1; i <= 5; i++) row.appendChild(starSVG(v - (i - 1), size));
	if (label !== '') row.appendChild(el('span.stars-label', { text: label }));
	return row;
}

// Interactive 1-5 star picker. onSelect(rating) fires on click. The returned
// element exposes ._setValue(n) to lock in a chosen value after submit.
export function starPicker({ onSelect, size = 34 } = {}) {
	let value = 0;
	const buttons = [];
	const row = el('div.stars.picker');
	const paint = (n) => buttons.forEach((bb, idx) => { bb.replaceChild(starSVG(Math.max(0, Math.min(1, n - idx)), size), bb.firstChild); });
	for (let i = 1; i <= 5; i++) {
		const b = el('button.star-btn', { type: 'button', 'aria-label': `${i} star${i > 1 ? 's' : ''}` }, starSVG(0, size));
		b.addEventListener('mouseenter', () => paint(i));
		b.addEventListener('mouseleave', () => paint(value));
		b.addEventListener('click', () => { value = i; paint(i); if (onSelect) onSelect(i); });
		buttons.push(b);
		row.appendChild(b);
	}
	row._setValue = (n) => { value = n; paint(n); };
	row._disable = () => buttons.forEach((bb) => { bb.disabled = true; });
	return row;
}

/* ---------------- Nimiq Pay Lightning block ----------------
 * The official Nimiq Pay integration, matched to the device:
 *   1. TAP the gold link -> `lightning:` URI. On phones Nimiq Pay
 *      registers this scheme and opens directly with the invoice
 *      (standard BOLT11 behavior — documented at nimiq.dev).
 *   2. SHOW QR -> the same invoice as a QR code: on a desktop, scan it
 *      with the Nimiq Pay app (or any Lightning wallet).
 *   3. COPY the invoice -> paste into any wallet.
 * Desktop browsers have no `lightning:` handler — that is expected;
 * the QR and copy paths carry the payment instead.
 */


/* BOLT11 embeds its BTC amount in the human-readable part —
 * "lnbc15690n…" = 15690 nano-BTC. Decoding it lets us show the PRICE next
 * to the invoice everywhere, with zero extra API calls. */
function bolt11AmountBtc(invoice) {
  const m = String(invoice || '').match(/^ln(?:bc|tb|bcrt)(\d+)([munp]?)1/i);
  if (!m) return 0;
  const n = parseInt(m[1], 10);
  if (!isFinite(n)) return 0;
  const mult = { m: 1e-3, u: 1e-6, n: 1e-9, p: 1e-12 }[m[2].toLowerCase()] || 1;
  return n * mult; // BTC
}

/* "Seems like you don't have Nimiq Pay" — desktop dialog. Own overlay node:
 * openSheet() would REPLACE an already-open payment sheet, so this builds
 * its own lightweight overlay on top instead. */
function nimiqPayMissingDialog(invoice) {
  const ov = el('div.overlay.open', { role: 'alertdialog', 'aria-modal': 'true', 'aria-label': 'Nimiq Pay not detected' });
  const close = () => { ov.classList.remove('open'); setTimeout(() => ov.remove(), 200); };
  // NIM price decoded straight from the BOLT11 string, when present.
  const btcAmt = bolt11AmountBtc(invoice);
  const priceLine = (btcAmt > 0)
    ? el('div.row.center', { style: { gap: '8px', alignItems: 'baseline', justifyContent: 'center', flexWrap: 'wrap', margin: '10px 0 0' } },
        el('span.xs.faint', { text: 'Invoice amount' }),
        nimAmountNode({ coin_amount: btcAmt }),
      )
    : null;
  const sheet = el('div.sheet', { style: { maxWidth: '460px' } },
    el('div.sheet-handle'),
    el('div.sheet-head', {},
      el('h3', { text: 'Nimiq Pay not detected' }),
      el('button.sheet-close', { 'aria-label': 'Close', on: { click: close } }, icon('x', 18)),
    ),
    el('div.sheet-body', {},
      el('div.locked', { style: { display: 'flex', flexDirection: 'column', alignItems: 'center', paddingTop: '6px' } },
        el('div.lock-ico', {}, icon('nimiq', 34)),
      ),
      priceLine,
      el('p.small.muted.center', { style: { margin: '12px 0 14px' }, text: 'Seems like you don\u2019t have Nimiq Pay installed. Install it on your phone and scan the QR code on this page with the app — or copy the Lightning invoice into any wallet.' }),
      el('div.row', { style: { gap: '8px', flexWrap: 'wrap' } },
        el('a.btn.btn-outline', { style: { flex: '1' }, href: NIMIQ_PAY_IOS_URL, target: '_blank', rel: 'noopener noreferrer' }, icon('external', 15), el('span.btn-label', { text: 'App Store' })),
        el('a.btn.btn-outline', { style: { flex: '1' }, href: NIMIQ_PAY_ANDROID_URL, target: '_blank', rel: 'noopener noreferrer' }, icon('external', 15), el('span.btn-label', { text: 'Google Play' })),
      ),
    ),
  );
  ov.appendChild(sheet);
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) close(); });
  document.addEventListener('keydown', function onKey(e) { if (e.key === 'Escape') { close(); document.removeEventListener('keydown', onKey); } });
  document.body.appendChild(ov);
}

export function lightningPayBlock({ invoice, uri, onLaunch, avatarAddress = '' }) {
  const isMobile = /Android|iPhone|iPad|iPod/i.test((typeof navigator !== 'undefined' && navigator.userAgent) || '');
  const label = isMobile ? 'Pay with Nimiq Pay — opens app' : 'Pay with Nimiq Pay Lightning';

  /* NIMIQ-PAY OPEN FLOW — one system, three situations:
   *   1. INSIDE Nimiq Pay (inNimiqPay()): nothing special — the invoice is
   *      paid by the app around us; no dialogs, no fallbacks.
   *   2. MOBILE browser: the href tries the spec-compliant `lightning:`
   *      URI first. An OS handler (Nimiq Pay / any LN wallet) opens and
   *      takes the user away — done. If nothing opened (back visible
   *      after ~1.6s or the transient OS sheet dismissed) the SAME
   *      "Nimiq Pay not detected" dialog as on desktop appears.
   *   3. DESKTOP browser: no lightning: handler exists, the click is a
   *      guaranteed silent no-op — preventDefault and show the dialog
   *      immediately.
   * In every fallback the invoice is copied to the clipboard first, because
   * Nimiq Pay auto-detects Lightning invoices there (v1.3.3+). */
  let fallbackShown = false;
  let disarm = null;
  const fireFallback = () => {
    if (fallbackShown || inNimiqPay()) return; // inside Nimiq Pay: never
    fallbackShown = true;
    if (disarm) { disarm(); disarm = null; }
    copyText(invoice).catch(() => {});
    nimiqPayMissingDialog(invoice);
  };

  const armOpenFallback = () => {
    if (fallbackShown || inNimiqPay()) return;
    let navigatedAway = false;
    const onHide = () => { navigatedAway = true; };
    const onVis = () => { if (document.visibilityState === 'visible') fireFallback(); };
    const cleanup = () => {
      window.removeEventListener('pagehide', onHide);
      document.removeEventListener('visibilitychange', onVis);
    };
    disarm = cleanup;
    window.addEventListener('pagehide', onHide, { once: true });
    document.addEventListener('visibilitychange', onVis);
    setTimeout(() => {
      if (navigatedAway) { cleanup(); return; }  // a real app/wallet took over
      if (document.visibilityState === 'visible') fireFallback();
      // else: transiently hidden (OS chooser) — onVis fires when we're back
    }, 1600);
  };

  const insidePay = inNimiqPay();
  let link;
  if (insidePay) {
    // INSIDE Nimiq Pay the lightning: href is a DEAD navigation (the WebView
    // does not route it) and the mini-app provider has no pay-invoice API.
    // The app's own clipboard detection (v1.3.3+) is the payment path:
    // copy the invoice, Nimiq Pay offers to pay it.
    link = el('button.btn.btn-gold.btn-block.btn-lg.mt-2', { type: 'button', on: { click: async () => {
      if (onLaunch) { try { onLaunch(); } catch {} }
      try {
        await copyText(invoice);
        toast('Invoice copied — open ☰ → Scan in Nimiq Pay to pay it', 'success');
      } catch { toast('Copy failed — use the Copy button below', 'error'); }
    } } }, nimIcon(18), el('span.btn-label', { text: 'Copy invoice & pay' }));
  } else {
    link = el('a.btn.btn-gold.btn-block.btn-lg.mt-2', { href: uri, on: { click: (e) => {
      if (onLaunch) { try { onLaunch(); } catch {} }
      const platform = detectMobilePlatform();
      if (!platform) {
        // DESKTOP: no browser registers lightning: — the navigation is a
        // guaranteed silent no-op. Skip it (and the 1.6s wait) entirely:
        // copy the invoice and show the missing-app dialog IMMEDIATELY.
        e.preventDefault();
        fireFallback();
        return;
      }
      armOpenFallback(); // mobile browser: let the OS try, dialog if nothing opened
    } } }, nimIcon(18), el('span.btn-label', { text: label }));
  }
  const copyBtn = el('button.btn.btn-outline.btn-block.mt-1', {
    on: { click: async () => { try { await copyText(invoice); toast('Invoice copied — paste it into any Lightning wallet', 'success'); } catch {} } },
  }, icon('copy', 16), el('span.btn-label', { text: 'Copy Lightning invoice' }));
  let qrBox = null;
  const qrBtn = el('button.btn.btn-outline.btn-block.mt-1', { /* BTN-GAP FIX: was flush against the copy button above */
    on: { click: async () => {
      try { await ensureLib('qrcode'); } catch {} // warm/idle-loaded; QR builds instantly after
      if (qrBox) { qrBox.remove(); qrBox = null; qrLabel.textContent = 'Show QR — scan with Nimiq Pay'; return; }
      // Framed QR card: dashed kraft border, centered code on white
      // (scanners need contrast), quiet caption below — with breathing
      // room from the buttons above.
      // The QR frame holds the code on a white canvas; the payer's wallet
      // identicon sits in a small white-rounded badge at the center — QR
      // error-correction (level M) absorbs a ~22% centre overlay, and the
      // finder patterns in the corners stay clear.
      const qrInner = el('div', { style: { position: 'relative', display: 'inline-block', lineHeight: 0 } },
        qrSvgNode(invoice, 29),
        avatarAddress ? el('div.qr-ava', {}, identiconImg(avatarAddress, 'qr-ava-img')) : null,
      );
      qrBox = el('div.pay-qr.mt-3', {},
        el('div.pay-qr-frame', {}, qrInner),
        el('div.xs.faint', { text: 'Scan with Nimiq Pay or any Lightning wallet', style: { marginTop: '8px' } }),
      );
      qrLabel.textContent = 'Hide QR';
      wrap.appendChild(qrBox);
    } },
  }, icon('qr', 16), el('span.btn-label', { text: 'Show QR — scan with Nimiq Pay' }));
  const qrLabel = qrBtn.querySelector('.btn-label');
  const note = el('div.xs.faint.mt-1', {
    text: isMobile
      ? 'Opens Nimiq Pay with the invoice pre-filled.'
      : 'Desktop browsers have no Lightning handler — scan the QR with Nimiq Pay on your phone, or copy the invoice.',
  });
  if (inNimiqPay()) {
    // Clipboard reality check: some WebViews accept navigator.clipboard
    // writes silently WITHOUT bridging them to the system clipboard — the
    // app then has nothing to detect. Copy + read back to VERIFY; when the
    // copy cannot be confirmed, fall back to a long-press selectable
    // invoice box (native WebView selection always writes the clipboard).
    const btcAmtBox = bolt11AmountBtc(invoice);
    const invoiceBox = el('div.mt-2', {},
      el('div.row', { style: { gap: '8px', alignItems: 'baseline', flexWrap: 'wrap', marginBottom: '6px' } },
        el('span.xs.faint', { text: 'Lightning invoice' }),
        btcAmtBox > 0 ? nimAmountNode({ coin_amount: btcAmtBox }) : null,
      ),
      el('div.mono.xs.pay-selectall', { text: invoice, title: 'Long-press to select, then Copy' }),
    );
    /* SOURCE-VERIFIED (@nimiq/mini-app-sdk WALLET_METHODS): the mini-app
     * bridge exposes exactly 10 methods — accounts, sign, NIM tx, staking.
     * There is NO pay-invoice API, so the payment sheet cannot be popped
     * programmatically. Clipboard detection (v1.3.3+) and the Scan screen's
     * paste input are the official in-app paths; the browser route below is
     * the one that hands the invoice to the OS → Nimiq Pay directly. */
    const here = location.origin + location.pathname + location.search;
    const btcAmt = bolt11AmountBtc(invoice);
    const priceLine = el('div.row.mb-1', { style: { gap: '8px', alignItems: 'baseline', flexWrap: 'wrap', borderBottom: '1.5px dashed var(--line-dash)', paddingBottom: '8px' } },
      el('span.small.strong', { text: 'Invoice amount:' }),
      btcAmt > 0 ? nimAmountNode({ coin_amount: btcAmt }) : el('span.small.muted', { text: 'shown in Nimiq Pay' }),
    );
    const steps = el('div.pay-fallback.mt-1', {},
      priceLine,
      el('div.small.strong', { text: 'Paying inside Nimiq Pay ⚡' }),
      el('ol.xs.mt-1', { style: { paddingInlineStart: '18px', margin: '6px 0 0', display: 'grid', gap: '4px' } },
        el('li', { text: 'Tap “Copy invoice & pay” — the Lightning invoice is copied.' }),
        el('li', { text: 'Open the ☰ menu → Scan — Nimiq Pay reads the copied payment request there and shows the NIM amount (v1.3.3+).' }),
        el('li', { text: 'Alternative: long-press the invoice text below → Select all → Copy, then ☰ → Scan.' }),
      ),
      el('div.xs.faint.mt-1', { style: { borderTop: '1.5px dashed var(--line-dash)', paddingTop: '8px' } },
        'Or open this payment page in your phone\u2019s browser (menu → \u201cOpen in browser\u201d) and tap Pay there — the system hands the invoice straight to Nimiq Pay. ',
        el('a', { href: here, target: '_blank', rel: 'noopener noreferrer', style: { fontWeight: 800 } }, 'Open page ↗'),
      ),
    );
    // The dedicated copy button below (copyBtn) also works; keep the QR for
    // cross-device cases. Wire the honest toast through the main button.
    const mainBtn = link; // already the "Copy invoice & pay" button
    mainBtn.addEventListener('click', async () => {
      let verified = false;
      try {
        const back = await (navigator.clipboard && navigator.clipboard.readText ? navigator.clipboard.readText() : Promise.reject(new Error('no read')));
        verified = back === invoice;
      } catch { verified = true; /* read blocked → trust the execCommand path */ }
      if (!verified) toast('Copy not confirmed — long-press the invoice text below to copy', 'error');
    });
    const wrapIn = el('div', {}, steps, mainBtn, invoiceBox, copyBtn, qrBtn);
    return { wrap: wrapIn, link };
  }
  const wrap = el('div', {}, link, copyBtn, qrBtn, note);
  return { wrap, link };
}

/* Pretty "Send as gift" toggle — card with a custom check square; the
 * native input stays in the DOM (opacity 0) for accessibility/keyboard. */
export function giftToggle({ title = 'Send as gift', sub = 'Deliver the code straight to someone else\u2019s email', ico = 'gift', id = '', checked = false, inline = false } = {}) {
  const input = el('input', { type: 'checkbox' });
  if (id) input.id = id;
  input.checked = !!checked;
  const node = el('label.gift-toggle' + (inline ? '.inline' : ''), {},
    input,
    el('span.gbox'),
    el('span.gtext', {},
      el('span.gtitle', {}, icon(ico, 15), el('span', { text: title })),
      sub ? el('span.gsub', { text: sub }) : null,
    ),
  );
  if (checked) node.classList.add('on');
  input.addEventListener('change', () => node.classList.toggle('on', input.checked));
  return { node, input };
}

/* ---------------- Chat composer (support threads) ----------------
 * Enter sends, Shift+Enter adds a newline, the box grows with the text,
 * and the button shows a "Sending…" state while the request is in flight.
 * Shared by the support page and the order page's inline thread. */
/* One support-chat message row — shared by the support page and the order
 * page's inline thread (they used to render the exact same bubble markup in
 * two places; a day-chip header is the caller's job). */
export function chatMessageRow(m, myAddress = '') {
  const isAdmin = m.sender === 'admin';
  return el('div.chat-row.' + (isAdmin ? 'admin' : 'user'), {},
    el('div.ava', {}, isAdmin ? icon('headset', 18) : identiconImg(myAddress || m.sender, 'chat-ava')),
    el('div.bubble-col', {},
      el('div.xs.faint.sender-name', { text: isAdmin ? '🛡️ Support' : '🟠 You' }),
      el('div.bubble.' + (isAdmin ? 'admin' : 'user'), {},
        el('span', { text: m.message }),
        el('span.meta', { text: fmtDate(m.created_at) }),
      ),
    ),
  );
}

export function chatComposer({ placeholder = 'Write a reply…', maxLength = 4000, sendLabel = 'Send', onSend, hint = 'Enter to send · Shift+Enter for a new line' }) {
  const ta = el('textarea.input.composer-input', { placeholder, maxlength: maxLength, rows: 1, 'aria-label': placeholder });
  const label = el('span.btn-label', { text: sendLabel });
  const btn = el('button.btn.btn-gold.composer-send', { type: 'button' }, icon('send', 16), label);
  const autogrow = () => { ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'; };
  ta.addEventListener('input', autogrow);
  const send = async () => {
    const text = ta.value.trim();
    if (!text || btn.disabled) return;
    btn.disabled = true;
    ta.disabled = true;
    label.textContent = 'Sending…';
    try {
      await onSend(text);
      ta.value = '';
      autogrow();
    } finally {
      btn.disabled = false;
      ta.disabled = false;
      label.textContent = sendLabel;
      ta.focus();
    }
  };
  btn.addEventListener('click', send);
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
  const wrap = el('div.composer', {}, el('div.stretch', {}, ta), btn);
  const hintEl = hint ? el('div.xs.faint.composer-hint', { text: hint }) : null;
  const box = el('div', {}, wrap, hintEl);
  return { box, ta, btn, send };
}
