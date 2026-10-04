/**
 * LeaderboardPage.tsx — the /leaderboard route: the cashback card and the
 * cashback & burn leaderboard on their OWN page, one nav-bar tab away
 * (owner, 2026-10-04: "leaderboard navbarda ayrı sayfaydı lütfen"). Nothing
 * here is buried in the programme page any more.
 *
 * The day/night switch sits on the leaderboard card (as the owner placed it)
 * and themes the whole page content — both cards, chips, badges and all —
 * and the choice is remembered per browser.
 */
import { useEffect, useState } from 'react';
import { AppRoot } from '../AppRoot';
import { Icon } from '../ui/Icon';
import { useT } from '../../i18n';
import { useSession } from '../../lib/useSession';
import { getMyCashback } from '../../lib/api';
import { RecentCashbackList } from './RecentCashbackList';
import { CashbackImpactSection } from './CashbackImpactSection';

const PAYOUTS_PER_PAGE = 3;

export function LeaderboardView() {
  const { t } = useT();
  const authed = useSession();
  const [ledger, setLedger] = useState<any>(null);
  const [lbMode, setLbMode] = useState<'night' | 'day'>(() => {
    try { return localStorage.getItem('nimshop:lb-theme') === 'day' ? 'day' : 'night'; } catch { return 'night'; }
  });
  useEffect(() => {
    try { localStorage.setItem('nimshop:lb-theme', lbMode); } catch {}
  }, [lbMode]);
  useEffect(() => {
    if (!authed) { setLedger(null); return; }
    let alive = true;
    getMyCashback()
      .then((r) => { if (alive) setLedger(r || null); })
      .catch(() => { if (alive) setLedger(null); });
    return () => { alive = false; };
  }, [authed]);
  const rows = ledger?.cashbacks || [];
  return (
    <div className="container">
      <h1 className="cb-title" style={{ display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' }}>
        <Icon name="trophy" size={28} /> {t('cashbackCard.leaderboard')}
      </h1>
      <div className={`lb-wrap lb-${lbMode}`}>
        {/* Visible logged in or not — the owner's standing rule for the
            cashback rows carries over to their new home. */}
        {authed ? (
          <RecentCashbackList rows={ledger ? rows : null} loading={!ledger} pageSize={PAYOUTS_PER_PAGE} />
        ) : (
          <div className="card mt-2">
            <div className="card-title cb-payouts-head">
              <span><Icon name="gift" size={16} /> {t('cashback.recentTitle')}</span>
            </div>
            <div className="small muted" style={{ textAlign: 'center', padding: '20px 12px' }}>
              {t('cashback.connectHistory')}
            </div>
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: 12 }}>
              <span className="chip"><Icon name="wallet" size={13} /> {t('cashback.noWalletChip')}</span>
            </div>
          </div>
        )}
        <CashbackImpactSection authed={authed === true} myTotals={ledger?.totals || null} lbMode={lbMode} onLbMode={setLbMode} />
      </div>
    </div>
  );
}

/** Static-page wrapper: mounts the providers + shell around the content. */
export function LeaderboardPage() {
  return (
    <AppRoot activeKey="leaderboard">
      <LeaderboardView />
    </AppRoot>
  );
}
