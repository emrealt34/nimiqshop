/* shell.js — site chrome: top bar, navigation, account chip, login modal,
 * mobile tab bar. Every page calls bootShell(activeKey).
 */
import { el, icon, brandMark, shortAddr, formatWalletAddress, CFG, replaceChildren } from './util.js';
import { isAuthed, getAddress, signOut } from './session.js';
import { loginWithHub, initHubRedirectHandling, friendlyHubError } from './hub.js';
import { identiconImg } from './identicon.js';
import { toast, openSheet, closeSheet } from './ui.js';
import { openCart, cartCount, onCartChange } from './cart.js';
import { listQuotes } from './api.js';
import { inNimiqPay } from './miniapp.js';

/* First paint ALWAYS starts at the top. Browsers restore the previous
 * scroll position on reload, which dropped visitors into a random middle
 * of the page while content was still loading ("am I in the right
 * place?"). Manual restoration + an explicit top scroll fixes it. */
if (typeof history !== 'undefined' && 'scrollRestoration' in history) {
  history.scrollRestoration = 'manual';
}
try { window.scrollTo(0, 0); } catch { /* jsdom/no-scroll env */ }

const NAV = [
  { key: 'shop', label: 'Shop', href: '/', icon: 'bag' },
  { key: 'activity', label: 'Activity', href: '/activity', icon: 'pulse' },
  { key: 'orders', label: 'Orders', href: '/orders', icon: 'receipt' },
  { key: 'support', label: 'Support', href: '/support', icon: 'headset' },
];

/* ---------------- Header ---------------- */

/* Cart button (header) */
function cartButton() {
  const badge = el('span.cart-badge');
  const btn = el('button.cart-btn', { 'aria-label': 'Cart', on: { click: () => openCart() } }, icon('bag', 20), badge);
  const sync = (n) => { badge.textContent = n > 0 ? String(n) : ''; badge.style.display = n > 0 ? '' : 'none'; };
  sync(cartCount());
  onCartChange(sync);
  return btn;
}

function accountArea() {
  const wrap = el('div.acct-wrap');
  render();

  async function render() {
    wrap.textContent = '';
    if (isAuthed()) {
      const addr = getAddress();
      const btn = el('button.acct-btn', { 'aria-haspopup': 'menu' },
        identiconImg(addr),
        el('span.addr', { text: formatWalletAddress(shortAddr(addr)) }),
        icon('chevron', 14),
      );
      const menu = el('div.acct-menu', { role: 'menu' });
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        menu.classList.toggle('open');
      });
      document.addEventListener('click', () => menu.classList.remove('open'));
      buildMenu(menu);
      wrap.append(btn, menu);
    } else {
      wrap.appendChild(el('button.btn.btn-gold.btn-sm', {
        on: { click: () => openLogin() },
      }, icon('wallet', 18), el('span.btn-label', { text: 'Connect wallet' })));
    }
  }

  function buildMenu(menu) {
    menu.textContent = '';
    const addr = getAddress();
    menu.appendChild(el('div.menu-head', {},
      identiconImg(addr),
      el('div', { style: { minWidth: 0 } },
        el('div.strong.small.truncate', { text: formatWalletAddress(shortAddr(addr, 12, 8)), title: addr }),
        el('div.xs.faint', { text: 'Connected via Nimiq ' + (inNimiqPay() ? 'Pay' : 'Hub') }),
      ),
    ));
    const items = [
      { icon: 'user', label: 'Account & limits', fn: () => { clientNavigate('/profile'); } },
      { icon: 'receipt', label: 'My orders', fn: () => { clientNavigate('/orders'); } },
      { icon: 'pulse', label: 'Public activity', fn: () => { clientNavigate('/activity'); } },
      { icon: 'headset', label: 'Support tickets', fn: () => { clientNavigate('/support'); } },
      { icon: 'copy', label: 'Copy my address', fn: async () => {
          const { copyText } = await import('./util.js');
          const ok = await copyText(addr);
          toast(ok ? 'Address copied' : 'Copy failed', ok ? 'success' : 'error');
        } },
      { icon: 'logout', label: 'Sign out', danger: true, fn: () => {
          signOut();
          toast('Signed out. Your funds stay safe in your own wallet.', 'info');
          window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false } }));
        } },
    ];
    for (const it of items) {
      menu.appendChild(el(it.danger ? 'button.danger' : 'button', { on: { click: () => { menu.classList.remove('open'); it.fn(); } } },
        icon(it.icon, 17), el('span', { text: it.label })));
    }
  }

  window.addEventListener('nimshop:session', () => render());
  return wrap;
}

/* ---------------- Login sheet ---------------- */

export function openLogin(onSuccess) {
  const { body } = openSheet({ title: 'Sign in with your wallet' });

  let spinnerNote = null;

  function drawIntro() {
    const inPay = inNimiqPay(); // inside Nimiq Pay: pay-brand wording; on the web: Hub
    body.textContent = '';
    body.append(
      el('div.login-hero', {},
        el('div.identicon-lg.placeholder', {}, icon('nimiq', 38)),
        el('h3.center', { text: 'No email. No password.', style: { marginBottom: '8px' } }),
        el('p.muted.small.center', {
          text: `Your Nimiq wallet is your account. Sign in by approving one message in Nimiq ${inPay ? 'Pay' : 'Hub'} — your keys never leave your wallet.`,
          style: { maxWidth: '400px', margin: '0 auto' },
        }),
      ),
      el('div.mt-3', {},
        el('button.btn.btn-gold.btn-block.btn-lg', {
          on: { click: async (e) => {
            const btn = e.currentTarget;
            btn.disabled = true;
            spinnerNote && spinnerNote.remove();
            spinnerNote = el('div.center.small.muted.mt-2', { text: `Opening Nimiq ${inPay ? 'Pay' : 'Hub'}…` });
            body.appendChild(spinnerNote);
            try {
              const r = await loginWithHub((s) => {
                spinnerNote.textContent = s === 'challenge' ? 'Preparing secure challenge…' : `Waiting for your signature in Nimiq ${inPay ? 'Pay' : 'Hub'}…`;
              });
              closeSheet();
              const connectedToast = toast(`Connected: ${formatWalletAddress(r.address)}`, 'success'); connectedToast.prepend(identiconImg(r.address, 'toast-avatar'));
              if (onSuccess) onSuccess(r.address);
              window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: true, address: r.address } }));
            } catch (err) {
              btn.disabled = false;
              const msg = friendlyHubError(err);
              if (spinnerNote) spinnerNote.remove();
              // SINGLE error box: a second failed attempt REPLACES the old
              // message instead of stacking a duplicate alert under it.
              let box = body.querySelector('.login-error');
              if (!box) { box = el('div.alert.error.mt-2.login-error', { style: { marginBottom: 0 } }); body.appendChild(box); }
              replaceChildren(box, icon('alert', 19), el('div', { text: msg }));
            }
          } },
        }, icon('nimiq', 20), el('span.btn-label', { text: `Continue with Nimiq ${inPay ? 'Pay' : 'Hub'}` })),
        el('p.xs.faint.center.mt-2', {
          text: CFG.NETWORK === 'testnet' ? 'Connected to the Nimiq TESTNET network.' : `Powered by Nimiq ${inPay ? 'Pay' : 'Hub'} — keys stay in your wallet, always.`,
        }),
      ),
    );
  }

  drawIntro();
}

/* ---------------- Build verification strip ----------------
 * Rendered at the very bottom of every page. Proves, live and
 * client-side, that THE FRONTEND and THE BACKEND are exactly the
 * open-source build:
 *   1. Fetches /integrity.json and recomputes its root hash from the
 *      listed file hashes (same check verify.html deep-runs per file).
 *   2. Fetches /api/integrity where the backend reports its own
 *      binary SHA-256 + embedded source-manifest root.
 * If either check fails the strip turns stamp-red. The full, deep
 * per-file verification runs on /verify.html (linked from the strip).
 */

function shortHash(h, n = 8) { return h && h.length > n ? h.slice(0, n) + '…' : (h || '—'); }

async function stripSha256Hex(buf) {
  const h = await crypto.subtle.digest('SHA-256', buf);
  return [...new Uint8Array(h)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

function mountBuildStrip() {
  const webCode = el('code.bb-hash', { text: '…' });
  const apiCode = el('code.bb-hash', { text: '…' });
  const webChip = el('span.bb-chip', { title: 'Frontend build — root hash recomputed from /integrity.json in your browser' },
    icon('package', 14), el('span.bb-label', { text: 'web' }), webCode, el('button.bb-copy', { type: 'button', title: 'Copy web hash', 'aria-label': 'Copy web hash' }, icon('copy', 13)));
  const apiChip = el('span.bb-chip', { title: 'Backend build — SHA-256 of the running binary, reported at /api/integrity' },
    icon('server', 14), el('span.bb-label', { text: 'api' }), apiCode, el('button.bb-copy', { type: 'button', title: 'Copy API hash', 'aria-label': 'Copy API hash' }, icon('copy', 13)));
  const sub = el('span.bb-sub', { text: 'Checking hashes against this deployment…' });

  const strip = el('div.buildbar', {},
    el('div.container.bb-inner', {},
      el('div.bb-left', {},
        icon('shield', 24),
        el('div.bb-title', {},
          el('strong', { text: 'Open-source, hash-verified build' }),
          sub,
        ),
      ),
      el('div.bb-chips', {}, webChip, apiChip),
      el('div.bb-stamp.hide', {}, 'Verified ⚡', document.createElement('br'), 'build'),
      el('div.bb-right', {},
        el('a.bb-link', { href: '/verify.html', title: 'Deep-check: hash every file of this site against the manifest, in your browser' },
          icon('check', 14), el('span', { text: 'Full check' }), icon('external', 13)),
        CFG.GITHUB_URL
          ? el('a.bb-gh', { href: CFG.GITHUB_URL, target: '_blank', rel: 'noopener noreferrer', 'aria-label': 'Open-source repository on GitHub', title: 'Open-source repository on GitHub' }, icon('github', 19))
          : null,
      ),
    ),
  );
  // Keep the verification panel directly above the footer, not below it.
  const footer = document.querySelector('.footer');
  if (footer) footer.before(strip); else document.body.appendChild(strip);
  const copyHash = async (chip) => {
    const code = chip.querySelector('.bb-hash')?.title || chip.querySelector('.bb-hash')?.textContent;
    try { await navigator.clipboard.writeText(code || ''); toast('Hash copied', 'success'); }
    catch { toast('Could not copy hash', 'error'); }
  };
  for (const chip of strip.querySelectorAll('.bb-chip')) {
    chip.addEventListener('click', (e) => { if (!e.target.closest('.bb-copy')) copyHash(chip); });
    chip.style.cursor = 'copy';
  }
  for (const copy of strip.querySelectorAll('.bb-copy')) copy.addEventListener('click', (e) => {
    e.stopPropagation(); copyHash(copy.parentElement);
  });

  const stamp = strip.querySelector('.bb-stamp');
  let webOk = null, apiOk = null;
  const refresh = () => {
    const chip = (elc, codeEl, ok, hash) => {
      elc.classList.toggle('ok', ok === true);
      elc.classList.toggle('bad', ok === false);
      codeEl.textContent = ok ? shortHash(hash) : (ok === false ? 'fail' : '…');
      codeEl.title = hash || '';
    };
    chip(webChip, webCode, webOk, strip._webRoot);
    chip(apiChip, apiCode, apiOk, strip._apiBin);
    if (webOk !== null || apiOk !== null) {
      const allOk = webOk === true && apiOk === true;
      const anyBad = webOk === false || apiOk === false;
      sub.textContent = allOk
        ? 'Live site matches the manifest — frontend and backend hashes check out.'
        : anyBad
          ? 'Hash check failed — open the full check before trusting this deployment.'
          : 'Partially verified — one side is still unreachable.';
      if (allOk) { stamp.classList.remove('hide'); strip.classList.add('bb-good'); }
      if (anyBad) strip.classList.add('bb-alert');
    }
  };

  // Frontend: recompute the manifest root hash from its own file list.
  (async () => {
    try {
      const r = await fetch('/integrity.json', { cache: 'no-store' });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const manifest = JSON.parse(await r.text());
      const canon = manifest.files.map((fe) => `${fe.path}\n${fe.sha256}\n`).join('');
      const root = await stripSha256Hex(new TextEncoder().encode(canon));
      strip._webRoot = root;
      webOk = root === manifest.rootHash;
    } catch { webOk = false; }
    refresh();
  })();

  // Backend: the running binary reports its own SHA-256 + source root.
  (async () => {
    try {
      const r = await fetch((CFG.API_BASE || '/api').replace(/\/$/, '') + '/integrity', { cache: 'no-store' });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const rep = await r.json();
      strip._apiBin = rep.binary_sha256 || '';
      apiOk = Boolean(rep.binary_sha256) && Boolean(rep.source_root)
        && rep.schema === 'nimiq-shop.integrity/v1';
    } catch { apiOk = false; }
    refresh();
  })();
}

/* ---------------- Shell boot ---------------- */

let navBound = false;
const ROUTES = { '/': '/index.html', '/orders': '/orders.html', '/support': '/support.html', '/profile': '/profile.html', '/activity': '/activity.html', '/product': '/product.html', '/order': '/order.html', '/track': '/track.html', '/admin': '/admin.html' };
let navigating = false;

async function clientNavigate(url, push = true) {
  if (navigating) return;
  const target = new URL(url, location.href);
  if (target.origin !== location.origin) return;
  const file = ROUTES[target.pathname] || (target.pathname.endsWith('.html') ? target.pathname : null);
  if (!file) { location.href = target.href; return; }
  navigating = true;
  try {
    const r = await fetch(file + target.search, { headers: { 'X-Client-Navigation': '1' }, cache: 'no-store' });
    if (!r.ok) throw new Error('navigation failed');
    const html = await r.text();
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const nextMain = doc.querySelector('#main');
    const script = doc.querySelector('script[type="module"][src]');
    const current = document.querySelector('#main');
    if (!nextMain || !script || !current) throw new Error('invalid page shell');
    if (push) history.pushState({}, '', target.pathname + target.search + target.hash);
    document.title = doc.title;
    // Tell page modules their timers are dead BEFORE the old content goes
    // away — pageInterval()-registered polls (activity, presence, support,
    // orders…) must stop the moment the visitor leaves that page.
    window.dispatchEvent(new Event('nimshop:pagechange'));
    const swap = () => { current.replaceChildren(); import(new URL(script.getAttribute('src'), location.origin + file).href + '?nav=' + Date.now()); };
    // Hold the navigation lock until the new module FINISHED executing:
    // releasing it early let a double-click start a SECOND navigation whose
    // module rendered after the first — two stacked page containers, one
    // gone after a refresh.
    // Soft entrance: the new content glides in (CSS rise-in) instead of
    // the old jarring view-transition snap.
    swap();
    const page = document.querySelector('#main');
    if (page) { page.classList.remove('page-enter'); requestAnimationFrame(() => page.classList.add('page-enter')); }
    window.scrollTo({ top: 0, behavior: 'instant' });
  } catch { location.href = target.href; }
  finally { navigating = false; }
}

function bindClientNavigation() {
  if (navBound) return; navBound = true;
  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[href]');
    if (!a || a.target || a.hasAttribute('download') || e.defaultPrevented || a.origin !== location.origin) return;
    const u = new URL(a.href);
    if (!ROUTES[u.pathname]) return;
    e.preventDefault(); clientNavigate(u.href, true);
  });
  window.addEventListener('popstate', () => clientNavigate(location.href, false));
}

export const navigate = (url, push = true) => clientNavigate(url, push);

/* Red "awaiting payment" badge on every Orders link (topbar + tabbar).
 * orders.js / cart.js publish the count via sessionStorage + the
 * 'nimshop:awaiting' event; F5 keeps it (session-scoped). */
function awaitingCount() {
  try { const v = parseInt(sessionStorage.getItem('nimshop_awaiting') || '0', 10); return isFinite(v) && v > 0 ? v : 0; } catch { return 0; }
}
function updateNavBadges() {
  const n = awaitingCount();
  document.querySelectorAll('a[href="/orders"]').forEach((a) => {
    // Tabbar links wrap their icon in .tab-ico — the badge rides the ICON's
    // top-right corner there (classic notification-dot placement), not the
    // link edge. Desktop topbar links take the badge directly.
    const holder = a.querySelector('.tab-ico') || a;
    let b = holder.querySelector('.nav-badge');
    if (n > 0) {
      if (!b) { b = el('span.nav-badge'); holder.appendChild(b); }
      b.textContent = String(n);
    } else if (b) b.remove();
  });
}
window.addEventListener('nimshop:awaiting', updateNavBadges);
window.addEventListener('nimshop:session', updateNavBadges);

/* NAV-BADGE FIX 2: a session that starts on ANY page (not /orders) never
 * learned the awaiting count — the badge only existed after visiting Orders.
 * When signed in, fetch the count once per page load and publish it, so the
 * red "1" is on the Orders link everywhere from the first render. */
let badgePrefetched = false;
async function prefetchAwaitingCount() {
  if (badgePrefetched || !isAuthed()) return;
  badgePrefetched = true;
  try {
    const quotes = await listQuotes().catch(() => []);
    const n = (quotes || []).filter((q) => String(q.status).toLowerCase() === 'awaiting_payment').length;
    const known = parseInt(sessionStorage.getItem('nimshop_awaiting') || '0', 10) || 0;
    if (n !== known) {
      sessionStorage.setItem('nimshop_awaiting', String(n));
      window.dispatchEvent(new Event('nimshop:awaiting'));
    }
  } catch { /* offline → sessionStorage value (if any) stays */ }
}

export function bootShell(activeKey) {
  bindClientNavigation();
  try { window.scrollTo(0, 0); } catch {}
  // Reuse the shell during client-side navigation: page transitions do not
  // rebuild the whole document or flash a blank page.
  document.querySelectorAll('.topbar,.tabbar,.buildbar').forEach((n) => n.remove());
  // Every page mount gets the same entry animation, including returning to a
  // page through client-side navigation.
  // Top bar
  const header = el('header.topbar', {},
    el('div.topbar-inner', {},
      el('a.brand', { href: '/', 'aria-label': 'nim.shop home' },
        brandMark(50),
        el('span', {}, 'nim', el('span.dot', { text: '.' }), 'shop'),
      ),
      el('nav.mainnav', {},
        NAV.map((n) => el('a' + (n.key === activeKey ? '.active' : ''), {
          href: n.href,
          'aria-current': n.key === activeKey ? 'page' : null,
          text: n.label,
        })),
      ),
      el('div.topbar-spacer'),
      cartButton(),
      accountArea(),
    ),
  );

  // Mobile tab bar
  const tabbar = el('nav.tabbar', { 'aria-label': 'Primary' },
    NAV.map((n) => el('a' + (n.key === activeKey ? '.active' : ''), {
      href: n.href,
      'aria-current': n.key === activeKey ? 'page' : null,
    }, el('span.tab-ico', {}, icon(n.icon, 22)), el('span', { text: n.label }))),
  );

  document.body.prepend(tabbar);

  // Badge both bars (the old call ran BEFORE the tabbar existed — the mobile
  // Orders tab never got its red count until some later event).
  updateNavBadges();
  prefetchAwaitingCount();
  document.body.prepend(header);

  mountBuildStrip();

  // Redirect-return handling for mobile login/payment flows.
  initHubRedirectHandling({
    onLogin: (address) => {
      const connectedToast = toast(`Connected: ${formatWalletAddress(address)}`, 'success'); connectedToast.prepend(identiconImg(address, 'toast-avatar'));
      window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: true, address } }));
    },
  });

  window.addEventListener('nimshop:hub-error', (e) => {
    toast(e.detail?.message || 'Wallet request failed', 'error');
  });

  window.addEventListener('nimshop:session', (e) => {
    if (e.detail && e.detail.expired) toast('Your session expired — please sign in again.', 'info');
  });
}
