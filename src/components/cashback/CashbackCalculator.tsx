/**
 * CashbackCalculator.tsx — "what would I earn?" on the Cashback page.
 *
 * Everything is in NIM: the stake, the order value, the result. Two sliders,
 * one loyalty pick. The maths is lib/cashbackCalc.ts, a replay of the
 * backend's single-ledger model, so the number shown is the number the shop
 * would actually pay under the same assumptions.
 *
 * The base rule lives in the pool and is mirrored here: staked with ANY
 * amount → the staker base (0.5%); not staked → no base at all (0%), unless
 * the operator runs a promotion. The ledger boost stacks on top.
 */
import { useEffect, useMemo, useState } from 'react';
import { Icon } from '../ui/Icon';
import { getNimRate, cachedNimRate } from '../../lib/api';
import { estimateCashback, sliderToValue, valueToSlider } from '../../lib/cashbackCalc';
import { FALLBACK_RAMP_DAYS, fmtStakeNIM, pctLabel, type StakeCashbackProgram } from '../../lib/stakerCashback';
import { NimUnit } from './CashbackPage';
import { useT } from '../../i18n';

const STAKE_MAX = 1_000_000_000;
/** Monthly order value slider, NIM: 1K → 10M. */
const SPEND_MIN = 1_000;
const SPEND_MAX = 10_000_000;

function clampSpend(n: number): number {
  return Math.max(SPEND_MIN, Math.min(SPEND_MAX, n));
}

function clampStake(n: number, min: number): number {
  if (!(n > 0)) return 0;
  return Math.max(min, Math.min(STAKE_MAX, n));
}

/**
 * Typed-amount helpers. The input shows a comma-formatted number while
 * unfocused and the user's raw text while focused; no matter which string a
 * keystroke lands in, only digits and ONE decimal point survive, so a zero
 * can never "stick" into a formatted value or a comma turn the number into
 * a decimal (e.g. "10,000,000" + "0" still means 10,000,000, not 1.0).
 */
function cleanNimText(s: string): string {
  const t = s.replace(/[^0-9.]/g, '');
  const i = t.indexOf('.');
  return i < 0 ? t : t.slice(0, i + 1) + t.slice(i + 1).replace(/\./g, '');
}
function parseNim(s: string): number {
  const t = cleanNimText(s);
  if (t === '' || t === '.') return NaN;
  const n = Number(t);
  return Number.isFinite(n) && n >= 0 ? n : NaN;
}
/** Network-wide staking reward estimate; the buyer can change it. */
const DEFAULT_APY = 15;
const POOL_FEE_PCT = 5;

function nim(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0';
  if (n < 1) return n.toFixed(2).replace(/\.?0+$/, '');
  if (n < 100) return n.toFixed(1).replace(/\.0$/, '');
  return fmtStakeNIM(n);
}
function pctText(n: number): string {
  if (!(n > 0)) return '0%';
  if (n < 0.01) return '<0.01%';
  return (n >= 10 ? n.toFixed(1) : n >= 1 ? n.toFixed(2) : n.toFixed(3)).replace(/\.?0+$/, '') + '%';
}

export function CashbackCalculator({
  program,
  baseBps,
  stakerBaseBps,
  myStakeNIM,
  myLoyaltyDays,
  onUseAmount,
}: {
  program: StakeCashbackProgram;
  /** Operator's universal base (non-stakers), bps. 0 by default. */
  baseBps: number;
  /** The pool's staker base, bps — earned by ANY positive stake. */
  stakerBaseBps: number;
  /** Signed-in buyer's live stake — pre-fills the slider. */
  myStakeNIM?: number;
  myLoyaltyDays?: number;
  /** "Stake this amount" hands the slider value to the stake form. */
  onUseAmount?: (nim: number) => void;
}) {
  const stakeMin = Math.max(1, program.min_stake_nim || 100);
  const [stake, setStake] = useState<number>(() => (myStakeNIM && myStakeNIM > 0 ? myStakeNIM : 10_000));
  const [stakeText, setStakeText] = useState<string>('');
  const [spend, setSpend] = useState<number>(SPEND_MIN);
  const [spendText, setSpendText] = useState<string>('');
  const [loyalty, setLoyalty] = useState<number>(() => Math.max(0, myLoyaltyDays || 0));
  const [apy, setApy] = useState<string>(String(DEFAULT_APY));
  const [price, setPrice] = useState<number>(() => Number(cachedNimRate()?.usd_per_nim) || 0);
  const [advanced, setAdvanced] = useState(false);

  useEffect(() => {
    let alive = true;
    getNimRate()
      .then((r) => {
        if (alive && Number(r?.usd_per_nim) > 0) setPrice(Number(r.usd_per_nim));
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    if (myStakeNIM && myStakeNIM > 0) setStake(myStakeNIM);
  }, [myStakeNIM]);
  useEffect(() => {
    if (myLoyaltyDays !== undefined) setLoyalty(Math.max(0, myLoyaltyDays));
  }, [myLoyaltyDays]);

  // Same fallback the maths uses, so the loyalty presets and the multiplier can
  // never disagree.
  const ramp = Math.max(1, program.ramp_days || FALLBACK_RAMP_DAYS);

  const { t } = useT();
  const r = useMemo(
    () =>
      estimateCashback({
        stakeNIM: stake,
        spendNIM: spend,
        nimUsd: price,
        apyPct: Number(apy) || 0,
        poolFeePct: POOL_FEE_PCT,
        loyaltyDays: loyalty,
        baseBps,
        stakerBaseBps,
        program,
      }),
    [stake, spend, price, apy, loyalty, baseBps, stakerBaseBps, program],
  );

  const rampLabel = ramp >= 365
    ? t('fmt.yearsShort', { n: (ramp / 365).toFixed(ramp % 365 ? 1 : 0) })
    : t('fmt.daysShort', { n: ramp });
  const loyaltyPresets = [{ label: t('cashback.calcPresetNew'), days: 0 }];
  if (ramp > 400) loyaltyPresets.push({ label: t('cashback.calcPresetYear'), days: 365 });
  loyaltyPresets.push({ label: t('cashback.calcPresetFull', { ramp: rampLabel }), days: ramp });
  // Already staked: the button adds the difference, not the whole slider.
  const addNIM = myStakeNIM && myStakeNIM > 0 ? stake - myStakeNIM : stake;
  const canUse = !!onUseAmount && addNIM >= stakeMin;
  const monthlyCapNIM = price > 0 ? program.monthly_cap_usd / price : 0;
  const stakedOff = stake <= 0;

  return (
    <div className="card mt-2 cb-calc">
      <div className="card-title">
        <Icon name="spark" size={16} /> {t('cashback.calcTitle')}
      </div>
      <div className="small muted" style={{ margin: '2px 0 12px' }}>
        {t('cashback.calcIntro')}
      </div>
      <button
        type="button"
        className={'cb-toggle wide' + (stakedOff ? '' : ' on')}
        aria-pressed={!stakedOff}
        title={t('cashback.calcToggleTitle')}
        onClick={() => {
          setStakeText('');
          setStake(stakedOff ? (myStakeNIM && myStakeNIM > 0 ? myStakeNIM : 10_000) : 0);
        }}
      >
        <span className="cb-toggle-knob" />
        {stakedOff ? t('cashback.calcToggleOff') : t('cashback.calcToggleOn')}
      </button>

      {/* ---------------------------------------------------------- inputs */}
      <div className="cb-calc-inputs">
        <label className={'cb-slider' + (stakedOff ? ' off' : '')}>
          <span className="cb-slider-top">
            <span className="xs faint">{t('cashback.calcYourStake')}</span>
            <span className="cb-slider-val">
              <input
                className="cb-slider-input"
                inputMode="numeric"
                aria-label={t('cashback.calcStakeAria')}
                value={stakeText !== '' ? stakeText : fmtStakeNIM(stake)}
                onFocus={() => setStakeText(String(Math.round(stake)))}
                onChange={(e) => {
                  const text = cleanNimText(e.target.value);
                  setStakeText(text);
                  const n = parseNim(text);
                  if (!Number.isNaN(n)) setStake(Math.min(STAKE_MAX, n));
                }}
                onBlur={() => {
                  const n = parseNim(stakeText);
                  if (!Number.isNaN(n)) setStake(clampStake(n, stakeMin));
                  setStakeText('');
                }}
              />
              <NimUnit size={12} />
            </span>
          </span>
          <input
            type="range"
            min={0}
            max={1000}
            step={1}
            value={stakedOff ? 0 : Math.round(valueToSlider(stake, stakeMin, STAKE_MAX) * 1000)}
            onChange={(e) => {
              setStakeText('');
              setStake(sliderToValue(Number(e.target.value) / 1000, stakeMin, STAKE_MAX));
            }}
            aria-label={t('cashback.calcStakeSlider')}
          />
          <span className="cb-slider-foot">
            <span className="xs faint">{fmtStakeNIM(stakeMin)} NIM</span>
            <span className="xs faint">{stakedOff ? t('cashback.calcNoStakeSmall') : ''}</span>
            <span className="xs faint">1B NIM</span>
          </span>
        </label>

        <label className="cb-slider">
          <span className="cb-slider-top">
            <span className="xs faint">{t('cashback.calcOrdersLabel')}</span>
            <span className="cb-slider-val">
              <input
                className="cb-slider-input"
                inputMode="numeric"
                aria-label={t('cashback.calcOrdersAria')}
                value={spendText !== '' ? spendText : fmtStakeNIM(spend)}
                onFocus={() => setSpendText(String(Math.round(spend)))}
                onChange={(e) => {
                  const text = cleanNimText(e.target.value);
                  setSpendText(text);
                  const n = parseNim(text);
                  if (!Number.isNaN(n)) setSpend(Math.min(SPEND_MAX, n));
                }}
                onBlur={() => {
                  const n = parseNim(spendText);
                  setSpend(Number.isNaN(n) ? clampSpend(spend) : clampSpend(n));
                  setSpendText('');
                }}
              />
              <NimUnit size={12} />
            </span>
          </span>
          <input
            type="range"
            min={0}
            max={1000}
            step={1}
            value={Math.round(valueToSlider(spend, SPEND_MIN, SPEND_MAX) * 1000)}
            onChange={(e) => {
              setSpendText('');
              setSpend(sliderToValue(Number(e.target.value) / 1000, SPEND_MIN, SPEND_MAX));
            }}
            aria-label={t('cashback.calcOrdersSlider')}
          />
          <span className="cb-slider-foot">
            <span className="xs faint">{fmtStakeNIM(SPEND_MIN)} NIM</span>
            <span className="xs faint">{r.capped && monthlyCapNIM > 0 ? t('cashback.calcBoostCountsFirst', { nim: fmtStakeNIM(monthlyCapNIM) }) : ''}</span>
            <span className="xs faint">10M NIM</span>
          </span>
        </label>
      </div>

      <div className="cb-calc-picks">
        <div className="cb-seg" role="radiogroup" aria-label={t('cashback.calcLoyaltyGroup')}>
          <span className="xs faint cb-seg-label">{t('cashback.calcStakedFor')}</span>
          {loyaltyPresets.map((p) => (
            <button
              key={p.label}
              type="button"
              role="radio"
              aria-checked={loyalty === p.days}
              className={'cb-seg-btn' + (loyalty === p.days ? ' on' : '')}
              disabled={stakedOff}
              onClick={() => setLoyalty(p.days)}
            >
              {p.label}
            </button>
          ))}
        </div>
      </div>

      {/* --------------------------------------------------------- results */}
      <div className="cb-calc-out mt-2">
        <div className={'cb-calc-main' + (r.totalNIM > 0 ? '' : ' zero')}>
          <div className="xs faint">{t('cashback.calcCashbackOnOrders')}</div>
          <div className="cb-calc-big">
            {nim(r.totalNIM)}
            <small> <NimUnit size={11} /> {t('cashback.calcPerMonth')}</small>
          </div>
          <div className="small muted">
            {r.staked ? t('cashback.calcRateOfOrder', { pct: pctText(r.effectivePct) }) : t('cashback.calcNoStakeNoCb')}
          </div>
        </div>
        <div className="cb-calc-main alt">
          <div className="xs faint">{t('cashback.calcRewardsKeep')}</div>
          <div className="cb-calc-big">
            {nim(r.poolRewardsNIM)}
            <small> <NimUnit size={11} /> {t('cashback.calcPerMonth')}</small>
          </div>
          <div className="small muted">{t('cashback.calcAfterPoolFee', { fee: String(POOL_FEE_PCT) })}</div>
        </div>
      </div>

      <dl className="kv cb-calc-kv mt-2">
        <div>
          <dt>
            {t('cashback.calcBaseRate', { pct: pctLabel(r.baseBpsApplied) })}
            {r.staked && r.baseBpsApplied >= stakerBaseBps && stakerBaseBps > 0
              ? t('cashback.calcBecauseStake')
              : !r.staked && stakerBaseBps > 0
                ? t('cashback.calcStakeAnythingFor', { pct: pctLabel(stakerBaseBps) })
                : ''}
          </dt>
          <dd>{nim(r.baseNIM)} NIM</dd>
        </div>
        <div>
          <dt>
            {t('cashback.calcStakerBoost')}{r.boostRatePct > 0 ? t('cashback.calcBoostApprox', { pct: pctText(r.boostRatePct) }) : ''}
            {r.staked && r.belowMin ? t('cashback.calcFromMin', { nim: fmtStakeNIM(program.min_stake_nim) }) : ''}
          </dt>
          <dd style={{ color: r.boostNIM > 0 ? 'var(--green)' : undefined }}>+{nim(r.boostNIM)} NIM</dd>
        </div>
        <div>
          <dt>{t('cashback.calcLoyaltyMultiplier')}</dt>
          <dd>×{r.loyaltyMultiplier.toFixed(2)}</dd>
        </div>
      </dl>

      {canUse && (
        <button type="button" className="btn btn-sm btn-gold mt-2" onClick={() => onUseAmount!(addNIM)}>
          <Icon name="bolt" size={14} /> {myStakeNIM ? t('cashback.calcAdd') : t('cashback.calcStake')} {fmtStakeNIM(addNIM)} <NimUnit size={10} />
        </button>
      )}

      {/* Collapsible like the other notes on this page. */}
      <details
        className="cb-details mt-2"
        open={advanced}
        onToggle={(e) => setAdvanced((e.target as HTMLDetailsElement).open)}
      >
        <summary className="small strong">
          <Icon name="chevron" size={14} /> {t('cashback.calcAssumptions')}
        </summary>
        <div className="mt-2">
          <div className="cb-form-row">
            <span className="small muted" style={{ flex: '1 1 auto', whiteSpace: 'nowrap' }}>
              {t('cashback.calcApyLabel')}
            </span>
            <input
              className="input"
              type="number"
              inputMode="decimal"
              min={0}
              max={100}
              step="0.5"
              value={apy}
              onChange={(e) => setApy(e.target.value)}
              aria-label={t('cashback.calcApyAria')}
              style={{ flex: '0 0 84px', width: '84px' }}
            />
          </div>
          <ul className="xs faint" style={{ margin: '8px 0 0', paddingLeft: 18 }}>
            <li>
              {t('cashback.calcBaseBullet', {
                pct: pctLabel(stakerBaseBps),
                promo: baseBps > 0 ? t('cashback.calcBasePromo', { pct: pctLabel(baseBps) }) : '',
              })}
            </li>
            <li>
              {t('cashback.calcBoostBullet', {
                share: String(Math.round(program.credit_share * 100)),
                start: program.loyalty_start.toFixed(1),
                ramp: String(ramp),
                cap: String(program.max_boost_percent),
                max: price > 0 ? `${fmtStakeNIM(program.ledger_max_usd / price)} NIM` : `$${program.ledger_max_usd}`,
              })}
            </li>
            <li>
              {t('cashback.calcLedgerBullet', { carry: String(Math.round(program.carry_share * 100)) })}
            </li>
            <li>
              {t('cashback.calcCapsBullet', {
                dayCap: price > 0 ? `${fmtStakeNIM(program.daily_cap_usd / price)} NIM` : `$${program.daily_cap_usd}`,
                monthCap: price > 0 ? `${fmtStakeNIM(monthlyCapNIM)} NIM` : `$${program.monthly_cap_usd}`,
              })}
            </li>
            <li>
              {t('cashback.calcRewardsBullet', {
                conversion: price > 0 ? t('cashback.calcConversion', { price: price.toFixed(5) }) : '',
              })}
            </li>
          </ul>
        </div>
      </details>
      <div className="xs faint mt-1">{t('cashback.calcEstimateOnly')}</div>
    </div>
  );
}
