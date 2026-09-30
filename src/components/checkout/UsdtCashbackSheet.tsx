import { CashbackFeeNotice } from './CashbackFeeNotice';
/**
 * UsdtCashbackSheet — the "wait, you're leaving cashback behind" interception.
 *
 * The buyer just tapped USDT · Polygon at the payment picker. That choice is
 * the single most expensive tap on the site: the USDT rail pays only a
 * fraction (default 50%) of the NIM cashback rate on every order, forever.
 * This sheet puts the real numbers of THIS cart in front of them at exactly
 * that moment and offers the two ways out:
 *
 *   1. Pay with NIM · BTC Lightning instead — restores the full rate (one tap, still pre-quote);
 *   2. Stake NIM now — QuickStake, one confirmation dialog inside Nimiq Pay.
 *
 * It never traps anyone: "Continue with USDT anyway" is always right there.
 * The comparison is honest to the cent: it mirrors the backend payout math
 * (estimateCashbackNIM = bps/10_000 of the cart, rounded in Luna) and uses
 * the buyer's live staker rate when they have one, the pool's staker base
 * when they don't — labelled as "with a stake" so nothing is promised that
 * a stake would not deliver.
 *
 * UsdtPayStakeNote is the same push, compressed, for AFTER the quote is
 * locked (the USDT pay screens): the rail can no longer change, so it sells
 * the stake that makes every FUTURE order earn the full rate.
 */
import { useEffect, useState } from 'react';
import type { CartItem } from '../../lib/cartStore';
import { rowUSD } from '../../lib/cartStore';
import { getNimRate } from '../../lib/api';
import { estimateCashbackNIM, fmtCashbackNIM } from '../../lib/cashback';
import { loadStakerProgram, loadMyStake, pctLabel, type MyStake, type StakerProgram } from '../../lib/stakerCashback';
import { useSession } from '../../lib/useSession';
import { Icon } from '../ui/Icon';
import { QuickStake } from './QuickStake';
import { useT } from '../../i18n';

/** usdt_cashback_multiplier default mirrors the backend's (0.5). */
export const DEFAULT_USDT_MULT = 0.5;

export function factorLabel(mult: number): string {
  const f = mult > 0 && mult < 1 ? 1 / mult : 0;
  if (!f) return '';
  return Number.isInteger(f) ? `${f}×` : `${f.toFixed(1)}×`;
}

/**
 * Best-effort cart total in NIM — the same priority the cart sheet uses:
 * the supplier's BTC invoice amount when a line carries one, face value ×
 * FX as the fallback. An estimate is all the picker ever needs; the quote
 * still locks the real number.
 */
function useCartNIM(items: CartItem[]): number | null {
  const [nim, setNim] = useState<number | null>(null);
  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const m: any = await getNimRate();
        const nimUsd = Number(m && m.usd_per_nim);
        if (!(nimUsd > 0)) return;
        const btcUsd = Number(m && m.usd_per_btc);
        let usd = 0;
        for (const it of items) {
          if (it.coinAmount > 0 && btcUsd > 0) usd += it.coinAmount * (it.qty || 1) * btcUsd;
          else usd += rowUSD(it);
        }
        if (alive && usd > 0) setNim(usd / nimUsd);
      } catch {
        /* leave null — the sheet degrades to rate-only copy */
      }
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items]);
  return nim;
}

function useProgramAndMine(): { program: StakerProgram | null; mine: MyStake | null } {
  const authed = useSession();
  const [program, setProgram] = useState<StakerProgram | null>(null);
  const [mine, setMine] = useState<MyStake | null>(null);
  useEffect(() => {
    let alive = true;
    loadStakerProgram().then((p) => {
      if (alive) setProgram(p);
    });
    return () => {
      alive = false;
    };
  }, []);
  useEffect(() => {
    let alive = true;
    if (!authed) return;
    loadMyStake().then((m) => {
      if (alive) setMine(m);
    });
    return () => {
      alive = false;
    };
  }, [authed]);
  return { program, mine };
}

/** The comparison block's shared numbers. */
function useComparison(items: CartItem[], mult: number) {
  const { program, mine } = useProgramAndMine();
  const cartNIM = useCartNIM(items);
  const stakedHere = !!mine?.staked && (mine?.stake_luna || 0) > 0;
  const fullBps = stakedHere ? mine?.cashback_bps || 0 : program?.stakerBaseBps ?? 0;
  const usdtBps = Math.round(fullBps * mult);
  const fullNIM = cartNIM != null ? estimateCashbackNIM(cartNIM, fullBps) : 0;
  const usdtNIM = cartNIM != null ? estimateCashbackNIM(cartNIM, usdtBps) : 0;
  return { program, mine, cartNIM, stakedHere, fullBps, usdtBps, fullNIM, usdtNIM };
}

/* ------------------------------------------------------------------ sheet */

export function UsdtCashbackSheetBody({
  items,
  multiplier = DEFAULT_USDT_MULT,
  onSwitchNim,
  onClose,
}: {
  items: CartItem[];
  multiplier?: number;
  onSwitchNim: () => void;
  onClose: () => void;
}) {
  const { t } = useT();
  const mult = multiplier > 0 && multiplier < 1 ? multiplier : DEFAULT_USDT_MULT;
  const usdtPct = Math.round(mult * 100);
  const { program, mine, cartNIM, stakedHere, fullBps, fullNIM, usdtNIM } = useComparison(items, mult);
  const [mineFresh, setMineFresh] = useState<MyStake | null>(null);
  const effective = mineFresh || mine;
  const nowStaked = !!effective?.staked && (effective?.stake_luna || 0) > 0;

  return (
    <div>
      {/* The headline: what the USDT tap just cost */}
      <div className="small" style={{ display: 'flex', gap: 8, alignItems: 'flex-start', fontWeight: 700 }}>
        <span style={{ color: 'var(--stamp,#B4471C)', flexShrink: 0 }} aria-hidden="true">
          <Icon name="bolt" size={17} />
        </span>
        <span>
          {t('usdtCashback.paysPrefix')}
          <span style={{ color: 'var(--stamp,#B4471C)' }}>{t('usdtCashback.paysRate', { pct: usdtPct })}</span>
          {t('usdtCashback.paysSuffix')}
        </span>
      </div>

      {/* The numbers for THIS cart, when they are computable */}
      {fullBps > 0 && cartNIM != null && fullNIM > 0 ? (
        <div
          className="mt-2"
          style={{
            display: 'grid',
            gridTemplateColumns: '1fr 1fr',
            gap: 8,
          }}
        >
          <div style={{ border: '1.5px solid var(--line-strong,#4E3D28)', borderRadius: 12, padding: '10px 12px', background: 'var(--panel-2)' }}>
            <div className="xs faint">
              {t('usdtCashback.nimRail')}
              {stakedHere ? '' : t('usdtCashback.withStake')}
            </div>
            <div className="strong" style={{ fontSize: 18, color: 'var(--green,#2f5540)' }}>
              ≈ {fmtCashbackNIM(fullNIM)} NIM
            </div>
            <div className="xs faint">
              {t('usdtCashback.rateSuffix', { rate: pctLabel(stakedHere ? fullBps : program?.stakerBaseBps ?? fullBps) })}
            </div>
          </div>
          <div style={{ border: '1.5px dashed var(--line-dash,#e0d7c2)', borderRadius: 12, padding: '10px 12px' }}>
            <div className="xs faint">{t('usdtCashback.usdtRail')}</div>
            <div className="strong" style={{ fontSize: 18, color: 'var(--stamp,#B4471C)' }}>
              ≈ {fmtCashbackNIM(usdtNIM)} NIM
            </div>
            <div className="xs faint">{t('usdtCashback.usdtRateOfRate', { pct: usdtPct })}</div>
          </div>
        </div>
      ) : (
        <div className="small muted mt-2">
          {t('usdtCashback.nimKeeps', {
            mode: stakedHere ? t('usdtCashback.includingBoost') : t('usdtCashback.anyStake'),
          })}
        </div>
      )}

      <CashbackFeeNotice />

      {/* Way out #1: switch rail (still possible — no quote yet) */}
      <button type="button" className="btn btn-gold btn-block mt-2" onClick={onSwitchNim}>
        <Icon name="nimiq" size={15} />{' '}
        {factorLabel(mult)
          ? t('usdtCashback.switchToNimCashback', {
              line: t('usdtCashback.switchToNim'),
              factor: factorLabel(mult),
            })
          : t('usdtCashback.switchToNim')}
      </button>

      {/* Way out #2: stake right now, one tap inside Pay */}
      {!nowStaked && (
        <div className="mt-2" style={{ borderTop: '1px dashed var(--line-dash,#e0d7c2)', paddingTop: 10 }}>
          <div className="small strong" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
            <Icon name="spark" size={14} /> {t('usdtCashback.stakeNow')}
          </div>
          <div className="xs faint mt-1" style={{ marginBottom: 8 }}>
            {t('usdtCashback.stakeUnlocks', {
              rate: pctLabel(program?.stakerBaseBps ?? 0),
              max: program?.program?.max_boost_percent ?? '?',
            })}
          </div>
          <QuickStake onStaked={(m) => setMineFresh(m)} />
        </div>
      )}
      {nowStaked && (
        <div className="xs faint mt-2" style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--green,#2f5540)' }}>
          <Icon name="check" size={13} />{' '}
          {t('usdtCashback.youStake', {
            amount: Math.round(effective?.stake_nim || 0).toLocaleString('en-US'),
            rate: pctLabel(effective?.cashback_bps || 0),
          })}
        </div>
      )}

      {/* Never trap the buyer */}
      <button
        type="button"
        className="btn btn-ghost btn-block mt-2"
        onClick={onClose}
        style={{ fontWeight: 500 }}
      >
        {t('usdtCashback.continueUsdt')}
      </button>
    </div>
  );
}

/* ------------------------------------------------------- pay-screen note */

export function UsdtPayStakeNote({ quote }: { quote: any }) {
  const { t } = useT();
  const isUsdt = String(quote?.payment_method || '') === 'usdt_polygon';
  const { program, mine } = useProgramAndMine();
  // Only the USDT rail carries the discount — the note never renders for NIM.
  if (!isUsdt) return null;
  const mult = Number(quote?.usdt_mult);
  const m = mult > 0 && mult < 1 ? mult : DEFAULT_USDT_MULT;
  const usdtPct = Math.round(m * 100);
  const stakedHere = !!mine?.staked && (mine?.stake_luna || 0) > 0;
  const orderNIM = Number(quote?.estimated_nim) || 0;
  const currentBps = Number(quote?.cashback_bps) || 0;
  const rawCashbackNIM = Number(quote?.estimated_cashback_nim);
  const currentCashbackNIM = rawCashbackNIM > 0 ? rawCashbackNIM * m : estimateCashbackNIM(orderNIM, currentBps) * m;

  // Even when the staking programme is unavailable, a locked quote can still
  // tell the buyer the cashback amount for this order.
  if (program && !program.enabled && !(orderNIM > 0)) return null;

  return (
    <div
      className="mt-1"
      style={{
        border: '1.5px dashed var(--line-strong,#4E3D28)',
        borderRadius: 'var(--r-l,14px)',
        padding: '10px 12px',
        background: 'var(--gold-soft)',
      }}
    >
      <div className="small" style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
        <span style={{ flexShrink: 0 }} aria-hidden="true">
          <Icon name="bolt" size={15} />
        </span>
        <span>
          {t('usdtCashback.noteEarnsPrefix')}
          <b>{t('usdtCashback.noteEarnsRate', { pct: usdtPct })}</b>
          {t('usdtCashback.noteEarnsSuffix', {
            factor: factorLabel(m) || t('usdtCashback.noteMore'),
            boost: program?.program?.max_boost_percent
              ? t('usdtCashback.noteBoost', { max: program.program.max_boost_percent })
              : '',
          })}
        </span>
      </div>
      {orderNIM > 0 && (
        <div className="small mt-2" style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <Icon name="wallet" size={15} />
          <span>
            <b>
              {t('usdtCashback.noteCashbackOnOrder', {
                amount:
                  currentCashbackNIM > 0
                    ? `≈ ${fmtCashbackNIM(currentCashbackNIM)} NIM`
                    : t('usdtCashback.zeroNim'),
              })}
            </b>
            {t('usdtCashback.notePaidAfter')}
          </span>
        </div>
      )}
      {!stakedHere && (
        <div className="mt-2">
          <QuickStake collapsed />
        </div>
      )}
    </div>
  );
}
