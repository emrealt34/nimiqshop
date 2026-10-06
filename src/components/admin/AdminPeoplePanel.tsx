/**
 * AdminPeoplePanel — the operator console's customer side.
 *
 * Three things an operator asks every morning, answered from the same Go
 * endpoints the rest of the console uses:
 *
 *   1. Players              — how many customers, how many are new, how many
 *                             actually did something today / this week.
 *   2. Cashback payment     — is the payout pipeline moving? pending, retrying,
 *      queue                  sent, failed, and how long the oldest row waited.
 *   3. Customers            — every buyer sortable by register time, orders,
 *                             spending and cashback earned; a row opens their
 *                             full activity timeline (spun, bought, paid,
 *                             expired, cashback paid, ticket…).
 *
 * Sorting is server-side over every registered buyer, then the page is cut —
 * sorting one page of 50 would only reorder those 50 and hide the real top
 * spender.
 */
import { useCallback, useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { AlertBox } from '../ui/uiKit';
import { useSheet } from '../AppProviders';
import { adminDashboard, adminListUsers, adminUserDetail } from '../../lib/api';
import { fmtNIM, fmtUSD, shortAddr, timeAgo, fmtDate, countryName, formatWalletAddress, flag } from '../../lib/format';
import { Clipboard } from '../../lib/clipboard';

/* ---------------- shared bits ---------------- */

/**
 * The customer's origin, shown where the operator asked for it: on the person
 * row and again — in full, with the exact address and a copy button — when the
 * person is opened ("kişi ip adresi ülke de yazsa, basınca o yerde daha güzel
 * olur").
 *
 * The stored address is the last OBSERVED client IP (the same value the
 * checkout hands the supplier, resolved through the proxy policy) together
 * with Cloudflare's CF-IPCountry for the same request. It is recorded on sign
 * in, on every session restore (so someone who only browses is not invisible),
 * on the presence heartbeat and on both checkout paths — see
 * backend/internal/handlers/presence_note.go.
 *
 * A /64 IPv6 address is 39 characters and would push the row's other facts off
 * the line, so long addresses are clipped in the middle: the network prefix
 * and the host part stay readable, which is what an operator matches against.
 */
function shortIP(ip: unknown): string {
  const s = String(ip || '').trim();
  return s.length <= 26 ? s : s.slice(0, 15) + '…' + s.slice(-8);
}

/** Country + flag as one readable phrase, falling back to the raw code. */
function originCountry(cc: unknown): string {
  const code = String(cc || '').trim();
  if (!code) return '';
  return countryName(code) || code;
}

function num(v: unknown): number {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

function fmtPct(v: unknown, digits = 2): string {
  const n = num(v);
  return n.toFixed(digits).replace(/\.?0+$/, '') + '%';
}

function rateSourceLabel(src?: string, code?: string): string {
  if (code) return 'promo ' + code;
  switch (src) {
    case 'staker':
    case 'ledger':
      return 'staker boost';
    case 'code':
      return 'promo';
    case 'base':
      return 'base';
    default:
      return src || 'base';
  }
}

function StatTile({ label, value, hint, tone }: { label: string; value: string | number; hint?: string; tone?: 'good' | 'warn' | 'bad' | 'plain' }) {
  const color =
    tone === 'good' ? 'var(--green)' : tone === 'warn' ? 'var(--stamp)' : tone === 'bad' ? 'var(--red)' : 'var(--ink)';
  return (
    <div className="astat">
      <div className="astat-label">{label}</div>
      <div className="astat-value" style={{ color }}>
        {value}
      </div>
      {hint ? <div className="astat-hint">{hint}</div> : null}
    </div>
  );
}

function fmtClock(iso?: string | null): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  return sameDay ? `${hh}:${mm}` : `${d.getDate()}/${d.getMonth() + 1} ${hh}:${mm}`;
}

/* ---------------- Players + payment queue ---------------- */

export function PlayersPanel() {
  const [data, setData] = useState<any | null>(null);
  const [err, setErr] = useState('');

  const load = useCallback(() => {
    adminDashboard()
      .then((d) => setData(d))
      .catch((e) => setErr((e as Error).message || 'Could not load the dashboard'));
  }, []);
  useEffect(load, [load]);

  if (err) {
    return (
      <div className="card mt-2">
        <div className="card-title">
          <Icon name="user" size={16} />
          <span>Players</span>
        </div>
        <AlertBox type="error">{err}</AlertBox>
      </div>
    );
  }

  const p = data?.players || {};
  const q = data?.cashback_queue || {};
  const totals = q.totals || {};
  const pending = q.pending || {};
  const retrying = q.retrying || {};
  const failed = q.failed || {};

  return (
    <>
      <div className="card mt-2">
        <div className="card-title">
          <Icon name="user" size={18} />
          <span>Players</span>
          <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={load} aria-label="Refresh players">
            <Icon name="refresh" size={14} />
          </button>
        </div>
        <div className="xs faint mt-1">Daily activity window: {p.today_start ? fmtDate(p.today_start) : 'today'} UTC</div>
        <div className="astat-grid">
          <StatTile label="Total registered" value={num(p.total_registered)} hint="every wallet that ever signed in" />
          <StatTile label="Active today" value={num(p.active_today)} tone={num(p.active_today) > 0 ? 'good' : 'plain'} hint="did something since 00:00 UTC" />
          <StatTile label="Active this week" value={num(p.active_this_week)} tone="good" hint={`rolling ${num(p.week_window_days) || 7} days`} />
          <StatTile label="New today" value={num(p.new_today)} hint="first sign-in today" />
          <StatTile label="New this week" value={num(p.new_this_week)} hint={`rolling ${num(p.week_window_days) || 7} days`} />
        </div>
        <div className="row mt-1" style={{ gap: 8, flexWrap: 'wrap' }}>
          <span className="chip xs">{num(p.with_orders)} bought at least once</span>
          <span className="chip xs">
            <Icon name="spark" size={12} /> {num(p.stakers)} staking in your pool
          </span>
        </div>
      </div>

      <div className="card mt-2">
        <div className="card-title">
          <Icon name="wallet" size={18} />
          <span>Cashback payment queue</span>
        </div>
        <div className="small muted">
          Every NIM payout the shop wallet owes. A row is queued the moment an order is fulfilled, then signed,
          broadcast and confirmed on-chain.
        </div>
        <div className="astat-grid">
          <StatTile
            label="Pending"
            value={num(pending.count)}
            tone={num(pending.count) > 0 ? 'warn' : 'plain'}
            hint={num(pending.count) ? fmtNIM(num(pending.amount_nim), 2) + ' NIM waiting' : 'nothing waiting'}
          />
          <StatTile
            label="Retrying"
            value={num(retrying.count)}
            tone={num(retrying.last_error_count) > 0 ? 'bad' : 'plain'}
            hint={num(retrying.last_error_count) ? num(retrying.last_error_count) + ' hit an RPC error' : 'signing / broadcasting'}
          />
          <StatTile label="Sent" value={num(totals.sent_count)} tone="good" hint={fmtNIM(num(totals.sent_nim), 2) + ' NIM on-chain'} />
          <StatTile
            label="Failed"
            value={num(failed.count)}
            tone={num(failed.count) > 0 ? 'bad' : 'plain'}
            hint={num(failed.count) ? 'never payable — bad address or 0%' : 'none'}
          />
        </div>
        <div className="row mt-1" style={{ gap: 8, flexWrap: 'wrap' }}>
          <span className="chip xs">
            In flight {num(totals.in_flight_count)} · {fmtNIM(num(totals.in_flight_nim), 2)} NIM
          </span>
          <span className="chip xs">
            <Icon name="spark" size={12} /> {num(totals.boosted_count)} paid at a pool-staker rate
          </span>
          {totals.oldest_pending_at ? (
            <span className="chip xs">
              <Icon name="clock" size={12} /> oldest payout queued {timeAgo(totals.oldest_pending_at)}
            </span>
          ) : null}
        </div>
        <div className="xs faint mt-1">
          "Sent" counts payouts that reached the chain (confirming + confirmed). "Failed" counts rows that were never
          payable — no recipient address, or a 0% rate — not transfers the network rejected.
        </div>
      </div>
    </>
  );
}

/* ---------------- Customer list ---------------- */

type SortKey = 'registered' | 'orders' | 'spend' | 'cashback' | 'last_seen';

const SORTS: Array<{ key: SortKey; label: string }> = [
  { key: 'registered', label: 'Register time' },
  { key: 'orders', label: 'Orders' },
  { key: 'spend', label: 'Spending' },
  { key: 'cashback', label: 'Cashback earned' },
  { key: 'last_seen', label: 'Last seen' },
];

export function UsersPanel() {
  const [rows, setRows] = useState<any[] | null>(null);
  const [err, setErr] = useState('');
  const [sort, setSort] = useState<SortKey>('registered');
  const [dir, setDir] = useState<'asc' | 'desc'>('desc');
  const { openSheet } = useSheet();

  useEffect(() => {
    setRows(null);
    adminListUsers(sort, dir, 100)
      .then((d: any) => setRows(d.users || []))
      .catch((e) => setErr((e as Error).message || 'Could not load customers'));
  }, [sort, dir]);

  const toggle = (key: SortKey) => {
    if (key === sort) setDir((d) => (d === 'desc' ? 'asc' : 'desc'));
    else {
      setSort(key);
      setDir('desc');
    }
  };

  const openUser = (id: string, address: string) => {
    openSheet({
      title: 'Customer · ' + shortAddr(address, 6, 4),
      wide: true,
      render: () => <UserDetail userId={id} />,
    });
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="user" size={18} />
        <span>Customers</span>
        <span className="xs faint" style={{ marginLeft: 'auto' }}>
          {rows ? rows.length + ' shown' : '…'}
        </span>
      </div>
      <div className="small muted">
        Tap a column to sort, tap a customer for their whole history — purchases, payments, expiries, cashback
        payouts and support tickets.
      </div>

      <div className="row mt-1 ausr-sorts" style={{ gap: 6, flexWrap: 'wrap' }}>
        {SORTS.map((s) => (
          <button
            key={s.key}
            className={'btn btn-sm ' + (sort === s.key ? 'btn-gold' : 'btn-ghost')}
            onClick={() => toggle(s.key)}
            aria-pressed={sort === s.key}
          >
            {s.label}
            {sort === s.key ? <span aria-hidden="true">{dir === 'desc' ? ' ↓' : ' ↑'}</span> : null}
          </button>
        ))}
      </div>

      {err ? <AlertBox type="error">{err}</AlertBox> : null}
      {!rows && !err ? <div className="small muted mt-1">Loading…</div> : null}
      {rows && rows.length === 0 ? <div className="xs faint mt-2">No customers registered yet.</div> : null}

      {rows && rows.length ? (
        <div className="ausr-list mt-1">
          {rows.map((u: any) => (
            <button key={u.id} className="ausr-row" onClick={() => openUser(u.id, u.nimiq_address)}>
              <Identicon address={u.nimiq_address} className="identicon ausr-ava" size={30} />
              <span className="ausr-main">
                <span className="ausr-addr mono">{shortAddr(u.nimiq_address, 8, 6)}</span>
                <span className="ausr-sub">
                  {u.last_country ? flag(u.last_country) + ' ' + originCountry(u.last_country) + ' · ' : ''}
                  {u.last_ip ? <span className="mono">{shortIP(u.last_ip)} · </span> : null}
                  joined {u.created_at ? fmtDate(u.created_at, false) : '—'}
                  {u.last_seen_at ? ' · seen ' + timeAgo(u.last_seen_at) : ''}
                </span>
              </span>
              <span className="ausr-metrics">
                <span className="ausr-metric">
                  <b style={{ color: num(u.live_percent) > num(u.base_percent) ? 'var(--green)' : undefined }}>
                    {fmtPct(u.live_percent ?? u.last_cashback_percent)}
                  </b>
                  <i>{rateSourceLabel(u.live_source, u.last_cashback_code)}</i>
                </span>
                <span className="ausr-metric">
                  <b>{num(u.order_count)}</b>
                  <i>orders</i>
                </span>
                <span className="ausr-metric">
                  <b>{fmtUSD(num(u.spend_usd), { compact: true })}</b>
                  <i>spent</i>
                </span>
                <span className="ausr-metric">
                  <b>{fmtNIM(num(u.cashback_paid_nim) + num(u.cashback_pending_nim), 2)}</b>
                  <i>NIM back</i>
                </span>
                <span className="ausr-metric">
                </span>
              </span>
              <Icon name="chevron" size={16} />
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/* ---------------- One customer: stats + timeline ---------------- */

const TIMELINE_META: Record<string, { icon: string; tone: string }> = {
  registered: { icon: 'user', tone: 'plain' },
  seen: { icon: 'eye', tone: 'plain' },
  order_created: { icon: 'bag', tone: 'plain' },
  supplier_order: { icon: 'server', tone: 'plain' },
  paid: { icon: 'wallet', tone: 'good' },
  delivered: { icon: 'check', tone: 'good' },
  cashback_queued: { icon: 'clock', tone: 'stamp' },
  cashback_paid: { icon: 'nimiq', tone: 'good' },
  cashback_skipped: { icon: 'x', tone: 'faint' },
  expired: { icon: 'clock', tone: 'bad' },
  refunded: { icon: 'alert', tone: 'bad' },
  manual_review: { icon: 'alert', tone: 'warn' },
  rated: { icon: 'star', tone: 'stamp' },
  support_opened: { icon: 'headset', tone: 'warn' },
  support_update: { icon: 'message', tone: 'plain' },
};

function UserDetail({ userId }: { userId: string }) {
  const [data, setData] = useState<any | null>(null);
  const [err, setErr] = useState('');
  // Activity timeline is paged — 5 events per page, newest first.
  const [evPage, setEvPage] = useState(0);
  const [ipCopied, setIpCopied] = useState(false);

  useEffect(() => {
    setEvPage(0);
  }, [userId]);

  useEffect(() => {
    adminUserDetail(userId)
      .then(setData)
      .catch((e) => setErr((e as Error).message || 'Could not load this customer'));
  }, [userId]);

  if (err) return <AlertBox type="error">{err}</AlertBox>;
  if (!data) return <div className="small muted">Loading…</div>;

  const u = data.user || {};
  const s = data.stats || {};
  const st = data.staking || {};
  const rate = data.rate || {};
  const ledger = data.ledger || {};
  const cashbacks: any[] = data.cashbacks || [];
  const timeline: any[] = data.timeline || [];

  // Pager math: 5 events per page, clamped so a stale page index (e.g. after
  // a refresh shrinks the list) never renders an empty slice.
  const EV_PAGE_SIZE = 5;
  const evPages = Math.max(1, Math.ceil(timeline.length / EV_PAGE_SIZE));
  const evCur = Math.min(evPage, evPages - 1);
  const evFrom = evCur * EV_PAGE_SIZE;
  const evShown = timeline.slice(evFrom, evFrom + EV_PAGE_SIZE);

  return (
    <div className="mt-1">
      <div className="row" style={{ gap: 10, alignItems: 'center' }}>
        <Identicon address={u.nimiq_address} className="identicon" size={38} />
        <div>
          <div className="mono strong">{formatWalletAddress(u.nimiq_address)}</div>
          <div className="xs faint">Joined {u.created_at ? fmtDate(u.created_at) : '—'}</div>
        </div>
      </div>

      {/* Where this person is coming from — the clicked-open place for it. */}
      <div className="anet mt-2">
        <div className="anet-head">
          <Icon name="pulse" size={13} />
          <span>Network</span>
          {u.last_seen_at ? <span className="xs faint">seen {timeAgo(u.last_seen_at)} · {fmtDate(u.last_seen_at)}</span> : null}
        </div>
        <div className="anet-row">
          {u.last_country ? (
            <span className="anet-cc">
              <span className="anet-flag" aria-hidden="true">{flag(u.last_country)}</span>
              {originCountry(u.last_country)}
            </span>
          ) : (
            <span className="anet-cc xs faint">Country not observed yet</span>
          )}
          {u.last_ip ? (
            <>
              <span className="anet-ip mono">{shortIP(u.last_ip)}</span>
              <button
                type="button"
                className="btn btn-ghost btn-sm"
                title={String(u.last_ip)}
                onClick={() => {
                  if (Clipboard.copy(String(u.last_ip))) {
                    setIpCopied(true);
                    window.setTimeout(() => setIpCopied(false), 1500);
                  }
                }}
              >
                <Icon name="copy" size={13} />
                <span className="btn-label">{ipCopied ? 'Copied' : 'Copy'}</span>
              </button>
            </>
          ) : (
            <span className="anet-cc xs faint">No address recorded yet</span>
          )}
        </div>
        <div className="anet-hint xs faint">
          {u.last_ip
            ? 'Last observed client address and the country reported with it — noted on sign in, session restore, the heartbeat and every checkout.'
            : 'The address is recorded the next time this account signs in or opens the shop.'}
        </div>
      </div>

      <div className="astat-grid mt-2">
        <StatTile
          label="Live cashback rate"
          value={fmtPct(rate.live_percent)}
          hint={
            num(rate.boost_percent)
              ? `base ${fmtPct(rate.base_percent)} + staker ${fmtPct(rate.boost_percent)}`
              : `shop base ${fmtPct(rate.base_percent)}`
          }
          tone={num(rate.boost_percent) > 0 ? 'good' : 'plain'}
        />
        <StatTile label="Orders" value={num(s.order_count)} hint={num(s.quote_count) + ' purchase attempts'} />
        <StatTile label="Spending" value={fmtUSD(num(s.spend_usd))} hint="paid and delivered" />
        <StatTile label="Cashback paid" value={fmtNIM(num(s.cashback_paid_nim), 3)} hint={num(s.cashback_count) + ' payout(s)'} tone="good" />
        <StatTile
          label="Cashback pending"
          value={fmtNIM(num(s.cashback_pending_nim), 3)}
          hint={num(s.cashback_skipped) + ' skipped'}
          tone={num(s.cashback_pending_nim) > 0 ? 'warn' : 'plain'}
        />
      </div>

      <div className="row mt-1 admin-chips" style={{ gap: 8, flexWrap: 'wrap' }}>
        <span className="chip xs">
          <Icon name="spark" size={12} /> {rateSourceLabel(rate.live_source)} · next order without a promo
        </span>
        {ledger.has_ledger ? (
          <span className="chip xs">
            Ledger {fmtUSD(num(ledger.available_cashback_usd))} · boost {fmtPct(ledger.boost_percent)} · {num(ledger.loyalty_days)}d loyalty
          </span>
        ) : null}
        <span className="chip xs">
          <Icon name="spark" size={12} /> {num(s.cashback_boosted_count)} paid at the staker rate
        </span>
        <span className="chip xs">
          {st.staked
            ? `Staking ${fmtNIM(num(st.stake_nim), 0)} NIM · ${num(st.locked_days)}d locked`
            : st.pool_checked
              ? 'Not staking in your pool'
              : 'Pool not configured'}
        </span>
      </div>

      {cashbacks.length ? (
        <>
          <div className="card-title mt-2" style={{ marginTop: 18 }}>
            <Icon name="wallet" size={16} />
            <span>Cashback payouts — locked rate per order</span>
          </div>
          <div className="xs faint">The % is frozen at fulfill time. Promo codes replace base+staker for that order.</div>
          <div className="mt-1">
            {cashbacks.map((cb: any) => (
              <div key={cb.id || cb.quote_id} style={{ padding: '10px 0', borderTop: '1px solid var(--line)' }}>
                <div className="row between" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <div className="small">
                    <span className="strong">{fmtPct(cb.cashback_percent ?? cb.bps / 100)}</span>
                    {' · '}
                    {rateSourceLabel(cb.cashback_source, cb.cashback_code)}
                    {cb.boosted ? ' · boosted' : ''}
                    {' · '}
                    {fmtNIM(num(cb.amount_nim), 3)} NIM
                  </div>
                  <div className="xs faint">{cb.status}{cb.paid_at ? ' · ' + timeAgo(cb.paid_at) : cb.created_at ? ' · ' + timeAgo(cb.created_at) : ''}</div>
                </div>
                <div className="xs faint mt-1" style={{ wordBreak: 'break-word' }}>
                  {cb.product_id || 'item'}
                  {cb.cashback_code ? ` · code ${cb.cashback_code}` : ''}
                  {cb.stake_nim ? ` · stake ${fmtNIM(num(cb.stake_nim), 0)} NIM` : ''}
                  {cb.tx_hash ? ` · ${cb.tx_hash}` : ''}
                </div>
              </div>
            ))}
          </div>
        </>
      ) : null}

      <div className="card-title mt-2" style={{ marginTop: 18 }}>
        <Icon name="history" size={16} />
        <span>Activity</span>
        <span className="xs faint" style={{ marginLeft: 'auto' }}>
          {timeline.length > EV_PAGE_SIZE
            ? `${evFrom + 1}–${Math.min(timeline.length, evFrom + EV_PAGE_SIZE)} of ${timeline.length} events`
            : `${timeline.length} events`}
        </span>
      </div>

      {timeline.length === 0 ? <div className="xs faint">Nothing recorded yet.</div> : null}

      <ol className="atl">
        {evShown.map((e, i) => {
          const meta = TIMELINE_META[e.kind] || { icon: 'pulse', tone: 'plain' };
          return (
            <li key={evFrom + i} className={'atl-row tone-' + meta.tone}>
              <span className="atl-time mono">{fmtClock(e.at)}</span>
              <span className="atl-dot">
                <Icon name={meta.icon as any} size={13} />
              </span>
              <span className="atl-body">
                <span className="atl-label">{e.label}</span>
                {e.detail ? <span className="atl-detail">{e.detail}</span> : null}
                <span className="atl-meta">
                  {e.amount_nim ? fmtNIM(num(e.amount_nim), 2) + ' NIM · ' : ''}
                  {e.status ? e.status : ''}
                  {e.ref ? ' · ' + String(e.ref).slice(0, 8) : ''}
                </span>
              </span>
            </li>
          );
        })}
      </ol>

      {timeline.length > EV_PAGE_SIZE ? (
        <div className="row" style={{ gap: 8, justifyContent: 'flex-end', alignItems: 'center', marginTop: 10 }}>
          <span className="xs faint mono">
            page {evCur + 1} / {evPages}
          </span>
          <button className="btn btn-ghost btn-sm" disabled={evCur === 0} onClick={() => setEvPage(evCur - 1)} aria-label="Previous events">
            ‹ Prev
          </button>
          <button
            className="btn btn-ghost btn-sm"
            disabled={evCur >= evPages - 1}
            onClick={() => setEvPage(evCur + 1)}
            aria-label="Next events"
          >
            Next ›
          </button>
        </div>
      ) : null}
    </div>
  );
}
