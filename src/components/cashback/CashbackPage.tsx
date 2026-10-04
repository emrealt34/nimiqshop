import { CashbackFeeNotice } from '../checkout/CashbackFeeNotice';
/**
 * CashbackPage.tsx — the buyer-facing "Cashback & staking" page (v2: single
 * ledger, no tiers).
 *
 * One screen answers the whole question the programme is built on:
 *
 *   what do I earn now → what staking adds on top → stake right now
 *
 * The base is earned by staking (any amount) plus one staker boost that
 * grows out of the pool fees the buyer's stake actually earns — capped at
 * $50/day and $500/month. There are no levels to climb and no locks to
 * choose: the page shows the programme once, then the buyer's own ledger.
 *
 * All stake figures come from the operator's own pool (GET /api/poolstake/me).
 * The chain is never queried from here.
 *
 * v3 layout: rate hero → three-step explainer → calculator → stake form →
 * payouts → fine print (collapsed). The calculator replays the backend
 * ledger model (lib/cashbackCalc.ts).
 *
 * v4: the base is the POOL's call — any positive stake earns the staker base
 * (0.5%), no stake earns nothing (unless the operator runs a promotion).
 * Everything on the page is in NIM; no addresses are shown; recent cashback
 * is paged three at a time and each row shows the rate that order was paid.
 *
 * v5: the NIM word is branded with the site's real NIM symbol
 * (/img/nimiq-hexagon.png, the same asset the product & checkout pages use)
 * next to the currency in every amount, and the dead v3 wallet-block styles
 * are gone. The page keeps the site's standard top bar — same "Connect
 * wallet" / account chip as every other page; there is no page-specific
 * sign-in of its own.
 *
 * v6: the calculator is explicitly PUBLIC. A signed-out visitor sees it (it
 * never read the session before either — only the "Stake this amount" hand-off
 * is wallet-bound), the card says "no account needed", and the page no longer
 * leaves a login wall where the calculator should be. Its numbers stay
 * strictly real: when GET /api/cashback/rate cannot be read the calculator
 * slot explains that and offers a retry, rather than inventing programme
 * defaults — a rate the shop would not pay is worse than no number.
 *
 * v7: v6 stopped at the calculator — the stake form itself was still behind a
 * session check ("Connect your Nimiq wallet to stake") and the calculator's
 * "Stake this amount" hand-off was hidden from signed-out visitors. Both walls
 * are gone. The whole action column is public now: the hero CTA, the calculator
 * hand-off, the preset ladder, the amount field and the stake button all render
 * for a visitor with no shop session, because none of them ever needed one —
 * the transaction is signed by the buyer's own wallet (the native Nimiq Pay
 * dialog, or the guided wallet hand-off in a plain browser), not by the shop
 * login. Signed-out visitors get one extra line saying exactly that
 * (cashback.formNoAccount). What remains session-bound is only what is
 * *personal*: the "add to your stake" wording, the move-to-our-pool flow, the
 * manage tools (active stake / retire / withdraw) and the cashback history —
 * each of those needs the buyer's own stake or ledger to mean anything.
 * The form also renders while the session is still undecided (`null`), so the
 * static HTML carries the real form instead of a skeleton.
 */
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { AppRoot } from '../AppRoot';
import type { ShellKey } from '../shell/SiteShell';
import { Icon } from '../ui/Icon';
import { CashbackCalculator } from './CashbackCalculator';
import { RecentCashbackList } from './RecentCashbackList';
import { CashbackImpactSection } from './CashbackImpactSection';
import { useSheet, useToast } from '../AppProviders';
import { useSession } from '../../lib/useSession';
import { useInNimiqPay } from '../../lib/miniapp';
import { getMyCashback, getNimRate, cachedNimRate, onRatesChange } from '../../lib/api';
import {
  loadStakerProgram,
  clearStakerProgramCache,
  loadMyStake,
  refreshMyStake,
  watchStakeDetection,
  type StakeWatch,
  fmtStakeNIM,
  pctLabel,
  type StakerProgram,
  type StakerLedgerCard,
  type MyStake,
} from '../../lib/stakerCashback';
import {
  MIN_STAKE_NIM,
  POOL_VALIDATOR_BADGE,
  POOL_VALIDATOR_NAME,
  StakeCancelledError,
  StakeInvalidError,
  StakeUnsupportedError,
  consensusEstablished,
  normalizeValidator,
  runStakeOp,
  supportsOp,
  walletStakeGuide,
  type StakeOp,
} from '../../lib/stake';

/** Rows per page in "Recent cashback". */
const PAYOUTS_PER_PAGE = 3;

type CashbackRow = {
  id: string;
  quote_id: string;
  product_id: string;
  amount_nim: number;
  bps: number;
  status: string;
  status_label: string;
  boosted: boolean;
  cashback_source?: string;
  tx_hash?: string;
  paid_at?: string | null;
  created_at?: string;
};

type MyCashback = {
  totals: {
    paid_nim: number;
    paid_count: number;
    pending_nim: number;
    pending_count: number;
    earned_nim?: number;
    burned_nim?: number;
    burned_count?: number;
    wallet_nim?: number;
    wallet_count?: number;
    orders?: number;
    preference?: string;
  };
  cashbacks: CashbackRow[];
  ledger?: StakerLedgerCard;
};

const pct = pctLabel;

import { useT, rich } from '../../i18n';
import { asset } from '../../lib/asset';

type WalletGuide = ReturnType<typeof walletStakeGuide>;

/** The six stake ops, as translation keys (lib/stake.ts owns opLabel()). */
const OP_KEY: Record<StakeOp['kind'], string> = {
  newStaker: 'cashback.opNewStaker',
  addStake: 'cashback.opAddStake',
  setActiveStake: 'cashback.opSetActiveStake',
  changeDelegation: 'cashback.opChangeDelegation',
  retireStake: 'cashback.opRetireStake',
  removeStake: 'cashback.opRemoveStake',
};

/** The guide's four steps, in order (lib/stake.ts owns the raw English). */
const GUIDE_STEP_KEYS = [
  'cashback.guideStep1',
  'cashback.guideStep2',
  'cashback.guideStep3',
  'cashback.guideStep4',
] as const;

/** A non-Pay browser cannot sign a delegation here. Make that route feel like
 * a guided hand-off instead of a disabled button: explain the three wallet
 * steps, then take the buyer to the official wallet automatically. */
function WalletRedirectSheet({ guide }: { guide: WalletGuide }) {
  const { t } = useT();
  const [seconds, setSeconds] = useState(5);
  const [opened, setOpened] = useState(false);

  // Do both actions at the same time after the countdown: open the official
  // wallet URL directly in a new tab. Never create an intermediate blank tab first.
  const openWallet = useCallback(() => {
    let didOpen = false;
    try {
      didOpen = !!window.open(guide.url, '_blank', 'noopener,noreferrer');
    } catch {}
    if (didOpen) {
      // Keep this guide sheet open on the original page. Opening the wallet
      // must not dismiss the instructions or navigate the shop away.
      setOpened(true);
    }
  }, [guide.url]);

  useEffect(() => {
    if (seconds <= 0) {
      openWallet();
      return;
    }
    // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
    const timer = window.setTimeout(() => setSeconds((n) => Math.max(0, n - 1)), 1000);
    return () => window.clearTimeout(timer);
  }, [seconds, openWallet]);

  return (
    <div className="cb-wallet-redirect">
      <div className="cb-wallet-redirect-hero">
        <span className="cb-wallet-redirect-icon"><Icon name="wallet" size={25} /></span>
        <div>
          <div className="strong cb-wallet-redirect-title">{t('cashback.wrTitle')}</div>
          <div className="small muted mt-1">{t('cashback.wrBody')}</div>
        </div>
      </div>

      <div className="cb-wallet-redirect-countdown" role="status" aria-live="polite">
        <Icon name="clock" size={16} />
        {opened
          ? t('cashback.wrOpened')
          : seconds <= 0
            ? t('cashback.wrBlocked')
            : <>{rich(t('cashback.wrOpening', { seconds: String(seconds) }))}</>}
      </div>

      <div className="cb-wallet-redirect-steps">
        {GUIDE_STEP_KEYS.slice(0, 3).map((key, idx) => (
          <div className="cb-wallet-redirect-step" key={key}>
            <span>{idx + 1}</span>
            <div className="small">{t(key, { validator: POOL_VALIDATOR_NAME })}</div>
          </div>
        ))}
      </div>

      <div className="cb-wallet-redirect-validator">
        <img src={POOL_VALIDATOR_BADGE} alt="" width={30} height={30} />
        <span className="small"><strong>{POOL_VALIDATOR_NAME}</strong>{t('cashback.wrChooseValidator')}</span>
      </div>

      <div className="cb-wallet-redirect-actions">
        <button type="button" className="btn btn-gold btn-block" onClick={openWallet} disabled={opened}>
          <Icon name="external" size={15} /> {opened ? t('cashback.wrOpenedBtn') : t('cashback.wrOpenNow')}
        </button>
        <a
          className="btn btn-outline btn-block"
          href={guide.url}
          target="_blank"
          rel="noopener noreferrer"
        >
          {t('cashback.wrOpenTab')}
        </a>
      </div>
    </div>
  );
}

export function CashbackView() {
  const { t } = useT();
  const authed = useSession();
  const { toast } = useToast();
  const { openSheet } = useSheet();

  const [program, setProgram] = useState<StakerProgram | null>(null);
  const [mine, setMine] = useState<MyStake | null>(null);
  const watchRef = useRef<StakeWatch | null>(null);
  const [ledger, setLedger] = useState<MyCashback | null>(null);
  const [amount, setAmount] = useState<string>('10000000');
  const [busy, setBusy] = useState<StakeOp['kind'] | ''>('');
  const [err, setErr] = useState('');
  const [manageOpen, setManageOpen] = useState(false);
  // The cashback card & leaderboard used to drown at the bottom of this long
  // programme page (owner, 2026-10-04) — they now live in their own tab.
  const [tab, setTab] = useState<'program' | 'community'>(() => {
    try {
      const q = new URLSearchParams(window.location.search).get('tab');
      if (q === 'leaderboard' || q === 'community') return 'community';
      if (q === 'program') return 'program';
      return sessionStorage.getItem('nimshop:cb-tab') === 'community' ? 'community' : 'program';
    } catch { return 'program'; }
  });
  useEffect(() => {
    try { sessionStorage.setItem('nimshop:cb-tab', tab); } catch {}
  }, [tab]);
  // Day/night mode for the whole Leaderboard tab (owner, 2026-10-04): ONE
  // toggle themes the cashback card and the leaderboard card together, and
  // the choice is remembered per browser.
  const [lbMode, setLbMode] = useState<'night' | 'day'>(() => {
    try { return localStorage.getItem('nimshop:lb-theme') === 'day' ? 'day' : 'night'; } catch { return 'night'; }
  });
  useEffect(() => {
    try { localStorage.setItem('nimshop:lb-theme', lbMode); } catch {}
  }, [lbMode]);
  // Revealed after a rejected first delegation (the wallet already stakes
  // with another validator) and kept for the session: the fix is the "move"
  // flow, not retrying the same button.
  const [moveHint, setMoveHint] = useState(false);
  // Best-effort consensus note (see consensusEstablished): true = synced.
  const [synced, setSynced] = useState(true);
  const [activeNIM, setActiveNIM] = useState<string>('');
  // NIM rate is still tracked (setPrice) for other displays; the caps are
  // now shown in USD directly, so the converted value is no longer read here.
  const [, setPrice] = useState<number>(() => Number(cachedNimRate()?.usd_per_nim) || 0);
  /** "Try again" on the programme card is in flight. */
  const [retrying, setRetrying] = useState(false);

  const inPay = useInNimiqPay();

  /* -------------------------------------------------------------- loading */

  const reload = useCallback(async () => {
    const [p, m] = await Promise.all([loadStakerProgram(), authed ? loadMyStake() : Promise.resolve(null)]);
    setProgram(p);
    setMine(m);
    if (m && m.stake_nim > 0 && (!activeNIM || Number(activeNIM) === 0)) setActiveNIM(String(m.stake_nim));
    // setActiveNIM is a state setter (stable) — listed because the React
    // Compiler's inferred dependency set includes it, and a mismatch there
    // costs this page its automatic memoization.
  }, [authed, activeNIM, setActiveNIM]);

  /**
   * Re-asks the backend for the live programme. The programme is PUBLIC —
   * it costs a visitor nothing and needs no account — so when it fails to
   * load the page offers this instead of quietly showing no calculator.
   */
  async function retryProgram() {
    setRetrying(true);
    try {
      clearStakerProgramCache();
      setProgram(await loadStakerProgram());
    } finally {
      setRetrying(false);
    }
  }

  useEffect(() => {
    void reload();
  }, [reload]);

  useEffect(() => {
    let alive = true;
    getNimRate()
      .then((r) => {
        if (alive && Number(r?.usd_per_nim) > 0) setPrice(Number(r.usd_per_nim));
      })
      .catch(() => {});
    const off = onRatesChange((r) => {
      if (alive && Number(r.usd_per_nim) > 0) setPrice(Number(r.usd_per_nim));
    });
    return () => {
      alive = false;
      off();
    };
  }, []);

  // Best-effort consensus note: when Nimiq Pay is still syncing with the
  // network, the confirmation dialog can take a moment. Informational only —
  // an old host or a failed check reads as "synced" and never blocks a button.
  useEffect(() => {
    if (!inPay) return;
    let alive = true;
    consensusEstablished().then((ok) => {
      if (alive) setSynced(ok);
    });
    return () => {
      alive = false;
    };
  }, [inPay]);

  useEffect(() => {
    if (!authed) {
      setLedger(null);
      return;
    }
    let alive = true;
    getMyCashback()
      .then((r) => {
        if (!alive) return;
        // Normalise: a proxy error page / partial payload must not crash the
        // whole page (ledger.totals.pending_nim used to throw on `{}`).
        const raw = (r || {}) as Partial<MyCashback>;
        const tot = (raw.totals || {}) as Partial<MyCashback['totals']>;
        setLedger({
          ...raw,
          totals: {
            paid_nim: Number(tot.paid_nim) || 0,
            paid_count: Number(tot.paid_count) || 0,
            pending_nim: Number(tot.pending_nim) || 0,
            pending_count: Number(tot.pending_count) || 0,
          },
          cashbacks: Array.isArray(raw.cashbacks) ? raw.cashbacks : [],
        } as MyCashback);
      })
      .catch(() => {
        /* the ledger is a nice-to-have; the page still works without it */
      });
    return () => {
      alive = false;
    };
  }, [authed]);

  /* ---------------------------------------------------------------- state */

  const params = program?.program || null;
  const validator = normalizeValidator(mine?.pool_validator_address || program?.validator || '');
  const stakedHere = !!mine?.staked && (mine?.stake_luna || 0) > 0;
  // The universal base (non-stakers; 0 by default) and the pool's staker
  // base (any positive stake). Both come from the API — the pool's number,
  // verbatim. There is no local fallback: what the pool publishes is what
  // the page says (an explicit 0 means "no base right now").
  const baseBps = program?.baseBps ?? 0;
  const stakerBaseBps = program?.stakerBaseBps && program.stakerBaseBps > 0 ? program.stakerBaseBps : 100;
  const myRate = mine?.cashback_bps ?? baseBps;
  const boosted = !!mine?.boosted;

  /* --------------------------------------------------------------- actions */

  /**
   * The one place a staking transaction leaves this page. Inside Nimiq Pay the
   * native confirmation dialog IS the confirmation — it shows the exact amount
   * and the validator, so a card tap can send immediately. Outside Pay there is
   * nothing to sign, so the same tap opens the guided wallet hand-off instead.
   */
  async function send(op: StakeOp, successNote?: string) {
    setErr('');
    setBusy(op.kind);
    try {
      const hash = await runStakeOp(op);
      toast(t('cashback.toastStakeSent', { op: t(OP_KEY[op.kind]), hash: String(hash).slice(0, 10) }), 'success');
      // No "I've staked" claim is sent (and the shop no longer accepts one):
      // buyer claims are not evidence, the pool is the single source. If an
      // order is delivered before the pool indexes this delegation, the
      // shop's automatic pool re-check upgrades that cashback within
      // minutes of the index catching up — trust-free.
      // The pool indexes a new/changed delegation on its next pass, so the
      // refreshed rate can still be the old one. Say so rather than implying
      // the new rate is already locked in.
      const fresh = await refreshMyStake();
      if (fresh) setMine(fresh);
      if (fresh?.staked) toast(t('cashback.toastStakerActive', { pct: pct(fresh.cashback_bps) }), 'success');
      else {
        toast(successNote || t('cashback.toastPoolPicksUp'), 'info');
        // The pool's index is the only authority on the delegation, and it
        // lands on its own pass — so watch for it instead of leaving the page
        // showing "not staked" until the buyer reloads by hand.
        watchRef.current?.stop();
        watchRef.current = watchStakeDetection({
          onUpdate: (m) => {
            if (m) setMine(m);
          },
          onDetected: (m) => toast(t('cashback.toastStakerActive', { pct: pct(m.cashback_bps) }), 'success'),
        });
      }
      return true;
    } catch (e) {
      if (e instanceof StakeCancelledError) setErr(e.message);
      else if (e instanceof StakeUnsupportedError) setErr(e.message);
      else if (e instanceof StakeInvalidError) {
        setErr(e.message);
        // A rejected FIRST delegation means the wallet already has a staker
        // (usually with another validator) — the move flow is the way in.
        if (op.kind === 'newStaker') setMoveHint(true);
      } else setErr((e as Error).message || t('cashback.toastTxFailed'));
      return false;
    } finally {
      setBusy('');
    }
  }

  function stakeCustom() {
    const nim = Number(amount);
    if (!Number.isFinite(nim) || !(nim > 0)) {
      setErr(t('cashback.errAmountZero'));
      return;
    }
    if (!stakedHere && nim < MIN_STAKE_NIM) {
      setErr(t('cashback.errFirstStakeMin', { nim: String(MIN_STAKE_NIM) }));
      return;
    }
    if (!inPay) {
      setErr('');
      showWalletRedirect();
      return;
    }
    void send(stakedHere ? { kind: 'addStake', amountNIM: nim } : { kind: 'newStaker', delegation: validator, amountNIM: nim });
  }

  function doSetActive() {
    const nim = Number(activeNIM);
    if (!Number.isFinite(nim) || nim < 0) {
      setErr(t('cashback.errActiveZero'));
      return;
    }
    void send({ kind: 'setActiveStake', activeNIM: nim }, t('cashback.savedMsg'));
  }

  function doRetire() {
    const nim = Number(amount);
    if (!Number.isFinite(nim) || !(nim > 0)) {
      setErr(t('cashback.errAmountZero'));
      return;
    }
    void send({ kind: 'retireStake', amountNIM: nim }, t('cashback.noteRetireSent'));
  }

  function doRemove() {
    const nim = Number(amount);
    if (!Number.isFinite(nim) || !(nim > 0)) {
      setErr(t('cashback.errAmountZero'));
      return;
    }
    void send({ kind: 'removeStake', amountNIM: nim }, t('cashback.noteWithdrawSent'));
  }

  /**
   * For a wallet that already stakes with ANOTHER validator: creating a new
   * staker is impossible (the chain rejects it), so the only way in is moving
   * the existing delegation to our pool — the stake stays, only the validator
   * changes, and everything inactive is reactivated.
   */
  function doMove() {
    if (!validator) {
      setErr(t('cashback.errNoValidator'));
      return;
    }
    void send(
      { kind: 'changeDelegation', delegation: validator, reactivateAll: true },
      t('cashback.noteMoveSent')
    );
  }

  /* --------------------------------------------------------------- render */

  const walletGuide = walletStakeGuide(validator);

  function showWalletRedirect() {
    openSheet({
      title: t('cashback.formNotInPay'),
      render: () => <WalletRedirectSheet guide={walletGuide} />,
    });
  }

  const myLedger = mine?.ledger || ledger?.ledger;
  const accruedNIM = Number(myLedger?.available_cashback_nim) || 0;
  const stakeBusy = busy === 'newStaker' || busy === 'addStake';

  function useCalcAmount(nim: number) {
    setAmount(String(Math.round(nim)));
    setErr('');
    document.getElementById('cb-stake-form')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  const rows = ledger?.cashbacks || [];

  return (
    <div className="container">
      <Header />
      <div className="cb-tabs" role="tablist">
        <button type="button" role="tab" aria-selected={tab === 'program'} className={tab === 'program' ? 'cb-tab active' : 'cb-tab'} onClick={() => setTab('program')}>
          <Icon name="gift" size={14} /> {t('cashback.tabProgram')}
        </button>
        <button type="button" role="tab" aria-selected={tab === 'community'} className={tab === 'community' ? 'cb-tab active' : 'cb-tab'} onClick={() => setTab('community')}>
          <Icon name="trophy" size={14} /> {t('cashback.tabCommunity')}
        </button>
      </div>
      {tab === 'program' && (
        <>
      <CashbackFeeNotice example="nim" />

      {/* ------------------------------------------------------ your rate */}
      <div className="card mt-2 fade-in cb-hero">
        <div className="cb-hero-main">
          <div className="xs faint">{t('cashback.heroNow')}</div>
          <div className="strong cb-rate">
            {pct(myRate)}
            {stakedHere && (
              <span className="chip" style={{ fontSize: 12, color: 'var(--green)' }}>
                <Icon name="check" size={12} /> {boosted && (mine?.boost_bps || 0) > 0 ? t('cashback.heroBaseBoost') : t('cashback.heroBase')}
              </span>
            )}
          </div>
          <div className="small muted mt-1">
            {!authed
              ? t('cashback.heroStakePrompt', { pct: pct(stakerBaseBps), max: String(params?.max_boost_percent ?? '?') })
              : stakedHere
                ? t('cashback.heroStaked', {
                    nim: fmtStakeNIM(mine?.stake_nim || 0),
                    loyalty: (mine?.loyalty_days || 0) > 0 ? t('cashback.heroLoyalty', { days: String(mine?.loyalty_days) }) : '',
                  })
                : t('cashback.heroNoStake', { pct: pct(stakerBaseBps), max: String(params?.max_boost_percent ?? '?') })}
          </div>
        </div>

        {/* The buyer's own numbers, at a glance: what they stake (the asset),
            what it has earned (boost credit) and what the shop has paid. The
            stake stat leads — it is the number a staked buyer manages here. */}
        {authed && (ledger || stakedHere) && (
          <div className="cb-hero-stats">
            {stakedHere && (
              <div>
                <div className="xs faint">{t('cashback.statYourStake')}</div>
                <div className="strong">
                  {fmtStakeNIM(mine?.stake_nim || 0)} <NimUnit size={13} />
                </div>
              </div>
            )}
            {myLedger && (
              <div>
                <div className="xs faint">{t('cashback.statBoostCredit')}</div>
                <div className="strong" style={{ color: accruedNIM > 0 ? 'var(--green)' : undefined }}>
                  {fmtStakeNIM(accruedNIM)} <NimUnit size={13} />
                </div>
              </div>
            )}
            {ledger && (
              <>
                <div>
                  <div className="xs faint">{t('cashback.statPending')}</div>
                  <div className="strong">{fmtStakeNIM(ledger.totals.pending_nim)} <NimUnit size={13} /></div>
                </div>
                <div>
                  <div className="xs faint">{t('cashback.statPaid')}</div>
                  <div className="strong">{fmtStakeNIM(ledger.totals.paid_nim)} <NimUnit size={13} /></div>
                </div>
              </>
            )}
          </div>
        )}

        {/* Public, like everything else on this page: the CTA is a hand-off to
            the stake form (or to the wallet guide), and neither needs the shop
            session. A signed-out visitor used to get no CTA at all. */}
        {!stakedHere && (
          <div className="cb-hero-cta">
            {inPay ? (
              <a className="btn btn-gold" href="#cb-stake-form">
                <Icon name="bolt" size={14} /> {t('cashback.ctaStartStaking')}
              </a>
            ) : (
              <button type="button" className="btn btn-gold" onClick={showWalletRedirect}>
                <Icon name="external" size={14} /> {t('cashback.ctaStartInWallet')}
              </button>
            )}
          </div>
        )}
      </div>

      {/* --------------------------------------------------- how it works */}
      {params && (
        <div className="cb-steps mt-2">
          <div className="cb-step">
            <span className="cb-step-n">1</span>
            <div>
              <div className="small strong">{t('cashback.step1Title')}</div>
              <div className="xs faint">{t('cashback.step1Body', { pct: pct(stakerBaseBps) })}</div>
            </div>
          </div>
          <div className="cb-step">
            <span className="cb-step-n">2</span>
            <div>
              <div className="small strong">{t('cashback.step2Title')}</div>
              <div className="xs faint">{t('cashback.step2Body', { nim: fmtStakeNIM(params.min_stake_nim) })}</div>
            </div>
          </div>
          <div className="cb-step">
            <span className="cb-step-n">3</span>
            <div>
              <div className="small strong">{t('cashback.step3Title')}</div>
              <div className="xs faint">
                {t('cashback.step3Body', { pct: pct(stakerBaseBps), max: String(params.max_boost_percent) })}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* ------------------------------------------------------ calculator */}
      {/*
        PUBLIC on purpose. The calculator answers "what would I get back?"
        before a visitor has a wallet or an account — nothing on this card
        reads the session. Its numbers come from the live, unauthenticated
        programme (GET /api/cashback/rate); when that read fails the card
        keeps working on the last-known / published default programme and
        says so in one honest line — the maths needs no backend, and the
        money path still quotes live server-side.
      */}
      {params ? (
        <>
        {program?.degraded && (
          <div role="status" style={{ display: 'flex', gap: 6, alignItems: 'flex-start', margin: '16px 0 8px', padding: '7px 10px', borderRadius: 'var(--r-m)', background: 'var(--gold-grad-soft)', border: '1px solid var(--line)', fontSize: 'var(--fs-xs)', lineHeight: 1.4, color: 'var(--ink)' }}>
            <Icon name="info" size={13} style={{ flex: '0 0 auto', marginTop: 1, color: 'var(--gold-600)' }} />
            <span>{t('cashback.programDegraded')}</span>
          </div>
        )}
        <CashbackCalculator
          program={params}
          baseBps={baseBps}
          stakerBaseBps={stakerBaseBps}
          myStakeNIM={stakedHere ? mine?.stake_nim : undefined}
          myLoyaltyDays={stakedHere ? mine?.loyalty_days : undefined}
          onUseAmount={useCalcAmount}
        />
        </>
      ) : (
        <ProgrammeUnavailable loadError={!!program?.loadError} busy={retrying} onRetry={retryProgram} />
      )}

      {/* ------------------------------------------------- stake / manage */}
      <div className="card mt-2" id="cb-stake-form">
        <div className="card-title">
          <Icon name="bolt" size={16} /> {stakedHere ? t('cashback.formAddToStake') : t('cashback.ctaStartStaking')}
        </div>

        {/*
          PUBLIC on purpose, like the calculator: a visitor with no shop
          session still sees the whole form (presets, amount, stake button).
          Nothing here reads the session — the number, the validator line and
          the button are the same for everyone. Staking itself happens in the
          buyer's own Nimiq wallet, so the shop login is not what authorises
          it: inside Nimiq Pay the native dialog signs the transaction, and in
          a browser the button opens the guided wallet hand-off. The session
          only adds what is *personal* — the "add to your stake" wording, the
          move flow and the manage tools, all of which need the buyer's own
          stake to mean anything.
        */}
        {authed === false && (
          <div className="xs faint" style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8 }}>
            <Icon name="info" size={13} /> {t('cashback.formNoAccount')}
          </div>
        )}

        <>
          {/* Quick amounts: staking should be a two-tap affair — tap a
              preset, tap the button. A staked wallet sees "+N" (top-ups),
              and the preset ladder starts at 100,000 NIM. */}
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 8 }}>
            {[100_000, 500_000, 1_000_000, 10_000_000].map((n) => (
              <button
                key={n}
                type="button"
                className="btn btn-sm"
                style={{
                  borderRadius: 999,
                  ...(Number(amount) === n
                    ? { background: 'var(--gold)', color: 'var(--on-gold)', borderColor: 'var(--gold)' }
                    : {}),
                }}
                onClick={() => {
                  setAmount(String(n));
                  setErr('');
                }}
              >
                {stakedHere ? `+${n.toLocaleString('en-US')}` : n.toLocaleString('en-US')}
              </button>
            ))}
          </div>
          <div className="cb-form-row">
            <input
              className="input"
              type="number"
              inputMode="decimal"
              min={MIN_STAKE_NIM}
              step="1"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              aria-label={t('cashback.formAmountAria')}
            />
            <NimUnit size={12} className="cb-unit" />
            <button className="btn btn-gold cb-primary" onClick={stakeCustom} disabled={!!busy || (inPay && !validator)}>
              <Icon name={inPay ? 'bolt' : 'external'} size={14} />
              {stakeBusy ? t('cashback.formWaitingPay') : !inPay ? t('cashback.formOpenWalletStake') : stakedHere ? t('cashback.opAddStake') : t('cashback.opNewStaker')}
            </button>
          </div>
          <div className="xs faint mt-1" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <img src={POOL_VALIDATOR_BADGE} alt="" width={16} height={16} style={{ borderRadius: 4, background: 'var(--white-card)' }} />
            {stakedHere
              ? t('cashback.formAddsExisting')
              : t('cashback.formDelegates', { validator: POOL_VALIDATOR_NAME, min: String(MIN_STAKE_NIM) })}
          </div>

          {inPay && !synced && (
            <div className="xs mt-1" style={{ display: 'flex', gap: 6, alignItems: 'flex-start', color: 'var(--stamp, #B4471C)' }}>
              <Icon name="clock" size={13} />
              <span>{t('cashback.formSyncing')}</span>
            </div>
          )}

          {/* A wallet that already stakes with ANOTHER validator cannot send
              a new staker (the chain rejects it) — the move flow is its only
              way in. Shown inside Pay, and always after such a rejection. */}
          {authed && !stakedHere && (inPay || moveHint) && (
            <div className="mt-2" style={{ borderTop: '1px dashed var(--line-dash, #e0d7c2)', paddingTop: 10 }}>
              <div className="xs faint">
                {moveHint
                  ? t('cashback.formMoveHint')
                  : t('cashback.formMovePrompt', { validator: POOL_VALIDATOR_NAME })}
              </div>
              <button
                className="btn btn-outline btn-block mt-1"
                onClick={doMove}
                disabled={!!busy || !validator || !supportsOp({ kind: 'changeDelegation', delegation: validator })}
              >
                <Icon name="chevron" size={14} />
                {busy === 'changeDelegation' ? t('cashback.formWaitingPay') : t('cashback.formMoveBtn', { validator: POOL_VALIDATOR_NAME })}
              </button>
            </div>
          )}

          {err && (
            <div className="alert error mt-2" style={{ marginBottom: 0 }}>
              <div className="small">{err}</div>
            </div>
          )}

          {!inPay && (
            <div className="cb-wallet-redirect-inline mt-2">
              <div className="cb-wallet-redirect-inline-copy">
                <span className="cb-wallet-redirect-inline-icon"><Icon name="info" size={15} /></span>
                <span>
                  <strong>{t('cashback.formNotInPay')}</strong>
                  <small>{t('cashback.formNotInPayBody')}</small>
                </span>
              </div>
              <button type="button" className="btn btn-outline btn-sm" onClick={showWalletRedirect}>
                <Icon name="external" size={14} /> {t('cashback.wrOpenTab')}
              </button>
            </div>
          )}

          {stakedHere && (
            <details className="cb-details mt-2" open={manageOpen} onToggle={(e) => setManageOpen((e.target as HTMLDetailsElement).open)}>
              <summary className="small strong">
                <Icon name="chevron" size={14} /> {t('cashback.formMoreTools')}
              </summary>
              <div className="mt-2">
                <div className="xs faint">{t('cashback.formActiveStakeLabel')}</div>
                <div className="cb-form-row">
                  <input
                    className="input"
                    type="number"
                    min={MIN_STAKE_NIM}
                    step="1"
                  value={activeNIM}
                  onChange={(e) => setActiveNIM(e.target.value)}
                  aria-label={t('cashback.formActiveStakeAria')}
                />
                <NimUnit size={12} className="cb-unit" />
                  <button
                    className="btn"
                    onClick={doSetActive}
                    disabled={busy === 'setActiveStake' || !supportsOp({ kind: 'setActiveStake', activeNIM: 0 })}
                  >
                    {busy === 'setActiveStake' ? t('cashback.formWaiting') : t('cashback.opSetActiveStake')}
                  </button>
                </div>

                <div className="xs faint mt-2">{t('cashback.formRetireNote')}</div>
                <div className="cb-tools-row">
                  <button className="btn" onClick={doRetire} disabled={busy === 'retireStake'}>
                    {busy === 'retireStake' ? t('cashback.formWaiting') : t('cashback.opRetireStake')}
                  </button>
                  <button className="btn" onClick={doRemove} disabled={busy === 'removeStake'}>
                    {busy === 'removeStake' ? t('cashback.formWaiting') : t('cashback.opRemoveStake')}
                  </button>
                </div>
                <div className="xs faint mt-2">{t('cashback.formWithdrawResets', { pct: pct(stakerBaseBps) })}</div>
              </div>
            </details>
          )}
        </>
      </div>

        </>
      )}
      {tab === 'community' && (
        <div className={`lb-wrap lb-${lbMode}`}>
      {/* ------------------------------------ cashback rows — visible logged in or not (user: giriş farketmemeli) */}
      {(authed ? (
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
      ))}

      {/* ------------------------------------------------------ burn stats, cashback card & leaderboard */}
      <CashbackImpactSection authed={authed === true} myTotals={ledger?.totals || null} lbMode={lbMode} onLbMode={setLbMode} />
        </div>
      )}
      {tab === 'program' && (
        <>

      {/* ------------------------------------------------------ fine print */}
      <details className="cb-details cb-fineprint mt-2">
        <summary className="small strong">
          <Icon name="info" size={14} /> {t('cashback.fineTitle')}
        </summary>
        <ul className="small muted" style={{ margin: '8px 0 0', paddingLeft: 18 }}>
          <li>{rich(t('cashback.fine1', { base: pct(stakerBaseBps), zero: pct(baseBps) }))}</li>
          <li>
            {t('cashback.fine2', {
              dayCap: `$${params?.daily_cap_usd || 50}`,
              monthCap: `$${params?.monthly_cap_usd || 500}`,
            })}
          </li>
          <li>{t('cashback.fine3', { carry: String(Math.round((params?.carry_share ?? 0.1) * 100)) })}</li>
          <li>{t('cashback.fine4')}</li>
          <li>{t('cashback.fine5')}</li>
        </ul>
      </details>
        </>
      )}
    </div>
  );

}

/**
 * The calculator card when there is nothing REAL to calculate with.
 *
 * It never guesses: a rate the shop would not pay is worse than no number at
 * all. What it does make clear is that the gap is on our side — this page and
 * its calculator are public, and a visitor never needs an account to see them.
 */
function ProgrammeUnavailable({
  loadError,
  busy,
  onRetry,
}: {
  loadError: boolean;
  busy: boolean;
  onRetry: () => void;
}) {
  const { t } = useT();
  return (
    <div className="card mt-2 cb-calc">
      <div className="card-title">
        <Icon name="spark" size={16} /> {t('cashback.calcTitle')}
      </div>
      <div className="small muted" style={{ margin: '2px 0 8px' }}>
        {t('cashback.progLoadErr')}
      </div>
      <div className="xs faint">
        {t('cashback.progPublic')}
      </div>
      <button
        type="button"
        className="btn btn-sm mt-2"
        onClick={onRetry}
        disabled={busy || !loadError}
        style={loadError ? undefined : { visibility: 'hidden' }}
      >
        <Icon name="refresh" size={14} /> {busy ? t('actions.loading') : t('common.tryAgain')}
      </button>
    </div>
  );
}

/** The real NIM symbol — the same public asset the rest of the site uses. */
export function NimiqLogo({ size = 24 }: { size?: number }) {
  const h = Math.round(size * 0.9);
  return (
    <img
      src={asset("/img/nimiq-hexagon.png?v=40")}
      alt=""
      width={size}
      height={h}
      decoding="async"
      fetchPriority="low"
      aria-hidden="true"
      style={{ display: 'inline-block', verticalAlign: '-0.15em', flex: 'none', objectFit: 'contain' }}
    />
  );
}

/**
 * The NIM word, branded: hexagon + "NIM" as one unbreakable unit. Every
 * amount on this page ends in this, so the currency always carries the mark.
 */
export function NimUnit({ size = 12, className = '' }: { size?: number; className?: string }) {
  return (
    <span className={'nim-unit' + (className ? ' ' + className : '')} aria-label="NIM">
      <NimiqLogo size={size} />
      <span style={{ fontSize: '0.92em', fontWeight: 700, letterSpacing: '0.02em' }}>NIM</span>
    </span>
  );
}

function Header() {
  const { t } = useT();
  return (
    <>
      <h1 className="cb-title" style={{ display: 'flex', alignItems: 'center', gap: '10px', margin: '4px 0 2px' }}>
        <NimiqLogo size={28} /> {t('cashback.headerTitle')}
      </h1>
      <p className="lede">{t('cashback.headerLede')}</p>
    </>
  );
}

/** Static-page wrapper: mounts the providers + shell around the content.
 *  The Leaderboard nav tab deep-links here with ?tab=leaderboard, and then
 *  the shell highlights THAT tab instead of Cashback (owner, 2026-10-04). */
export function CashbackPage() {
  const [activeKey] = useState<ShellKey>(() => {
    try {
      return new URLSearchParams(window.location.search).get('tab') === 'leaderboard' ? 'leaderboard' : 'cashback';
    } catch { return 'cashback'; }
  });
  return (
    <AppRoot activeKey={activeKey}>
      <CashbackView />
    </AppRoot>
  );
}
