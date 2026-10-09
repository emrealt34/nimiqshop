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
 *   - "Total … in stake": the wallet's full figure, including active, inactive,
 *     and retired stake balances. A buyer who stakes otherwise sees only a
 *     fraction of their NIM and calls the strip wrong — which it was.
 * The figure and manual refresh stay together on the home card; verbose source,
 * address, timestamp, and network metadata are omitted there for a cleaner view.
 *
 * Shapes: `card` (home), `line` (product / cart / checkout), `chip` (orders).
 * Every failure mode renders as a quiet state, never a broken screen.
 */
import { useEffect, useState, useSyncExternalStore } from 'react';
import { useT } from '../../i18n';
import { fmtMoney, fmtNIM, localCurrencyCode } from '../../lib/format';
import { cachedFX, getFXRates, onRatesChange } from '../../lib/api';
import { NIM_LOGO } from '../../lib/nim';
import {
  getWalletBalanceState,
  getWalletBalanceServerState,
  refreshWalletBalance,
  subscribeWalletBalance,
  affordableUnits,
  coversTarget,
  isTight,
  requiredNim,
  type WalletBalanceState,
} from '../../lib/walletBalance';
import { Icon } from '../ui/Icon';
import { inNimiqPay } from '../../lib/miniapp';
import { useToast } from '../AppProviders';

// Several wallet strips can mount together; notify once per page instead of
// stacking the same old-host warning from every subscriber.
let updatePayToastShown = false;

/** Subscribe to the shared reading (one lookup feeds every mounted strip). */
export function useWalletBalance(): { state: WalletBalanceState; refresh: () => void } {
  const state = useSyncExternalStore(subscribeWalletBalance, getWalletBalanceState, getWalletBalanceServerState);
  useEffect(() => {
    void refreshWalletBalance();
  }, []);
  return { state, refresh: () => void refreshWalletBalance({ force: true }) };
}

/**
 * NIM as a WHOLE number, everywhere a NIM figure is shown.
 * Owner (2026-10-06): "o nimde virgülden sonrasını gösterme lütfen anladın mı
 * tam nim göster … 0,000 yok 0 var". A crypto balance with three or five
 * decimals reads as a different number every time the rate ticks, and the
 * buyer never asked for that precision — "12 NIM" is the honest, stable
 * reading. The displayed figure is rounded; verdicts and shortages round
 * differently on purpose (see nimShortText).
 */
export function nimText(n: number): string {
  const v = Number(n);
  if (!isFinite(v)) return '0';
  return fmtNIM(Math.round(v), 0);
}

/**
 * A SHORTAGE rounds UP, never down: telling a buyer they are 0 NIM short when
 * the wallet will refuse the payment is worse than saying 1. It never prints
 * "0" for a real gap either.
 */
export function nimShortText(n: number): string {
  const v = Number(n);
  if (!isFinite(v) || v <= 0) return '1';
  return fmtNIM(Math.max(1, Math.ceil(v)), 0);
}

export function WalletBalance({
  variant = 'line',
  targetNim = 0,
  targetTotal = false,
  signInHint = false,
  showVerdict = true,
  className = '',
}: {
  variant?: 'card' | 'line' | 'chip';
  /** NIM price of the thing being bought: one item, or the whole cart. */
  targetNim?: number;
  /** true → the target is a total ("covers this order"), false → a unit. */
  targetTotal?: boolean;
  /** Show the "sign in to see it" nudge when the wallet is unknown. */
  signInHint?: boolean;
  /** Suppress the inline affordability verdict when another control handles it. */
  showVerdict?: boolean;
  className?: string;
}) {
  const { t } = useT();
  const { toast } = useToast();
  const { state, refresh } = useWalletBalance();
  const [fxRates, setFxRates] = useState<Record<string, number> | null>(() => cachedFX());
  const ready = state.status === 'ready';

  useEffect(() => {
    if (variant === 'chip') return;
    let active = true;
    const applyRates = (payload: Record<string, any> | null) => {
      if (!active || !payload) return;
      const table = payload.usd_per_unit || payload;
      if (table && typeof table === 'object') setFxRates(table);
    };
    const unsubscribe = onRatesChange(applyRates);
    applyRates(cachedFX());
    void getFXRates().then(applyRates).catch(() => {});
    return () => {
      active = false;
      unsubscribe();
    };
  }, [variant]);

  useEffect(() => {
    if (state.hostBalance !== 'update-required' || updatePayToastShown) return;
    updatePayToastShown = true;
    toast(t('wallet.updatePayToast'), 'warn');
  }, [state.hostBalance, t, toast]);

  // Chip: a compact header marker earns its space only when it has a number.
  if (variant === 'chip') {
    if (!ready) return null;
    return (
      <span
        className={`chip wal-chip${className ? ' ' + className : ''}`}
        title={t('wallet.available')}
      >
        <img src={NIM_LOGO} alt="NIM" draggable={false} width={13} height={13} style={{ pointerEvents: "none", borderRadius: 3 }} />
        <strong>{nimText(state.availableNim)}</strong>
        <span className="faint">NIM</span>
      </span>
    );
  }

  if (state.status === 'unavailable' && !signInHint) return null;

  const stakedNim = state.stakedNim + state.inactiveNim + state.retiredNim;
  const hasStake = stakedNim > 0.0000001;

  const verdict = (() => {
    if (!ready || !(targetNim > 0)) return null;
    /* Affordability is decided on SPENDABLE NIM only (staked NIM cannot pay),
       and with the 1% cushion the shop keeps on every verdict — see
       the cushion (requiredNim): an exact comparison promises something the wallet's own fee
       and the next rate tick can take away, and the buyer pays for that with a
       refused payment. The DISPLAYED balance is never adjusted. */
    const units = affordableUnits(state.availableNim, targetNim);
    const missing = Math.max(0, requiredNim(targetNim) - state.availableNim);
    /* Inside the cushion the balance covers the price but may not cover the
       wallet's fees. Saying "covers" there is a promise Pay can break, so the
       buyer is warned instead. */
    const tight = isTight(targetNim, state.availableNim);
    if (targetTotal) {
      if (coversTarget(targetNim, state.availableNim)) return { ok: true, text: t('wallet.enough') };
      if (tight) return { ok: false, text: t('wallet.tight') };
      return { ok: false, text: t('wallet.short', { nim: nimShortText(missing) }) };
    }
    if (units >= 1) return { ok: true, text: t('wallet.afford', { count: units }) };
    if (tight) return { ok: false, text: t('wallet.tight') };
    return { ok: false, text: t('wallet.short', { nim: nimShortText(missing) }) };
  })();

  const localCurrency = localCurrencyCode();
  const usdPerUnit = localCurrency === 'USD' ? 1 : Number(fxRates?.[localCurrency] || 0);
  const localEquivalent = ready && state.usd > 0 && usdPerUnit > 0
    ? fmtMoney(state.usd / usdPerUnit, localCurrency)
    : '';

  const figure = ready ? (
    <span className="wal-bal-fig">
      <img src={NIM_LOGO} alt="NIM" draggable={false} width={15} height={15} style={{ pointerEvents: "none", borderRadius: 3 }} />
      <strong className="wal-nim">{nimText(state.availableNim)} NIM</strong>
      {localEquivalent && <span className="wal-usd small faint">≈ {localEquivalent}</span>}
    </span>
  ) : state.status === 'loading' ? (
    <span className="small muted">{t('wallet.loading')}</span>
  ) : state.status === 'unavailable' ? (
    <span className="small muted">{t('wallet.signIn')}</span>
  ) : (
    <span className="wal-bal-fig">
      <span className="small muted">{t('wallet.error')}</span>
      <button type="button" className="btn btn-sm btn-outline" onClick={refresh}>
        {t('actions.retry')}
      </button>
    </span>
  );

  /* Keep the balance and refresh control together. The home card intentionally
     omits the verbose read-source/address/time/network line. */
  const body = (
    <>
      <span className="wal-head">
        {figure}
        {ready && (
          <button type="button" className="wal-refresh" onClick={refresh} aria-label={t('wallet.refresh')}>
            <Icon name="refresh" size={13} />
          </button>
        )}
      </span>

      {variant !== 'card' && !ready && (
        <span className="wal-bal-label">
          <Icon name="wallet" size={14} /> {t('wallet.label')}
        </span>
      )}

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

      {showVerdict && verdict && (
        <span className={`wal-verdict ${verdict.ok ? 'ok' : 'short'}`}>
          <Icon name={verdict.ok ? 'check' : 'info'} size={13} /> {verdict.text}
        </span>
      )}


      {/* PR 216: a host that is too old to read balances is the BUYER's to fix,
          so the strip says exactly that instead of silently showing the shop's
          own figure. Only inside Nimiq Pay — a plain browser has no wallet
          version to update, and the cache ('via-request') is fine either way. */}
      {ready && state.hostBalance === 'update-required' && (
        <span className="wal-note xs">
          <Icon name="info" size={12} /> {t('wallet.updatePay')}
        </span>
      )}



      {inNimiqPay() && state.debugLines && (
        <details className="wal-note xs" style={{ marginTop: 8 }}>
          <summary style={{ cursor: 'pointer' }}>Balance debug</summary>
          <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontSize: 10, margin: '6px 0 0' }}>
            {state.debugLines.join('\\n')}
          </pre>
        </details>
      )}
    </>
  );

  if (variant === 'card') {
    return <div className={`card wal-bal wal-card${className ? ' ' + className : ''}`}>{body}</div>;
  }
  return <div className={`wal-bal wal-line${className ? ' ' + className : ''}`}>{body}</div>;
}
