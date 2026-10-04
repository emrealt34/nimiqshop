/**
 * cashbackCalc.ts — the "what would I earn?" estimator behind the calculator
 * on the Cashback & staking page.
 *
 * It replays the backend's single-ledger money model (internal/stakeledger)
 * month by month, so the number it shows is the number the shop would
 * actually pay under the same assumptions:
 *
 *   pool fee your stake generates  →  k · g(d) of it lands in your ledger A
 *   boost rate = min(max_boost, A · price / display_basis)
 *   every purchase pays that rate on its eligible amount and debits A
 *   month end: unused accrual is partly carried over (q), partly hair-cut
 *
 * Only ONE input is a guess: the network staking reward rate (APY). Price,
 * caps, shares and the loyalty ramp are the live programme parameters.
 */
import { FALLBACK_RAMP_DAYS, type StakeCashbackProgram } from './stakerCashback';

export type CalcInput = {
  /** Stake delegated to our pool, NIM. */
  stakeNIM: number;
  /** Monthly spend in the shop, NIM (the shop is priced in NIM). */
  spendNIM: number;
  /** Live NIM price, USD. */
  nimUsd: number;
  /** Assumed network staking reward rate, percent per year (e.g. 15). */
  apyPct: number;
  /** Pool fee for the chosen lane, percent (5 / 4 / 3). */
  poolFeePct: number;
  /** How long the stake has been with the pool, days (drives g(d)). */
  loyaltyDays: number;
  /** Operator's universal base (non-stakers), basis points. 0 by default. */
  baseBps: number;
  /** The pool's staker base, basis points — earned by ANY positive stake. */
  stakerBaseBps: number;
  program: StakeCashbackProgram;
};

export type CalcResult = {
  /** The base rate that applies (staker base when staked, else operator base), bps. */
  baseBpsApplied: number;
  /** Monthly spend, USD equivalent (for the caps). */
  spendUSD: number;
  /** Net staking rewards you keep, NIM per month (after the pool fee). */
  poolRewardsNIM: number;
  /** Pool fee your stake generates, NIM per month. */
  poolFeeNIM: number;
  /** Cashback money credited to your ledger, USD per month. */
  creditUSD: number;
  /** Loyalty multiplier g(d) used. */
  loyaltyMultiplier: number;
  /** Base cashback on the full spend, USD per month. */
  baseUSD: number;
  /** Same in NIM. */
  baseNIM: number;
  /** Staker boost paid in a typical (steady-state) month, USD. */
  boostUSD: number;
  /** Same in NIM. */
  boostNIM: number;
  /** Boost rate you would see on the page in a typical month, percent. */
  boostRatePct: number;
  /** Base + boost, USD per month. */
  totalUSD: number;
  /** Same in NIM. */
  totalNIM: number;
  /** Total cashback as a percentage of the spend. */
  effectivePct: number;
  /** Part of the monthly spend that counts for the boost (after the caps). */
  eligibleUSD: number;
  /** True when the caps trimmed the eligible spend. */
  capped: boolean;
  /** True when the stake is below the programme minimum (no ledger boost). */
  belowMin: boolean;
  /** True when the buyer is staked at all (any amount) → staker base applies. */
  staked: boolean;
};

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
const fin = (v: number, d = 0) => (Number.isFinite(v) ? v : d);

/** g(d) = g0 + (1 − g0) · min(1, d / T) — same as the backend's G(). */
export function loyaltyMultiplier(days: number, p: StakeCashbackProgram): number {
  const g0 = clamp(fin(p.loyalty_start, 0.5), 0, 1);
  // `|| FALLBACK` and not `fin(…, 360)`: the programme sends 0 when the field
  // is missing, 0 IS finite, so the old fallback never fired and T collapsed to
  // 1 day — every buyer looked fully matured (×1.00) on the day they staked.
  const T = Math.max(1, fin(p.ramp_days) || FALLBACK_RAMP_DAYS);
  return clamp(g0 + (1 - g0) * Math.min(1, Math.max(0, fin(days)) / T), 0, 1);
}

export function estimateCashback(i: CalcInput): CalcResult {
  const p = i.program;
  const stake = Math.max(0, fin(i.stakeNIM));
  const price = Math.max(0, fin(i.nimUsd));
  const spendNIM = Math.max(0, fin(i.spendNIM));
  const spend = spendNIM * price; // USD, for the caps
  const staked = stake > 0;
  // THE base rule: staked (any amount) → the pool's staker base; otherwise
  // the operator's universal base. The higher wins (a promo never pays a
  // staker less than a non-staker).
  const opBase = Math.max(0, fin(i.baseBps));
  const baseBpsApplied = staked ? Math.max(opBase, Math.max(0, fin(i.stakerBaseBps))) : opBase;
  const apy = Math.max(0, fin(i.apyPct)) / 100;
  const fee = clamp(fin(i.poolFeePct), 0, 100) / 100;
  const k = clamp(fin(p.credit_share, 0.8), 0, 1);
  // Month end carries exactly 10% of the unused ledger balance forward.
  const q = clamp(fin(p.carry_share, 0.1), 0, 1);
  const g = loyaltyMultiplier(i.loyaltyDays, p);
  const maxFrac = Math.max(0, fin(p.max_boost_percent)) / 100;
  const basis = Math.max(1, fin(p.display_basis_usd, 100));
  const dayCap = Math.max(0, fin(p.daily_cap_usd));
  const monthCap = Math.max(0, fin(p.monthly_cap_usd));
  const ledgerCapUSD = Math.max(0, fin(p.ledger_max_usd, 10));
  const belowMin = stake < Math.max(0, fin(p.min_stake_nim));

  const grossNIM = (stake * apy) / 12;
  const poolFeeNIM = grossNIM * fee;
  const poolRewardsNIM = grossNIM - poolFeeNIM;
  const creditNIM = belowMin ? 0 : k * g * poolFeeNIM;
  const creditUSD = creditNIM * price;

  const baseUSD = spend * (baseBpsApplied / 10_000);
  const baseNIM = spendNIM * (baseBpsApplied / 10_000);
  // Use a representative 30-day month. A zero/tiny day cap must not produce
  // millions of simulated purchases and freeze the calculator.
  const eligibleUSD = Math.min(spend, monthCap, dayCap * 30);
  const capped = eligibleUSD < spend - 1e-9;

  let boostUSD = 0;
  let boostRatePct = 0;
  if (creditNIM > 0 && price > 0 && eligibleUSD > 0 && maxFrac > 0) {
    // Spread the month's spend over a few purchases, none above the day cap.
    const n = Math.max(4, Math.min(30, Math.ceil(eligibleUSD / dayCap)));
    const x = eligibleUSD / n;
    const capNIM = ledgerCapUSD / price;
    let A = 0;
    let paidMonth = 0;
    let rateSum = 0;
    for (let m = 0; m < 12; m++) {
      paidMonth = 0;
      rateSum = 0;
      for (let j = 0; j < n; j++) {
        A = Math.min(capNIM, A + creditNIM / n);
        const rate = Math.min(maxFrac, (A * price) / basis);
        rateSum += rate;
        const paid = Math.min(A, (rate * x) / price);
        A -= paid;
        paidMonth += paid;
      }
      // Month end — the backend's rule verbatim (stakeledger.monthEnd):
      // A *= q. Purchases have already debited A, so what is left IS the
      // unused balance, and the carried share is FIXED. The old
      // `q + (1-q)*paid/credit` term let spend history raise the carry, which
      // inverted the curve: buying LESS hair-cut the ledger harder and LOWERED
      // the boost rate, while buying more preserved it.
      A *= q;
    }
    boostUSD = paidMonth * price;
    boostRatePct = (rateSum / n) * 100;
  }

  const totalUSD = baseUSD + boostUSD;
  const boostNIM = price > 0 ? boostUSD / price : 0;
  return {
    baseBpsApplied,
    spendUSD: spend,
    staked,
    poolRewardsNIM,
    poolFeeNIM,
    creditUSD,
    loyaltyMultiplier: g,
    baseUSD,
    baseNIM,
    boostUSD,
    boostNIM,
    boostRatePct,
    totalUSD,
    totalNIM: baseNIM + boostNIM,
    effectivePct: spend > 0 ? (totalUSD / spend) * 100 : 0,
    eligibleUSD,
    capped,
    belowMin,
  };
}

/* ------------------------------------------------------------- sliders */

/** Log-scale slider ↔ value helpers so 100 NIM and 10,000,000 NIM can share
 *  one track — but PIECEWISE, with the arithmetic middle of the range pinned
 *  to the middle of the track. A straight log scale put the GEOMETRIC mean at
 *  the centre (10M on the 100K–1B rail, 100K on the 1K–10M rail), so dragging
 *  to the middle never showed the middle of the labelled range (owner:
 *  "orta değerler hiç ortayı göstermiyor"). Each half is its own log segment
 *  (min→mid, mid→max): the centre is the centre, and the small end keeps the
 *  resolution a linear track would eat. */
const logLerp = (a: number, b: number, t: number) =>
  Math.exp(Math.log(a) + (Math.log(b) - Math.log(a)) * clamp(fin(t), 0, 1));
const logPos = (a: number, b: number, v: number) =>
  (Math.log(clamp(v, a, b)) - Math.log(a)) / (Math.log(b) - Math.log(a));

export function sliderToValue(t: number, min: number, max: number): number {
  const x = clamp(fin(t), 0, 1);
  const mid = (min + max) / 2;
  const v = x <= 0.5 ? logLerp(min, mid, x * 2) : logLerp(mid, max, (x - 0.5) * 2);
  return roundNice(v);
}
export function valueToSlider(v: number, min: number, max: number): number {
  const mid = (min + max) / 2;
  const x = clamp(fin(v, min), min, max);
  return x <= mid ? 0.5 * logPos(min, mid, x) : 0.5 + 0.5 * logPos(mid, max, x);
}
/** 1-2-5 style rounding so the slider lands on numbers people would type. */
export function roundNice(v: number): number {
  if (!(v > 0)) return 0;
  const mag = Math.pow(10, Math.floor(Math.log10(v)));
  const m = v / mag;
  const step = m < 2 ? 0.1 : m < 5 ? 0.25 : 0.5;
  const out = Math.round(m / step) * step * mag;
  // Kill float noise (1200.0000000000002) so the number is typeable as-is.
  return Number(out.toPrecision(12));
}
