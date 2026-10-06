/**
 * ProfilePage.tsx — React port of pages/profile.js: the signed-in user's
 * account view (wallet address, daily order + USD limits with live reset
 * countdown, NIM cashback summary).
 */
import { useCallback, useEffect, useState } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { AppRoot } from '../AppRoot';
import { openLoginSheet } from '../shell/SiteShell';
import { friendlyApiMessage, getAccountLimits } from '../../lib/api';
import { isAuthed, getAddress } from '../../lib/session';
import { useSession } from '../../lib/useSession';
import { StakerCashbackCard } from '../staker/StakerCashback';
import { inNimiqPay } from '../../lib/miniapp';
import { fmtNIM, fmtUSD, formatWalletAddress, fmtCountdown } from '../../lib/format';
import { useInterval } from '../../lib/useInterval';
import { useSheet, useToast } from '../AppProviders';
import { KvSkeleton } from '../ui/uiKit';
import { CopyButton, LockedSignInCard } from '../ui/uiKit';
import { getMyCashback } from '../../lib/api';
import { RecentCashbackList } from '../cashback/RecentCashbackList';
import { useT } from '../../i18n';
import { pagePath } from '../../lib/asset';

type RecentCashback = { id?: string; quote_id?: string; product_id?: string; status?: string; status_label?: string; amount_nim?: number; bps?: number; cashback_source?: string; tx_hash?: string; purchase_tx?: string };

type Limits = {
  resets_at?: string | null;
  server_now?: string | null;
  max_orders?: number;
  used_orders?: number;
  max_usd?: number;
  used_usd?: string | number;
  max_monthly_usd?: number;
  used_monthly_usd?: string | number;
  remaining_monthly_usd?: string | number;
  month_resets_at?: string | null;
  used_nim?: string | number;
  max_nim?: string | number;
  cashback?: {
    paid_nim?: number;
    pending_nim?: number;
    cashback_percent?: number;
  };
};

function cashbackCard(L: Limits, t: (k: string, v?: Record<string, string | number>) => string) {
  const cb = L.cashback || {};
  const paid = Number(cb.paid_nim) || 0;
  const pending = Number(cb.pending_nim) || 0;
  const pct = Number(cb.cashback_percent);
  const pctLabel = Number.isFinite(pct) ? (Number.isInteger(pct) ? String(pct) : String(Number(pct.toFixed(2)))) : '0';
  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="wallet" size={16} />
        <span>{t('accountPage.cashbackCard')}</span>
      </div>
      <div className="strong" style={{ fontSize: '1.35rem' }}>
        {t('accountPage.earnedNim', { amount: fmtNIM(paid, 2) })}
      </div>
      {pending > 0 ? (
        <div className="small muted mt-1">{t('accountPage.pendingNim', { amount: fmtNIM(pending, 2) })}</div>
      ) : null}
      <div className="small muted mt-1">{t('accountPage.cashbackRate', { pct: pctLabel })}</div>
    </div>
  );
}

export function ProfileView() {
  const { t } = useT();
  const [recentCashback, setRecentCashback] = useState<RecentCashback[]>([]);
  const [state, setState] = useState<{ L: Limits | null; err: string; loading: boolean; resetsAt: number | null; tick: number }>({
    L: null,
    err: '',
    loading: true,
    resetsAt: null,
    tick: 0,
  });
  const { openSheet, closeSheet } = useSheet();
  const { toast } = useToast();
  // null = not decided yet (SSR / first paint) → neutral skeleton, never the
  // signed-out card. See lib/useSession.ts.
  const authed = useSession();

  const load = useCallback(async () => {
    if (!isAuthed()) {
      setRecentCashback([]);
      setState((s) => ({ ...s, L: null, loading: false }));
      return;
    }
    try {
      const [L, cashback] = await Promise.all([getAccountLimits(), getMyCashback()]);
      setRecentCashback(Array.isArray((cashback as any)?.cashbacks) ? (cashback as any).cashbacks.filter((r: any) => r.status === 'paid' && r.tx_hash && !String(r.tx_hash).startsWith('TEST')).slice(0, 5) : []);
      let resetsAt: number | null = null;
      if (L.resets_at) resetsAt = new Date(L.resets_at).getTime();
      if (resetsAt && L.server_now) {
        const skew = Date.now() - new Date(L.server_now).getTime();
        if (Math.abs(skew) > 5000) resetsAt = resetsAt + skew;
      }
      setState({ L, err: '', loading: false, resetsAt, tick: 0 });
    } catch (err) {
      setState((s) => ({ ...s, err: friendlyApiMessage(err, t('errors.loadAccount')), loading: false }));
    }
  }, [t]);

  useEffect(() => {
    load();
  }, [load, authed]);

  // 1s countdown tick
  useInterval(() => setState((s) => ({ ...s, tick: s.tick + 1 })), state.resetsAt ? 1000 : null);

  if (authed === null) {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="mt-2">
          <div className="card">
            <KvSkeleton n={4} />
          </div>
        </div>
      </div>
    );
  }

  if (!authed) {
    return (
      <div className="container">
        <BackRow />
        <h1 style={{ display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' }}>
          <Icon name="user" size={24} /> {t('accountPage.heading')}
        </h1>
        <p className="lede">{t('accountPage.lede')}</p>
        <div className="mt-2">
          <LockedSignInCard
            title={t('accountPage.lockedTitle')}
            text={t('accountPage.lockedText')}
            onConnect={() => openLoginSheet({ openSheet, closeSheet, toast })}
          />
        </div>
      </div>
    );
  }

  if (state.loading) {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="mt-2">
          <div className="card">
            <KvSkeleton n={4} />
          </div>
        </div>
      </div>
    );
  }

  if (state.err || !state.L) {
    return (
      <div className="container">
        <BackRow />
        <Header />
        <div className="card fade-in mt-2">
          <div className="alert error">
            <Icon name="alert" size={19} />
            <div>{state.err || t('errors.loadAccount')}</div>
          </div>
          <button className="btn btn-gold mt-2" type="button" onClick={() => { setState((s) => ({ ...s, loading: true, err: '' })); load(); }}>
            <Icon name="refresh" size={16} /> {t('common.tryAgain')}
          </button>
        </div>
      </div>
    );
  }

  const L = state.L;
  const addr = getAddress() || '';
  const maxOrders = Number(L.max_orders) || 0;
  const usedOrders = Number(L.used_orders) || 0;
  const maxUSD = Number(L.max_usd) || 0;
  const usedUSD = parseFloat(String(L.used_usd)) || 0;
  const orderPct = maxOrders ? Math.min(100, (usedOrders / maxOrders) * 100) : 0;
  const usdPct = maxUSD ? Math.min(100, (usedUSD / maxUSD) * 100) : 0;
  const maxMonthlyUSD = Number(L.max_monthly_usd) || 0;
  const usedMonthlyUSD = parseFloat(String(L.used_monthly_usd)) || 0;
  const remainingMonthlyUSD = Math.max(
    0,
    Number.isFinite(Number(L.remaining_monthly_usd))
      ? Number(L.remaining_monthly_usd)
      : maxMonthlyUSD - usedMonthlyUSD,
  );
  const monthlyPct = maxMonthlyUSD ? Math.min(100, (usedMonthlyUSD / maxMonthlyUSD) * 100) : 0;
  const cdText = state.resetsAt ? fmtCountdown(state.resetsAt - Date.now()) : '—';

  return (
    <div className="container">
      <BackRow />
      <Header />
      <div className="fade-in">
        <div className="card mb-2">
          <div className="card-title">{t('accountPage.walletCard')}</div>
          <div className="row" style={{ gap: '14px', alignItems: 'center' }}>
            <Identicon address={addr} className="wallet-avatar" />
            <div style={{ minWidth: 0 }}>
              <div className="mono strong wallet-address" title={addr}>
                {formatWalletAddress(addr)}
              </div>
              <div className="xs faint">
                {inNimiqPay() ? t('accountPage.connectedViaPay') : t('accountPage.connectedViaHub')}
              </div>
            </div>
            <CopyButton getText={addr} label={t('accountPage.copyAddress')} />
          </div>
        </div>

        <StakerCashbackCard />

        <a className="card mt-2 mb-2" href={pagePath("/cashback")} style={{ display: 'block', textDecoration: 'none', color: 'inherit' }}>
          <div className="row between" style={{ alignItems: 'center', gap: 10 }}>
            <div>
              <div className="card-title" style={{ margin: 0 }}>
                <Icon name="coins" size={16} /> {t('account.cashbackStaking')}
              </div>
              <div className="xs faint mt-1">{t('accountPage.cashbackStakingText')}</div>
            </div>
            <Icon name="chevron" size={18} />
          </div>
        </a>

        <div className="card">
          <div className="card-title">{t('accountPage.dailyLimits')}</div>
          <div className="row between mb-1" style={{ alignItems: 'baseline' }}>
            <div className="strong">{t('accountPage.ordersLine', { used: usedOrders, max: maxOrders || '∞' })}</div>
            <div className="small muted">
              <Icon name="clock" size={13} /> {t('accountPage.resetsIn', { time: cdText })}
            </div>
          </div>
          <div className="mini-progress" style={{ width: '100%', display: 'block', height: '8px', background: 'var(--surface-3)', borderRadius: '99px', overflow: 'hidden' }}>
            <div style={{ width: orderPct + '%', height: '100%', background: 'var(--gold-grad)' }} />
          </div>
          <div className="row between mt-2 mb-1" style={{ alignItems: 'baseline' }}>
            <div className="strong">
              {t('accountPage.spendingLine', { used: fmtUSD(usedUSD), max: maxUSD ? fmtUSD(maxUSD) : '∞' })}
            </div>
          </div>
          <div className="mini-progress" style={{ width: '100%', display: 'block', height: '8px', background: 'var(--surface-3)', borderRadius: '99px', overflow: 'hidden' }}>
            <div style={{ width: usdPct + '%', height: '100%', background: 'var(--gold-grad)' }} />
          </div>
          <div className="card mt-2" style={{ background: 'var(--surface-2)' }}>
            <div className="card-title"><Icon name="calendar" size={16} /> {t('accountPage.monthlyLimit')}</div>
            <div className="row between" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '8px 24px' }}>
              <span className="strong">{t('accountPage.thisMonth', { used: fmtUSD(usedMonthlyUSD), max: maxMonthlyUSD > 0 ? fmtUSD(maxMonthlyUSD) : '∞' })}</span>
              <span className="small muted">{maxMonthlyUSD > 0 ? t('accountPage.pctUsed', { pct: Math.round(monthlyPct) }) : ' · ' + t('accountPage.noCeiling')}</span>
            </div>
            <div className="mini-progress mt-1" style={{ width: '100%', display: 'block', height: '8px', background: 'var(--surface-3)', borderRadius: '99px', overflow: 'hidden' }}>
              <div style={{ width: monthlyPct + '%', height: '100%', background: 'var(--gold-grad)' }} />
            </div>
            <div className="row between mt-1" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '8px 24px' }}>
              <span className="small muted">{t('accountPage.remainingThisMonth')} </span>
              <strong className="small">{maxMonthlyUSD > 0 ? fmtUSD(remainingMonthlyUSD) : t('common.unlimited')}</strong>
            </div>
            <div className="small muted mt-1">{t('accountPage.dailyMaximum', { amount: maxUSD > 0 ? `${fmtUSD(maxUSD)} USD` : t('common.unlimited') })}</div>
          </div>
          <div className="small muted mt-2">
            {t('accountPage.limitsFootnote')}
          </div>
        </div>

        {cashbackCard(L, t)}

        <RecentCashbackList rows={recentCashback} />
      </div>
    </div>
  );
}

function BackRow() {
  const { t } = useT();
  return (
    <div className="row mb-2" style={{ gap: '10px' }}>
      <a className="btn btn-ghost btn-sm" href={pagePath("/")}>
        <Icon name="back" size={16} />
        <span className="btn-label">{t('nav.shop')}</span>
      </a>
    </div>
  );
}

function Header() {
  const { t } = useT();
  return (
    <>
      <h1 style={{ display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' }}>
        <Icon name="user" size={24} /> {t('accountPage.heading')}
      </h1>
      <p className="lede">{t('accountPage.lede')}</p>
    </>
  );
}

export function ProfilePage() {
  return (
    <AppRoot activeKey="profile">
      <ProfileView />
    </AppRoot>
  );
}
