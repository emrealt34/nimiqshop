/**
 * WalletBalance.tsx — the buyer's NIM balance, wherever a purchase can start.
 *
 * Owner (2026-10-05): "ana sayfada ya da sipariş sayfasında NIM kuru gözükse
 * güzel olabilirdi, sipariş verecekken ilk gözüm onu aradı — sahip olduğum NIM
 * ile ne kadarlık ürün alabilirim?" and "neyi alıp alamayacağımı da göreyim".
 *
 * One component, three shapes, one reading:
 *   - `card`  home: the full line (amount + USD + verdict)
 *   - `line`  product / cart / checkout: one compact row, inline
 *   - `chip`  orders header: just the number
 *
 * With a `targetNim` it also answers the affordability question:
 *   - per item  → "your balance covers up to N items"
 *   - cart total → "your balance covers this order" / "42 NIM short"
 *
 * Every failure mode is a rendered state, never a broken screen: a wallet the
 * host refuses to read, a cancelled dialog, a 30-second bridge timeout or a
 * plain "not signed in" all end in a quiet row that keeps the page usable.
 */
import { useEffect, useState } from 'react';
import { useT } from '../../i18n';
import { fmtNIM } from '../../lib/format';
import { NIM_LOGO } from '../../lib/nim';
import {
  getWalletBalanceState,
  refreshWalletBalance,
  subscribeWalletBalance,
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
      <span className={`chip wal-chip${className ? ' ' + className : ''}`} title={t('wallet.label')}>
        <img src={NIM_LOGO} alt="NIM" width={13} height={13} style={{ borderRadius: 3 }} />
        <strong>{fmtNIM(state.nim, 0)}</strong>
        <span className="faint">NIM</span>
      </span>
    );
  }

  if (state.status === 'unavailable' && !signInHint) return null;

  const verdict = (() => {
    if (!ready || !(targetNim > 0)) return null;
    const units = Math.floor(state.nim / targetNim);
    if (targetTotal) {
      return units >= 1
        ? { ok: true, text: t('wallet.enough') }
        : { ok: false, text: t('wallet.short', { nim: fmtNIM(Math.max(1, Math.ceil(targetNim - state.nim)), 0) }) };
    }
    return units >= 1
      ? { ok: true, text: t('wallet.afford', { count: units }) }
      : { ok: false, text: t('wallet.short', { nim: fmtNIM(Math.max(1, Math.ceil(targetNim - state.nim)), 0) }) };
  })();

  const body = (
    <>
      <span className="wal-bal-top">
        <span className="wal-bal-label">
          <Icon name="wallet" size={14} /> {t('wallet.label')}
        </span>
        {state.status === 'loading' ? (
          <span className="small muted">{t('wallet.loading')}</span>
        ) : ready ? (
          <span className="wal-bal-fig">
            <img src={NIM_LOGO} alt="NIM" width={14} height={14} style={{ borderRadius: 3 }} />
            <strong className="wal-nim">{fmtNIM(state.nim, 0)} NIM</strong>
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
      {verdict && (
        <span className={`wal-verdict ${verdict.ok ? 'ok' : 'short'}`}>
          <Icon name={verdict.ok ? 'check' : 'info'} size={13} /> {verdict.text}
        </span>
      )}
    </>
  );

  if (variant === 'card') {
    return <div className={`card wal-bal wal-card${className ? ' ' + className : ''}`}>{body}</div>;
  }
  return <div className={`wal-bal wal-line${className ? ' ' + className : ''}`}>{body}</div>;
}
