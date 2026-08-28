/* delivery.js — SHARED checkout delivery steps. ONE source of truth for
 * every purchase path (product page, home quick-buy, cart checkout), so the
 * same flow can never behave differently per page:
 *
 *   gift cards / eSIMs  → delivery EMAIL (recipient email in gift mode)
 *   mobile top-ups      → the PHONE NUMBER to top up (recipient phone in
 *                         gift mode). No email is asked or sent for top-ups.
 *
 * Gift extras (channel / message / sms phone) are persisted to localStorage
 * under the nimshop_gift_* keys and attached to every order request by
 * buildOrderRequest() — the single payload builder for /api/quotes.
 */
import { el, icon, replaceChildren } from './util.js';
import { giftToggle } from './ui.js';
import { isValidEmail, emailError, normalizePhone } from './validate.js';
import { checkPhone } from './api.js';
import { getStoredEmail, setStoredEmail } from './session.js';

/* Normalize + live-supplier validation of a phone number. Pure value check
 * (no DOM) so every caller — standalone step, gift group, cart — shares it. */
async function collectPhoneValue(raw, country) {
  const local = normalizePhone(raw, country);
  if (local.error) return { ok: false, error: local.error };
  try {
    const res = await checkPhone(local.phone, country);
    return { ok: true, phone: (res && res.phone_number) ? res.phone_number : local.phone };
  } catch (e) {
    return { ok: false, error: e.message || 'Number not accepted — check country code and operator.' };
  }
}

/* Standalone "which number to top up" step (used for cart top-ups and any
 * self-purchase that only needs the number). Resolves to E.164 or null. */
function askTopUpPhone(body, { country, title = 'Which number to top up?', sub = 'The top-up credit goes straight to this number — validated live with the supplier before you pay. No email needed for a top-up.' } = {}) {
  return new Promise((resolve) => {
    let busy = false;
    const input = el('input.input', { type: 'tel', inputmode: 'tel', placeholder: '+90 555 123 45 67 (or 0555 123 45 67)', autocomplete: 'tel' });
    const error = el('div', { style: { minHeight: '0px' } });
    const btnLabel = 'Check number & continue';
    const btn = el('button.btn.btn-gold.btn-block.btn-lg', { text: btnLabel });
    const showErr = (msg) => {
      replaceChildren(error, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: msg })));
    };
    const submit = async () => {
      replaceChildren(error);
      if (busy) return;
      busy = true; btn.disabled = true; btn.textContent = 'Checking…';
      const r = await collectPhoneValue(input.value, country);
      busy = false; btn.disabled = false; btn.textContent = btnLabel;
      if (!r.ok) { showErr(r.error); input.focus(); return; }
      resolve(r.phone);
    };
    replaceChildren(body,
      el('div.mt-2.mb-1', {}, el('div.strong', { text: title }), el('div.small.muted.mt-1', { text: sub })),
      el('div.field', {}, input),
      el('div.error-slot', {}, error),
      btn,
    );
    btn.addEventListener('click', submit);
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); submit(); } });
    input.focus();
  });
}

/* Full top-up delivery step (product page + single top-up in a pure cart):
 * self-purchase asks only the number; gift mode asks the recipient's number
 * and — only when the note goes by email/both — the recipient email.
 * Resolves to { email, phone }. */
function askTopUpStep(body, p) {
  return new Promise((resolve) => {
    let busy = false;
    const input = el('input.input', { type: 'tel', inputmode: 'tel', placeholder: '+90 555 123 45 67 (or 0555 123 45 67)', autocomplete: 'tel' });
    const gt = giftToggle();
    const giftCheck = gt.input;
    const giftToggleEl = gt.node;

    const giftGroup = el('div', { style: { display: 'none' } },
      el('div.field', {},
        el('label', { text: "Recipient's phone — the number to top up" }),
        el('input.input', { type: 'tel', inputmode: 'tel', placeholder: '+90 555 123 45 67', autocomplete: 'tel', id: 'p-gift-phone' }),
      ),
      el('div.field.gift-email-wrap', { style: { display: 'none' } },
        el('label', { text: "Recipient's email (for the gift note — only needed when the note goes by email)" }),
        el('input.input', { type: 'email', placeholder: 'friend@gmail.com', autocomplete: 'email', id: 'p-gift-email' }),
      ),
      el('div.field', {},
        el('label', { text: 'How should we tell them? ' }),
        el('div.row', { style: { gap: '8px', flexWrap: 'wrap' } },
          el('label.chip', { for: 'p-ch-email' }, '📧 Email'),
          el('label.chip', { for: 'p-ch-sms' }, '📱 SMS'),
          el('label.chip', { for: 'p-ch-both' }, '📧 + 📱 Both'),
        ),
      ),
      el('div.field', {},
        el('label', { text: 'Message from you (the gifter)' }),
        el('textarea.input', { placeholder: 'Happy birthday! 🎂', rows: 3, maxlength: 2000, style: { minHeight: '80px', resize: 'vertical' }, id: 'p-gift-msg' }),
        el('div.small.muted', { text: 'Email: up to 2000 chars · SMS: capped at 160 chars. The top-up itself is applied to the phone number you enter below.' }),
      ),
      el('div.alert.info', { style: { fontSize: '0.78rem', padding: '10px 12px', marginTop: '4px', marginBottom: '10px' } },
        icon('info', 14),
        el('div', { text: 'The top-up credit goes straight to the recipient’s phone number — CryptoRefills confirms it from cryptorefills.com. We send a separate “you got a gift 🎁” note with your personal message on it.' }),
      ),
    );

    const giftChannel = () => {
      const sel = giftGroup.querySelector('input[name="p-gift-channel"]:checked');
      return sel ? sel.value : 'sms';
    };
    {
      const wrap = giftGroup.querySelector('.row');
      const chipWrap = wrap.parentElement;
      const inputs = ['email', 'sms', 'both'].map((v, i) => {
        const r = el('input', { type: 'radio', name: 'p-gift-channel', id: 'p-ch-' + v, value: v, style: { position: 'absolute', opacity: 0, pointerEvents: 'none' } });
        if (i === 1) r.checked = true; // top-ups: gift note defaults to SMS (no email needed)
        return r;
      });
      inputs.forEach((r) => chipWrap.insertBefore(r, chipWrap.firstChild));
      chipWrap.querySelectorAll('label.chip').forEach((c) => {
        c.style.cursor = 'pointer';
        c.addEventListener('click', () => {
          inputs.forEach((r) => r.checked = r.id === c.getAttribute('for'));
          const ch = giftChannel();
          const ew = giftGroup.querySelector('.gift-email-wrap');
          if (ew) ew.style.display = (ch === 'email' || ch === 'both') ? 'block' : 'none';
        });
      });
    }

    const typeTitle = 'Which number to top up?';
    const typeSub = 'The top-up credit goes straight to this number — validated live with the supplier before you pay. No email needed for a top-up.';
    const headerTitle = el('div.strong', { text: typeTitle });
    const headerSub = el('div.small.muted.mt-1', { text: typeSub });
    giftCheck.addEventListener('change', () => {
      const on = giftCheck.checked;
      giftGroup.style.display = on ? 'block' : 'none';
      ownWrap.style.display = on ? 'none' : 'block';
      headerTitle.textContent = on ? 'Top up a friend 🎁' : typeTitle;
      headerSub.textContent = on
        ? 'The top-up goes straight to the recipient’s phone number — their number goes below. Their email is only needed if the gift note goes by email.'
        : typeSub;
      if (on) {
        const gp = giftGroup.querySelector('#p-gift-phone');
        if (gp && gp.value === (input.value || '')) gp.value = '';
        setTimeout(() => gp.focus(), 100);
      }
    });

    const error = el('div', { style: { minHeight: '0px' } });
    const btn = el('button.btn.btn-gold.btn-block.btn-lg', { text: 'Check number & continue' });
    const showErr = (msg, focusEl) => {
      replaceChildren(error, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: msg })));
      if (focusEl && focusEl.focus) focusEl.focus();
    };
    const liveCheck = async (raw, focusEl) => {
      if (busy) return null;
      busy = true; btn.disabled = true; btn.textContent = 'Checking…';
      const r = await collectPhoneValue(raw, p.country);
      busy = false; btn.disabled = false; btn.textContent = 'Check number & continue';
      if (!r.ok) { showErr(r.error, focusEl); return null; }
      return r.phone;
    };
    const submit = async () => {
      replaceChildren(error);
      if (giftCheck.checked) {
        const gp = giftGroup.querySelector('#p-gift-phone');
        const v = await liveCheck(gp.value, gp);
        if (!v) return;
        const ch = giftChannel();
        let gEmail = '';
        if (ch === 'email' || ch === 'both') {
          const ge = giftGroup.querySelector('#p-gift-email');
          gEmail = ge.value.trim();
          if (!isValidEmail(gEmail)) { showErr(emailError(gEmail) || 'Enter the recipient email for the gift note', ge); return; }
        }
        const gMsg = giftGroup.querySelector('#p-gift-msg').value.trim();
        try {
          localStorage.setItem('nimshop_gift_channel', ch);
          if (gMsg) localStorage.setItem('nimshop_gift_msg', gMsg); else localStorage.removeItem('nimshop_gift_msg');
          if (ch === 'sms' || ch === 'both') localStorage.setItem('nimshop_gift_phone', v); else localStorage.removeItem('nimshop_gift_phone');
        } catch {}
        resolve({ email: gEmail, phone: v });
        return;
      }
      // Self-purchase: never carry stale gift extras into the quote.
      try {
        localStorage.removeItem('nimshop_gift_channel');
        localStorage.removeItem('nimshop_gift_msg');
        localStorage.removeItem('nimshop_gift_phone');
      } catch {}
      const v = await liveCheck(input.value, input);
      if (!v) return;
      // TOP-UP FIX: no email at all for top-ups — the top-up goes to the
      // phone number, nothing else. Not asked, not reused, not sent.
      resolve({ email: '', phone: v });
    };
    const ownWrap = el('div', {},
      el('div.mt-2.mb-1', {}, headerTitle, headerSub),
      el('div.field', {}, input),
    );
    replaceChildren(body, ownWrap, giftToggleEl, giftGroup, el('div.error-slot', {}, error), btn);
    btn.addEventListener('click', submit);
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); submit(); } });
    const geInput = giftGroup.querySelector('#p-gift-email');
    if (geInput) geInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') submit(); });
    const gpInput = giftGroup.querySelector('#p-gift-phone');
    if (gpInput) gpInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); submit(); } });
    input.focus();
  });
}

/* Shared email delivery step for gift cards / eSIMs. Self-purchase asks the
 * buyer's email (prefilled / saved); gift mode asks the recipient email plus
 * notification channel/message. Used by the product page AND the cart.
 * Resolves to the delivery email string. */
function askEmailStep(body, opts = {}) {
  const {
    allowEdit = true,          // false → show a saved email as a read-only display
    title = 'Delivery email',
    sub = 'Where should we deliver your gift card code?',
    savedHint = '',
    giftTitle = 'Gift delivery 🎁',
    giftSub = 'The code goes straight to the recipient\u2019s inbox — their email goes below.',
  } = opts;
  return new Promise((resolve) => {
    let myEmail = opts.prefill || getStoredEmail();
    const hasEmail = !!(myEmail && isValidEmail(myEmail));
    const gt = giftToggle();
    const checkbox = gt.input;

    const yourEmailDisplay = el('div.field', { style: { display: hasEmail ? 'block' : 'none' } },
      el('label', { text: 'Your email (for receipt)' }),
      el('div.input', { style: { background: 'var(--surface-2)', padding: '12px 15px', borderRadius: 'var(--r-m)', fontWeight: 600, border: '1.5px dashed var(--line-dash)' }, text: myEmail || '' }),
    );

    const channelGroup = el('div.field', { style: { display: 'none' } },
      el('label', { text: 'How should we tell them? ' }),
      el('div.row', { style: { gap: '8px', flexWrap: 'wrap' } },
        chanBtn('email', '📧 Email'),
        chanBtn('sms', '📱 SMS'),
        chanBtn('both', '📧 + 📱 Both'),
      ),
    );
    function chanBtn(val, label) {
      const id = 'ch-' + val;
      const wrap = el('label', { for: id, style: { display: 'inline-flex', alignItems: 'center', gap: '6px', padding: '8px 14px', border: '1.5px dashed var(--line-dash)', borderRadius: 'var(--r-m)', cursor: 'pointer', fontWeight: 700, fontSize: '0.85rem', background: 'var(--surface-1)' } },
        el('input', { type: 'radio', name: 'gift-channel', id, value: val, style: { accentColor: 'var(--stamp)' } }),
        el('span', { text: label }),
      );
      wrap.querySelector('input').addEventListener('change', () => updateChannel());
      return wrap;
    }
    function selectedChannel() {
      const checked = channelGroup.querySelector('input[type="radio"]:checked');
      return checked ? checked.value : 'email';
    }
    function updateChannel() {
      const ch = selectedChannel();
      const phoneWrap = recipientGroup.querySelector('.gift-phone-wrap');
      const msgHint = recipientGroup.querySelector('.gift-msg-hint');
      if (phoneWrap) phoneWrap.style.display = (ch === 'sms' || ch === 'both') ? 'block' : 'none';
      if (msgHint) msgHint.textContent = ch === 'sms'
        ? 'SMS body is capped at 160 chars (one segment).'
        : ch === 'both'
          ? 'Email: up to 2000 chars · SMS: capped at 160 chars.'
          : 'Email: up to 2000 chars.';
    }

    const recipientGroup = el('div', { style: { display: 'none' } },
      el('div.field', {},
        el('label', { text: "Recipient's email — the gift card code goes here" }),
        el('input.input', { type: 'email', placeholder: 'friend@gmail.com', autocomplete: 'email', id: 'gift-email' }),
      ),
      el('div.field.gift-phone-wrap', { style: { display: 'none' } },
        el('label', { text: "Recipient's phone (for SMS, E.164 like +905551234567)" }),
        el('input.input', { type: 'tel', inputmode: 'tel', placeholder: '+90 555 123 45 67', autocomplete: 'tel', id: 'gift-phone' }),
      ),
      channelGroup,
      el('div.field', {},
        el('label', { text: 'Personal message' }),
        el('textarea.input', { placeholder: 'Happy birthday! 🎂', rows: 3, maxlength: 2000, style: { minHeight: '80px', resize: 'vertical' }, id: 'gift-msg' }),
        el('div.small.muted.gift-msg-hint', { text: 'Email: up to 2000 chars · SMS: capped at 160 chars.' }),
      ),
      el('div.alert.info', { style: { fontSize: '0.78rem', padding: '10px 12px', marginTop: '10px', marginBottom: '12px' } },
        icon('info', 14),
        el('div', { text: 'The gift card code itself is delivered by CryptoRefills — the email/SMS we send is just a "you got a gift!" heads-up with your personal note.' }),
      ),
    );

    const error = el('div', { style: { minHeight: '0px' } });
    const btn = el('button.btn.btn-gold.btn-block.btn-lg', { text: 'Continue' });
    const submit = () => {
      let recipientEmail;
      if (checkbox.checked) {
        recipientEmail = recipientGroup.querySelector('#gift-email').value.trim();
        if (!isValidEmail(recipientEmail)) {
          replaceChildren(error, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: emailError(recipientEmail) || 'Enter a valid recipient email' })));
          recipientGroup.querySelector('#gift-email').focus();
          return;
        }
      } else {
        if (!myEmail || !isValidEmail(myEmail)) {
          replaceChildren(error, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: 'Please enter your email first' })));
          return;
        }
        recipientEmail = myEmail;
      }
      const ch = checkbox.checked ? selectedChannel() : '';
      const giftMsg = checkbox.checked ? recipientGroup.querySelector('#gift-msg').value.trim() : '';
      const giftPhone = checkbox.checked ? recipientGroup.querySelector('#gift-phone').value.trim() : '';
      if (ch === 'sms' || ch === 'both') {
        const p = giftPhone.replace(/\s+/g, '');
        if (!/^\+[1-9]\d{6,14}$/.test(p)) {
          replaceChildren(error, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: 'Enter a valid phone in E.164 format (+countrycode…) for SMS.' })));
          recipientGroup.querySelector('#gift-phone').focus();
          return;
        }
      }
      try {
        if (ch) localStorage.setItem('nimshop_gift_channel', ch); else localStorage.removeItem('nimshop_gift_channel');
        if (giftMsg) localStorage.setItem('nimshop_gift_msg', giftMsg); else localStorage.removeItem('nimshop_gift_msg');
        if (giftPhone) localStorage.setItem('nimshop_gift_phone', giftPhone); else localStorage.removeItem('nimshop_gift_phone');
      } catch {}
      resolve(recipientEmail);
    };

    const headerTitle = el('div.strong', { text: title });
    const headerSub = el('div.small.muted.mt-1', { text: sub });
    checkbox.addEventListener('change', () => {
      const on = checkbox.checked;
      recipientGroup.style.display = on ? 'block' : 'none';
      ownWrap.style.display = on ? 'none' : 'block';
      headerTitle.textContent = on ? giftTitle : title;
      headerSub.textContent = on ? giftSub : sub;
      if (on) {
        const ge = recipientGroup.querySelector('#gift-email');
        if (ge && ge.value === myEmail) ge.value = '';
        const def = channelGroup.querySelector('input[value="email"]');
        if (def && !channelGroup.querySelector('input[type="radio"]:checked')) def.checked = true;
        updateChannel();
        setTimeout(() => ge.focus(), 100);
      }
    });

    const ownWrap = el('div', {},
      el('div.mt-2.mb-1', {}, headerTitle, headerSub, savedHint ? el('div.xs.faint.mt-1', { text: savedHint }) : null),
      (allowEdit || !hasEmail)
        ? el('div.field', {},
            el('label', { text: 'Your email (for receipt and delivery info)' }),
            el('input.input', { type: 'email', placeholder: 'you@gmail.com', autocomplete: 'email', id: 'your-email-input' }),
          )
        : yourEmailDisplay,
    );
    replaceChildren(body, ownWrap, gt.node, recipientGroup, el('div.error-slot', {}, error), btn);

    const yi = body.querySelector('#your-email-input');
    if (yi) {
      if (hasEmail && allowEdit) yi.value = myEmail;
      yi.addEventListener('blur', () => { const v = yi.value.trim(); if (isValidEmail(v)) { setStoredEmail(v); myEmail = v; } });
    }
    btn.addEventListener('click', () => {
      const yi2 = body.querySelector('#your-email-input');
      if (yi2) { const v = yi2.value.trim(); if (isValidEmail(v)) { setStoredEmail(v); myEmail = v; } }
      submit();
    });
    if (yi) yi.addEventListener('keydown', (e) => { if (e.key === 'Enter') btn.click(); });
    const geInput = recipientGroup.querySelector('#gift-email');
    if (geInput) geInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') btn.click(); });
    const gpInput = recipientGroup.querySelector('#gift-phone');
    if (gpInput) gpInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') btn.click(); });
    const gmInput = recipientGroup.querySelector('#gift-msg');
    if (gmInput) gmInput.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) btn.click(); });
    setTimeout(() => { (yi || recipientGroup.querySelector('#gift-email'))?.focus(); }, 200);
  });
}

/* One-product delivery step (product page + home quick-buy). Always resolves
 * to the SAME shape as the top-up step: { email, phone }. */
export async function askSingleDelivery(body, p) {
  if (p && p.type === 'phone_refill') return askTopUpStep(body, p);
  const isEsim = !!(p && p.type === 'esim');
  const email = await askEmailStep(body, {
    allowEdit: true,
    title: isEsim ? 'eSIM delivery' : 'Delivery email',
    sub: isEsim
      ? 'Your eSIM QR code and activation details are emailed to this address.'
      : 'Where should we deliver your gift card code?',
    savedHint: 'Saved from your last order — edit if this is a gift or a new address.',
  });
  return { email: email || '', phone: '' };
}

/* Cart checkout delivery steps. Collects ONE email for the email-delivered
 * items (gift cards / eSIMs) and a phone number for every top-up item —
 * reusing the exact same steps as the product page, so the cart can never
 * behave differently. Resolves to { email, phones } where phones is a
 * Map(item -> E.164 phone), or null when the user backs out. */
export async function askCartCheckout(body, items) {
  const tops = (items || []).filter((it) => it.type === 'phone_refill');
  const cards = (items || []).filter((it) => it.type !== 'phone_refill');
  if (!cards.length && !tops.length) return null;
  let email = '';
  const phones = new Map();
  if (cards.length) {
    const saved = getStoredEmail();
    email = await askEmailStep(body, {
      allowEdit: !saved,
      prefill: saved,
      title: 'Delivery email',
      sub: 'Where should we deliver your gift card codes? Instant email delivery.',
    });
    if (!email) return null;
  }
  if (tops.length) {
    // A single top-up in an otherwise empty cart gets the FULL product-page
    // step (gift toggle included) — identical to buying it directly.
    if (tops.length === 1 && cards.length === 0) {
      const r = await askTopUpStep(body, tops[0]);
      if (!r) return null;
      email = r.email || '';
      phones.set(tops[0], r.phone);
    } else {
      // Multiple / mixed cart: each top-up number is collected with the same
      // standalone step (the gift toggle lives on the email side for the
      // card items; top-ups in a mixed cart are plain self top-ups).
      for (const it of tops) {
        const ph = await askTopUpPhone(body, { country: it.country });
        if (!ph) return null;
        phones.set(it, ph);
      }
    }
  }
  return { email, phones };
}

/* Build the /api/quotes request for ONE item from the shared delivery info.
 * Gift extras come from the nimshop_gift_* localStorage keys written by the
 * steps above — one payload shape for product page, home and cart. */
export function buildOrderRequest(item, { email, phone, gift = false } = {}) {
  const isTopUp = item.type === 'phone_refill';
  const req = {
    product_id: item.id,
    quantity: item.qty || 1,
    country: item.country,
    email: '', // TOP-UP RULE: top-ups never carry a real email (not asked, not
    // reused, no fallback). The card/eSIM branch below sets it to the
    // delivery email; the top-up branch keeps it '' unless the buyer typed
    // a RECIPIENT email for a gift note sent by email/both.
    denomination: item.denomination || '',
    product_value: item.value || 0,
  };
  let ch = '', gMsg = '', gPhone = '';
  try {
    ch = (localStorage.getItem('nimshop_gift_channel') || '').trim();
    gMsg = (localStorage.getItem('nimshop_gift_msg') || '').trim();
    gPhone = (localStorage.getItem('nimshop_gift_phone') || '').trim();
  } catch {}
  // Gift extras from the shared nimshop_gift_* storage attach ONLY when the
  // caller flags this item as the gift purchase (`gift: true`). A plain
  // top-up sitting next to a gifted card in a mixed cart must never inherit
  // the card's gift note or its email.
  if (gift && ch) {
    req.gift_channel = ch;
    if (gMsg) req.gift_message = gMsg;
    if (gPhone && (ch === 'sms' || ch === 'both')) req.gift_recipient_phone = gPhone;
  }
  if (isTopUp) {
    req.phone_number = phone || '';
    // The single legitimate email on a top-up: the RECIPIENT email the buyer
    // entered for a gift note sent by email/both. Everything else stays out.
    if (gift && ch && (ch === 'email' || ch === 'both') && (email || '').trim()) {
      req.email = (email || '').trim();
    }
  } else {
    req.email = (email || '').trim();
  }
  return req;
}
