/**
 * uiKit.tsx — React port of the shared UI helpers from ui.js: status badge,
 * stage timeline, mini progress, copy button, skeletons, empty/error/locked
 * states, kv, alert box, star display + picker, chat row + composer.
 */
import { useEffect, useId, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { Icon } from './Icon';
import { Identicon } from './Identicon';
import { fmtDate, fmtNIM } from '../../lib/format';
import { Clipboard } from '../../lib/clipboard';
import { getNimRate, cachedNimRate } from '../../lib/api';
import { nimAmountFor, nimFallbackText } from '../../lib/nim';
import { useT, t as i18nT } from '../../i18n';
import { asset, pagePath } from '../../lib/asset';

/** Inline Nimiq mark (the hexagon logomark). Use this instead of a generic
 *  money symbol (💰 / $ / ≈) wherever the NIM brand mark is meant. */
export function NimMark({ size = 16, className = '', style }: { size?: number; className?: string; style?: CSSProperties }) {
  return (
    <img
      src={asset("/img/nimiq-hexagon.png")}
      alt="NIM"
      width={size}
      height={size}
      className={className}
      style={{
        flexShrink: 0,
        display: 'inline-block',
        verticalAlign: '-0.18em',
        borderRadius: Math.max(2, Math.round(size * 0.18)),
        ...style,
      }}
    />
  );
}

/** "NIM" label with the hexagon mark, e.g. [⬡] NIM — used for cashback
 *  destination summaries that used to show a generic currency icon. */
export function NimWalletLabel({ size = 16 }: { size?: number }) {
  const { t } = useT();
  return (
    <span className="row" style={{ gap: '5px', alignItems: 'center', display: 'inline-flex', whiteSpace: 'nowrap' }}>
      <NimMark size={size} />
      <span>{t('ui.myNimWallet')}</span>
    </span>
  );
}

/** Site-wide "come back / repeat value" banner linking to the cashback &
 *  staking page. ONE shared source for what used to be two near-identical
 *  copies (home page + StakerCashback), so the responsive-safe markup and copy
 *  are identical everywhere. `label` is the bold lead-in; `text` is the
 *  sentence after it; both default to the home-page wording. */
export function ComeBackBanner({
  label,
  text,
  className = '',
}: {
  label?: ReactNode;
  text?: ReactNode;
  className?: string;
}) {
  const { t } = useT();
  const labelNode = label !== undefined ? label : t('ui.comeBackLabel');
  const textNode = text !== undefined ? text : t('ui.comeBackText');
  return (
    <a className={`come-back ${className}`.trim()} href={pagePath("/cashback")}>
      <Icon name="spark" size={18} />
      {/* data-fit="wrap": the banner is a one-line rail on purpose, so the
          type shrinks to keep that shape — and if a translation is still too
          long at the floor (French/German/Turkish are), the text wraps instead
          of being ellipsized. Re-measured on every language switch. */}
      <span className="come-back-txt" data-fit="wrap">
        <span className="strong">{labelNode}</span>
        {textNode != null ? <> {textNode}</> : null}
      </span>
      <span className="come-back-cta" data-fit="wrap">{t('ui.comeBackCta')}</span>
    </a>
  );
}

/** Live "≈ X NIM" node: renders from the cached rate instantly, otherwise
 *  fetches with retries (mirrors nim.js nimAmountNode). */
export function NimAmount({ q, fallback }: { q: any; fallback?: string | null }) {
  const [text, setText] = useState<string>(() => {
    const m = cachedNimRate();
    const n = nimAmountFor(q, m);
    return n > 0 ? `≈ ${fmtNIM(n, 0)} NIM` : '…';
  });

  useEffect(() => {
    let alive = true;
    const attempt = (i: number) => {
      if (!alive) return;
      getNimRate()
        .then((m) => {
          if (!alive) return;
          const n = nimAmountFor(q, m);
          if (n > 0) setText(`≈ ${fmtNIM(n, 0)} NIM`);
          else if (i < 4) setTimeout(() => attempt(i + 1), 2000);
          else setText(fallback || nimFallbackText());
        })
        .catch(() => {
          if (i < 4) setTimeout(() => attempt(i + 1), 2000);
          else setText(fallback || nimFallbackText());
        });
    };
    attempt(0);
    return () => {
      alive = false;
    };
  }, [q, fallback]);

  return <span>{text}</span>;
}

/* ---------------- Status badge ---------------- */

const STATUS_MAP: Record<string, [string, string, boolean?]> = {

  pending: ['statusLabel.pending', 'gold', true],
  created: ['statusLabel.created', 'blue', true],
  payment_detected: ['statusLabel.payment_detected', 'blue', true],
  processing: ['statusLabel.processing', 'orange', true],
  delivered: ['statusLabel.delivered', 'teal'],
  complete: ['statusLabel.complete', 'teal'],
  fulfilled: ['statusLabel.fulfilled', 'teal'],
  failed: ['statusLabel.failed', 'red'],
  refunded: ['statusLabel.refunded', 'purple'],
  blocked: ['statusLabel.blocked', 'red'],
  denied: ['statusLabel.denied', 'red'],
  payment_error: ['statusLabel.payment_error', 'red'],
  expired: ['statusLabel.expired', 'gray'],
  quoted: ['statusLabel.quoted', 'gold', true],
  invoice_creating: ['statusLabel.invoice_creating', 'blue', true],
  lightning_invoice_created: ['statusLabel.lightning_invoice_created', 'gold', true],
  order_creating: ['statusLabel.order_creating', 'blue', true],
  awaiting_payment: ['statusLabel.awaiting_payment', 'gold', true],
  payment_started: ['statusLabel.payment_started', 'blue', true],
  payment_received: ['statusLabel.payment_received', 'teal', true],
  delivering: ['statusLabel.delivering', 'orange', true],
  nim_payment_submitted: ['statusLabel.nim_payment_submitted', 'blue', true],
  nim_confirmed: ['statusLabel.nim_confirmed', 'teal'],
  supplier_invoice_created: ['statusLabel.supplier_invoice_created', 'orange', true],
  polygon_tx_submitted: ['statusLabel.polygon_tx_submitted', 'orange', true],
  polygon_confirmed: ['statusLabel.polygon_confirmed', 'orange', true],
  failed_supplier: ['statusLabel.failed_supplier', 'red', true],
  refunding: ['statusLabel.refunding', 'purple', true],
  manual_review: ['statusLabel.manual_review', 'orange'],
  open: ['statusLabel.open', 'gold', true],
  waiting_user: ['statusLabel.waiting_user', 'orange', true],
  waiting_admin: ['statusLabel.waiting_admin', 'blue', true],
  resolved: ['statusLabel.resolved', 'teal'],
  closed: ['statusLabel.closed', 'gray'],
};

export function StatusBadge({ status }: { status?: string | null }) {
  const { t } = useT();
  const s = String(status || '').toLowerCase();
  const [labelKey, color, pulsing] = STATUS_MAP[s] || ['statusLabel.unknown', 'gray', false];
  // STATUS_MAP now stores i18n keys; an unmapped raw status still renders as-is.
  const label = KNOWN_KEY.test(labelKey) ? t(labelKey) : (status || t('statusLabel.unknown'));
  return <span className={`badge ${color}${pulsing ? ' pulse' : ''}`}>{label}</span>;
}

/** Guard so an unmapped status string is never mistaken for a dict key. */
const KNOWN_KEY = /^statusLabel\./;

/** `labelKey` is an i18n key — resolve it with t() at render (kindLabel). */
export const KIND_META: Record<string, { icon: string; thumb: string; labelKey: string }> = {
  gift_card: { icon: 'gift', thumb: 'thumb-gc', labelKey: 'kindLabel.giftCard' },
  topup: { icon: 'phone', thumb: 'thumb-tu', labelKey: 'kindLabel.topup' },
  esim: { icon: 'globe', thumb: 'thumb-es', labelKey: 'kindLabel.esim' },
  quote: { icon: 'bolt', thumb: 'thumb-bp', labelKey: 'kindLabel.directPayment' },
  bill_payment: { icon: 'card', thumb: 'thumb-bp', labelKey: 'kindLabel.billPayment' },
};

export function kindMeta(kind?: string | null) {
  return KIND_META[kind || ''] || { icon: 'bag', thumb: 'thumb-gc', labelKey: 'kindLabel.item' };
}

/** Translated label for a kind — for non-React callers (module-level t). */
export function kindLabel(kind?: string | null): string {
  return i18nT(kindMeta(kind).labelKey);
}

/* ---------------- Stage timeline ---------------- */

const STAGE_COPY: Record<string, { title: string; desc: string; failedDesc?: string; failedTitle?: string }> = {
  order_placed: {
    title: 'stage.orderPlaced',
    desc: 'stage.orderPlacedDesc',
    failedDesc: 'stage.orderPlacedDesc',
  },
  payment_settled: {
    title: 'stage.paymentSettled',
    desc: 'stage.paymentSettledDesc',
  },
  // USDT variant — selected by rail in <StageTimeline>, because an order paid
  // on Polygon must never be narrated as a NIM conversion.
  payment_settled_usdt: {
    title: 'stage.paymentSettledUsdt',
    desc: 'stage.paymentSettledUsdtDesc',
  },
  supplier_processing: {
    title: 'stage.supplierProcessing',
    desc: 'stage.supplierProcessingDesc',
    failedDesc: 'stage.supplierFailedDesc',
  },
  delivery_complete: {
    title: 'stage.deliveryComplete',
    desc: 'stage.deliveryCompleteDesc',
    failedTitle: 'stage.deliveryNotConfirmed',
    failedDesc: 'stage.deliveryFailedDesc',
  },
};

/* Delivery-step wording per channel. A top-up has no code to show, so the
   generic "your code is ready below" line is wrong for phone-only orders. */
const DELIVERY_DONE_DESC: Record<string, string> = {
  email: 'stage.doneEmail',
  phone: 'stage.donePhone',
  both: 'stage.doneBoth',
};

export function StageTimeline({ stages, channel, usdt }: { stages?: any[]; channel?: 'email' | 'phone' | 'both' | 'none'; usdt?: boolean }) {
  const { t } = useT();
  const arr = Array.isArray(stages) ? stages : [];
  if (!arr.length) return <div className="empty small">{t('stage.noneYet')}</div>;
  return (
    <div className="timeline">
      {arr.map((st, i) => {
        const key = st.id === 'payment_settled' && usdt ? 'payment_settled_usdt' : st.id;
        let copy = STAGE_COPY[key] || { title: st.id, desc: '' };
        if (st.id === 'delivery_complete' && channel && DELIVERY_DONE_DESC[channel]) {
          copy = { ...copy, desc: DELIVERY_DONE_DESC[channel] };
        }
        const status = st.status || 'pending';
        const failed = status === 'failed';
        // Keys from STAGE_COPY are translated here; an unmapped server stage id
        // (which never starts with "stage.") is printed verbatim as before.
        const tx = (k: string) => (k && k.startsWith('stage.') ? t(k) : k);
        const title = tx(failed && copy.failedTitle ? copy.failedTitle : copy.title);
        const desc = tx(failed && copy.failedDesc ? copy.failedDesc : copy.desc);
        const dotIcon = status === 'completed' ? 'check' : failed ? 'x' : 'clock';
        return (
          <div key={i} className={`tl-item ${status}`}>
            <div className="tl-dot">
              <Icon name={dotIcon} size={14} />
            </div>
            <div className="tl-title">{title}</div>
            {desc ? <div className="tl-desc">{desc}</div> : null}
            {st.timestamp ? <div className="tl-time">{fmtDate(st.timestamp)}</div> : null}
          </div>
        );
      })}
    </div>
  );
}

export function MiniProgress({ order }: { order: any }) {
  const bar: ReactNode[] = [];
  const cur = Number(order.current_stage ?? 1);
  const bad = ['failed', 'refunded'].includes(String(order.status).toLowerCase());
  const stages = Array.isArray(order.stages) ? order.stages : Array.from({ length: 4 });
  for (let i = 0; i < Math.max(stages.length, 4); i++) {
    let cls = '';
    const st = stages[i];
    if (st) {
      if (st.status === 'completed') cls = 'done';
      else if (st.status === 'in_progress') cls = bad ? 'fail' : 'working';
      else if (st.status === 'failed') cls = 'fail';
    } else if (i < cur) cls = 'done';
    bar.push(<i key={i} className={cls || undefined} />);
  }
  return <div className="mini-progress">{bar}</div>;
}

/* ---------------- Copy button ---------------- */

export function CopyButton({ getText, label }: { getText: string | (() => string); label?: string }) {
  const { t } = useT();
  const labelText = label !== undefined ? label : t('ui.copy');
  const [copied, setCopied] = useState(false);
  return (
    <button
      className="copy-btn"
      type="button"
      onClick={() => {
        const text = typeof getText === 'function' ? getText() : getText;
        // Nimiq Pay's clipboard: synchronous + boolean, so "Copied" only
        // ever shows after a real copy.
        if (Clipboard.copy(String(text ?? ''))) {
          setCopied(true);
          setTimeout(() => setCopied(false), 1400);
        }
      }}
    >
      <Icon name="copy" size={14} />
      <span>{copied ? t('ui.copied') : labelText}</span>
    </button>
  );
}

/* ---------------- Skeletons & empty states ---------------- */

export function SkeletonCards({ n = 8, cls = 'skel card' }: { n?: number; cls?: string }) {
  return (
    <>
      {Array.from({ length: n }).map((_, i) => (
        <div key={i} className={cls} />
      ))}
    </>
  );
}

export function SkeletonLines({ n = 3 }: { n?: number }) {
  return (
    <>
      {Array.from({ length: n }).map((_, i) => (
        <div key={i} className="skel line" style={{ width: 90 - i * 18 + '%' }} />
      ))}
    </>
  );
}

export function EmptyState({ iconName = 'bag', title, text, action }: { iconName?: string; title: string; text?: string; action?: ReactNode }) {
  return (
    <div className="empty fade-in">
      <div className="empty-ico">
        <Icon name={iconName} size={32} />
      </div>
      <h3>{title}</h3>
      {text ? <p>{text}</p> : null}
      {action ? <div>{action}</div> : null}
    </div>
  );
}

export function ErrorState({ message, retry }: { message?: string; retry?: () => void }) {
  const { t } = useT();
  return (
    <div className="empty fade-in">
      <div className="empty-ico" style={{ background: 'var(--red-soft)', borderColor: 'rgba(240,97,97,.3)', color: 'var(--red)' }}>
        <Icon name="alert" size={30} />
      </div>
      <h3>{t('errors.wentWrong')}</h3>
      <p>{message || t('ui.pleaseTryAgain')}</p>
      {retry ? (
        <button className="btn btn-ghost" onClick={retry}>
          <Icon name="refresh" size={18} /> {t('common.tryAgain')}
        </button>
      ) : null}
    </div>
  );
}

export function LockedSignInCard({ title, text, onConnect, lg = false, iconSize = 19 }: { title: string; text: string; onConnect: () => void; lg?: boolean; iconSize?: number }) {
  const { t } = useT();
  return (
    <div className="card locked fade-in">
      <div className="lock-ico">
        <Icon name="lock" size={34} />
      </div>
      <h2>{title}</h2>
      <p>{text}</p>
      <button className={'btn btn-gold' + (lg ? ' btn-lg' : '')} onClick={onConnect}>
        <Icon name="nimiq" size={iconSize} />
        <span className="btn-label">{t('ui.connectWallet')}</span>
      </button>
    </div>
  );
}

export function KvCard({ title, rows }: { title: string; rows: Array<[string, ReactNode]> }) {
  return (
    <div className="card">
      <div className="card-title">{title}</div>
      <Kv rows={rows} />
    </div>
  );
}

export function Kv({ rows }: { rows: Array<[string, ReactNode]> }) {
  return (
    <dl className="kv">
      {rows.map(([k, v], i) => (
        <div key={i}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function AlertBox({ type, children }: { type: 'success' | 'error' | 'warn' | 'info'; children: ReactNode }) {
  const ico = type === 'success' ? 'check' : type === 'error' ? 'alert' : type === 'warn' ? 'alert' : 'info';
  const cls = type === 'warn' ? 'warn' : type;
  return (
    <div className={`alert ${cls}`}>
      <Icon name={ico} size={19} />
      <div>{children}</div>
    </div>
  );
}

/* ---------------- Star ratings ---------------- */

const STAR_D = 'M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z';

function StarSVG({ ratio, size = 16 }: { ratio: number; size?: number }) {
  const r = Math.max(0, Math.min(1, ratio));
  // useId(), not Math.random(): a random id differs between the server HTML
  // and the client, which made React fail hydration on any page with stars.
  const id = 'sg' + useId().replace(/[^a-zA-Z0-9_-]/g, '');
  // The previous accessibility enlargement made rating stars too dominant.
  // Keep the same shared sizing rule, but render every rating star 1.5× smaller
  // than that previous treatment without changing its surrounding card layout.
  const visualSize = size <= 30 ? Math.round(size * (5 / 3)) : Math.round(size / 1.5);
  return (
    <svg viewBox="0 0 24 24" width={visualSize} height={visualSize} aria-hidden="true">
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="1" y2="0">
          <stop offset={r * 100 + '%'} stopColor="#F7C948" />
          <stop offset={r * 100 + '%'} stopColor="rgba(226,166,43,0.18)" />
        </linearGradient>
      </defs>
      <path d={STAR_D} fill={`url(#${id})`} stroke="#E2A62B" strokeWidth="1.3" strokeLinejoin="round" />
    </svg>
  );
}

export function StarsDisplay({ rating, size = 16, label = '' }: { rating: number; size?: number; label?: string }) {
  const { t } = useT();
  const v = Math.max(0, Math.min(5, Number(rating) || 0));
  return (
    <span className="stars" role="img" aria-label={t('ui.outOf5', { value: v.toFixed(1) })}>
      {[1, 2, 3, 4, 5].map((i) => (
        <StarSVG key={i} ratio={v - (i - 1)} size={size} />
      ))}
      {label !== '' ? <span className="stars-label">{label}</span> : null}
    </span>
  );
}

export function StarPicker({ onSelect, size = 34 }: { onSelect?: (r: number) => void; size?: number }) {
  const { t } = useT();
  const [value, setValue] = useState(0);
  const [hover, setHover] = useState(0);
  const paint = hover || value;
  return (
    <div className="stars picker">
      {[1, 2, 3, 4, 5].map((i) => (
        <button
          key={i}
          className="star-btn"
          type="button"
          aria-label={t('ui.starAria', { count: i })}
          onMouseEnter={() => setHover(i)}
          onMouseLeave={() => setHover(0)}
          onClick={() => {
            setValue(i);
            if (onSelect) onSelect(i);
          }}
        >
          <StarSVG ratio={Math.max(0, Math.min(1, paint - i + 1))} size={size} />
        </button>
      ))}
    </div>
  );
}

/* ---------------- Chat ---------------- */

function chatClock(iso?: string | null): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d as unknown as number)) return '';
  // Locale-free on purpose: the shop renders in six languages, so the clock
  // must not be pinned to en-US.
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/**
 * A themed support/chat message. Outgoing ("user") bubbles align right in the
 * ink-stamp tone; incoming support ("admin") align left on cream. No emoji —
 * clean labels + a small STAFF chip keep it on-brand everywhere it is used.
 */
export function ChatMessageRow({ m, myAddress = '' }: { m: any; myAddress?: string }) {
  const { t } = useT();
  const isAdmin = m.sender === 'admin';
  return (
    <div className={`msg ${isAdmin ? 'msg-in' : 'msg-out'}`}>
      <div className={`msg-ava ${isAdmin ? 'msg-ava-staff' : 'msg-ava-you'}`}>
        {isAdmin ? <Icon name="headset" size={17} /> : <Identicon address={myAddress || m.sender} className="msg-ava-img" />}
      </div>
      <div className="msg-body">
        <div className="msg-who">
          <span>{isAdmin ? t('ui.supportTeam') : t('ui.you')}</span>
          {isAdmin ? <span className="msg-staff">{t('ui.staff')}</span> : null}
        </div>
        <div className={`msg-bubble ${isAdmin ? 'in' : 'out'}`}>{m.message}</div>
        <div className="msg-time">{chatClock(m.created_at)}</div>
      </div>
    </div>
  );
}

export function ChatComposer({
  placeholder,
  maxLength = 4000,
  sendLabel,
  onSend,
  hint,
  onDraft,
}: {
  placeholder?: string;
  maxLength?: number;
  sendLabel?: string;
  onSend: (text: string) => Promise<void>;
  hint?: string;
  onDraft?: (text: string) => void;
}) {
  const { t } = useT();
  const ph = placeholder ?? t('ui.writeReply');
  const sendText = sendLabel ?? t('ui.send');
  const hintText = hint !== undefined ? hint : t('ui.composerHint');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const taRef = useRef<HTMLTextAreaElement>(null);
  const labelRef = useRef<HTMLSpanElement>(null);

  const autogrow = () => {
    const ta = taRef.current;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = Math.min(ta.scrollHeight, 160) + 'px';
  };

  const send = async () => {
    if (busy) return;
    // NOT `t` — that name is the translator in this scope.
    const body = text.trim();
    if (!body) return;
    setBusy(true);
    if (labelRef.current) labelRef.current.textContent = t('ui.sending');
    try {
      await onSend(body);
      setText('');
      autogrow();
    } finally {
      setBusy(false);
      if (labelRef.current) labelRef.current.textContent = sendText;
      taRef.current?.focus();
    }
  };

  useEffect(autogrow, [text]);

  return (
    <div>
      <div className="composer">
        <div className="stretch">
          <textarea
            ref={taRef}
            className="input composer-input"
            placeholder={ph}
            maxLength={maxLength}
            rows={1}
            aria-label={ph}
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              if (onDraft) onDraft(e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                send();
              }
            }}
          />
        </div>
        <button className="btn btn-gold composer-send" type="button" onClick={send} disabled={busy}>
          <Icon name="send" size={16} />
          <span className="btn-label" ref={labelRef}>
            {sendText}
          </span>
        </button>
      </div>
      {hintText ? <div className="xs faint composer-hint">{hintText}</div> : null}
    </div>
  );
}
