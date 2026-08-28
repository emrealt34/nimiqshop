/* pages/admin.js — operator console for direct email + SMS notifications.
 *
 * Real flow (no fake UI):
 *   1. Login screen → POST /api/admin/auth/login with username + password +
 *      6-digit TOTP code. Backend sets HttpOnly nimshop_admin_session cookie.
 *   2. On success, GET /api/admin/notification/status shows live channel
 *      config (SMTP host, from addr, Pingram URL, dry-run flags).
 *   3. Compose form → POST /api/admin/notification/send. Backend
 *      dispatches via SMTP / HTTP, audit-logs the operator id + IP + body
 *      lengths, returns per-leg status (sent / dry_run / failed: …).
 *   4. Result block shows: which leg sent, which dry-ran, which failed.
 *
 * The "Dry run" checkbox forces a log-only send even when the channel is
 * globally enabled — operators can verify the providers before spending a
 * real SMS credit on a customer.
 */
import { bootShell, navigate } from '../shell.js';
import { el, icon, $, replaceChildren, fmtNIM } from '../util.js';
import { adminLogin, adminLogout, adminStatus, adminMe, adminSend, adminCatalogRules } from '../api.js';
import { toast, openSheet, closeSheet, copyButton, alertBox } from '../ui.js';

bootShell('');

const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on
// top of the first (the duplicate locked-card bug).
replaceChildren(main);

/* ---------------- Login sheet ---------------- */

function openAdminLogin(onSuccess) {
  const { body } = openSheet({ title: 'Admin login', wide: false });
  let busy = false;
  const errBox = el('div');
  const userEl = el('input.input', { type: 'text', placeholder: 'admin', autocomplete: 'username', id: 'admin-u' });
  const passEl = el('input.input', { type: 'password', placeholder: 'Password', autocomplete: 'current-password', id: 'admin-p' });
  const totpEl = el('input.input', { type: 'text', inputmode: 'numeric', pattern: '[0-9]*', placeholder: '6-digit code — leave empty if unused', maxlength: 6, autocomplete: 'one-time-code', id: 'admin-t' });
  const btn = el('button.btn.btn-gold.btn-block.btn-lg', { text: 'Sign in' });
  const submit = async () => {
    if (busy) return;
    const username = userEl.value.trim();
    const password = passEl.value;
    const totp = totpEl.value.trim();
    if (!username || !password || (totp && !/^[0-9]{6}$/.test(totp))) {
      replaceChildren(errBox, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: 'Username and password are required. (TOTP: 6 digits — only when your admin account uses it.)' })));
      return;
    }
    busy = true; btn.disabled = true; btn.textContent = 'Signing in…';
    try {
      const r = await adminLogin({ username, password, totp });
      replaceChildren(errBox);
      closeSheet();
      toast(`Signed in as ${r.admin?.username || 'admin'}`, 'success');
      onSuccess(r);
    } catch (e) {
      busy = false; btn.disabled = false; btn.textContent = 'Sign in';
      replaceChildren(errBox, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: e.message || 'Login failed' })));
    }
  };
  replaceChildren(body,
    el('div.mt-2.mb-1', {},
      el('div.strong', { text: 'Operator console' }),
      el('div.small.muted.mt-1', { text: 'Separate cookie session — your Nimiq wallet login is unrelated.' }),
    ),
    el('div.field', {}, el('label', { text: 'Username' }), userEl),
    el('div.field', {}, el('label', { text: 'Password' }), passEl),
    el('div.field', {}, el('label', { text: 'TOTP code (optional for env test logins)' }), totpEl),
    errBox,
    btn,
  );
  for (const inp of [userEl, passEl, totpEl]) {
    inp.addEventListener('keydown', (e) => { if (e.key === 'Enter') submit(); });
  }
  btn.addEventListener('click', submit);
  setTimeout(() => userEl.focus(), 100);
}

/* ---------------- Main page ---------------- */

function badge(label, on, detail) {
  const color = on ? 'var(--green)' : 'var(--red)';
  return el('span.chip', { style: { borderColor: color, color } },
    el('span', { style: { display: 'inline-block', width: '8px', height: '8px', borderRadius: '50%', background: color, marginRight: '6px' } }),
    el('span.strong', { text: label }),
    detail ? el('span.xs.faint', { text: ' · ' + detail }) : null,
  );
}

function statusCard(status) {
  const emailOn = status?.email?.enabled;
  const smsOn = status?.sms?.enabled;
  return el('div.card.mt-2', {},
    el('div.card-title', {}, icon('pulse', 14), el('span', { text: 'Channel status' })),
    el('div.row', { style: { gap: '12px', flexWrap: 'wrap' } },
      badge('Email ' + (emailOn ? 'READY' : 'OFF'), emailOn, status?.email?.host ? `${status.email.host} · from ${status.email.from || '?'}` : 'NOTIFY_EMAIL_ENABLED=false'),
      badge('SMS ' + (smsOn ? 'READY' : 'OFF'), smsOn, status?.sms?.url ? `${status.sms.url}${status.sms.sender ? ' · sender=' + status.sms.sender : ''}` : 'NOTIFY_SMS_ENABLED=false'),
    ),
    (status?.email?.dry_run || status?.sms?.dry_run)
      ? el('div.alert.warn.mt-2', { style: { marginBottom: 0 } }, icon('info', 16), el('div.small', { text: 'One or both channels are in DRY-RUN mode — sends are logged but not delivered. Toggle NOTIFY_*_DRY_RUN=false in .env to go live.' }))
      : null,
  );
}

function renderComposer(status) {
  // Channel selector (email / sms / both)
  const channelRow = el('div.row', { style: { gap: '8px', flexWrap: 'wrap' } });
  function chanRadio(val, label, emoji) {
    const id = 'notif-ch-' + val;
    const wrap = el('label', { for: id, style: { display: 'inline-flex', alignItems: 'center', gap: '6px', padding: '10px 14px', border: '2px solid var(--line-strong)', borderRadius: 'var(--r-s)', cursor: 'pointer', fontWeight: 800, fontSize: '0.92rem', background: 'var(--surface-1)' } },
      el('input', { type: 'radio', name: 'notif-channel', id, value: val, checked: val === 'email', style: { accentColor: 'var(--stamp)' } }),
      el('span', { text: `${emoji} ${label}` }),
    );
    return wrap;
  }
  channelRow.append(
    chanRadio('email', 'Email only', '📧'),
    chanRadio('sms', 'SMS only', '📱'),
    chanRadio('both', 'Email + SMS', '📧📱'),
  );
  function currentChannel() {
    const r = channelRow.querySelector('input[type="radio"]:checked');
    return r ? r.value : 'email';
  }

  // Recipient fields
  const emailInput = el('input.input', { type: 'email', placeholder: 'recipient@gmail.com', autocomplete: 'off', id: 'notif-email' });
  const phoneInput = el('input.input', { type: 'tel', inputmode: 'tel', placeholder: '+90 555 123 45 67 (E.164)', autocomplete: 'off', id: 'notif-phone' });
  const subjectInput = el('input.input', { type: 'text', placeholder: 'Email subject (ignored for SMS-only)', id: 'notif-subject' });
  const bodyInput = el('textarea.input', { id: 'notif-body', placeholder: 'Hi! Your order is ready. Best, nim.shop', rows: 6, maxlength: 2000, style: { minHeight: '120px', resize: 'vertical', lineHeight: 1.5 } });
  const dryRun = el('label', { style: { display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer', padding: '10px 0', fontWeight: 700 } },
    el('input', { type: 'checkbox', checked: true, style: { width: '18px', height: '18px', accentColor: 'var(--stamp)' } }),
    el('span.small', { text: 'Dry-run (log only, do NOT actually send — recommended when testing)' }),
  );

  // Live char counters
  const emailCharHint = el('div.xs.faint', { text: 'Email body: 0 / 2000' });
  const smsCharHint = el('div.xs.faint', { text: 'SMS body: 0 / 160' });
  bodyInput.addEventListener('input', () => {
    const s = bodyInput.value;
    emailCharHint.textContent = `Email body: ${s.length} / 2000`;
    smsCharHint.textContent = `SMS body: ${s.length} / 160`;
  });

  // Field visibility follows channel selection
  function syncFields() {
    const ch = currentChannel();
    emailInput.closest('.field').style.display = (ch === 'email' || ch === 'both') ? '' : 'none';
    phoneInput.closest('.field').style.display = (ch === 'sms' || ch === 'both') ? '' : 'none';
    subjectInput.closest('.field').style.display = (ch === 'sms') ? 'none' : '';
    smsCharHint.style.display = (ch === 'sms' || ch === 'both') ? '' : 'none';
  }
  channelRow.querySelectorAll('input[type="radio"]').forEach((r) => r.addEventListener('change', syncFields));
  syncFields();

  const errBox = el('div');
  const sendBtn = el('button.btn.btn-gold.btn-block.btn-lg.mt-2', {}, icon('send', 16), el('span.btn-label', { text: 'Send notification' }));
  const resultBox = el('div.mt-2');

  async function doSend() {
    errBox.textContent = '';
    resultBox.textContent = '';
    sendBtn.disabled = true;
    const origLabel = sendBtn.querySelector('.btn-label').textContent;
    sendBtn.querySelector('.btn-label').textContent = 'Sending…';
    try {
      const ch = currentChannel();
      const body = bodyInput.value.trim();
      if (!body) throw new Error('Message body cannot be empty');
      const req = {
        channel: ch,
        body,
        dry_run: dryRun.querySelector('input').checked,
        category: 'operator',
      };
      if (ch === 'email' || ch === 'both') req.to_email = emailInput.value.trim();
      if (ch === 'sms' || ch === 'both') req.to_phone = phoneInput.value.trim();
      if (subjectInput.value.trim()) req.subject = subjectInput.value.trim();
      const r = await adminSend(req);
      const color = (r.email === 'sent' || r.email === 'dry_run' || r.sms === 'sent' || r.sms === 'dry_run') ? 'success' : 'error';
      const tone = r.dry_run ? 'info' : (r.email === 'sent' || r.sms === 'sent') ? 'success' : 'error';
      toast(`Notification: email=${r.email} sms=${r.sms}`, tone);
      const rows = [];
      rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'Channel' }), el('div.strong', { text: r.channel })));
      rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'Email' }), el('div.strong', { text: String(r.email) })));
      rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'SMS' }), el('div.strong', { text: String(r.sms) })));
      rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'Dry-run' }), el('div.strong', { text: String(r.dry_run) })));
      if (r.to?.email) rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'To email' }), el('div.mono.small', { text: r.to.email })));
      if (r.to?.phone) rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'To phone' }), el('div.mono.small', { text: r.to.phone })));
      rows.push(el('div.row.between', {}, el('div.xs.faint', { text: 'Body chars' }), el('div.small', { text: `email=${r.body_chars?.email || 0} · sms=${r.body_chars?.sms || 0}` })));
      const summary = r.dry_run
        ? 'Dry-run completed — nothing was sent. Check the backend log for the recorded payload.'
        : (r.email === 'sent' || r.sms === 'sent')
          ? 'Notification delivered.'
          : 'No leg succeeded. See per-leg status below and the backend log.';
      const toneAlert = color === 'success' ? 'success' : (r.dry_run ? 'info' : 'error');
      replaceChildren(resultBox,
        el('div.alert.' + toneAlert, { style: { marginBottom: 0 } }, icon(color === 'success' ? 'check' : 'info', 18), el('div.small', { text: summary })),
        el('div.card.mt-2', { style: { padding: '14px 16px' } }, el('div.kv', {}, ...rows)),
      );
    } catch (e) {
      replaceChildren(errBox, el('div.alert.error.mt-1', { style: { marginBottom: 0 } }, icon('alert', 16), el('div.small', { text: e.message || 'Send failed' })));
    } finally {
      sendBtn.disabled = false;
      sendBtn.querySelector('.btn-label').textContent = origLabel;
    }
  }
  sendBtn.addEventListener('click', doSend);

  // Top-level help text
  const emailAvail = status?.email?.enabled;
  const smsAvail = status?.sms?.enabled;
  const helpLine = (emailAvail && smsAvail)
    ? 'Both channels are ready. Use "Dry-run" first to verify the body and recipient without spending a real SMS credit.'
    : (!emailAvail && !smsAvail)
      ? 'Neither channel is configured yet — set NOTIFY_EMAIL_ENABLED=true / NOTIFY_SMS_ENABLED=true in backend .env and restart. Dry-run still works (it logs locally).'
      : 'One channel is off. Toggle it on in backend .env if you need it.';

  return el('div.card.mt-2', {},
    el('div.card-title', {}, icon('send', 14), el('span', { text: 'Send direct notification' })),
    el('div.small.muted.mb-2', { text: helpLine }),
    el('div.field', {}, el('label', { text: 'Channel' }), channelRow),
    el('div.field', {}, el('label', { text: "Recipient email" }), emailInput),
    el('div.field', {}, el('label', { text: "Recipient phone (E.164, e.g. +905551234567)" }), phoneInput),
    el('div.field', {}, el('label', { text: 'Email subject' }), subjectInput),
    el('div.field', {}, el('label', { text: 'Message body' }), bodyInput, emailCharHint, smsCharHint),
    dryRun,
    errBox,
    sendBtn,
    resultBox,
  );
}

function renderSessionCard(me) {
  const isLive = !!me;
  return el('div.card', { style: { marginTop: '0' } },
    el('div.row.between', { style: { flexWrap: 'wrap', gap: '10px' } },
      el('div', {},
        el('div.card-title', {}, icon('shield', 14), el('span', { text: 'Admin session' })),
        isLive
          ? el('div', {},
              el('div.strong', { text: me.admin?.username || 'admin' }),
              el('div.xs.faint', { text: `Session expires: ${me.expires_at ? new Date(me.expires_at).toLocaleString() : '—'}` }),
            )
          : el('div.small.muted', { text: 'Not signed in. Sign in below to send notifications.' }),
      ),
      isLive
        ? el('button.btn.btn-ghost.btn-sm', { on: { click: async () => {
            try { await adminLogout(); }
            catch {} finally { toast('Admin session ended', 'info'); renderLoggedOut(); }
          } } }, icon('logout', 14), el('span.btn-label', { text: 'Sign out' }))
        : null,
    ),
    !isLive
      ? el('button.btn.btn-gold.mt-2', { on: { click: () => openAdminLogin(() => renderAdmin()) } }, icon('shield', 18), el('span.btn-label', { text: 'Sign in to admin console' }))
      : null,
  );
}

async function renderLoggedOut() {
  while (main.firstChild) main.removeChild(main.firstChild);
  main.appendChild(el('div.container', {},
    el('div.row.between.mt-2', { style: { flexWrap: 'wrap', gap: '12px', alignItems: 'center' } },
      el('div', {},
        el('h1', { style: { margin: 0, display: 'flex', alignItems: 'center', gap: '10px' } }, icon('shield', 24), 'Operator console'),
        el('div.xs.faint.mt-1', { text: 'Direct email + SMS to any recipient. Separate from the customer wallet login.' }),
      ),
      el('a.btn.btn-ghost.btn-sm', { href: '/' }, icon('back', 14), el('span.btn-label', { text: 'Back to shop' })),
    ),
    renderSessionCard(null),
  ));
  try {
    const status = await adminStatus();
    main.appendChild(statusCard(status));
    main.appendChild(renderComposer(status));
  } catch (e) {
    main.appendChild(el('div.alert.error.mt-2', {}, icon('alert', 16), el('div.small', { text: 'Could not load channel status: ' + (e.message || e) })));
  }
  main.appendChild(await renderCatalogRulesPanel());
}

async function renderAdmin() {
  let me = null;
  try { me = await adminMe(); } catch {}
  if (!me) return renderLoggedOut();
  while (main.firstChild) main.removeChild(main.firstChild);
  main.appendChild(el('div.container', {},
    el('div.row.between.mt-2', { style: { flexWrap: 'wrap', gap: '12px', alignItems: 'center' } },
      el('div', {},
        el('h1', { style: { margin: 0, display: 'flex', alignItems: 'center', gap: '10px' } }, icon('shield', 24), 'Operator console'),
        el('div.xs.faint.mt-1', { text: 'Direct email + SMS to any recipient. Audit-logged.' }),
      ),
      el('a.btn.btn-ghost.btn-sm', { href: '/' }, icon('back', 14), el('span.btn-label', { text: 'Back to shop' })),
    ),
    renderSessionCard(me),
  ));
  try {
    const status = await adminStatus();
    main.appendChild(statusCard(status));
    main.appendChild(renderComposer(status));
  } catch (e) {
    main.appendChild(el('div.alert.error.mt-2', {}, icon('alert', 16), el('div.small', { text: 'Could not load channel status: ' + (e.message || e) })));
  }
}

/* ---------------- Catalog rules (price cap + visibility) ----------------
 * The USD price cap applies to EVERY country in its own unit: setting 20
 * hides a "500.000 IDR" card (≈$33) but keeps "150.000 IDR" (≈$10) — the
 * backend converts with the curated FX table (same rates the frontend
 * estimates use). Ranges are clamped, over-cap fixed SKUs are never sent
 * to the browser at all.
 */
async function renderCatalogRulesPanel() {
  const card = el('div.card.mt-2', {}, el('div.card-title', {}, icon('lock', 18), el('span', { text: 'Catalog rules — price cap & visibility' })), el('div.small.muted', { text: 'Loading…' }));
  let rules;
  try { rules = await adminCatalogRules(); } catch (e) {
    replaceChildren(card.children[1] || card, el('div.alert.error', {}, el('div.small', { text: 'Could not load catalog rules: ' + (e.message || e) })));
    return card;
  }
  const capIn = el('input.input', { type: 'number', min: '0', step: '1', value: rules.max_face_value_usd > 0 ? String(rules.max_face_value_usd) : '', placeholder: '0 = off' });
  const hiddenFam = el('textarea.input', { rows: '3', placeholder: 'One brand per line' });
  hiddenFam.value = (rules.hidden_families || []).join('\n');
  const bannedCat = el('input.input', { value: (rules.banned_categories || []).join(', '), placeholder: 'e.g. gambling, e-money' });
  const bannedKind = el('input.input', { value: (rules.banned_kinds || []).join(', '), placeholder: 'e.g. giftcard' });
  const hiddenCC = el('input.input', { value: (rules.hidden_countries || []).join(', '), placeholder: 'e.g. RU, KP' });
  const visibleCC = el('input.input', { value: (rules.visible_countries || []).join(', '), placeholder: 'Leave empty = all countries visible' });
  const oosSel = el('select.input', {},
    el('option', { value: 'show', selected: (rules.out_of_stock_policy || 'show') !== 'hide', text: 'Show out-of-stock (flagged)' }),
    el('option', { value: 'hide', selected: rules.out_of_stock_policy === 'hide', text: 'Hide out-of-stock' }),
  );
  const err = el('div');
  const splitList = (v) => v.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
  const save = async () => {
    const payload = {
      max_face_value_usd: Math.max(0, parseFloat(capIn.value) || 0),
      hidden_families: splitList(hiddenFam.value),
      banned_categories: splitList(bannedCat.value),
      banned_kinds: splitList(bannedKind.value).map((x) => x.toLowerCase()),
      hidden_countries: splitList(hiddenCC.value).map((x) => x.toUpperCase()),
      visible_countries: splitList(visibleCC.value).map((x) => x.toUpperCase()),
      out_of_stock_policy: oosSel.value === 'hide' ? 'hide' : 'show',
    };
    try {
      const saved = await adminCatalogRules({ method: 'PUT', body: payload });
      toast('Catalog rules saved (cap ' + (saved.max_face_value_usd > 0 ? '$' + saved.max_face_value_usd : 'off') + ')', 'success');
    } catch (e) {
      replaceChildren(err, el('div.alert.error.mt-1', {}, el('div.small', { text: e.message || 'Save failed' })));
    }
  };
  const saveBtn = el('button.btn.btn-gold.btn-block.btn-lg.mt-2', { text: 'Save catalog rules' });
  saveBtn.addEventListener('click', save);
  replaceChildren(card.children[1] || card,
    el('div.field', {}, el('label', { text: 'Price cap — USD per order (applies in every country\'s own unit)' }), capIn,
      el('div.xs.faint.mt-1', { text: 'Example: 20 → a "150.000 IDR" card (≈$10) stays visible, "500.000 IDR" (≈$33) is hidden — conversion is automatic for all 160+ currencies. 0 disables the cap.' })),
    el('div.field.mt-1', {}, el('label', { text: 'Hidden brands (one per line)' }), hiddenFam),
    el('div.row.mt-1', { style: { gap: '10px', flexWrap: 'wrap' } },
      el('div.field', { style: { flex: '1', minWidth: '200px' } }, el('label', { text: 'Banned categories' }), bannedCat),
      el('div.field', { style: { flex: '1', minWidth: '200px' } }, el('label', { text: 'Banned kinds' }), bannedKind),
    ),
    el('div.row.mt-1', { style: { gap: '10px', flexWrap: 'wrap' } },
      el('div.field', { style: { flex: '1', minWidth: '200px' } }, el('label', { text: 'Hidden countries' }), hiddenCC),
      el('div.field', { style: { flex: '1', minWidth: '200px' } }, el('label', { text: 'Only these countries (allow list)' }), visibleCC),
    ),
    el('div.field.mt-1', {}, el('label', { text: 'Out-of-stock brands' }), oosSel),
    err,
    saveBtn,
  );
  return card;
}

renderAdmin();
