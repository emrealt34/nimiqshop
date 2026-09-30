/**
 * StakerCashback — the buyer-facing half of "stake in our pool, earn more
 * cashback" (v2: single ledger, no tiers).
 *
 * <StakerCashbackLine />  one compact line for product/checkout screens.
 * <StakerCashbackCard />  a full card (profile) with the in-app stake CTA.
 *
 * Both render nothing at all when the programme is not configured, so a shop
 * without POOL_API_URL or without a feed key looks exactly as it did before
 * this feature existed.
 */
import { useEffect, useState, type ReactNode, type CSSProperties } from 'react';
import {
  loadStakerProgram,
  loadMyStake,
  refreshMyStake,
  fmtStakeNIM,
  fmtUSD,
  type StakerProgram,
  type MyStake,
} from '../../lib/stakerCashback';
import { stakeWithUs, canStakeInApp, MIN_STAKE_NIM, StakeCancelledError, StakeInvalidError } from '../../lib/stake';
import { QuickStake } from '../checkout/QuickStake';
import { useInNimiqPay } from '../../lib/miniapp';
import { useSession } from '../../lib/useSession';
import { useToast } from '../AppProviders';
import { useT, rich, t as tr } from '../../i18n';
import { Icon } from '../ui/Icon';
import { ComeBackBanner } from '../ui/uiKit';
import { estimateCashbackNIM, fmtCashbackNIM } from '../../lib/cashback';
import { pagePath } from '../../lib/asset';

function pct(bps: number): string {
  const p = bps / 100;
  return Number.isInteger(p) ? String(p) + '%' : p.toFixed(2) + '%';
}

function useStakerState() {
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
    if (!authed || !program?.enabled) {
      setMine(null);
      return;
    }
    loadMyStake().then((m) => {
      if (alive) setMine(m);
    });
    return () => {
      alive = false;
    };
  }, [authed, program?.enabled]);

  return { authed, program, mine, setMine };
}

/** One compact, clickable line that links to the cashback & staking page. */
function Line({ className, style, children }: { className?: string; style?: CSSProperties; children: ReactNode }) {
  return (
    <a href={pagePath("/cashback")} className={`staker-cb-link ${className || ''}`.trim()} style={style}>
      {children}
    </a>
  );
}

/** The site-wide "come back" banner — now the ONE shared component, so the
 *  markup/copy is byte-for-byte the home page's (which renders responsively). */
function ComeBack({ program }: { program: StakerProgram | null }) {
  const { t } = useT();
  const max = program?.program?.max_boost_percent;
  return (
    <ComeBackBanner
      className="mt-2"
      label={t('staker.needsStake')}
      text={t('staker.needsStakeText', { pct: pct(program?.stakerBaseBps || 100), max: String(max || '?') })}
    />
  );
}

/**
 * The payment-screen version shows the locked money, not just a rate. The
 * quote's estimate is already rounded with the backend's Luna rules, so prefer
 * that snapshot and only calculate a fallback when an older quote omitted it.
 */
function quoteCashbackAmount(quote: any, bps: number): number {
  const locked = Number(quote?.estimated_cashback_nim);
  if (Number.isFinite(locked) && locked > 0) return locked;
  return estimateCashbackNIM(Number(quote?.estimated_nim) || 0, bps);
}

export function CheckoutCashbackSummary({
  quote,
  program,
  mine,
  inPay,
}: {
  quote: any;
  program: StakerProgram | null;
  mine: MyStake | null;
  inPay: boolean;
}) {
  const { t } = useT();
  const currentBps = Number.isFinite(Number(quote?.cashback_bps)) ? Number(quote.cashback_bps) : 0;
  const currentAmount = quoteCashbackAmount(quote, currentBps);
  const orderNim = Number(quote?.estimated_nim) || 0;
  const codeExclusive = String(quote?.cashback_source || '') === 'code' || quote?.cashback_exclusive === true;
  const stakedHere = !!mine?.staked && Number(mine?.stake_luna || 0) > 0;
  const stakerBps = stakedHere ? Number(mine?.cashback_bps || 0) : Number(program?.stakerBaseBps || 0);
  const stakerAmount = orderNim > 0 ? estimateCashbackNIM(orderNim, stakerBps) : 0;
  const canCompare = !codeExclusive && !!program?.enabled && orderNim > 0 && stakerBps > 0;
  const earnsMoreWithStake = canCompare && !stakedHere && (stakerBps > currentBps || stakerAmount > currentAmount);
  const maxBoost = Number(program?.program?.max_boost_percent || 0);

  return (
    <div className="alert info mt-1" role="status" style={{ marginBottom: 0, display: 'block' }}>
      <div style={{ display: 'flex', gap: '8px', alignItems: 'flex-start' }}>
        <Icon name="wallet" size={18} />
        <div className="small" style={{ flex: 1, minWidth: 0 }}>
          <div><b>{t('staker.onThisOrder')} {currentAmount > 0 ? `≈ ${fmtCashbackNIM(currentAmount)} NIM` : '0 NIM'}</b></div>
          <div className="xs faint mt-1">{t('staker.paidAfterDelivery', { pct: pct(currentBps) })}</div>
        </div>
      </div>
      {earnsMoreWithStake && (
        <div className="small mt-2" style={{ display: 'flex', gap: '8px', alignItems: 'flex-start', color: 'var(--green, #2f5540)' }}>
          <Icon name="spark" size={16} />
          <span>
            {rich(t('staker.withStake', {
              nim: fmtCashbackNIM(stakerAmount),
              pct: pct(stakerBps),
              boost: maxBoost > 0 ? t('staker.withStakeBoostSuffix', { max: String(maxBoost) }) : '',
            }))}.
          </span>
        </div>
      )}
      {canCompare && !stakedHere && !earnsMoreWithStake && (
        <div className="small mt-2" style={{ display: 'flex', gap: '8px', alignItems: 'flex-start', color: 'var(--green, #2f5540)' }}>
          <Icon name="spark" size={16} />
          <span>{t('staker.unlockRate', {
            pct: pct(stakerBps),
            more: maxBoost > 0 ? t('staker.unlockRateMoreSuffix', { max: String(maxBoost) }) : '',
          })}.</span>
        </div>
      )}
      {stakedHere && currentBps > 0 && (
        <div className="small mt-2" style={{ display: 'flex', gap: '8px', alignItems: 'flex-start', color: 'var(--green, #2f5540)' }}>
          <Icon name="check" size={16} /> <span>{t('staker.stakeIncluded', { pct: pct(currentBps) })}</span>
        </div>
      )}
      {inPay && !stakedHere && canCompare && <QuickStake collapsed />}
    </div>
  );
}

/**
 * Compact one-liner. Shows the rate the buyer actually earns plus the money
 * they have accrued. Tapping it opens the cashback & staking page.
 */
export function StakerCashbackLine({ quote }: { quote?: any } = {}) {
  const { t } = useT();
  const { authed, program, mine } = useStakerState();
  // Reactive Pay detection: the provider can arrive a beat after mount (SDK
  // init), and the one-tap CTA must appear the moment it does.
  const inPay = useInNimiqPay();
  // A locked quote is the payment screen: show the exact amount and the
  // staker comparison instead of the generic rate-only line.
  if (quote) return <CheckoutCashbackSummary quote={quote} program={program} mine={mine} inPay={inPay} />;
  if (!program?.enabled) {
    return <ComeBack program={program} />;
  }

  // useSession() is null until mounted: render nothing rather than the
  // signed-out branch, which would flash on every navigation (see lib/useSession.ts).
  if (authed === null) return null;

  if (!authed || !mine) {
    return <ComeBack program={program} />;
  }

  if (mine.staked && mine.cashback_bps > 0) {
    const usd = fmtUSD(mine.ledger?.available_cashback_usd);
    const dayLeft = fmtUSD(mine.ledger?.daily_remaining_usd);
    const monthLeft = fmtUSD(mine.ledger?.monthly_remaining_usd);
    const hasBoost = (mine.boost_bps || 0) > 0;
    return (
      <Line className="small mt-1" style={{ color: 'var(--green, #16a34a)' }}>
        <Icon name="check" size={13} />{' '}
        {t('staker.rateLine', {
          kind: hasBoost ? t('staker.basePlusBoost') : t('staker.base'),
          pct: pct(mine.cashback_bps),
        })}
        {usd ? t('staker.accruedSuffix', { usd }) : ''}
        {(dayLeft || monthLeft) ? (
          <span className="muted">{t('staker.capLeftInline', { day: dayLeft || '—', month: monthLeft || '—' })}</span>
        ) : ''}
      </Line>
    );
  }

  // Not staked: the base is 0 — say what any stake unlocks.
  const stakerBase = program.stakerBaseBps || 100;
  return (
    <div className="mt-1">
      <Line className="small muted">
        <Icon name="spark" size={13} /> {t('staker.earnBase', {
          pct: pct(stakerBase),
          max: String(program.program?.max_boost_percent ?? 0),
        })}
      </Line>
      {/* Inside Nimiq Pay the push is one tap away — no detour to another
          page while a payment is on screen. Outside Pay the link above is
          the whole answer (nothing can be signed in a browser). */}
      {inPay && <QuickStake collapsed />}
    </div>
  );
}

/**
 * Full card with the in-app delegation flow. Used on the profile page.
 */
export function StakerCashbackCard() {
  const { toast } = useToast();
  const { t } = useT();
  const { authed, program, mine, setMine } = useStakerState();
  const [amount, setAmount] = useState<string>(String(1000));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  if (!program?.enabled) return null;

  const params = program.program!;
  const validator = mine?.pool_validator_address || program.validator;
  const stakeable = canStakeInApp(validator);
  const amountNIM = Number(amount);

  async function doStake() {
    setErr('');
    if (!(amountNIM > 0)) {
      setErr(t('staker.errAmountZero'));
      return;
    }
    if (amountNIM < MIN_STAKE_NIM) {
      setErr(t('staker.errMinStake', { nim: String(MIN_STAKE_NIM) }));
      return;
    }
    setBusy(true);
    try {
      const hash = await stakeWithUs(validator, amountNIM, !!mine?.staked);
      toast(t('staker.toastStakeSent'), 'success');
      // The pool indexes a new delegation on the next epoch pass, so the
      // refreshed rate can still be the old one. Say so instead of lying.
      const fresh = await refreshMyStake();
      if (fresh) setMine(fresh);
      toast(
        fresh && fresh.boosted
          ? t('staker.toastBoostActive', { pct: pct(fresh.cashback_bps) })
          : t('staker.toastStakeRecorded', { hash: String(hash).slice(0, 10) }),
        fresh && fresh.boosted ? 'success' : 'info'
      );
    } catch (e) {
      if (e instanceof StakeCancelledError) setErr(e.message);
      else if (e instanceof StakeInvalidError)
        // Most common case: the wallet already stakes with another validator,
        // so a NEW staker is impossible — the Cashback page's move flow is
        // the way in.
        setErr(e.message + ' ' + t('staker.moveHintSuffix'));
      else setErr((e as Error).message || tr('checkout.stakeFailed'));
    } finally {
      setBusy(false);
    }
  }

  const ledger = mine?.ledger;

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="spark" size={16} /> {t('staker.cardTitle')}
      </div>

      <div className="small muted">
        {rich(t('staker.cardIntro', {
          pct: pct(program.baseBps),
          max: String(params.max_boost_percent),
          day: String(params.daily_cap_usd),
          month: String(params.monthly_cap_usd),
        }))}
      </div>

      {authed === null ? (
        <div className="small muted mt-2">{t('staker.checkingSession')}</div>
      ) : !authed ? (
        <div className="small muted mt-2">{t('staker.signInPrompt')}</div>
      ) : mine?.stake_check_error ? (
        <div className="alert warn mt-2">
          <div className="small">{t('staker.poolDownNotice', { pct: pct(program.baseBps) })}</div>
        </div>
      ) : mine ? (
        <div className="mt-2">
          <div className="kv">
            <div className="row between">
              <span className="small muted">{t('staker.activeStake')}</span>
              <span className="strong">{fmtStakeNIM(mine.stake_nim)} NIM</span>
            </div>
            <div className="row between">
              <span className="small muted">{t('staker.cashbackOnOrders')}</span>
              <span className="strong" style={{ color: mine.boosted ? 'var(--green, #16a34a)' : undefined }}>
                {pct(mine.cashback_bps)}
                {mine.boosted ? t('staker.basePlusBoostShort') : ''}
              </span>
            </div>
            {ledger?.has_ledger ? (
              <>
                <div className="row between">
                  <span className="small muted">{t('staker.cashbackAccrued')}</span>
                  <span className="strong" style={{ color: 'var(--green, #16a34a)' }}>
                    {fmtUSD(ledger.available_cashback_usd) || fmtStakeNIM(ledger.available_cashback_nim || 0) + ' NIM'}
                    {Number(ledger.available_cashback_nim || 0) > 0 ? ` (${fmtStakeNIM(ledger.available_cashback_nim || 0)} NIM)` : ''}
                  </span>
                </div>
                <div className="row between">
                  <span className="small muted">{t('staker.capLeftToday')}</span>
                  <span className="small strong">{fmtUSD(ledger.daily_remaining_usd) || '—'}</span>
                </div>
                <div className="row between">
                  <span className="small muted">{t('staker.capLeftMonth')}</span>
                  <span className="small strong">{fmtUSD(ledger.monthly_remaining_usd) || '—'}</span>
                </div>
              </>
            ) : (
              <div className="row between">
                <span className="small muted">{t('staker.cashbackAccrued')}</span>
                <span className="small muted">{t('staker.startsWhenCredited')}</span>
              </div>
            )}
          </div>

          <div className="row mt-2" style={{ gap: 8, alignItems: 'center' }}>
            <input
              type="number"
              inputMode="decimal"
              min={MIN_STAKE_NIM}
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              aria-label={t('staker.amountAria')}
              style={{ width: 120 }}
            />
            <span className="small muted">NIM</span>
            <button className="btn" onClick={doStake} disabled={busy || !stakeable} style={{ marginLeft: 'auto' }}>
              {busy ? t('staker.waitingPay') : mine.staked ? t('staker.addStake') : t('staker.stakeWithUs')}
            </button>
          </div>
          {!stakeable && (
            <div className="xs faint mt-1">{t('staker.openInPayNote')}</div>
          )}
          {err && <div className="alert error mt-1"><div className="small">{err}</div></div>}
        </div>
      ) : (
        <div className="small muted mt-2">{t('staker.loadingStake')}</div>
      )}
    </div>
  );
}
