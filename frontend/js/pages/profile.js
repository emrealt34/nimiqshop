/* pages/profile.js — the signed-in user's account view: their wallet address and
 * the per-account daily limits (orders + USD) with a live reset countdown.
 */
import { bootShell, openLogin } from '../shell.js';
import { el, icon, $, replaceChildren, fmtNIM, formatWalletAddress, pageInterval, fmtCountdown } from '../util.js';
import { getAccountLimits, startEmailVerification, verifyEmail } from '../api.js';
import { isAuthed, getAddress } from '../session.js';
import { identiconImg } from '../identicon.js';
import { inNimiqPay } from '../miniapp.js';
import { toast, skeletonLines } from '../ui.js';

bootShell('');

const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on top
// of the first (the duplicate locked-card bug).
replaceChildren(main);
main.appendChild(el('div.container', {},
  el('div.row.mb-2', { style: { gap: '10px' } },
    el('a.btn.btn-ghost.btn-sm', { href: '/' }, icon('back', 16), el('span.btn-label', { text: 'Shop' })),
  ),
  el('h1', { style: { display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' } }, icon('user', 24), 'Your account'),
  el('p.lede', { text: 'Your wallet is your account. These are your daily purchase limits — the same for everyone, reset on a rolling 24-hour window.' }),
  el('div#detail.mt-2', {}, el('div.card', {}, skeletonLines(4))),
));

const detail = $('#detail');
let tickTimer = null;
let resetsAt = null;

// fmtCountdown lives in ../util.js (shared).

async function load() {
  if (!isAuthed()) {
    replaceChildren(detail, lockedSignInCard({
      title: 'Sign in to view your account',
      text: 'Connect your Nimiq wallet to see your address and daily limits.',
      onConnect: () => openLogin(() => load()),
    }));
    return;
  }
  try {
    const L = await getAccountLimits();
    resetsAt = L.resets_at ? new Date(L.resets_at) : null;
    // Trust the SERVER clock, not the device's: a wrong client clock would
    // render "Resets in now"/negative countdowns. Compensate the skew.
    if (resetsAt && L.server_now) {
      const skew = Date.now() - new Date(L.server_now).getTime();
      if (Math.abs(skew) > 5000) resetsAt = new Date(resetsAt.getTime() + skew);
    }
    render(L, getAddress());
  } catch (err) {
    replaceChildren(detail, el('div.card.fade-in', {}, el('div.alert.error', {}, icon('alert', 19), el('div', { text: err.message || 'Could not load your account.' }))));
  }
}

function emailCard() {
  const email = el('input.input', { type: 'email', placeholder: 'you@gmail.com', autocomplete: 'email', 'aria-label': 'Gmail address' });
  const code = el('input.input', { type: 'text', inputmode: 'numeric', maxlength: 6, placeholder: '6-digit verification code', autocomplete: 'one-time-code', 'aria-label': 'Verification code', style: { display: 'none', letterSpacing: '0.25em', textAlign: 'center' } });
  const status = el('div.small.muted.mt-1', { text: 'Add Gmail to receive order delivery and payment confirmations.' });
  const button = el('button.btn.btn-outline', { text: 'Send verification code' });
  button.onclick = async () => {
    if (!email.value || !email.validity.valid || !/@gmail\.com$/i.test(email.value.trim())) { email.focus(); status.textContent = 'Enter a valid Gmail address first.'; return; }
    button.disabled = true; status.textContent = 'Requesting a verification code…';
    try {
      await startEmailVerification(email.value.trim());
      code.style.display = ''; code.focus(); button.textContent = 'Verify Gmail';
      status.textContent = 'The server accepted the request. Enter the 6-digit code from Gmail.';
      button.disabled = false;
      button.onclick = async () => {
        if (!/^\d{6}$/.test(code.value)) { status.textContent = 'Please enter the 6-digit code from Gmail.'; return; }
        button.disabled = true; status.textContent = 'Checking code…';
        try { await verifyEmail(email.value.trim(), code.value); status.textContent = 'Gmail verified for this account.'; button.textContent = 'Gmail verified'; toast('Gmail verified', 'success'); }
        catch (err) { button.disabled = false; status.textContent = err.message || 'The code could not be verified.'; }
      };
    } catch (err) { button.disabled = false; status.textContent = err.message || 'Email verification is not available.'; }
  };
  return el('div.card.mt-2.email-card', {}, el('div.card-title', { text: 'Email delivery' }), el('p.small.muted', { text: 'Optional: verify Gmail once so order codes and payment updates can arrive automatically.' }), el('div.field', {}, email), el('div.row', { style: { gap: '10px' } }, code, button), status);
}

function render(L, addr) {
  const maxOrders = Number(L.max_orders) || 0;
  const usedOrders = Number(L.used_orders) || 0;
  const maxUSD = Number(L.max_usd) || 0;
  const usedUSD = parseFloat(L.used_usd) || 0;
  const orderPct = maxOrders ? Math.min(100, (usedOrders / maxOrders) * 100) : 0;
  const usdPct = maxUSD ? Math.min(100, (usedUSD / maxUSD) * 100) : 0;

  const cd = el('span.mono', { text: '—' });
  function tick() {
    if (resetsAt) cd.textContent = fmtCountdown(resetsAt.getTime() - Date.now());
  }
  tick();
  clearInterval(tickTimer);
  tickTimer = pageInterval(tick, 1000);
  window.addEventListener('beforeunload', () => clearInterval(tickTimer));

  replaceChildren(detail, el('div.fade-in', {},
    el('div.card.mb-2', {},
      el('div.card-title', { text: 'Your wallet' }),
      el('div.row', { style: { gap: '14px', alignItems: 'center' } },
        identiconImg(addr, 'wallet-avatar'),
        el('div', { style: { minWidth: 0 } },
          el('div.mono.strong.wallet-address', { text: formatWalletAddress(addr), title: addr }),
          el('div.xs.faint', { text: 'Connected via Nimiq ' + (inNimiqPay() ? 'Pay' : 'Hub') + ' · keys never leave your wallet' }),
        ),
        copyButton(addr, 'Copy address'),
      ),
    ),
    el('div.card', {},
      el('div.card-title', { text: 'Daily limits' }),
      el('div.row.between.mb-1', { style: { alignItems: 'baseline' } },
        el('div.strong', { text: `Orders: ${usedOrders} / ${maxOrders || '∞'}` }),
        el('div.small.muted', {}, icon('clock', 13), ' Resets in ', cd),
      ),
      el('div.mini-progress', { style: { width: '100%', display: 'block', height: '8px', background: 'var(--surface-3)', borderRadius: '99px', overflow: 'hidden' } }, el('div', { style: { width: orderPct + '%', height: '100%', background: 'var(--gold-grad)' } })),
      el('div.row.between.mt-2.mb-1', { style: { alignItems: 'baseline' } },
        el('div.strong', { text: `Spending: ${L.used_nim ? fmtNIM(L.used_nim) + ' NIM' : '— NIM'} / ${L.max_nim ? fmtNIM(L.max_nim) + ' NIM' : (maxUSD ? 'NIM limit' : '∞')}` }),
      ),
      el('div.mini-progress', { style: { width: '100%', display: 'block', height: '8px', background: 'var(--surface-3)', borderRadius: '99px', overflow: 'hidden' } }, el('div', { style: { width: usdPct + '%', height: '100%', background: 'var(--gold-grad)' } })),
      el('div.small.muted.mt-2', { text: 'Limits are the same for every account and protect everyone from fraud and abuse. Failed purchases do not count. The window slides — your oldest purchase drops off after 24 hours.' }),
    ),
    emailCard(),
  ));
}

load();
