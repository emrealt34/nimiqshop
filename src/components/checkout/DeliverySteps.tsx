/**
 * DeliverySteps.tsx — React port of delivery.js askEmailStep / askTopUpStep /
 * askTopUpPhone. These are the shared "delivery" steps used by the product
 * page and the cart checkout. Each resolves via onDone(payload).
 *
 * LAYOUT RULE (gift fix): the "Send as gift" toggle must NEVER move when it is
 * clicked. It used to sit *below* the own-email / own-number field, and that
 * field was unmounted on check — so the toggle jumped ~100px up out from under
 * the pointer. Two rules now keep it pinned:
 *   1. the toggle is rendered ABOVE the fields (only content below it swaps),
 *   2. the heading uses <HeadSwap>, which reserves the taller of the two
 *      copies so a longer/shorter sentence can't re-wrap and nudge it either.
 * Content that a control reveals appears BELOW it, never above — the reason the
 * gift block has no channel picker anymore is not only that there is one
 * channel (email, see lib/giftNote), it is that every "reveal a field when you
 * choose X" pattern is a control that moves.
 *
 * GIFT RULE (this file's other half): what the gift NOTE asks for comes from
 * <GiftNoteFields> + lib/giftNote, from the cart's own delivery route — never
 * per screen. The note is emailed, so a cart whose lines are emailed reuses the
 * recipient address it already asks for (one email field, not two) and a
 * top-up cart adds exactly one address field for the note.
 *
 * NUMBER RULE: a top-up is credited onto a number IN THE PRODUCT'S COUNTRY, so
 * every number field here is a <CountryPhoneInput>: the flag and the +dial code
 * come from the product, the buyer types the national digits. Nothing here
 * decides whether the number EXISTS — collectPhoneValue (supplier live lookup)
 * and phone.Normalize on the server do. This only makes a wrong country
 * impossible to type.
 */
import { useState } from 'react';
import { Icon } from '../ui/Icon';
import { isValidEmail, emailError } from '../../lib/validate';
import { collectPhoneValue, setGiftExtras, setGiftIdenticon, clearGiftExtras } from '../../lib/delivery';
import { identiconPngDataUrl } from '../../lib/identicon';
import { getAddress } from '../../lib/session';
import { useT, t as tr } from '../../i18n';

/**
 * When a gift note goes on, pre-rasterize the BUYER's own identicon (the same
 * @nimiq/identicons face the site shows) into a PNG for the gift email. Fire
 * and forget: by checkout time it is cached in lib/delivery's gift extras,
 * and a failure just leaves the email's placeholder avatar — the gift itself
 * is unaffected.
 */
function preloadGiftAvatar() {
  const addr = getAddress();
  if (!addr) return;
  identiconPngDataUrl(addr)
    .then((png) => setGiftIdenticon(png))
    .catch(() => setGiftIdenticon(''));
}
import { CashbackCodeField } from '../cashback/CashbackCodeField';
import { CountryPhoneInput } from './CountryPhoneInput';
import { parseCountryPhone, phoneCountry } from '../../lib/phoneCountry';
import {
  GIFT_EMAIL_MAX,
  giftNoteRules,
  validateGiftNote,
  type GiftNoteResult,
  type ProductRoute,
} from '../../lib/giftNote';


/** InfoRow — a compact kraft note box (not bare inline text) so the hint under a
 *  field reads as its own little card and wraps cleanly on narrow screens. */
function InfoRow({ icon, children, tone = 'warn' }: { icon: string; children: React.ReactNode; tone?: 'warn' | 'info' }) {
  return (
    <div className={`note-box ${tone}`}>
      <Icon name={icon as any} size={15} />
      <span>{children}</span>
    </div>
  );
}

/**
 * HeadSwap — stacks two heading variants in the SAME grid cell. The block is
 * always as tall as the taller copy, so swapping the text on toggle can never
 * change the height of anything above (or below) it. Hidden copy is
 * visibility:hidden, not display:none — it still reserves its space.
 */
function HeadSwap({ a, b, showB }: { a: React.ReactNode; b: React.ReactNode; showB: boolean }) {
  const cell: React.CSSProperties = { gridArea: '1 / 1 / 2 / 2', minWidth: 0 };
  return (
    <div style={{ display: 'grid' }}>
      <div style={{ ...cell, visibility: showB ? 'hidden' : 'visible' }} aria-hidden={showB || undefined}>
        {a}
      </div>
      <div style={{ ...cell, visibility: showB ? 'visible' : 'hidden' }} aria-hidden={showB ? undefined : true}>
        {b}
      </div>
    </div>
  );
}

/** The pinned "Send as gift" checkbox — identical markup in every step. */
function GiftToggle({ checked, sub, onChange }: { checked: boolean; sub: string; onChange: (v: boolean) => void }) {
  const { t } = useT();
  return (
    <label className={`gift-toggle gift-toggle--best ${checked ? 'is-on' : ''}`} style={{ display: 'flex', alignItems: 'center', gap: '12px', cursor: 'pointer', padding: '14px 14px', border: checked ? '2px solid var(--line-strong)' : '1.5px dashed var(--line-mid)', borderRadius: '10px', background: checked ? 'var(--paper-tint)' : 'var(--surface-1)', boxShadow: checked ? '2px 2px 0 rgba(78,61,40,.12)' : 'none', transition: '.15s' }}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} style={{ accentColor: 'var(--stamp)', width: '18px', height: '18px', flex: 'none' }} />
      <span style={{ width: 36, height: 36, borderRadius: 999, background: checked ? 'var(--stamp)' : 'var(--surface-2)', border: '1.5px solid var(--line-strong)', display: 'grid', placeItems: 'center', flex: 'none', fontSize: 16 }}>🎁</span>
      <span className="gtext" style={{ minWidth: 0 }}>
        <span className="gtitle" style={{ display: 'flex', alignItems: 'center', gap: '6px', fontWeight: 900, fontSize: 13 }}>
          {t('delivery.sendAsGift')}
          {checked && <span style={{ fontSize: 10, fontWeight: 900, letterSpacing: '.08em', textTransform: 'uppercase', background: 'var(--stamp)', color: 'var(--on-stamp)', padding: '2px 6px', borderRadius: 999 }}>{t('delivery.on')}</span>}
        </span>
        <span className="gsub" style={{ fontSize: 12, color: 'var(--ink-dim)', lineHeight: 1.4 }}>{sub}</span>
      </span>
    </label>
  );
}

/**
 * lockNumber — what a locked number box holds → what the supplier is asked
 * about. For a country with no dial-code entry the box stays free-form (the old
 * behaviour), so the raw text goes through untouched: guessing "+90" for an
 * unknown country is how a wrong number becomes a real charge.
 */
function lockNumber(value: string, country?: string): { e164: string; error: string } {
  const v = String(value || '').trim();
  if (!v) return { e164: '', error: tr('delivery.enterPhone') };
  if (!phoneCountry(country)) return { e164: v, error: '' };
  const p = parseCountryPhone(v, country);
  if (p.error) return { e164: '', error: p.error };
  if (!p.e164) return { e164: '', error: tr('delivery.enterComplete') };
  return { e164: p.e164, error: '' };
}


/**
 * useGiftNote — the ONE controller behind the gift block in all three steps.
 *
 * It owns the toggle, the note's own address (only when the cart has no emailed
 * line) and the message, and hands the step a `check()` that runs the shared
 * validator. Switching the gift off blanks the fields here, so a value typed
 * and then hidden can never reach the order.
 */
export function useGiftNote(route: ProductRoute) {
  const rules = giftNoteRules(route);
  const [on, setOnState] = useState(false);
  const [noteEmail, setNoteEmail] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const setOn = (v: boolean) => {
    setOnState(v);
    if (!v) {
      setNoteEmail('');
      setMessage('');
    }
    setError('');
  };

  /** `sharedEmail` is the product's own delivery address (the one the code goes
   *  to). When the cart has emailed lines that IS the note's target, so no
   *  second field is asked for; otherwise the block's own address is used. */
  const check = (shared: { email?: string } = {}): GiftNoteResult => {
    const email = rules.emailShared ? String(shared.email || '').trim() : noteEmail.trim();
    const r = validateGiftNote({ on, email, message, isValidEmail, emailError });
    setError(r.error);
    return r;
  };

  return {
    rules,
    on,
    setOn,
    message,
    setMessage,
    noteEmail,
    setNoteEmail,
    error,
    setError,
    check,
    /** The note needs its own address field only when no cart line is emailed. */
    showEmail: !rules.emailShared,
  };
}

type GiftNoteApi = ReturnType<typeof useGiftNote>;

/**
 * GiftNoteFields — the address the note goes to (only when the cart does not
 * already have one), the buyer's message, where it lands, and the standing
 * promise that a note never carries a code. Rendered identically in every step
 * so the three screens cannot drift apart again.
 */
function GiftNoteFields({
  api,
  idPrefix,
  targetLine,
  note,
}: {
  api: GiftNoteApi;
  idPrefix: string;
  /** Where the note will physically land, spelled out before paying. */
  targetLine?: React.ReactNode;
  /** Step-specific sentence about what the note is NOT (it never carries a code). */
  note?: React.ReactNode;
}) {
  const { t } = useT();
  const { rules, message, setMessage, noteEmail, setNoteEmail, error, setError, showEmail } = api;
  const emailId = `${idPrefix}-gift-note-email`;
  const msgId = `${idPrefix}-gift-note-message`;

  return (
    <div style={{ display: 'grid', gap: '12px' }}>
      {/* The address the note needs, and only when the cart does not already
          carry one: an emailed order has exactly one recipient field, above
          this block. */}
      {showEmail && (
        <div className="field soft-reveal">
          <label htmlFor={emailId}>{rules.emailLabel}</label>
          <input
            id={emailId}
            className="input"
            type="email"
            placeholder="friend@gmail.com"
            autoComplete="email"
            aria-label={t('delivery.giftEmailAria')}
            value={noteEmail}
            onChange={(e) => {
              setNoteEmail(e.target.value);
              setError('');
            }}
          />
        </div>
      )}

      <div className="field">
        <label htmlFor={msgId}>{t('delivery.personalMessage')}</label>
        <textarea
          id={msgId}
          className="input"
          placeholder={t('delivery.giftMsgPlaceholder')}
          rows={3}
          maxLength={GIFT_EMAIL_MAX}
          style={{ minHeight: '80px', resize: 'vertical' }}
          value={message}
          onChange={(e) => {
            setMessage(e.target.value);
            setError('');
          }}
        />
        <div className="small muted">
          {t('delivery.charsNote', { used: message.length, max: String(GIFT_EMAIL_MAX) })}
        </div>
      </div>

      <div className="small muted">{targetLine ?? rules.noteWhere}</div>

      <div className="note-box info">
        <Icon name="info" size={15} />
        <span>{note ?? rules.codeNote}</span>
      </div>

      {error && (
        <div className="alert error" role="alert" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="alert" size={16} />
          <div className="small">{error}</div>
        </div>
      )}
    </div>
  );
}

/* ---------------- Email step (gift cards / eSIMs) ---------------- */
export function EmailStep({
  title = tr('delivery.deliveryEmail'),
  sub = tr('delivery.stepEmailSub'),
  giftTitle = tr('delivery.giftDeliveryTitle'),
  giftSub = tr('delivery.giftDeliverySub'),
  recipientLabel = tr('delivery.recipientEmailQr'),
  selfWarn = tr('delivery.selfWarn'),
  giftToggleSub = tr('delivery.giftToggleSub'),
  onDone,
}: {
  title?: string;
  sub?: string;
  giftTitle?: string;
  giftSub?: string;
  recipientLabel?: string;
  selfWarn?: string;
  giftToggleSub?: string;
  /** Kept optional so older callers compile. Nothing in this step dials a
   *  number: a gift note is emailed, and the country lock only matters for the
   *  top-up fields (TopUpStep · PhoneStep · MixedStep). */
  country?: string;
  onDone: (email: string) => void;
}) {
  // The product is emailed, so the recipient address below is BOTH the code
  // destination and the note's email — one field, never two.
  const { t } = useT();
  const api = useGiftNote({ hasEmail: true, hasTopUp: false });
  const { on: gift, setOn: setGift } = api;
  const [myEmail, setMyEmail] = useState('');
  const [giftEmail, setGiftEmail] = useState('');
  const [selfError, setSelfError] = useState('');

  const submit = () => {
    if (!gift) {
      if (!isValidEmail(myEmail)) {
        setSelfError(t('delivery.giftEmailFirst'));
        return;
      }
      clearGiftExtras();
      onDone(myEmail);
      return;
    }
    const r = api.check({ email: giftEmail });
    if (!r.ok) return;
    // The gift store keeps its (channel, message, phone) shape — the channel is
    // always "email" now and the phone is always empty, so no caller of
    // lib/delivery.ts has to change.
    setGiftExtras('email', r.message);
    preloadGiftAvatar();
    onDone(r.email);
  };

  const recipientId = 'email-step-recipient';
  const selfId = 'email-step-self';

  return (
    <div>
      {/* Heading: fixed height across both modes (see <HeadSwap>). */}
      <div className="mt-2 mb-1">
        <HeadSwap
          showB={gift}
          a={<><div className="strong">{title}</div><div className="small muted mt-1">{sub}</div></>}
          b={<><div className="strong">{giftTitle}</div><div className="small muted mt-1">{giftSub}</div></>}
        />
      </div>

      {/* Pinned toggle — never moves, whatever changes below it. */}
      <GiftToggle
        checked={gift}
        sub={giftToggleSub}
        onChange={(v) => {
          setGift(v);
          setSelfError('');
          api.setError('');
        }}
      />

      {gift ? (
        <div className="mt-2 soft-reveal" style={{ display: 'grid', gap: '12px' }}>
          <div className="field">
            <label htmlFor={recipientId}>{recipientLabel}</label>
            <input
              id={recipientId}
              className="input"
              type="email"
              placeholder="friend@gmail.com"
              autoComplete="email"
              value={giftEmail}
              onChange={(e) => {
                setGiftEmail(e.target.value);
                api.setError('');
              }}
            />
          </div>
          <GiftNoteFields api={api} idPrefix="email" />
        </div>
      ) : (
        <div className="field soft-reveal">
          <label htmlFor={selfId}>{t('delivery.yourEmailReceipt')}</label>
          <input
            id={selfId}
            className="input"
            type="email"
            placeholder="you@gmail.com"
            autoComplete="email"
            value={myEmail}
            onChange={(e) => {
              setMyEmail(e.target.value);
              setSelfError('');
            }}
          />
          <InfoRow icon="alert">{selfWarn}</InfoRow>
        </div>
      )}

      <CashbackCodeField compact />

      {selfError && (
        <div className="alert error mt-1" role="alert" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="alert" size={16} />
          <div className="small">{selfError}</div>
        </div>
      )}

      <button className="btn btn-gold btn-block btn-lg mt-2" onClick={submit}>
        Continue
      </button>
    </div>
  );
}

/* ---------------- Top-up step (phone, with gift toggle) ---------------- */
export function TopUpStep({ country, onDone }: { country: string; onDone: (r: { email: string; phone: string }) => void }) {
  // The credit lands on the NUMBER, which cannot hold a message — so a gift
  // top-up needs one more thing than a gift card does: the address the note is
  // emailed to. Asking for it is the honest half of removing the SMS channel.
  const { t } = useT();
  const api = useGiftNote({ hasEmail: false, hasTopUp: true });
  const { on: gift, setOn: setGift, check } = api;
  const [selfPhone, setSelfPhone] = useState('');
  const [giftPhone, setGiftPhone] = useState('');
  const [checking, setChecking] = useState(false);
  // The number's own error lives HERE, not in the gift block — the block is
  // unmounted in self-purchase mode, and an error that disappears with it
  // leaves a dead "Check number & continue" button behind.
  const [numError, setNumError] = useState('');

  /** The locked box holds national digits; the supplier wants E.164. */
  const liveCheck = async (digits: string): Promise<string | null> => {
    const locked = lockNumber(digits, country);
    if (locked.error) {
      // A number the box cannot lock (another country, letters) is explained BY
      // the box, right under it — repeating it in the step alert would show the
      // same sentence twice. An empty box has nothing to show, so say it here.
      if (!String(digits || '').trim()) setNumError(locked.error);
      return null;
    }
    setChecking(true);
    const r = await collectPhoneValue(locked.e164, country);
    setChecking(false);
    if (!r.ok) {
      setNumError(r.error || t('delivery.numberNotAccepted'));
      return null;
    }
    setNumError('');
    return r.phone || null;
  };

  const submit = async () => {
    setNumError('');
    api.setError('');
    if (gift) {
      // Cheap checks first: don't burn a supplier lookup for a bad address.
      const r = check();
      if (!r.ok) return;
      const v = await liveCheck(giftPhone);
      if (!v) return;
      setGiftExtras('email', r.message);
    preloadGiftAvatar();
      onDone({ email: r.email, phone: v });
      return;
    }
    clearGiftExtras();
    const v = await liveCheck(selfPhone);
    if (!v) return;
    onDone({ email: '', phone: v });
  };

  const numberField = (label: string, value: string, setter: (v: string) => void, id: string) => (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <CountryPhoneInput
        id={id}
        country={country}
        value={value}
        invalid={!!numError}
        onChange={(v) => {
          setter(v);
          setNumError('');
          api.setError('');
        }}
        onEnter={submit}
      />
    </div>
  );

  return (
    <div>
      {/* Heading: fixed height across both modes (see <HeadSwap>). */}
      <div className="mt-2 mb-1">
        <HeadSwap
          showB={gift}
          a={
            <>
              <div className="strong">{t('delivery.whichNumber')}</div>
              <div className="small muted mt-1">{t('delivery.topupSelfNote')}</div>
            </>
          }
          b={
            <>
              <div className="strong">{t('delivery.topupFriendTitle')}</div>
              <div className="small muted mt-1">{t('delivery.topupFriendNote')}</div>
            </>
          }
        />
      </div>

      {/* Pinned toggle — never moves, whatever changes below it. */}
      <GiftToggle
        checked={gift}
        sub={t('delivery.topupFriendSub')}
        onChange={(v) => {
          setGift(v);
          setNumError('');
          api.setError('');
        }}
      />

      {gift ? (
        <div className="mt-2 soft-reveal" style={{ display: 'grid', gap: '12px' }}>
          {numberField(t('delivery.recipientPhoneLabel'), giftPhone, setGiftPhone, 'topup-recipient-phone')}
          <GiftNoteFields
            api={api}
            idPrefix="topup"
            note={t('delivery.topupGiftNote')}
          />
        </div>
      ) : (
        <div className="soft-reveal">
          {numberField(t('delivery.yourNumber'), selfPhone, setSelfPhone, 'topup-self-phone')}
          <InfoRow icon="alert">
            {t('checkout.dsCreditCheck')}
          </InfoRow>
        </div>
      )}

      <CashbackCodeField compact />

      {numError && (
        <div className="alert error mt-1" role="alert" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="alert" size={16} />
          <div className="small">{numError}</div>
        </div>
      )}

      <button className="btn btn-gold btn-block btn-lg mt-2" disabled={checking} onClick={submit}>
        {checking ? t('delivery.checking') : t('delivery.checkNumberContinue')}
      </button>
    </div>
  );
}

/* Standalone phone step (mixed cart top-ups). Resolves to E.164. */
export function PhoneStep({ country, title, onDone }: { country: string; title?: string; onDone: (phone: string) => void }) {
  const { t } = useT();
  const [phone, setPhone] = useState('');
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(false);

  const submit = async () => {
    setError('');
    const locked = lockNumber(phone, country);
    if (locked.error) {
      setError(locked.error);
      return;
    }
    setChecking(true);
    const r = await collectPhoneValue(locked.e164, country);
    setChecking(false);
    if (!r.ok) {
      setError(r.error || t('delivery.numberNotAccepted'));
      return;
    }
    onDone(r.phone || '');
  };

  return (
    <div>
      <div className="mt-2 mb-1">
        <div className="strong">{title || t('delivery.whichNumber')}</div>
        <div className="small muted mt-1">{t('delivery.topupCreditNote')}</div>
      </div>
      <div className="field">
        <label htmlFor="phone-step-number">{t('delivery.numberToTopUp')}</label>
        <CountryPhoneInput id="phone-step-number" country={country} value={phone} invalid={!!error} onChange={(v) => { setPhone(v); setError(''); }} onEnter={submit} />
      </div>
      <CashbackCodeField compact />
      {error && (
        <div className="alert error mt-1" role="alert" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="alert" size={16} />
          <div className="small">{error}</div>
        </div>
      )}
      <button className="btn btn-gold btn-block btn-lg mt-2" disabled={checking} onClick={submit}>
        {checking ? t('delivery.checking') : t('delivery.checkNumberContinue')}
      </button>
    </div>
  );
}

/* ---------------- Combined step (mixed cart: top-up + card) ----------------
 * ONE step, not two: the phone number(s) and the delivery email are asked
 * together, so the next thing the buyer sees is the payment screen.
 *
 * GIFT RULE: the "Send as gift" option behaves EXACTLY like the one in
 * <EmailStep> — same toggle position (pinned above the fields), same shared
 * <GiftNoteFields> block and the same setGiftExtras('email', message) /
 * clearGiftExtras() calls. A mixed cart must not lose the gift option it had
 * when the email had its own step. A cart of ONLY top-ups does need an address
 * when it is sent as a gift — not for the product (the credit needs no email),
 * but because the note is an email and a phone number cannot carry it.
 *
 * NUMBER RULE: every top-up line has its own <CountryPhoneInput> locked to THAT
 * line's country, because a cart can hold a Türkcell line and a Vodafone DE
 * line at once. One shared prefix would be a wrong number for one of them.
 */
export function MixedStep({
  tops,
  cards,
  onDone,
}: {
  tops: any[];
  cards: any[];
  onDone: (r: { email: string; phones: Map<unknown, string> }) => void;
}) {
  const route: ProductRoute = { hasEmail: cards.length > 0, hasTopUp: tops.length > 0 };
  const { t } = useT();
  const api = useGiftNote(route);
  const { on: gift, setOn: setGift } = api;
  const [myEmail, setMyEmail] = useState('');
  const [giftEmail, setGiftEmail] = useState('');
  const [phones, setPhones] = useState<string[]>(() => tops.map(() => ''));
  const [checking, setChecking] = useState(false);
  // Errors for the fields OUTSIDE the gift block (top-up numbers, own email).
  // They cannot live in the block's state: the block is unmounted in
  // self-purchase mode and the message would vanish with it.
  const [stepError, setStepError] = useState('');

  const setPhone = (i: number, v: string) => {
    setPhones((prev) => prev.map((p, idx) => (idx === i ? v : p)));
    setStepError('');
    api.setError('');
  };

  const submit = async () => {
    setStepError('');
    api.setError('');
    // --- top-up numbers: live validated, one per top-up item ---
    const map = new Map<unknown, string>();
    for (let i = 0; i < tops.length; i++) {
      const raw = String(phones[i] || '').trim();
      if (!raw) {
        setStepError(t('delivery.enterPhoneTopUp'));
        return;
      }
      // The box holds national digits for THAT line's country; compose before
      // asking, so a number that belongs to another country is refused here
      // instead of being shipped to the supplier as a "+9049…" hybrid.
      const locked = lockNumber(raw, tops[i].country);
      if (locked.error) {
        // The field explains a number it cannot lock; an empty one is the
        // step's to name (see TopUpStep for the same split).
        if (!raw) setStepError(locked.error);
        return;
      }
      setChecking(true);
      const r = await collectPhoneValue(locked.e164, tops[i].country);
      setChecking(false);
      if (!r.ok) {
        setStepError(r.error || t('delivery.numberNotAccepted'));
        return;
      }
      map.set(tops[i], r.phone || '');
      // also key it by product id: the cart store can hand out new item objects
      // between this step and the request, and identity-only keys are lost.
      if (tops[i] && tops[i].id) map.set(tops[i].id, r.phone || '');
    }
    // --- gift branch / delivery address ---
    let recipientEmail = '';
    if (gift) {
      // The email the ORDER carries: the code destination when the cart has
      // emailed lines; the note's own address when it has none — and a note
      // without an address is refused, not dropped.
      const r = api.check({ email: cards.length ? giftEmail : '' });
      if (!r.ok) return;
      recipientEmail = r.email;
      setGiftExtras('email', r.message);
    preloadGiftAvatar();
    } else if (cards.length) {
      if (!isValidEmail(myEmail)) {
        setStepError(emailError(myEmail) || t('delivery.enterValidEmail'));
        return;
      }
      recipientEmail = myEmail.trim();
      clearGiftExtras();
    } else {
      clearGiftExtras();
    }

    onDone({ email: recipientEmail.trim(), phones: map });
  };

  const recipientId = 'mixed-recipient-email';
  const selfId = 'mixed-self-email';

  return (
    <div>
      <div className="mt-2 mb-1">
        <div className="strong">{t('delivery.deliveryDetails')}</div>
        <div className="small muted mt-1">
          {tops.length && cards.length
            ? cards.every((c: any) => c.type === 'esim')
              ? t('delivery.mixedTopUpEsim')
              : cards.some((c: any) => c.type === 'esim')
                ? t('delivery.mixedTopUpEmailed')
                : t('delivery.mixedTopUpGift')
            : tops.length > 1
              ? t('delivery.mixedEnterNumbers')
              : t('delivery.mixedCreditNote')}
        </div>
      </div>

      {/* MIXED-CART RECAP: two different delivery routes in one order must be
          spelled out before payment, not summarised as "lands in your email".
          Each row names the item and where THAT item ends up. */}
      {tops.length > 0 && cards.length > 0 && (
        <div className="note-box info" style={{ display: 'block' }}>
          <div className="strong small" style={{ marginBottom: 4 }}>
            <Icon name="info" size={15} /> {t('checkout.dsTwoWays')}
          </div>
          <div className="small">
            📧 <strong>{t('delivery.byEmail')}</strong> {cards.map((c: any) => c.name || c.id).join(', ')}
          </div>
          <div className="small">
            📱 <strong>{t('delivery.toPhoneNumber')}</strong> {tops.map((x: any) => x.name || x.id).join(', ')} — {t('delivery.creditOnly')}.
          </div>
        </div>
      )}

      {/* Pinned toggle — never moves (see the layout rule at the top). */}
      <GiftToggle
        checked={gift}
        sub={cards.length ? t('delivery.giftSubCards') : t('delivery.giftSubTopup')}
        onChange={(v) => {
          setGift(v);
          setStepError('');
          api.setError('');
        }}
      />

      {tops.map((it: any, i: number) => {
        const where = phoneCountry(it.country);
        return (
          <div className="field" key={i}>
            <label htmlFor={`mixed-top-${i}`}>
              {it.name || it.id || 'Top-up'}
              {where ? ` · ${where.name}` : it.country ? ` · ${String(it.country).toUpperCase()}` : ''} — number to top up
            </label>
            <CountryPhoneInput
              id={`mixed-top-${i}`}
              country={it.country}
              value={phones[i] || ''}
              invalid={!!stepError}
              onChange={(v) => setPhone(i, v)}
              onEnter={submit}
              aria-label={`${it.name || it.id || 'Top-up'} number, without the country code`}
            />
          </div>
        );
      })}

      {gift ? (
        <div className="mt-2 soft-reveal" style={{ display: 'grid', gap: '12px' }}>
          {cards.length > 0 && (
            <div className="field">
              <label htmlFor={recipientId}>{t('delivery.recipientEmailCodes')}</label>
              <input
                id={recipientId}
                className="input"
                type="email"
                placeholder="friend@gmail.com"
                autoComplete="email"
                aria-label={t('delivery.recipientEmailAria')}
                value={giftEmail}
                onChange={(e) => {
                  setGiftEmail(e.target.value);
                  setStepError('');
                  api.setError('');
                }}
              />
            </div>
          )}
          <GiftNoteFields
            api={api}
            idPrefix="mixed"
            note={
              cards.length ? t('delivery.mixedNote') : undefined
            }
          />
        </div>
      ) : (
        cards.length > 0 && (
          <div className="field soft-reveal">
            <label htmlFor={selfId}>{t('delivery.yourEmailReceipt')}</label>
            <input
              id={selfId}
              className="input"
              type="email"
              placeholder="you@gmail.com"
              autoComplete="email"
              aria-label={t('delivery.deliveryEmail')}
              value={myEmail}
              onChange={(e) => {
                setMyEmail(e.target.value);
                setStepError('');
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  submit();
                }
              }}
            />
            <InfoRow icon="alert">
              {t('checkout.dsCodeCheck')}
            </InfoRow>
          </div>
        )
      )}

      <CashbackCodeField compact />

      {stepError && (
        <div className="alert error mt-1" role="alert" style={{ marginBottom: 0, display: 'flex', gap: '6px', alignItems: 'center' }}>
          <Icon name="alert" size={16} />
          <div className="small">{stepError}</div>
        </div>
      )}

      <button className="btn btn-gold btn-block btn-lg mt-2" disabled={checking} onClick={submit}>
        {checking ? t('delivery.checking') : tops.length ? t('delivery.checkNumberContinue') : t('actions.continue')}
      </button>
    </div>
  );
}
