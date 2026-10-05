/**
 * WalletBalance.tsx — the buyer's NIM, wherever a purchase can start.
 *
 * Owner (2026-10-05): "ana sayfada ya da sipariş sayfasında NIM kuru gözükse
 * güzel olabilirdi… sahip olduğum NIM ile ne kadarlık ürün alabilirim?" and,
 * after seeing it: "your wallet'deki NIM miktarım yanlış".
 *
 * The wrong number was a one-figure answer. This component states BOTH figures
 * the buyer needs and never lets them be confused:
 *   - "Spendable": the address balance — what a payment can actually use, and
 *     therefore the only number the affordability verdict may be based on.
 *   - "Total … of it is staked": what the wallet shows. A buyer who stakes (our
 *     own cashback programme asks them to) otherwise sees a fraction of their
 *     NIM and calls the strip wrong — which it was.
 * The address, the reading time and a manual refresh are on the card, so the
 * number is verifiable instead of merely asserted.
 *
 * Shapes: `card` (home), `line` (product / cart / checkout), `chip` (orders).
 * Every failure mode renders as a quiet state, never a broken screen.
 */
import { useEffect, useState } from 'react';
import { useT } from '../../i18n';
import { fmtNIM } from '../../lib/format';
import { NIM_LOGO } from '../../lib/nim';
import {
  getWalletBalanceState,
  refreshWalletBalance,
  subscribeWalletBalance,
  nimText,
  affordableUnits,
  coversTarget,
  SPEND_MARGIN,
  type WalletBalanceState,
} from '../../lib/walletBalance';
import { Icon } from '../ui/Icon';

/** Subscribe to the shared reading (one lookup feeds every mounted strip). */
export function useWalletBalance(): { state: WalletBalanceState; refresh: () => void } {
  const [state, setState] = useState<WalletBalanceState>(() => getWalletBalanceState());
  useEffect(() => {
    const off = subscribeWalletBalance(setState);
    void refreshWalletBalance();
    return off;
  }, []);
  return { state, refresh: () => void refreshWalletBalance({ force: true }) };
}

/**
 * NIM with as much precision as the amount deserves: an integer-rounded
 * 1,240.62 NIM looked like a different number ("miktarım yanlış"), and a
 * 0.4 NIM wallet rounded to "0 NIM". Below 1 NIM nothing is rounded away.
 */
export function nimText(n: number): string {
  const abs = Math.abs(n);
  const decimals = abs >= 1000 ? 2 : abs >= 10 ? 2 : abs >= 1 ? 3 : 5;
  return fmtNIM(n, decimals);
}

/** "NQ73 SE1X YRRF … 2HPD" → "NQ73SE1X…2HPD" */
function shortAddress(addr: string): string {
  const a = String(addr || '').replace(/\s+/g, '').toUpperCase();
  if (a.length <= 12) return a;
  return `${a.slice(0, 8)}…${a.slice(-4)}`;
}

function clockOf(at?: number): string {
  if (!at) return '';
  try {
    return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  } catch {
    return '';
  }
}

export function WalletBalance({
  variant = 'line',
  targetNim = 0,
  targetTotal = false,
  signInHint = false,
  className = '',
}: {
  variant?: 'card' | 'line' | 'chip';
  /** NIM price of the thing being bought: one item, or the whole cart. */
  targetNim?: number;
  /** true → the target is a total ("covers this order"), false → a unit. */
  targetTotal?: boolean;
  /** Show the "sign in to see it" nudge when the wallet is unknown. */
  signInHint?: boolean;
  className?: string;
}) {
  const { t } = useT();
  const { state, refresh } = useWalletBalance();
  const ready = state.status === 'ready';

  // Chip: a compact header marker earns its space only when it has a number.
  if (variant === 'chip') {
    if (!ready) return null;
    return (
      <span
        className={`chip wal-chip${className ? ' ' + className : ''}`}
        title={t('wallet.available')}
      >
        <img src={NIM_LOGO} alt="NIM" width={13} height={13} style={{ borderRadius: 3 }} />
        <strong>{nimText(state.availableNim)}</strong>
        <span className="faint">NIM</span>
      </span>
    );
  }

  if (state.status === 'unavailable' && !signInHint) return null;

  const stakedNim = state.stakedNim + state.inactiveNim;
  const hasStake = stakedNim > 0.0000001;

  const verdict = (() => {
    if (!ready || !(targetNim > 0)) return null;
    /* Affordability is decided on SPENDABLE NIM only (staked NIM cannot pay),
       and with the 1% cushion the shop keeps on every verdict — see
       SPEND_MARGIN: an exact comparison promises something the wallet's own fee
       and the next rate tick can take away, and the buyer pays for that with a
       refused payment. The DISPLAYED balance is never adjusted. */
    const units = affordableUnits(state.availableNim, targetNim);
    const missing = Math.max(0, targetNim * (1 + SPEND_MARGIN) - state.availableNim);
    if (targetTotal) {
      return coversTarget(targetNim, state.availableNim)
        ? { ok: true, text: t('wallet.enough') }
        : { ok: false, text: t('wallet.short', { nim: nimText(missing) }) };
    }
    return units >= 1
      ? { ok: true, text: t('wallet.afford', { count: units }) }
      : { ok: false, text: t('wallet.short', { nim: nimText(missing) }) };
  })();

  const body = (
    <>
      <span className="wal-bal-top">
        <span className="wal-bal-label">
          <Icon name="wallet" size={14} /> {ready ? t('wallet.available') : t('wallet.label')}
        </span>
        {state.status === 'loading' ? (
          <span className="small muted">{t('wallet.loading')}</span>
        ) : ready ? (
          <span className="wal-bal-fig">
            <img src={NIM_LOGO} alt="NIM" width={14} height={14} style={{ borderRadius: 3 }} />
            <strong className="wal-nim">{nimText(state.availableNim)} NIM</strong>
            {state.usd > 0 && (
              <span className="wal-usd small faint">
                ≈ ${state.usd.toLocaleString('en-US', { maximumFractionDigits: 2 })}
              </span>
            )}
          </span>
        ) : state.status === 'unavailable' ? (
          <span className="small muted">{t('wallet.signIn')}</span>
        ) : (
          <span className="wal-bal-fig">
            <span className="small muted">{t('wallet.error')}</span>
            <button type="button" className="btn btn-sm btn-outline" onClick={refresh}>
              {t('actions.retry')}
            </button>
          </span>
        )}
      </span>

      {/* The reconciliation line. Without it the strip answers "how much NIM do
          I have?" with the spendable figure and looks wrong to anyone counting
          their stake. */}
      {ready && hasStake && (
        <span className="wal-total small faint">
          {t('wallet.totalLine', {
            total: nimText(state.totalNim),
            staked: nimText(stakedNim),
          })}
        </span>
      )}

      {verdict && (
        <span className={`wal-verdict ${verdict.ok ? 'ok' : 'short'}`}>
          <Icon name={verdict.ok ? 'check' : 'info'} size={13} /> {verdict.text}
        </span>
      )}

      {ready && variant === 'card' && (
        <span className="wal-meta xs faint">
          {state.address && <span className="mono">{shortAddress(state.address)}</span>}
          {state.address && clockOf(state.at) ? ' · ' : ''}
          {clockOf(state.at) ? t('wallet.updated', { time: clockOf(state.at) }) : ''}
          {/* WHICH CHAIN the figure was read from. A testnet balance shown as a
              real one is a wrong reading in every sense — this line makes the
              mix-up visible instead of mysterious. */}
          {state.network ? ' · ' + t('wallet.onNetwork', { network: state.network }) : ''}
          {state.stale ? ' · ' + t('wallet.stale') : ''}
          <button type="button" className="wal-refresh" onClick={refresh} aria-label={t('wallet.refresh')}>
            <Icon name="refresh" size={13} />
          </button>
        </span>
      )}

      {/* Two sources answered differently and it was NOT a unit slip: the chain
          (the wallet the shop charges) won, and the buyer is told. */}
      {ready && state.mismatch && (
        <span className="wal-note xs short">
          <Icon name="info" size={12} /> {t('wallet.mismatch')}
        </span>
      )}
    </>
  );

  if (variant === 'card') {
    return <div className={`card wal-bal wal-card${className ? ' ' + className : ''}`}>{body}</div>;
  }
  return <div className={`wal-bal wal-line${className ? ' ' + className : ''}`}>{body}</div>;
}
