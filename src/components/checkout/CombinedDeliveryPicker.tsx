import { useState } from 'react';
import { isValidEmail, confirmMatches } from '../../lib/validate';
import { clearGiftExtras, setGiftExtras, setGiftIdenticon, collectPhoneValue } from '../../lib/delivery';
import { needsPhone } from '../../lib/catalog';
import { parseCountryPhone, phoneCountry } from '../../lib/phoneCountry';
import { CountryPhoneInput } from './CountryPhoneInput';
import { ConfirmField } from './ConfirmField';
import type { CartItem } from '../../lib/cartStore';
import { identiconPngDataUrl } from '../../lib/identicon';
import { getAddress } from '../../lib/session';
import { CashbackCodeField } from '../cashback/CashbackCodeField';
import { useGiftNote } from './DeliverySteps';
import type { DeliveryInfo } from '../../lib/delivery';
import { useT, t as tr } from '../../i18n';

function preloadGiftAvatar() {
  const addr = getAddress();
  if (!addr) return;
  identiconPngDataUrl(addr).then((png) => setGiftIdenticon(png)).catch(() => setGiftIdenticon(''));
}

/** National digits typed in a locked box → E.164 the supplier understands. */
function lockNumber(value: string, country?: string): { e164: string; error: string } {
  const v = String(value || '').trim();
  if (!v) return { e164: '', error: tr('delivery.enterPhone') };
  if (!phoneCountry(country)) return { e164: v, error: '' };
  const p = parseCountryPhone(v, country);
  if (p.error) return { e164: '', error: p.error };
  if (!p.e164) return { e164: '', error: tr('delivery.enterComplete') };
  return { e164: p.e164, error: '' };
}

export function CombinedDeliveryPicker({
  items = [],
  onDone,
  onBack,
}: {
  /** The cart lines being bought — decides which contact fields are shown. */
  items?: CartItem[];
  /** Site config: which rails are on (`enable_usdt`) and the USDT cashback
   *  rate shown on its card. */
  onDone: (info: DeliveryInfo) => void;
  onBack: () => void;
}) {
  // ROOT RULE: a line delivered by_phone needs a NUMBER, everything else an EMAIL.
  const tops = items.filter((it) => needsPhone(it));
  const cards = items.filter((it) => !needsPhone(it));
  const hasEmail = cards.length > 0;
  const hasTopUp = tops.length > 0;
  const { t } = useT();
  const api = useGiftNote({ hasEmail, hasTopUp });
  const { on: gift, setOn: setGift } = api;
  const [myEmail, setMyEmail] = useState('');
  const [giftEmail, setGiftEmail] = useState('');
  const [selfError, setSelfError] = useState('');
  const [phones, setPhones] = useState<string[]>(() => tops.map(() => ''));
  const [phoneErr, setPhoneErr] = useState<string[]>(() => tops.map(() => ''));
  // Second entries. A typo in the address the code goes to (or in the number a
  // top-up lands on) cannot be undone, so both are typed twice and the two
  // entries must agree before the order is created.
  const [myEmail2, setMyEmail2] = useState('');
  const [giftEmail2, setGiftEmail2] = useState('');
  const [noteEmail2, setNoteEmail2] = useState('');
  const [email2Err, setEmail2Err] = useState('');
  const [phones2, setPhones2] = useState<string[]>(() => tops.map(() => ''));
  const [phone2Err, setPhone2Err] = useState<string[]>(() => tops.map(() => ''));
  const [checking, setChecking] = useState(false);
  const setPhone = (i: number, v: string) => {
    setPhones((prev) => prev.map((p, idx) => (idx === i ? v : p)));
    setPhoneErr((prev) => prev.map((p, idx) => (idx === i ? '' : p)));
    setPhone2Err((prev) => prev.map((p, idx) => (idx === i ? '' : p)));
    setSelfError('');
  };
  const setPhone2 = (i: number, v: string) => {
    setPhones2((prev) => prev.map((p, idx) => (idx === i ? v : p)));
    setPhone2Err((prev) => prev.map((p, idx) => (idx === i ? '' : p)));
  };
  // TWO visible choices on purpose. A buyer whose coins are USDT should be able
  // to say so up front and settle from wherever that USDT lives — any Polygon
  // wallet, an exchange, or one tap inside Nimiq Pay — instead of being pushed
  // through a single rail. NIM runs through Nimiq Pay's Bitcoin Lightning
  // payment (`payLightningInvoice`, see "Bitcoin Lightning Payments in Mini
  // Apps"); USDT-on-Polygon is the shop's own rail with its own one-time
  // address. Both rails pay the SAME cashback rate, so both cards carry the
  // same cashback line.
  // Single payment rail: BTC Lightning (Nimiq Pay or any Lightning wallet).
  // The USDT option was removed by owner decision; legacy USDT quotes keep
  // their pay screen in CheckoutFlow, but no new USDT quote can be created.
  const [cashbackDest, setCashbackDest] = useState<'cashback' | 'burn'>('cashback');
  const [anonymous, setAnonymous] = useState(false);

  const submit = async () => {
    setSelfError('');
    api.setError('');
    setEmail2Err('');
    // ---- 1) phone numbers: one per phone-delivered line, typed twice ----
    const phoneMap = new Map<unknown, string>();
    for (let i = 0; i < tops.length; i++) {
      const raw = String(phones[i] || '').trim();
      const locked = lockNumber(raw, tops[i].country);
      if (locked.error) {
        setPhoneErr((prev) => prev.map((p, idx) => (idx === i ? locked.error : p)));
        return;
      }
      // The confirmation is compared on the digits the buyer typed, so a typo
      // shows up as a mismatch even before the number is normalised.
      if (!confirmMatches(raw, phones2[i], 'phone')) {
        setPhone2Err((prev) => prev.map((p, idx) => (idx === i ? t('delivery.confirmPhoneMismatch') : p)));
        return;
      }
      setChecking(true);
      const r = await collectPhoneValue(locked.e164, tops[i].country);
      setChecking(false);
      if (!r.ok) {
        setPhoneErr((prev) => prev.map((p, idx) => (idx === i ? (r.error || t('delivery.numberNotAccepted')) : p)));
        return;
      }
      phoneMap.set(tops[i], r.phone || '');
      if (tops[i].id) phoneMap.set(tops[i].id, r.phone || '');
    }
    // ---- 2) email: delivery address for emailed lines / note address for a gift ----
    let email = '';
    if (gift) {
      // emailed lines → the recipient's email IS the delivery target;
      // phone-only cart → the note still needs an inbox (api.noteEmail).
      const target = hasEmail ? giftEmail : api.noteEmail;
      const target2 = hasEmail ? giftEmail2 : noteEmail2;
      if (!confirmMatches(target, target2, 'email')) {
        setEmail2Err(t('delivery.confirmEmailMismatch'));
        return;
      }
      const r = api.check({ email: hasEmail ? giftEmail : '' });
      if (!r.ok) return;
      setGiftExtras('email', r.message);
      preloadGiftAvatar();
      email = r.email;
    } else if (hasEmail) {
      if (!isValidEmail(myEmail)) {
        setSelfError(t('delivery.cdpEnterValidEmail'));
        return;
      }
      if (!confirmMatches(myEmail, myEmail2, 'email')) {
        setEmail2Err(t('delivery.confirmEmailMismatch'));
        return;
      }
      clearGiftExtras();
      email = myEmail.trim();
    } else {
      // phone-only, not a gift: no email at all — the credit lands on the number.
      clearGiftExtras();
    }
    const info: DeliveryInfo = {
      email,
      phones: phoneMap,
      paymentMethod: 'nimiq_pay',
      cashbackDestination: cashbackDest,
      anonymous,
    } as any;
    onDone(info);
  };

  return (
    <div>
      {/* Stage 2 combined header */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', justifyContent: 'center', margin: '6px 0 12px' }}>
        <b style={{ width: 22, height: 22, borderRadius: 999, display: 'grid', placeItems: 'center', fontSize: 11, border: '2px solid var(--line-strong)', background: 'var(--surface-1)' }}>1</b>
        <i style={{ width: 28, height: 2, background: 'var(--line-soft)', display: 'block' }} />
        <b style={{ width: 22, height: 22, borderRadius: 999, display: 'grid', placeItems: 'center', fontSize: 11, border: '2px solid var(--stamp)', background: 'var(--stamp)', color: 'var(--on-stamp)' }}>2</b>
        <i style={{ width: 28, height: 2, background: 'var(--line-soft)', display: 'block' }} />
        <b style={{ width: 22, height: 22, borderRadius: 999, display: 'grid', placeItems: 'center', fontSize: 11, border: '2px solid var(--line-strong)', background: 'var(--surface-1)' }}>3</b>
        <span style={{ fontSize: 11, color: 'var(--ink-dim)', marginLeft: 6, fontWeight: 700 }}>{t('delivery.cdpOneStep')}</span>
      </div>

      <div style={{ fontSize: 11, letterSpacing: '.12em', fontWeight: 900, textTransform: 'uppercase', color: 'var(--ink-dim)' }}>{t('delivery.cdpStep2')}</div>
      <div style={{ fontFamily: 'Fraunces, serif', fontSize: '1.2rem', fontWeight: 900, margin: '4px 0 8px' }}>{t('delivery.cdpTitle')}</div>
      <div className="small muted" style={{ marginBottom: 12 }}>
        {hasTopUp && hasEmail
          ? t('delivery.cdpSubMixed')
          : hasTopUp
            ? t('delivery.cdpSubTopup')
            : t('delivery.cdpSubCards')}
      </div>

      {/* Gift toggle — improved card with emoji */}
      <label
        className={`gift-toggle gift-toggle--best ${gift ? 'is-on' : ''}`}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          cursor: 'pointer',
          padding: '12px 14px',
          border: gift ? '2px solid var(--line-strong)' : '1.5px dashed var(--line-mid)',
          borderRadius: 10,
          background: gift ? 'var(--paper-tint)' : 'var(--surface-1)',
          boxShadow: gift ? '2px 2px 0 rgba(78,61,40,.12)' : 'none',
          transition: '.15s',
          marginBottom: 12,
        }}
      >
        <input type="checkbox" checked={gift} onChange={(e) => { setGift(e.target.checked); setSelfError(''); api.setError(''); setEmail2Err(''); setNoteEmail2(''); setGiftEmail2(''); }} style={{ accentColor: 'var(--stamp)', width: 18, height: 18, flex: 'none' }} />
        <span style={{ width: 32, height: 32, borderRadius: 999, background: gift ? 'var(--stamp)' : 'var(--surface-2)', border: '1.5px solid var(--line-strong)', display: 'grid', placeItems: 'center', flex: 'none', fontSize: 14 }}>🎁</span>
        <span style={{ minWidth: 0 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 900, fontSize: 13 }}>
            {t('delivery.sendAsGift')} {gift && <span style={{ fontSize: 9, fontWeight: 900, letterSpacing: '.08em', textTransform: 'uppercase', background: 'var(--stamp)', color: 'var(--on-stamp)', padding: '2px 6px', borderRadius: 999 }}>{t('delivery.on')}</span>}
          </span>
          <span style={{ fontSize: 11, color: 'var(--ink-dim)' }}>{hasEmail ? t('delivery.giftToggleSub') : t('delivery.cdpGiftSubNumber')}</span>
        </span>
      </label>

      {/* Phone number(s) — locked to the product's country (flag + dial code), buyer types national digits */}
      {tops.map((it, i) => {
        const id = `cdp-phone-${i}`;
        const label = tops.length > 1
          ? gift ? t('delivery.cdpNumberGift', { name: it.name }) : t('delivery.cdpNumberSelf', { name: it.name })
          : gift ? t('delivery.cdpRecipientNumber') : t('delivery.cdpPhoneCredit');
        return (
          <div className="field" key={id} style={{ marginBottom: 12 }}>
            <label htmlFor={id} style={{ fontSize: 12, fontWeight: 800, color: 'var(--ink-dim)' }}>{label}</label>
            <CountryPhoneInput
              id={id}
              country={it.country}
              value={phones[i] || ''}
              invalid={!!phoneErr[i]}
              autoFocus={i === 0}
              onChange={(v) => setPhone(i, v)}
              onEnter={() => { void submit(); }}
            />
            {phoneErr[i] && <div className="small" style={{ color: 'var(--stamp)', marginTop: 4 }}>{phoneErr[i]}</div>}
            <ConfirmField
              id={`${id}-again`}
              label={t('delivery.confirmPhoneLabel')}
              type="tel"
              inputMode="tel"
              placeholder={t('delivery.confirmPhonePlaceholder')}
              value={phones2[i] || ''}
              invalid={!!phone2Err[i]}
              error={phone2Err[i]}
              onChange={(v) => setPhone2(i, v)}
              onEnter={() => { void submit(); }}
            />
          </div>
        );
      })}

      {gift ? (
        <div style={{ display: 'grid', gap: 10, marginBottom: 14 }}>
          {hasEmail ? (
            <div className="field">
              <label style={{ fontSize: 12, fontWeight: 800, color: 'var(--ink-dim)' }}>{t('delivery.cdpRecipientCode')}</label>
              <input
                className="input"
                type="email"
                placeholder="friend@gmail.com"
                value={giftEmail}
                onChange={(e) => { setGiftEmail(e.target.value); api.setError(''); setEmail2Err(''); }}
                style={{ padding: '12px 14px', border: '2px solid var(--line-strong)', borderRadius: 8, background: 'var(--surface-1)', width: '100%' }}
              />
              <ConfirmField
                id="cdp-gift-email-again"
                label={t('delivery.confirmEmailLabel')}
                type="email"
                inputMode="email"
                placeholder="friend@gmail.com"
                value={giftEmail2}
                invalid={!!email2Err}
                error={email2Err}
                onChange={(v) => { setGiftEmail2(v); setEmail2Err(''); }}
              />
            </div>
          ) : (
            <div className="field">
              <label style={{ fontSize: 12, fontWeight: 800, color: 'var(--ink-dim)' }}>{t('delivery.cdpRecipientNoteEmail')}</label>
              <input
                className="input"
                type="email"
                placeholder="friend@gmail.com"
                value={api.noteEmail}
                onChange={(e) => { api.setNoteEmail(e.target.value); api.setError(''); setEmail2Err(''); }}
                style={{ padding: '12px 14px', border: '2px solid var(--line-strong)', borderRadius: 8, background: 'var(--surface-1)', width: '100%' }}
              />
              <ConfirmField
                id="cdp-note-email-again"
                label={t('delivery.confirmEmailLabel')}
                type="email"
                inputMode="email"
                placeholder="friend@gmail.com"
                value={noteEmail2}
                invalid={!!email2Err}
                error={email2Err}
                onChange={(v) => { setNoteEmail2(v); setEmail2Err(''); }}
              />
            </div>
          )}
          <div>
            <label style={{ fontSize: 12, fontWeight: 800, color: 'var(--ink-dim)' }}>{t('delivery.cdpGiftMessage')}</label>
            <textarea
              placeholder={t('delivery.cdpGiftMessagePh')}
              value={api.message}
              onChange={(e) => api.setMessage(e.target.value)}
              rows={2}
              style={{ width: '100%', padding: '10px 12px', border: '1.5px solid var(--line-mid)', borderRadius: 8, background: 'var(--surface-1)', resize: 'vertical', fontFamily: 'inherit' }}
            />
            {api.error && <div className="small" style={{ color: 'var(--stamp)', marginTop: 4 }}>{api.error}</div>}
          </div>
        </div>
      ) : hasEmail ? (
        <div className="field" style={{ marginBottom: 14 }}>
          <label style={{ fontSize: 12, fontWeight: 800, color: 'var(--ink-dim)' }}>{t('delivery.yourEmailReceipt')}</label>
          <input
            className="input"
            type="email"
            placeholder="you@gmail.com"
            value={myEmail}
            onChange={(e) => { setMyEmail(e.target.value); setSelfError(''); setEmail2Err(''); }}
            style={{ padding: '12px 14px', border: '2px solid var(--line-strong)', borderRadius: 8, background: 'var(--surface-1)', width: '100%' }}
          />
          <ConfirmField
            id="cdp-my-email-again"
            label={t('delivery.confirmEmailLabel')}
            type="email"
            inputMode="email"
            placeholder="you@gmail.com"
            value={myEmail2}
            invalid={!!email2Err}
            error={email2Err}
            hint={t('delivery.confirmHint')}
            onChange={(v) => { setMyEmail2(v); setEmail2Err(''); }}
            onEnter={() => { void submit(); }}
          />
          <div className="small muted" style={{ marginTop: 6, display: 'flex', gap: 6, alignItems: 'flex-start', background: 'var(--paper-tint)', border: '1px dashed rgba(78,61,40,.25)', borderRadius: 8, padding: '8px 10px' }}>
            <span>⚠️</span><span>{t('delivery.cdpCodeToEmail')}</span>
          </div>
          {selfError && <div className="small" style={{ color: 'var(--stamp)', marginTop: 6 }}>{selfError}</div>}
        </div>
      ) : null}


      {/* Cashback destination */}
      <div style={{ fontSize: 12, fontWeight: 900, color: 'var(--ink)', marginBottom: 6 }}>{t('checkout.flowWhereCashback')}</div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
        <label
          onClick={() => setCashbackDest('cashback')}
          style={{
            flex: 1,
            padding: '10px',
            border: cashbackDest === 'cashback' ? '2px solid var(--line-strong)' : '1.5px dashed var(--line-mid)',
            borderRadius: 9,
            background: cashbackDest === 'cashback' ? 'var(--paper-tint)' : 'var(--surface-1)',
            cursor: 'pointer',
            display: 'flex',
            gap: 6,
            alignItems: 'center',
            fontWeight: 800,
            fontSize: 12,
          }}
        >
          <input type="radio" checked={cashbackDest === 'cashback'} onChange={() => setCashbackDest('cashback')} style={{ accentColor: 'var(--stamp)' }} /> {t('delivery.cdpMyWallet')}
        </label>
        <label
          onClick={() => setCashbackDest('burn')}
          style={{
            flex: 1,
            padding: '10px',
            border: cashbackDest === 'burn' ? '2px solid var(--line-strong)' : '1.5px dashed var(--line-mid)',
            borderRadius: 9,
            background: cashbackDest === 'burn' ? 'var(--paper-tint)' : 'var(--surface-1)',
            cursor: 'pointer',
            display: 'flex',
            gap: 6,
            alignItems: 'center',
            fontWeight: 800,
            fontSize: 12,
          }}
        >
          <input type="radio" checked={cashbackDest === 'burn'} onChange={() => setCashbackDest('burn')} style={{ accentColor: 'var(--stamp)' }} /> {t('checkout.flowBurnNim')}
        </label>
      </div>

      <label style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '10px', border: anonymous ? '2px solid var(--line-strong)' : '1.5px dashed var(--line-mid)', borderRadius: 8, background: anonymous ? 'var(--paper-tint)' : 'var(--surface-1)', cursor: 'pointer', fontSize: 12, fontWeight: 700, marginBottom: 12 }}>
        <input type="checkbox" checked={anonymous} onChange={(e) => setAnonymous(e.target.checked)} style={{ accentColor: 'var(--stamp)' }} /> {t('delivery.cdpPrivate')}
      </label>

      <CashbackCodeField compact />

      <div style={{ display: 'flex', gap: 10, marginTop: 14 }}>
        <button className="btn btn-ghost" onClick={onBack} style={{ flex: '0 0 auto', padding: '12px 16px', border: '1.5px solid var(--line-strong)', borderRadius: 999, background: 'var(--surface-1)', fontWeight: 900 }}>
          {t('actions.back')}
        </button>
        <button className="btn btn-gold" onClick={() => { void submit(); }} disabled={checking} style={{ flex: 1, padding: '12px 16px', border: '2px solid var(--line-strong)', borderRadius: 999, background: 'var(--stamp)', color: 'var(--on-stamp)', fontWeight: 900, boxShadow: '2px 2px 0 rgba(78,61,40,.18)' }}>
          {checking ? t('delivery.cdpCheckingNumber') : t('delivery.cdpContinueToPayment')}
        </button>
      </div>
    </div>
  );
}
