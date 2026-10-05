/**
 * AdminPage.tsx — React port of pages/admin.js: operator console for direct
 * email notifications (Mailtrap — the one mail transport), NIM cashback
 * settings, orders panel, catalog rules. Login via a separate admin cookie
 * session.
 */
import { useCallback, useEffect, useState } from 'react';
import { Icon, type IconName } from '../ui/Icon';
import { PlayersPanel, UsersPanel } from './AdminPeoplePanel';
import { AppRoot } from '../AppRoot';
import { useSheet, useToast } from '../AppProviders';
import {
  adminLogin,
  adminLogout,
  adminStatus,
  adminMe,
  adminSend,
  adminTestEmail,
  adminCatalogRules,
  adminGetCashback,
  adminSetCashback,
  adminGetStakeLedger,
  adminResetStakeLedger,
  adminListQuotes,
  adminPurgeTestOrders,
} from '../../lib/api';
import { fmtNIM, formatWalletAddress } from '../../lib/format';
import { siteName } from '../../lib/config';
import { orderedCountries } from '../../lib/countries';
import { CopyButton, AlertBox } from '../ui/uiKit';
import { pagePath } from '../../lib/asset';

function badge(label: string, on: boolean, detail?: string) {
  // Two tokens: the vivid one fills the dot and the border, the text-only one
  // (>=4.6:1 on the chip surface) carries the label. Using the fill colour for
  // the text measured 4.30:1 (red) / 4.85:1 (green) — under AA on the dot's row.
  const fill = on ? 'var(--green)' : 'var(--red)';
  const text = on ? 'var(--text-ok)' : 'var(--text-danger)';
  return (
    <span className="chip" style={{ borderColor: fill, color: text }}>
      <span style={{ display: 'inline-block', width: '8px', height: '8px', borderRadius: '50%', background: fill, marginRight: '6px' }} />
      <span className="strong">{label}</span>
      {detail ? <span className="xs"> · {detail}</span> : null}
    </span>
  );
}

type AdminSection = 'overview' | 'catalog' | 'orders' | 'cashback' | 'people' | 'email';

type RuleOption = { value: string; label: string };

// These are the supplier category slugs surfaced by the catalog, with a
// readable name in the operator UI. The code remains visible in small text so
// an operator can still match an API rule when needed.
const CATALOG_CATEGORY_OPTIONS: RuleOption[] = [
  { value: 'e-commerce', label: 'Shopping & e-commerce' },
  { value: 'games', label: 'Games' },
  { value: 'entertainment', label: 'Entertainment' },
  { value: 'streaming', label: 'Streaming' },
  { value: 'electronics', label: 'Electronics' },
  { value: 'apparel_clothing', label: 'Apparel & clothing' },
  { value: 'home', label: 'Home' },
  { value: 'books_learning', label: 'Books & learning' },
  { value: 'food', label: 'Food & restaurants' },
  { value: 'groceries', label: 'Groceries' },
  { value: 'retail', label: 'Retail' },
  { value: 'health_beauty', label: 'Health & beauty' },
  { value: 'sports_fitness', label: 'Sports & fitness' },
  { value: 'travel_flights', label: 'Travel & flights' },
  { value: 'charity_donations', label: 'Charity & donations' },
  { value: 'e-money', label: 'E-money / financial' },
  { value: 'mobile_credits', label: 'Mobile credits' },
  { value: 'mobile_data', label: 'Mobile data' },
  { value: 'mobile_talk_time', label: 'Mobile talk time' },
  { value: 'mobile_bundle', label: 'Mobile bundles' },
  { value: 'e-sim', label: 'eSIM' },
  { value: 'other_products', label: 'Other products' },
  { value: 'gambling', label: 'Gambling' },
];

const CATALOG_KIND_OPTIONS: RuleOption[] = [
  { value: 'giftcard', label: 'Gift cards' },
  { value: 'mobile_recharge', label: 'Mobile recharge / top-up' },
];

const ADMIN_COUNTRY_OPTIONS: RuleOption[] = (() => {
  const { popular, rest } = orderedCountries();
  return [...popular, ...rest].map(([value, label]) => ({ value, label }));
})();

const ADMIN_SECTIONS: Array<{ id: AdminSection; label: string; hint: string; icon: IconName }> = [
  { id: 'overview', label: 'Overview', hint: 'health & players', icon: 'pulse' },
  { id: 'catalog', label: 'Catalog', hint: 'types & visibility', icon: 'package' },
  { id: 'orders', label: 'Orders', hint: 'cashback & tx', icon: 'receipt' },
  { id: 'cashback', label: 'Cashback', hint: 'rates & ledgers', icon: 'wallet' },
  { id: 'people', label: 'People', hint: 'users & players', icon: 'user' },
  { id: 'email', label: 'Email', hint: 'Mailtrap tools', icon: 'send' },
];

function initialAdminSection(): AdminSection {
  if (typeof window === 'undefined') return 'overview';
  const requested = new URLSearchParams(window.location.search).get('section') as AdminSection | null;
  return requested && ADMIN_SECTIONS.some((item) => item.id === requested) ? requested : 'overview';
}

function AdminSectionNav({ active, onChange }: { active: AdminSection; onChange: (section: AdminSection) => void }) {
  return (
    <nav className="admin-section-nav" aria-label="Operator console pages">
      {ADMIN_SECTIONS.map((section) => (
        <button
          key={section.id}
          type="button"
          className={'admin-section-tab' + (active === section.id ? ' is-active' : '')}
          aria-current={active === section.id ? 'page' : undefined}
          onClick={() => onChange(section.id)}
        >
          <Icon name={section.icon} size={16} />
          <span className="admin-section-tab-copy">
            <span className="admin-section-tab-label">{section.label}</span>
            <span className="admin-section-tab-hint">{section.hint}</span>
          </span>
        </button>
      ))}
    </nav>
  );
}

function AdminLoginSheet({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const { closeSheet } = useSheet();
  const { toast } = useToast();

  const submit = async () => {
    if (busy) return;
    if (!username.trim() || !password || (totp && !/^[0-9]{6}$/.test(totp))) {
      setErr('Username and password are required. (TOTP: 6 digits — only when your admin account uses it.)');
      return;
    }
    setBusy(true);
    try {
      const r = await adminLogin({ username: username.trim(), password, totp });
      closeSheet();
      toast(`Signed in as ${r.admin?.username || 'admin'}`, 'success');
      onSuccess();
    } catch (e) {
      setBusy(false);
      setErr((e as Error).message || 'Login failed');
    }
  };

  return (
    <div>
      <div className="mt-2 mb-1">
        <div className="strong">Operator console</div>
        <div className="small muted mt-1">Separate cookie session — your Nimiq wallet login is unrelated.</div>
      </div>
      <div className="field">
        <label>Username</label>
        <input className="input" type="text" placeholder="admin" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
      </div>
      <div className="field">
        <label>Password</label>
        <input className="input" type="password" placeholder="Password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </div>
      <div className="field">
        <label>TOTP code (optional for env test logins)</label>
        <input className="input" type="text" inputMode="numeric" placeholder="6-digit code — leave empty if unused" maxLength={6} autoComplete="one-time-code" value={totp} onChange={(e) => setTotp(e.target.value)} />
      </div>
      {err ? (
        <div className="alert error mt-1" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={16} />
          <div className="small">{err}</div>
        </div>
      ) : null}
      <button className="btn btn-gold btn-block btn-lg" disabled={busy} onClick={submit}>
        {busy ? 'Signing in…' : 'Sign in'}
      </button>
    </div>
  );
}

function StatusCard({ status }: { status: any }) {
  // Mailtrap is the ONE mail transport: no SMTP client and no SMS sender
  // exist any more, so this is the whole channel status.
  const emailOn = status?.email?.enabled;
  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="pulse" size={14} />
        <span>Mail status</span>
      </div>
      <div className="row" style={{ gap: '12px', flexWrap: 'wrap' }}>
        {badge('Mailtrap ' + (emailOn ? 'READY' : 'OFF'), emailOn, emailOn ? `${status.email.sandbox ? 'SANDBOX · ' : ''}from ${status.email.from || '?'}` : 'set MAILTRAP_API_TOKEN + MAILTRAP_FROM_EMAIL in .env')}
      </div>
      {emailOn && status?.email?.sandbox ? (
        <div className="alert warn mt-2" style={{ marginBottom: 0 }}>
          <Icon name="info" size={16} />
          <div className="small">Mailtrap runs in SANDBOX mode — emails are captured in the test inbox, never delivered. Unset MAILTRAP_USE_SANDBOX to go live.</div>
        </div>
      ) : null}
    </div>
  );
}

function Composer({ status }: { status: any }) {
  // Email is the one channel: Mailtrap carries it, the SMTP client and the
  // SMS sender are gone from the backend for good.
  const [email, setEmail] = useState('');
  const [subject, setSubject] = useState('');
  const [body, setBody] = useState('');
  const [dryRun, setDryRun] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [result, setResult] = useState<any | null>(null);
  const { toast } = useToast();

  const emailAvail = status?.email?.enabled;
  const helpLine = emailAvail
    ? 'Mailtrap is ready. Use "Dry-run" first to verify the body and recipient — a dry-run logs the payload and delivers nothing.'
    : 'Mailtrap is not configured yet — set MAILTRAP_API_TOKEN and MAILTRAP_FROM_EMAIL in backend .env and restart. Dry-run still works (it logs locally).';

  const doSend = async () => {
    setErr('');
    setResult(null);
    setBusy(true);
    try {
      const b = body.trim();
      if (!b) throw new Error('Message body cannot be empty');
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) throw new Error('A valid email address is required.');
      const req: any = { to_email: email.trim(), body: b, dry_run: dryRun, category: 'operator' };
      if (subject.trim()) req.subject = subject.trim();
      const r = await adminSend(req);
      setResult(r);
      const tone = r.dry_run ? 'info' : r.email === 'sent' ? 'success' : 'error';
      toast(`Notification: email=${r.email}`, tone);
    } catch (e) {
      setErr((e as Error).message || 'Send failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="send" size={14} />
        <span>Send direct notification</span>
      </div>
      <div className="small muted mb-2">{helpLine}</div>
      <div className="field">
        <label>Recipient email</label>
        <input className="input" type="email" placeholder="recipient@gmail.com" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
      </div>
      <div className="field">
        <label>Email subject</label>
        <input className="input" type="text" placeholder="Email subject" value={subject} onChange={(e) => setSubject(e.target.value)} />
      </div>
      <div className="field">
        <label>Message body</label>
        <textarea className="input" placeholder={'Hi! Your order is ready. Best, ' + siteName()} rows={6} maxLength={2000} style={{ minHeight: '120px', resize: 'vertical', lineHeight: 1.5 }} value={body} onChange={(e) => setBody(e.target.value)} />
        <div className="xs faint">Email body: {body.length} / 2000</div>
      </div>
      <label style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer', padding: '10px 0', fontWeight: 700 }}>
        <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} style={{ width: '18px', height: '18px', accentColor: 'var(--stamp)' }} />
        <span className="small">Dry-run (log only, do NOT actually send — recommended when testing)</span>
      </label>
      {err ? (
        <div className="alert error mt-1" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={16} />
          <div className="small">{err}</div>
        </div>
      ) : null}
      <button className="btn btn-gold btn-block btn-lg mt-2" disabled={busy} onClick={doSend}>
        <Icon name="send" size={16} />
        <span className="btn-label">{busy ? 'Sending…' : 'Send notification'}</span>
      </button>
      {result ? <ResultBlock r={result} /> : null}
    </div>
  );
}

function TestEmailCard() {
  const [email, setEmail] = useState('');
  const [kind, setKind] = useState('card');
  const [productLabel, setProductLabel] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [result, setResult] = useState<any | null>(null);
  const { toast } = useToast();

  const kinds = [
    { id: 'card', label: '🎁 Gift card' },
    { id: 'esim', label: '📶 eSIM' },
    { id: 'topup', label: '📞 Top-up' },
  ];

  const doSend = async () => {
    setErr('');
    setResult(null);
    setBusy(true);
    try {
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) throw new Error('Enter a valid recipient email.');
      const req: any = { to_email: email.trim(), kind };
      if (productLabel.trim()) req.product_label = productLabel.trim();
      if (message.trim()) req.message = message.trim();
      const r = await adminTestEmail(req);
      setResult(r);
      toast(`Test email sent to ${email.trim()} (${r.kind})`, 'success');
    } catch (e) {
      setErr((e as Error).message || 'Send failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="send" size={14} />
        <span>Send a test purchase email</span>
      </div>
      <div className="small muted mb-2">Sends a realistic gift/purchase email — built by the same Mailtrap GiftNote + transport real orders use, with the real identicon avatar mosaic — to any address, <em>as if someone bought something</em>. Great for checking the email renders correctly before trusting it on a real order. Nothing is recorded as a gift or a purchase.</div>
      <div className="field">
        <label>Recipient email</label>
        <input className="input" type="email" placeholder="you@example.com" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
      </div>
      <div className="field">
        <label>Email type</label>
        <div className="row" style={{ gap: '8px', flexWrap: 'wrap' }}>
          {kinds.map((k) => (
            <label
              key={k.id}
              htmlFor={'te-kind-' + k.id}
              style={{ display: 'inline-flex', alignItems: 'center', gap: '6px', padding: '10px 14px', border: '2px solid var(--line-strong)', borderRadius: 'var(--r-s)', cursor: 'pointer', fontWeight: 800, fontSize: '0.92rem', background: 'var(--surface-1)' }}
            >
              <input type="radio" name="te-kind" id={'te-kind-' + k.id} value={k.id} checked={kind === k.id} onChange={() => setKind(k.id)} style={{ accentColor: 'var(--stamp)' }} />
              <span>{k.label}</span>
            </label>
          ))}
        </div>
      </div>
      <div className="field">
        <label>Product label (optional override)</label>
        <input className="input" type="text" placeholder="e.g. Steam · 50 USD" value={productLabel} onChange={(e) => setProductLabel(e.target.value)} />
      </div>
      <div className="field">
        <label>Note from the sender (optional — what a buyer's message would say)</label>
        <input className="input" type="text" placeholder="e.g. Happy birthday! 🎂" value={message} onChange={(e) => setMessage(e.target.value)} />
      </div>
      {err ? (
        <div className="alert error mt-1" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={16} />
          <div className="small">{err}</div>
        </div>
      ) : null}
      <button className="btn btn-gold btn-block btn-lg mt-2" disabled={busy} onClick={doSend}>
        <Icon name="send" size={16} />
        <span className="btn-label">{busy ? 'Sending…' : 'Send test email'}</span>
      </button>
      {result ? (
        <div className="small mt-1" style={{ color: 'var(--green, #1a7f37)' }}>
          Sent as <b>{result.kind}</b> — message id(s): {Array.isArray(result.message_ids) ? result.message_ids.join(', ') : '—'}
          {result.url ? <div className="xs faint">Check <a href={result.url} target="_blank" rel="noreferrer">Mailtrap logs</a>.</div> : null}
        </div>
      ) : null}
    </div>
  );
}

function ResultBlock({ r }: { r: any }) {
  const color = r.email === 'sent' || r.email === 'dry_run' ? 'success' : 'error';
  const summary = r.dry_run
    ? 'Dry-run completed — nothing was sent. Check the backend log for the recorded payload.'
    : r.email === 'sent'
      ? 'Notification delivered.'
      : 'The send failed. See the status below and the backend log.';
  const tone = color === 'success' ? 'success' : r.dry_run ? 'info' : 'error';
  return (
    <div className="mt-2">
      <AlertBox type={tone}>{summary}</AlertBox>
      <div className="card mt-2" style={{ padding: '14px 16px' }}>
        <div className="kv">
          {(
            [
              ['Email', String(r.email)],
              ['Dry-run', String(r.dry_run)],
              r.to?.email ? ['To email', r.to.email] : null,
              ['Body chars', `email=${r.body_chars?.email || 0}`],
            ] as Array<[string, string] | null>
          )
            .filter((x): x is [string, string] => !!x)
            .map(([k, v], i) => (
              <div key={i}>
                <dt>{k}</dt>
                <dd className={k === 'To email' ? 'mono small' : ''}>{v}</dd>
              </div>
            ))}
        </div>
      </div>
    </div>
  );
}

function cashbackPaidLabel(cb: any) {
  if (!cb) return 'not queued';
  // Owner (2026-10-05): "paid" means the tx REALLY left the wallet — a queued
  // or confirming row must never wear the paid badge.
  if (cb.status === 'paid' && cb.tx_hash && !String(cb.tx_hash).startsWith('TEST')) return 'paid';
  return cb.status || 'unknown';
}

function CashbackRow({ cb, extraLeft }: { cb: any; extraLeft?: string }) {
  const paid = cashbackPaidLabel(cb);
  const amt = cb && cb.amount_nim != null ? fmtNIM(cb.amount_nim) + ' NIM' : '—';
  const hash = cb && cb.tx_hash ? String(cb.tx_hash) : '';
  const left = extraLeft || (cb && cb.product_id) || 'item';
  return (
    <div className="mt-1" style={{ padding: '8px 0', borderTop: '1px solid var(--line)' }}>
      <div className="row between" style={{ gap: '8px', flexWrap: 'wrap' }}>
        <div className="small">{left}</div>
        {badge('Cashback ' + paid, paid === 'paid', amt)}
      </div>
      {hash ? (
        <div className="row between mt-1" style={{ gap: '8px', flexWrap: 'wrap', alignItems: 'center' }}>
          <div className="mono xs" style={{ wordBreak: 'break-all' }}>{hash}</div>
          <CopyButton getText={hash} label="Copy tx" />
        </div>
      ) : (
        <div className="xs faint">{paid === 'not queued' ? 'No cashback row yet' : cb.skip_reason || cb.last_error || 'No tx hash yet'}</div>
      )}
    </div>
  );
}

function CashbackPanel() {
  const [data, setData] = useState<any | null>(null);
  const [err, setErr] = useState('');
  const [val, setVal] = useState('0');
  const [stakerVal, setStakerVal] = useState('1');
  const [saveErr, setSaveErr] = useState('');
  const { toast } = useToast();

  useEffect(() => {
    adminGetCashback()
      .then((d) => {
        setData(d);
        const pct = Number(d.cashback_percent);
        setVal(Number.isFinite(pct) ? String(pct) : '0');
        const spct = Number(d.staker_cashback_percent);
        setStakerVal(Number.isFinite(spct) ? String(spct) : '1');
      })
      .catch((e) => setErr((e as Error).message || 'Could not load cashback settings'));
  }, []);

  const save = async () => {
    const v = parseFloat(val);
    if (!(v >= 0) || v > 20) {
      setSaveErr('Enter 0–20 (percent of the product NIM price).');
      return;
    }
    const sv = parseFloat(stakerVal);
    if (!(sv >= 0) || sv > 20) {
      setSaveErr('Staker base: enter 0–20 (percent of the product NIM price).');
      return;
    }
    try {
      const saved = await adminSetCashback({ cashback_percent: v, staker_cashback_percent: sv });
      setData((prev: any) => ({ ...prev, ...saved }));
      toast('Cashback set to ' + saved.cashback_percent + '% (stakers ' + saved.staker_cashback_percent + '%) — applies to the next fulfilled order', 'success');
    } catch (e) {
      setSaveErr((e as Error).message || 'Save failed');
    }
  };

  if (!data && !err) return <div className="card mt-2"><div className="card-title">NIM cashback</div><div className="small muted">Loading…</div></div>;
  if (err) return <div className="card mt-2"><div className="card-title">NIM cashback</div><div className="alert error"><div className="small">{err}</div></div></div>;

  const pct = Number(data.cashback_percent);
  const walletOn = !!data.wallet_configured;
  const rows = (data.recent || []).slice(0, 12);

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="wallet" size={18} />
        <span>NIM cashback — paid only when the order is fulfilled</span>
      </div>
      <div className="small muted">
        Base cashback is the default rate for non-stakers with no promo code. Set it to 0% to make cashback exclusive to pool stakers and promo campaigns. Every payout is still locked at fulfill time, so a later settings change cannot rewrite a queued cashback row.
      </div>
      <div className="row mt-2" style={{ gap: '12px', flexWrap: 'wrap' }}>
        {badge('Wallet ' + (walletOn ? 'READY' : 'OFF'), walletOn, walletOn ? data.network || 'mainnet' : 'set CASHBACK_ENABLED=true + CASHBACK_WALLET_SEED')}
        {badge('Base rate ' + (Number.isFinite(pct) ? pct + '%' : '0%'), true, String(Number(data.cashback_bps) || 0) + ' bps')}
        {badge('Staker base ' + (Number.isFinite(Number(data.staker_cashback_percent)) ? Number(data.staker_cashback_percent) + '%' : '1%'), true, String(Number(data.staker_cashback_bps) || 100) + ' bps')}
      </div>
      <div className="field mt-2">
        <label>Cashback percent of product NIM price (non-stakers)</label>
        <input className="input" type="number" min="0" max="20" step="0.01" value={val} onChange={(e) => setVal(e.target.value)} />
        <div className="xs faint mt-1">Recommended default: 0. Example: 100 NIM product → 0 NIM at 0%, 1 NIM at 1%. Max 20%.</div>
      </div>
      <div className="field mt-2">
        <label>Staker base cashback percent (pool stakers, before boost)</label>
        <input className="input" type="number" min="0" max="20" step="0.01" value={stakerVal} onChange={(e) => setStakerVal(e.target.value)} />
        <div className="xs faint mt-1">What any staked buyer earns on every order, on top of which the single-ledger boost stacks. Default: 1%. Max 20%.</div>
      </div>
      {saveErr ? (
        <div className="alert error mt-1">
          <div className="small">{saveErr}</div>
        </div>
      ) : null}
      <button className="btn btn-gold btn-block mt-2" onClick={save}>
        Save cashback percent
      </button>

      <StakeCashbackEditor data={data} onChanged={setData} basePct={Number.isFinite(pct) ? pct : 0} />
      <PromoCodesEditor data={data} onChanged={setData} basePct={Number.isFinite(pct) ? pct : 0} />
      {rows.length ? (
        <div className="mt-2">
          <div className="xs faint">Recent payouts</div>
          {rows.map((cb: any, i: number) => (
            <CashbackRow key={i} cb={cb} />
          ))}
        </div>
      ) : (
        <div className="xs faint mt-2">No cashback rows yet — they appear when an order is fulfilled.</div>
      )}
    </div>
  );
}

/**
 * StakeCashbackEditor — the single-ledger staker programme (v2).
 *
 * One parameter set, no ladder, no lock list: the engine's own struct is
 * edited directly, so what the console saves is exactly what the backend
 * runs. The boost a staker earns is a share of the pool fees their stake
 * actually earned, on top of the base rate, capped at $/day and $/month.
 */
function StakeCashbackEditor({ data, onChanged, basePct }: { data: any; onChanged: (d: any) => void; basePct: number }) {
  const { toast } = useToast();
  const [f, setF] = useState<Record<string, string>>(() => {
    const p = data?.stake_cashback || {};
    return {
      k: String(p.k ?? 0.8),
      q: String(p.q ?? 0.1),
      g0: String(p.g0 ?? 0.5),
      t_days: String(p.t_days ?? 360),
      min_stake_nim: String(p.min_stake_nim ?? 100),
      max_boost_percent: String((p.max_boost_bps ?? 1000) / 100),
      a_max_usd: String(p.a_max_usd ?? 10),
      daily_cap_usd: String(p.daily_cap_usd ?? 50),
      monthly_cap_usd: String(p.monthly_cap_usd ?? 500),
      display_basis_usd: String(p.display_basis_usd ?? 100),
    };
  });
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const poolOn = !!data?.pool_api_configured;
  const feedOn = !!data?.pool_feed_configured;
  const live = !!data?.stake_program_live;

  const set = (k: string, v: string) => setF((prev) => ({ ...prev, [k]: v }));

  const save = async () => {
    setErr('');
    const num = (k: string) => Number(f[k]);
    const payload = {
      k: num('k'),
      q: num('q'),
      g0: num('g0'),
      t_days: Math.round(num('t_days')),
      min_stake_nim: num('min_stake_nim'),
      max_boost_bps: Math.round(num('max_boost_percent') * 100),
      a_max_usd: num('a_max_usd'),
      daily_cap_usd: num('daily_cap_usd'),
      monthly_cap_usd: num('monthly_cap_usd'),
      display_basis_usd: num('display_basis_usd'),
    };
    for (const [k, v] of Object.entries(payload)) {
      if (!Number.isFinite(v as number)) {
        setErr(`"${k}" must be a number.`);
        return;
      }
    }
    if (!(payload.k > 0 && payload.k <= 1)) return setErr('k (share of pool fees credited) must be 0–1.');
    if (!(payload.q >= 0 && payload.q <= 1)) return setErr('q (month-end carry-over) must be 0–1.');
    if (!(payload.g0 >= 0 && payload.g0 <= 1)) return setErr('g0 (fresh-stake multiplier) must be 0–1.');
    if (!(payload.max_boost_bps >= 0)) return setErr('Max boost must be 0% or more.');
    if (!(payload.daily_cap_usd > 0) || !(payload.monthly_cap_usd >= payload.daily_cap_usd))
      return setErr('Daily cap must be positive and monthly cap ≥ daily cap.');
    setBusy(true);
    try {
      // cashback_percent rides along because the endpoint writes the base
      // rate too — sending the programme alone would reset it.
      const saved: any = await adminSetCashback({ cashback_percent: basePct, stake_cashback: payload });
      onChanged({ ...data, ...saved });
      const p = saved.stake_cashback || {};
      setF({
        k: String(p.k ?? 0.8),
        q: String(p.q ?? 0.1),
        g0: String(p.g0 ?? 0.5),
        t_days: String(p.t_days ?? 360),
        min_stake_nim: String(p.min_stake_nim ?? 100),
        max_boost_percent: String((p.max_boost_bps ?? 1000) / 100),
        a_max_usd: String(p.a_max_usd ?? 10),
        daily_cap_usd: String(p.daily_cap_usd ?? 50),
        monthly_cap_usd: String(p.monthly_cap_usd ?? 500),
        display_basis_usd: String(p.display_basis_usd ?? 100),
      });
      toast('Staker programme saved — it applies to orders fulfilled from now on', 'success');
    } catch (e) {
      setErr((e as Error).message || 'Save failed');
    } finally {
      setBusy(false);
    }
  };

  const field = (key: string, label: string, hint: string, step = 'any') => (
    <div className="field" style={{ flex: '1 1 140px', minWidth: '140px' }}>
      <label>{label}</label>
      <input
        className="input"
        type="number"
        step={step}
        min="0"
        value={f[key]}
        onChange={(e) => set(key, e.target.value)}
        aria-label={label}
      />
      <div className="xs faint mt-1">{hint}</div>
    </div>
  );

  return (
    <div className="mt-2" style={{ borderTop: '1px solid var(--line, #e5e7eb)', paddingTop: 12 }}>
      <div className="strong">
        <Icon name="spark" size={15} /> Pool-staker cashback — single ledger
      </div>
      <div className="small muted">
        One rule, no levels: the boost is a share of the pool fees the staker's stake actually earned, on top of the
        base rate. k = share of each fee NIM credited to the staker's ledger; the boost buys rate on the next
        ${f.display_basis_usd || '100'} of spend; a wallet's book can never be overdrawn.
      </div>
      <div className="row mt-2" style={{ gap: '12px', flexWrap: 'wrap' }}>
        {badge('Pool API ' + (poolOn ? 'CONNECTED' : 'MISSING'), poolOn, poolOn ? 'POOL_API_URL set' : 'set POOL_API_URL to verify stakes')}
        {badge('Fee feed ' + (feedOn ? 'Wired' : 'MISSING'), feedOn, feedOn ? 'POOL_FEED_API_KEY set' : 'set POOL_FEED_API_KEY — no feed, no boost')}
        {badge('Programme ' + (live ? 'LIVE' : 'INACTIVE'), live, live ? 'pool + feed ready' : 'needs pool + feed')}
        {badge('Ledgers', (Number(data?.ledger_count) || 0) > 0, (Number(data?.ledger_count) || 0) + ' wallet(s) · feed at batch ' + (Number(data?.ledger_watermark_batch) || 0))}
      </div>
      {!poolOn && (
        <div className="alert warn mt-1">
          <div className="small">POOL_API_URL is not set, so no stake can be verified and every buyer gets the base rate.</div>
        </div>
      )}
      {poolOn && !feedOn && (
        <div className="alert warn mt-1">
          <div className="small">The pool is reachable but the fee feed key is missing — stakers' ledgers cannot accrue, so the boost stays 0%.</div>
        </div>
      )}

      <div className="mt-2" style={{ display: 'grid', gap: 10 }}>
        {field('max_boost_percent', 'Max boost, % on top of base', 'Hard ceiling of the staker rate (10 = up to +10%)', '0.01')}
      </div>
      <div className="row mt-1" style={{ gap: 10, flexWrap: 'wrap' }}>
        {field('min_stake_nim', 'Minimum stake (NIM)', 'Ledger accrues only above this active stake', '1')}
        {field('daily_cap_usd', 'Daily cap ($)', 'Max cashback-eligible spend per day — $50', '1')}
        {field('monthly_cap_usd', 'Monthly cap ($)', 'Max cashback-eligible spend per month — $500', '1')}
      </div>
      <div className="row mt-1" style={{ gap: 10, flexWrap: 'wrap' }}>
        {field('a_max_usd', 'Book ceiling (A_MAX, $)', 'Max NIM a single ledger may hold', '1')}
        {field('display_basis_usd', 'Rate basis ($)', '"The next $100 of spend" display basis', '1')}
      </div>
      <div className="xs faint mt-1 strong">Loyalty engine (advanced)</div>
      <div className="row mt-1" style={{ gap: 10, flexWrap: 'wrap' }}>
        {field('k', 'k — fee share credited', '0–1 · share of each pool-fee NIM to the ledger (0.8)', '0.01')}
        {field('q', 'q — month-end carry-over', '0–1 · fraction of unused monthly accrual carried (0.1 = 10%)', '0.01')}
        {field('g0', 'g0 — fresh-stake multiplier', '0–1 · multiplier at day 0, ramping to 1 (0.5)', '0.01')}
        {field('t_days', 'Loyalty ramp (days)', 'Days of age to reach the full multiplier (360)', '1')}
      </div>

      {err && (
        <div className="alert error mt-1">
          <div className="small">{err}</div>
        </div>
      )}
      <div className="row mt-1" style={{ gap: 8 }}>
        <button className="btn btn-gold" onClick={save} disabled={busy} style={{ marginLeft: 'auto' }}>
          {busy ? 'Saving…' : 'Save staker programme'}
        </button>
      </div>

      <StakeLedgerTable />
    </div>
  );
}

/**
 * StakeLedgerTable — every wallet in the programme with its book: how much
 * cashback money it holds, the boost it buys right now, its loyalty age, and
 * the operator's per-wallet reset.
 */
function StakeLedgerTable() {
  const { toast } = useToast();
  const [rows, setRows] = useState<any[] | null>(null);
  const [nimUsd, setNimUsd] = useState<number | null>(null);
  const [err, setErr] = useState('');
  const [busyAddr, setBusyAddr] = useState('');

  const load = useCallback(() => {
    adminGetStakeLedger()
      .then((d: any) => {
        setRows(d.ledgers || []);
        setNimUsd(Number.isFinite(Number(d.nim_usd)) ? Number(d.nim_usd) : null);
      })
      .catch((e) => setErr((e as Error).message || 'Could not load the staker ledgers'));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const reset = async (address: string) => {
    if (!window.confirm("Reset this wallet's staker ledger? Its accrued book and loyalty age are wiped; the observed stake and today's spend counters survive.")) return;
    setBusyAddr(address);
    try {
      await adminResetStakeLedger(address);
      toast('Ledger reset for ' + formatWalletAddress(address.slice(0, 8)) + '…', 'success');
      load();
    } catch (e) {
      toast((e as Error).message || 'Reset failed', 'error');
    } finally {
      setBusyAddr('');
    }
  };

  const usd = (v: number) => '$' + Number(v || 0).toFixed(2);

  return (
    <div className="mt-2" style={{ borderTop: '1px solid var(--line, #e5e7eb)', paddingTop: 12 }}>
      <div className="row between" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <div className="strong">
          <Icon name="receipt" size={15} /> Staker ledgers
        </div>
        <div className="xs faint">{rows ? rows.length + ' wallet(s)' : ''}{nimUsd ? ` · NIM at $${Number(nimUsd).toFixed(4)}` : ''}</div>
      </div>
      {err ? (
        <div className="alert error mt-1"><div className="small">{err}</div></div>
      ) : !rows ? (
        <div className="small muted mt-1">Loading…</div>
      ) : rows.length === 0 ? (
        <div className="xs faint mt-1">No ledgers yet — one appears per wallet as soon as the pool feed credits its first fee share.</div>
      ) : (
        <div className="mt-1" style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
            <thead>
              <tr style={{ textAlign: 'left', color: 'var(--muted)' }}>
                <th style={{ padding: '6px 8px' }}>Wallet</th>
                <th style={{ padding: '6px 8px' }}>Stake</th>
                <th style={{ padding: '6px 8px' }}>Book</th>
                <th style={{ padding: '6px 8px' }}>Boost</th>
                <th style={{ padding: '6px 8px' }}>Age</th>
                <th style={{ padding: '6px 8px' }}>Spent (day / month)</th>
                <th style={{ padding: '6px 8px' }} />
              </tr>
            </thead>
            <tbody>
              {rows.map((r: any) => (
                <tr key={r.address} style={{ borderTop: '1px solid var(--line)' }}>
                  <td className="mono" style={{ padding: '6px 8px', wordBreak: 'break-all', fontSize: 12 }}>
                    {formatWalletAddress(r.address)}
                  </td>
                  <td style={{ padding: '6px 8px' }}>{Number(r.stake_nim || 0).toLocaleString('en-US')} NIM</td>
                  <td style={{ padding: '6px 8px' }}>
                    {Number(r.ledger_nim || 0).toFixed(2)} NIM
                    {nimUsd ? ` · ${usd(r.ledger_usd)}` : ''}
                  </td>
                  <td style={{ padding: '6px 8px', color: Number(r.boost_bps) > 0 ? 'var(--green)' : 'var(--muted)' }}>
                    +{(Number(r.boost_percent) || 0)}%
                  </td>
                  <td style={{ padding: '6px 8px' }}>{Number(r.loyalty_days || 0)} d</td>
                  <td style={{ padding: '6px 8px' }}>{usd(r.spent_day_usd)} / {usd(r.spent_month_usd)}</td>
                  <td style={{ padding: '6px 8px', textAlign: 'right' }}>
                    <button className="btn btn-ghost btn-sm" onClick={() => reset(r.address)} disabled={busyAddr === r.address}>
                      {busyAddr === r.address ? 'Resetting…' : 'Reset'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
function PromoCodesEditor({ data, onChanged, basePct }: { data: any; onChanged: (d: any) => void; basePct: number }) {
  const { toast } = useToast();
  const [rows, setRows] = useState<Array<{ code: string; pct: string; maxUSD: string }>>(() =>
    (data?.cashback_code_rules || []).map((r: any) => ({
      code: String(r.code || ''),
      pct: String(Number(r.cashback_percent) || ''),
      maxUSD: Number(r.max_order_usd) > 0 ? String(Number(r.max_order_usd)) : '',
    }))
  );
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const setRow = (i: number, patch: Partial<{ code: string; pct: string; maxUSD: string }>) =>
    setRows((list) => list.map((row, j) => (j === i ? { ...row, ...patch } : row)));
  const dropRow = (i: number) => setRows((list) => list.filter((_, j) => j !== i));
  const addRow = () => setRows((list) => [...list, { code: '', pct: '', maxUSD: '' }]);

  const save = async () => {
    setErr('');
    const payload = rows
      .map((r) => ({ code: r.code.trim(), cashback_bps: Math.round(Number(r.pct) * 100), max_order_usd: Number(r.maxUSD) || 0 }))
      .filter((r) => r.code !== '' || r.cashback_bps > 0);
    if (payload.length !== rows.length) {
      setErr('Every promo code needs a code and a cashback percent. Leave the list empty to disable promo codes.');
      return;
    }
    setBusy(true);
    try {
      const saved: any = await adminSetCashback({ cashback_percent: basePct, cashback_code_rules: payload });
      onChanged({ ...data, ...saved });
      setRows(
        (saved.cashback_code_rules || []).map((r: any) => ({
          code: String(r.code || ''),
          pct: String(Number(r.cashback_percent) || ''),
          maxUSD: Number(r.max_order_usd) > 0 ? String(Number(r.max_order_usd)) : '',
        }))
      );
      toast(
        payload.length > 0
          ? 'Promo codes saved (' + payload.length + ' active code' + (payload.length > 1 ? 's' : '') + ')'
          : 'Promo codes disabled',
        'success'
      );
    } catch (e) {
      setErr((e as Error).message || 'Could not save promo codes');
    } finally {
      setBusy(false);
    }
  };

  const ruleViews = data?.cashback_code_rules || [];
  const uses = (data?.cashback_code_uses || []) as any[];
  const envManaged = !data?.cashback_codes_managed && data?.cashback_codes_source === 'env';

  return (
    <div className="mt-2" style={{ borderTop: '1px solid var(--line, #e5e7eb)', paddingTop: 12 }}>
      <div className="strong">
        <Icon name="gift" size={15} /> Promo-code cashback
      </div>
      <div className="small muted">
        Promo codes are backend-owned and exclusive: when a buyer applies one, its rate replaces both the base rate and
        any staker/loyalty cashback for that order.
      </div>
      {envManaged && (
        <div className="alert info mt-1">
          <div className="small">
            Promo codes are currently coming from backend env. The first save here moves control to the admin panel.
          </div>
        </div>
      )}
      <div className="row mt-1" style={{ gap: '12px', flexWrap: 'wrap' }}>
        {badge('Promo codes ' + (ruleViews.length ? 'ON' : 'OFF'), ruleViews.length > 0, ruleViews.length ? ruleViews.length + ' active' : 'no active code')}
        {badge('Source', data?.cashback_codes_source === 'admin', data?.cashback_codes_source === 'admin' ? 'admin managed' : 'env fallback')}
      </div>
      {rows.map((r, i) => (
        <div className="row mt-1" key={i} style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <input
            className="input mono"
            type="text"
            placeholder="NIM10"
            style={{ width: 160 }}
            value={r.code}
            onChange={(e) => setRow(i, { code: e.target.value.toUpperCase() })}
            aria-label={'Promo code ' + (i + 1)}
          />
          <span className="xs faint">→</span>
          <input
            className="input"
            type="number"
            min="0.01"
            max="20"
            step="0.01"
            placeholder="10"
            style={{ width: 110 }}
            value={r.pct}
            onChange={(e) => setRow(i, { pct: e.target.value })}
            aria-label={'Promo code ' + (i + 1) + ' cashback percent'}
          />
          <span className="xs faint">% cashback</span>
          <input
            className="input"
            type="number"
            min="0"
            step="0.01"
            placeholder="No cap"
            style={{ width: 120 }}
            value={r.maxUSD}
            onChange={(e) => setRow(i, { maxUSD: e.target.value })}
            aria-label={'Promo code ' + (i + 1) + ' maximum cart USD'}
          />
          <span className="xs faint">max cart USD</span>
          <button className="btn btn-ghost" onClick={() => dropRow(i)} aria-label={'Remove promo code ' + (i + 1)}>
            <Icon name="x" size={14} />
          </button>
        </div>
      ))}
      <div className="row mt-1" style={{ gap: 8 }}>
        <button className="btn" onClick={addRow} disabled={rows.length >= 24}>
          <Icon name="plus" size={14} /> Add promo code
        </button>
        <button className="btn btn-gold" onClick={save} disabled={busy} style={{ marginLeft: 'auto' }}>
          {busy ? 'Saving…' : 'Save promo codes'}
        </button>
      </div>
      {rows.length === 0 && <div className="xs faint mt-1">No promo codes configured. Leave it empty if cashback should come only from pool staking.</div>}
      {err && <div className="alert error mt-1"><div className="small">{err}</div></div>}

      {ruleViews.length > 0 && (
        <div className="mt-2">
          <div className="xs faint">Active promo codes</div>
          <div style={{ display: 'grid', gap: 8 }}>
            {ruleViews.map((r: any) => (
              <div key={String(r.code)} className="row between" style={{ gap: 10, flexWrap: 'wrap', padding: '10px 0', borderTop: '1px solid var(--line)' }}>
                <div>
                  <div className="strong mono">{r.code}</div>
                  <div className="xs faint">{Number(r.cashback_percent)}% cashback · {Number(r.max_order_usd) > 0 ? `max $${Number(r.max_order_usd).toFixed(2)} cart · ` : ''}{r.usage_count || 0} use(s)</div>
                </div>
                <div className="xs faint">{r.last_used_at ? 'last used ' + new Date(r.last_used_at).toLocaleString() : 'not used yet'}</div>
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="mt-2">
        <div className="xs faint">Recent promo-code uses</div>
        {uses.length ? (
          <div style={{ display: 'grid', gap: 8 }}>
            {uses.slice(0, 20).map((u: any, i: number) => (
              <div key={String(u.quote_id || i)} style={{ padding: '10px 0', borderTop: '1px solid var(--line)' }}>
                <div className="row between" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <div className="small">
                    <span className="mono strong">{String(u.code || '')}</span> · {Number(u.cashback_percent)}% · {String(u.status || 'unknown')}
                  </div>
                  <div className="xs faint">{u.created_at ? new Date(u.created_at).toLocaleString() : '—'}</div>
                </div>
                <div className="xs faint mt-1" style={{ wordBreak: 'break-word' }}>
                  {u.user_address ? `Wallet: ${formatWalletAddress(u.user_address)}` : u.user_id ? `User: ${u.user_id}` : 'Unknown user'}
                  {u.customer_email ? ` · Email: ${u.customer_email}` : ''}
                  {u.quote_id ? ` · Quote: ${u.quote_id}` : ''}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="xs faint mt-1">No promo code has been used yet.</div>
        )}
      </div>
    </div>
  );
}

function OrdersPanel() {
  const { toast } = useToast();
  const [purging, setPurging] = useState(false);
  const [rows, setRows] = useState<any[] | null>(null);
  const [err, setErr] = useState('');
  // Paged like the rest of the console: 5 orders per page.
  const [page, setPage] = useState(0);
  useEffect(() => {
    adminListQuotes(50)
      .then((d) => setRows(d.quotes || []))
      .catch((e) => setErr((e as Error).message || 'Could not load orders'));
  }, []);
  if (!rows && !err) return <div className="card mt-2"><div className="card-title">Orders</div><div className="small muted">Loading…</div></div>;
  if (err) return <div className="card mt-2"><div className="card-title">Orders</div><div className="alert error"><div className="small">{err}</div></div></div>;
  const ORDERS_PAGE_SIZE = 5;
  const pages = Math.max(1, Math.ceil((rows?.length || 0) / ORDERS_PAGE_SIZE));
  const cur = Math.min(page, pages - 1);
  const from = cur * ORDERS_PAGE_SIZE;
  const shown = (rows || []).slice(from, from + ORDERS_PAGE_SIZE);
  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="pulse" size={18} />
        <span>Orders — amount, tx, cashback status</span>
        <button
          type="button"
          className="btn btn-ghost btn-sm"
          style={{ marginLeft: 8 }}
          disabled={purging}
          onClick={() => {
            if (!window.confirm('Delete EVERY order except the real UniPin purchase? Test orders, old quotes, their supplier orders — all gone. Only UniPin stays.')) return;
            setPurging(true);
            adminPurgeTestOrders({ scope: 'all-except-unipin' })
              .then((d: any) => {
                toast(`Deleted ${Number(d?.deleted || 0)} orders (+${Number(d?.deleted_orders || 0)} supplier rows) — UniPin kept`, 'success');
                setRows(null);
                adminListQuotes(50).then((r) => setRows(r.quotes || [])).catch(() => {});
              })
              .catch((e: Error) => toast(e.message || 'Purge failed', 'error'))
              .finally(() => setPurging(false));
          }}
        >
          Delete all orders except UniPin
        </button>
        <span className="xs faint" style={{ marginLeft: 'auto' }}>
          {rows && rows.length > ORDERS_PAGE_SIZE
            ? `${from + 1}–${Math.min(rows.length, from + ORDERS_PAGE_SIZE)} of ${rows.length} orders`
            : `${rows?.length || 0} orders`}
        </span>
      </div>
      <div className="small muted">Each purchase (quote) with whether cashback was paid, how much NIM, and the on-chain tx hash. Cashback is queued only after fulfillment.</div>
      {shown.length ? (
        <div className="mt-1">
          {shown.map((q: any, i: number) => (
            <CashbackRow key={from + i} cb={q.cashback} extraLeft={(q.test_mode ? '🧪 TEST · ' : '') + (q.product_id || 'item') + ' · ' + (q.status || '') + (q.id ? ' · ' + String(q.id).slice(0, 8) : '')} />
          ))}
        </div>
      ) : (
        <div className="xs faint mt-2">No orders yet.</div>
      )}
      {rows && rows.length > ORDERS_PAGE_SIZE ? (
        <div className="row" style={{ gap: 8, justifyContent: 'flex-end', alignItems: 'center', marginTop: 10 }}>
          <span className="xs faint mono">
            page {cur + 1} / {pages}
          </span>
          <button className="btn btn-ghost btn-sm" disabled={cur === 0} onClick={() => setPage(cur - 1)} aria-label="Previous orders">
            ‹ Prev
          </button>
          <button className="btn btn-ghost btn-sm" disabled={cur >= pages - 1} onClick={() => setPage(cur + 1)} aria-label="Next orders">
            Next ›
          </button>
        </div>
      ) : null}
    </div>
  );
}

function splitRuleList(value: string): string[] {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function ruleOptionsWithCurrent(options: RuleOption[], current: string[]): RuleOption[] {
  const known = new Set(options.map((option) => option.value.toLowerCase()));
  const extra = current
    .filter((value) => !known.has(value.toLowerCase()))
    .map((value) => ({ value, label: `Other: ${value}` }));
  return [...options, ...extra];
}

function RuleMultiPicker({
  label,
  hint,
  options,
  value,
  onChange,
  searchable = false,
}: {
  label: string;
  hint: string;
  options: RuleOption[];
  value: string;
  onChange: (value: string) => void;
  searchable?: boolean;
}) {
  const [query, setQuery] = useState('');
  const current = splitRuleList(value);
  const selected = new Set(current.map((item) => item.toLowerCase()));
  const allOptions = ruleOptionsWithCurrent(options, current);
  const filtered = allOptions.filter((option) => {
    const q = query.trim().toLowerCase();
    return !q || option.label.toLowerCase().includes(q) || option.value.toLowerCase().includes(q);
  });

  const toggle = (option: RuleOption, checked: boolean) => {
    const next = current.filter((item) => item.toLowerCase() !== option.value.toLowerCase());
    if (checked) next.push(option.value);
    onChange(next.join(', '));
  };

  return (
    <div className="admin-rule-picker">
      <div className="admin-rule-picker-head">
        <div>
          <div className="strong">{label}</div>
          <div className="xs faint mt-1">{hint}</div>
        </div>
        <span className="xs faint admin-rule-count">{selected.size} selected</span>
      </div>
      {searchable ? (
        <input
          className="input admin-rule-search"
          type="search"
          value={query}
          placeholder="Search by country name or code"
          onChange={(e) => setQuery(e.target.value)}
          aria-label={`Search ${label}`}
        />
      ) : null}
      <div className="admin-rule-option-list" role="group" aria-label={label}>
        {filtered.map((option) => {
          const checked = selected.has(option.value.toLowerCase());
          return (
            <label key={option.value} className={'admin-rule-option' + (checked ? ' is-selected' : '')}>
              <input type="checkbox" checked={checked} onChange={(e) => toggle(option, e.target.checked)} />
              <span className="admin-rule-option-name">{option.label}</span>
              <span className="admin-rule-option-code">{option.value}</span>
            </label>
          );
        })}
        {!filtered.length ? <div className="xs faint admin-rule-empty">No matching options.</div> : null}
      </div>
    </div>
  );
}

function CatalogRulesPanel() {
  const [rules, setRules] = useState<any | null>(null);
  const [err, setErr] = useState('');
  const [saveErr, setSaveErr] = useState('');
  const [cap, setCap] = useState('');
  const [hidden, setHidden] = useState('');
  const [bannedCat, setBannedCat] = useState('');
  const [bannedKind, setBannedKind] = useState('');
  const [hiddenCC, setHiddenCC] = useState('');
  const [visibleCC, setVisibleCC] = useState('');
  const [oosPolicy, setOosPolicy] = useState('show');
  const { toast } = useToast();

  useEffect(() => {
    adminCatalogRules()
      .then((r) => {
        setRules(r);
        setCap(r.max_face_value_usd > 0 ? String(r.max_face_value_usd) : '');
        setHidden((r.hidden_families || []).join('\n'));
        setBannedCat((r.banned_categories || []).join(', '));
        setBannedKind((r.banned_kinds || []).join(', '));
        setHiddenCC((r.hidden_countries || []).join(', '));
        setVisibleCC((r.visible_countries || []).join(', '));
        setOosPolicy((r.out_of_stock_policy || 'show') === 'hide' ? 'hide' : 'show');
      })
      .catch((e) => setErr((e as Error).message || 'Could not load catalog rules'));
  }, []);

  const splitList = splitRuleList;

  const save = async () => {
    const payload = {
      max_face_value_usd: Math.max(0, parseFloat(cap) || 0),
      hidden_families: splitList(hidden),
      banned_categories: splitList(bannedCat),
      banned_kinds: splitList(bannedKind).map((x) => x.toLowerCase()),
      hidden_countries: splitList(hiddenCC).map((x) => x.toUpperCase()),
      visible_countries: splitList(visibleCC).map((x) => x.toUpperCase()),
      out_of_stock_policy: oosPolicy === 'hide' ? 'hide' : 'show',
    };
    try {
      const saved = await adminCatalogRules({ method: 'PUT', body: payload });
      setRules(saved);
      setSaveErr('');
      toast('Catalog rules saved (cap ' + (saved.max_face_value_usd > 0 ? '$' + saved.max_face_value_usd : 'off') + ')', 'success');
    } catch (e) {
      setSaveErr((e as Error).message || 'Save failed');
    }
  };

  if (!rules && !err) return <div className="card mt-2"><div className="card-title">Catalog rules</div><div className="small muted">Loading…</div></div>;
  if (err) return <div className="card mt-2"><div className="card-title">Catalog rules</div><div className="alert error"><div className="small">{err}</div></div></div>;

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="lock" size={18} />
        <span>Catalog rules — price cap & visibility</span>
      </div>
      <div className="field">
        <label>Price cap — USD per order (applies in every country's own unit)</label>
        <input className="input" type="number" min="0" step="1" value={cap} placeholder="0 = off" onChange={(e) => setCap(e.target.value)} />
        <div className="xs faint mt-1">Example: 20 → a "150.000 IDR" card (≈$10) stays visible, "500.000 IDR" (≈$33) is hidden — conversion is automatic for all 160+ currencies. 0 disables the cap.</div>
      </div>
      <div className="field mt-1">
        <label>Hidden brands (one per line)</label>
        <textarea className="input" rows={3} placeholder="One brand per line" value={hidden} onChange={(e) => setHidden(e.target.value)} />
      </div>
      <div className="row mt-1 admin-rule-picker-row">
        <RuleMultiPicker
          label="Blocked categories"
          hint="Select category names to hide. Empty = no category blocked."
          options={CATALOG_CATEGORY_OPTIONS}
          value={bannedCat}
          onChange={setBannedCat}
        />
        <RuleMultiPicker
          label="Blocked kinds"
          hint="Select the product kind by name. Empty = all kinds allowed."
          options={CATALOG_KIND_OPTIONS}
          value={bannedKind}
          onChange={setBannedKind}
        />
      </div>
      <div className="row mt-1 admin-rule-picker-row">
        <RuleMultiPicker
          label="Hidden countries"
          hint="Select countries to hide from the catalog. Empty = no country hidden."
          options={ADMIN_COUNTRY_OPTIONS}
          value={hiddenCC}
          onChange={setHiddenCC}
          searchable
        />
        <RuleMultiPicker
          label="Allowed countries"
          hint="Optional allow-list. Empty = all countries allowed."
          options={ADMIN_COUNTRY_OPTIONS}
          value={visibleCC}
          onChange={setVisibleCC}
          searchable
        />
      </div>
      <div className="field mt-1">
        <label>Global out-of-stock display</label>
        <select className="input" value={oosPolicy} onChange={(e) => setOosPolicy(e.target.value)}>
          <option value="show">Show all products, mark out of stock</option>
          <option value="hide">Hide out-of-stock products globally</option>
        </select>
        <div className="xs faint mt-1">This controls the public catalog and all country listings. The visible option keeps the product card but marks it unavailable; the hidden option removes it.</div>
      </div>
      {saveErr ? (
        <div className="alert error mt-1">
          <div className="small">{saveErr}</div>
        </div>
      ) : null}
      <button className="btn btn-gold btn-block btn-lg mt-2" onClick={save}>
        Save catalog rules
      </button>
    </div>
  );
}

function SessionCard({ me, onLoggedOut, onSignIn }: any) {
  const { toast } = useToast();
  const isLive = !!me;
  return (
    <div className="card" style={{ marginTop: '0' }}>
      <div className="row between" style={{ flexWrap: 'wrap', gap: '10px' }}>
        <div>
          <div className="card-title">
            <Icon name="shield" size={14} />
            <span>Admin session</span>
          </div>
          {isLive ? (
            <div>
              <div className="strong">{me.admin?.username || 'admin'}</div>
              <div className="xs faint">Session expires: {me.expires_at ? new Date(me.expires_at).toLocaleString() : '—'}</div>
            </div>
          ) : (
            <div className="small muted">Not signed in. Sign in below to send notifications.</div>
          )}
        </div>
        {isLive ? (
          <button
            className="btn btn-ghost btn-sm"
            onClick={async () => {
              try {
                await adminLogout();
              } catch {}
              toast('Admin session ended', 'info');
              onLoggedOut();
            }}
          >
            <Icon name="logout" size={14} />
            <span className="btn-label">Sign out</span>
          </button>
        ) : null}
      </div>
      {!isLive ? (
        <button className="btn btn-gold mt-2" onClick={onSignIn}>
          <Icon name="shield" size={18} />
          <span className="btn-label">Sign in to admin console</span>
        </button>
      ) : null}
    </div>
  );
}

export function AdminContent() {
  const [me, setMe] = useState<any>(null);
  const [ready, setReady] = useState(false);
  const [status, setStatus] = useState<any>(null);
  const [section, setSection] = useState<AdminSection>('overview');
  const { openSheet } = useSheet();

  const refresh = useCallback(async () => {
    let m: any = null;
    try {
      m = await adminMe();
    } catch {}
    setMe(m);
    setReady(true);
    try {
      setStatus(await adminStatus());
    } catch {}
  }, []);

  useEffect(() => {
    setSection(initialAdminSection());
    refresh();
  }, [refresh]);

  if (!ready) {
    return (
      <div className="container">
        <AdminHeader />
        <div className="card">
          <div className="small muted">Loading…</div>
        </div>
      </div>
    );
  }

  const openLogin = () => {
    openSheet({ title: 'Admin login', wide: false, render: () => <AdminLoginSheet onSuccess={() => { refresh(); }} /> });
  };

  const goToSection = (next: AdminSection) => {
    setSection(next);
    if (typeof window !== 'undefined') {
      const url = new URL(window.location.href);
      url.searchParams.set('section', next);
      window.history.replaceState({}, '', url.pathname + '?' + url.searchParams.toString() + url.hash);
    }
  };

  const activeMeta = ADMIN_SECTIONS.find((item) => item.id === section) || ADMIN_SECTIONS[0];
  const activePanel = (() => {
    switch (section) {
      case 'catalog':
        return <CatalogRulesPanel />;
      case 'orders':
        return <OrdersPanel />;
      case 'cashback':
        return <CashbackPanel />;
      case 'people':
        return <UsersPanel />;
      case 'email':
        return <><StatusCard status={status} /><Composer status={status} /><TestEmailCard /></>;
      case 'overview':
      default:
        return <><PlayersPanel /><StatusCard status={status} /></>;
    }
  })();

  return (
    <div className="container admin-console">
      <AdminHeader />
      <SessionCard me={me} onLoggedOut={() => { setMe(null); setSection('overview'); }} onSignIn={openLogin} />
      {me ? (
        <>
          <AdminSectionNav active={section} onChange={goToSection} />
          <div className="admin-section-heading">
            <div>
              <h2>{activeMeta.label}</h2>
              <div className="small muted">{activeMeta.hint} · one operator page at a time</div>
            </div>
            <span className="chip xs">{ADMIN_SECTIONS.findIndex((item) => item.id === section) + 1} / {ADMIN_SECTIONS.length}</span>
          </div>
          {activePanel}
        </>
      ) : (
        <>
          {/* Signed-out: say what this page IS and how to get in, instead of
              showing panels that would just 403 and read as "broken admin". */}
          <div className="card mt-2">
            <div className="strong">Operator console</div>
            <div className="small muted mt-1">
              Sign in to manage orders & quotes, direct email notifications, cashback & the stake
              ledger, catalog rules and people. The support inbox is retired —
              buyers are routed to the FAQ and the supplier. Access uses a separate
              operator login — the customer wallet session is not enough.
            </div>
            <button className="btn btn-gold mt-2" onClick={openLogin}>
              <Icon name="lock" size={15} /> Operator sign-in
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function AdminHeader() {
  return (
    <div className="row between mt-2" style={{ flexWrap: 'wrap', gap: '12px', alignItems: 'center' }}>
      <div>
        <h1 style={{ margin: 0, display: 'flex', alignItems: 'center', gap: '10px' }}>
          <Icon name="shield" size={24} /> Operator console
        </h1>
        <div className="xs faint mt-1">Catalog, test orders, cashback, people and Mailtrap tools — separate from the customer wallet login.</div>
      </div>
      <a className="btn btn-ghost btn-sm" href={pagePath("/")}>
        <Icon name="back" size={14} />
        <span className="btn-label">Back to shop</span>
      </a>
    </div>
  );
}

export function AdminPage() {
  return (
    <AppRoot activeKey="none">
      <AdminContent />
    </AppRoot>
  );
}
