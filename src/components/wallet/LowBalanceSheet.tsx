/**
 * LowBalanceSheet.tsx — THE short-balance moment, as a POPUP WITH OPTIONS.
 *
 * Owner (2026-10-06): "abi popup olarak çıkacaktı o yetersiz, anladın mı,
 * seçenek sunacaktı o kadar, lütfen toast değil ya". The warning used to be a
 * toast, which announces without letting the buyer decide anything. This is the
 * popup: the two figures that disagree, one sentence explaining why they can,
 * and three explicit choices —
 *
 *   1. continue anyway  → the attempt proceeds, exactly as before (the shop
 *                         does not know the wallet's exact fee, so it never
 *                         blocks a payment on a guess);
 *   2. refresh          → re-read the wallet and re-evaluate IN PLACE: if the
 *                         figure now covers the price, the popup says so and the
 *                         primary button becomes "continue";
 *   3. cancel           → nothing happens.
 *
 * The popup reads the SHARED wallet state (no extra request) and re-renders live,
 * so "refresh" changes the numbers right here instead of closing and hoping.
 */
import { useEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { useT, type Translator } from '../../i18n';
import { Icon } from '../ui/Icon';
import { useWalletBalance } from './WalletBalance';
import { coversTarget, isTight, neededWholeNim, shortByWholeNim } from '../../lib/walletBalance';

/** The subset of the sheet provider this module needs (keeps it caller-agnostic).
 *  Mirrors the provider's own SheetTitle shape exactly, so any caller can pass
 *  its `openSheet` without a cast. */
export type SheetOpener = (opts: {
  title: string | ((t: Translator) => string);
  wide?: boolean;
  render: (close: () => void) => ReactNode;
}) => number;

export function LowBalanceSheet({
  targetNim,
  onContinue,
  onCancel,
}: {
  /** The NIM price of the thing being bought (one item, or the whole cart). */
  targetNim: number;
  onContinue: () => void;
  onCancel: () => void;
}) {
  const { t } = useT();
  const { state, refresh } = useWalletBalance();
  const settled = useRef(false);
  const touched = useRef(false);

  /* A popup dismissed with the X or the backdrop is still an answer: the promise
     the caller awaits must never hang on a missing button press. */
  useEffect(
    () => () => {
      if (!settled.current) onCancel();
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    []
  );

  const go = (ok: boolean) => {
    settled.current = true;
    if (ok) onContinue();
    else onCancel();
  };

  const need = neededWholeNim(targetNim);
  const have = Math.max(0, Math.floor(state.availableNim));
  const short = shortByWholeNim(targetNim, state.availableNim);
  const ready = state.status === 'ready';
  const covers = ready && coversTarget(targetNim, state.availableNim);
  // Covers the price but not the fee margin: a warning, not a shortage.
  const tight = ready && !covers && isTight(targetNim, state.availableNim);

  return (
    <div className="lowbal" data-testid="low-balance-popup">
      <div className={`lowbal-fig ${covers ? 'ok' : 'short'}`}>
        <Icon name={covers ? 'check' : 'alert'} size={20} />
        <div className="lowbal-fig-body">
          <div className="strong">
            {covers ? t('wallet.shortFixed') : tight ? t('wallet.tight') : t('wallet.shortTitle')}
          </div>
          <div className="small muted">
            {t('checkout.flowLowBalanceBody', { need, have, short })}
          </div>
        </div>
      </div>

      {/* Why "continue" exists at all: the fee is the wallet's to add, after the
          amount is converted — so this is a warning, not a refusal. */}
      <p className="small muted lowbal-note">{t('wallet.shortNote')}</p>

      {touched.current && !covers && (
        <div className="alert warn" style={{ marginBottom: 0, display: 'flex', gap: '8px', alignItems: 'center' }}>
          <Icon name="refresh" size={16} />
          <div className="small">{t('wallet.shortStill')}</div>
        </div>
      )}

      <div className="lowbal-opts">
        <button
          type="button"
          className="btn btn-gold btn-block"
          onClick={() => go(true)}
          data-testid="low-balance-continue"
        >
          <Icon name="bolt" size={16} />
          {covers ? t('actions.continue') : t('checkout.flowLowBalanceAnyway')}
        </button>
        <button
          type="button"
          className="btn btn-outline btn-block"
          onClick={() => {
            touched.current = true;
            refresh();
          }}
          data-testid="low-balance-refresh"
        >
          <Icon name="refresh" size={16} /> {t('wallet.refresh')}
        </button>
        <button
          type="button"
          className="btn btn-ghost btn-block"
          onClick={() => go(false)}
          data-testid="low-balance-cancel"
        >
          {t('actions.cancel')}
        </button>
      </div>
    </div>
  );
}

/**
 * Ask BEFORE a purchase leaves the shop. Resolves `true` when the buyer may
 * proceed (the balance covers the price, or they chose "continue anyway") and
 * `false` when they cancelled. Callers do not need to know why: they just
 * `if (!(await guardLowBalance(...))) return;`.
 */
export function guardLowBalance(opts: {
  openSheet: SheetOpener;
  targetNim: number;
  availableNim: number;
  ready: boolean;
}): Promise<boolean> {
  const { openSheet, targetNim, availableNim, ready } = opts;
  if (!ready || !(Number(targetNim) > 0)) return Promise.resolve(true);
  if (coversTarget(targetNim, availableNim)) return Promise.resolve(true);
  return new Promise<boolean>((resolve) => {
    let done = false;
    const finish = (ok: boolean) => {
      if (done) return;
      done = true;
      resolve(ok);
    };
    openSheet({
      title: (t) => t('wallet.shortTitle'),
      render: (close) => (
        <LowBalanceSheet
          targetNim={targetNim}
          onContinue={() => {
            finish(true);
            close();
          }}
          onCancel={() => {
            finish(false);
            close();
          }}
        />
      ),
    });
  });
}
