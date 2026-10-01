/**
 * stakerCashback.ts — the "stake in our pool, earn more cashback" programme,
 * v2 (single ledger).
 *
 * Two public facts are loaded here:
 *   1. the programme itself (public, unauthenticated): ONE parameter set —
 *      the flat base rate every order earns, plus the staker boost ceiling
 *      (up to max_boost_percent on top, funded by the pool fees the staker
 *      actually earned) with a $50/day and $500/month cap;
 *   2. the signed-in buyer's own standing (authenticated): their live stake
 *      and their single ledger — how much cashback money they have accrued,
 *      the boost rate it buys right now, and how much of today's / this
 *      month's cap is left.
 *
 * There are no tiers and no lock ladder in v2: the backend sends empty
 * legacy arrays and the UI renders nothing for them. Everything degrades
 * quietly: no programme configured, pool unreachable, or not signed in all
 * render as "the base rate applies". The backend makes the same decision,
 * so the UI can never promise a rate the server would not pay.
 */
import { getCashbackRate, getPoolStake, refreshPoolStake } from './api';
import { fmtCashbackNIM, estimateCashbackNIM, cachedCashbackBps } from './cashback';
import { t as tr } from '../i18n';

export const LUNA_PER_NIM = 100_000;

/**
 * Fallback for the loyalty ramp when the programme payload omits it. It must
 * match the backend's published default (stakeledger.Defaults.TDays, sent as
 * `loyalty_ramp_days`): guessing a SHORTER ramp would hand the buyer the full
 * ×1.00 multiplier on day one and promise a rate the server would not pay.
 */
export const FALLBACK_RAMP_DAYS = 1825;

/** The single parameter set (public shape of the backend's stake_cashback). */
export type StakeCashbackProgram = {
  /** Boost ceiling in percent, on top of the base rate (10 = up to +10%). */
  max_boost_percent: number;
  /** Minimum active stake (NIM) for the ledger to accrue at all. */
  min_stake_nim: number;
  /** Daily cap on cashback-eligible spend, USD (50). */
  daily_cap_usd: number;
  /** Monthly cap on cashback-eligible spend, USD (500). */
  monthly_cap_usd: number;
  /** Days of loyalty age until the multiplier reaches 1.0 (360). */
  ramp_days: number;
  /** The "next $X of spend" basis the displayed rate is computed over (100). */
  display_basis_usd: number;
  /** Ledger cap in USD (A_MAX, 10) — the most cashback money a book can hold. */
  ledger_max_usd: number;
  /** Share of each realized pool-fee NIM credited to the ledger (k, 0.8). */
  credit_share: number;
  /** Loyalty multiplier for a brand-new stake (g0, 0.5); ramps to 1 over ramp_days. */
  loyalty_start: number;
  /** Share of the unused month accrual carried into the next month (q, 0.1). */
  carry_share: number;
};

/** The buyer's single-ledger card (available money, boost rate, caps). */
export type StakerLedgerCard = {
  has_ledger: boolean;
  boost_bps?: number;
  boost_percent?: number;
  /** Accrued cashback money, USD (the number the page leads with). */
  available_cashback_usd?: number;
  /** Same money in NIM (protocol unit). */
  available_cashback_nim?: number;
  loyalty_days?: number;
  loyalty_max_days?: number;
  loyalty_multiplier?: number;
  daily_remaining_usd?: number;
  monthly_remaining_usd?: number;
  updated_at?: string;
};

/** The programme from /api/cashback/rate. */
export type StakerProgram = {
  enabled: boolean;
  /** Operator's UNIVERSAL base (non-stakers). 0 by default. */
  baseBps: number;
  /** The pool's staker base: ANY positive stake earns this (50 = 0.5%). */
  stakerBaseBps: number;
  /** Validator the in-app stake button delegates to. Empty = no in-app CTA. */
  validator: string;
  program: StakeCashbackProgram | null;
  /**
   * True when GET /api/cashback/rate could not be read at all (backend down,
   * network, CORS). The calculator then runs on the LAST KNOWN programme from
   * the session cache, or — with no cache at all — on the published default
   * programme the Go backend itself ships (stakeledger.Defaults), and the
   * page shows a small honest note. It no longer hides behind an error wall:
   * the maths needs no backend (programme params + static NIM rate), and the
   * money path (quotes/orders) still reads live values server-side.
   * Every
   * figure the calculator prints would then be fiction.
   */
  loadError?: boolean;
  /** Programme read failed; numbers are last-known or published defaults. */
  degraded?: boolean;
};

/** The signed-in buyer's standing from /api/poolstake/me. */
export type MyStake = {
  address: string;
  staked: boolean;
  stake_luna: number;
  stake_nim: number;
  base_bps: number;
  /** 'pool_staker' when the pool's staker base is what applies. */
  base_source?: string;
  boost_bps: number;
  cashback_bps: number;
  cashback_percent: number;
  boosted: boolean;
  pool_validator_address: string;
  staker_program_enabled?: boolean;
  stake_check_error?: string;
  /** ISO date the shop first saw a positive stake. */
  staked_since?: string;
  /** Whole days the stake has stayed positive in the pool. */
  loyalty_days?: number;
  ledger?: StakerLedgerCard;
};

const PROGRAM_KEY = 'nim_staker_program_v3';
const MY_STAKE_KEY = 'nim_my_stake_v2';
const PROGRAM_TTL_MS = 5 * 60 * 1000;
const MY_STAKE_TTL_MS = 60 * 1000;

function readCache<T>(key: string, ttl: number): T | null {
  try {
    const j = JSON.parse(sessionStorage.getItem(key) || '');
    if (j && now() - Number(j.fetched_at) < ttl) return j.value as T;
  } catch {
    /* ignore */
  }
  return null;
}

function writeCache(key: string, value: unknown) {
  try {
    sessionStorage.setItem(key, JSON.stringify({ value, fetched_at: now() }));
  } catch {
    /* private mode — the UI simply re-fetches */
  }
}

function now() {
  return Date.now();
}

function clamp01(v: number, fallback: number): number {
  if (!Number.isFinite(v) || v <= 0 || v > 1) return fallback;
  return v;
}

function normProgram(t: any): StakeCashbackProgram | null {
  if (!t || typeof t !== 'object') return null;
  const maxPct = Number(t.max_boost_percent);
  if (!Number.isFinite(maxPct) || maxPct <= 0) return null;
  return {
    max_boost_percent: maxPct,
    min_stake_nim: Number(t.min_stake_nim) || 0,
    daily_cap_usd: Number(t.daily_cap_usd) || 0,
    monthly_cap_usd: Number(t.monthly_cap_usd) || 0,
    // The Go backend sends loyalty_ramp_days; the dev mock sends t_days.
    ramp_days: Number(t.loyalty_ramp_days ?? t.ramp_days ?? t.t_days) || 0,
    display_basis_usd: Number(t.display_basis_usd) || 100,
    ledger_max_usd: Number(t.ledger_max_usd ?? t.a_max_usd) || 10,
    credit_share: clamp01(Number(t.profit_credit_share ?? t.k), 0.8),
    loyalty_start: clamp01(Number(t.loyalty_start ?? t.g0), 0.5),
    carry_share: clamp01(Number(t.monthly_carry_share ?? t.q), 0.1),
  };
}

function normLedger(l: any): StakerLedgerCard | null {
  if (!l || typeof l !== 'object') return null;
  if (l.has_ledger !== true) return { has_ledger: false };
  const num = (v: unknown) => (Number.isFinite(Number(v)) ? Number(v) : undefined);
  return {
    has_ledger: true,
    boost_bps: num(l.boost_bps),
    boost_percent: num(l.boost_percent),
    available_cashback_usd: num(l.available_cashback_usd),
    available_cashback_nim: num(l.available_cashback_nim),
    loyalty_days: num(l.loyalty_days),
    loyalty_max_days: num(l.loyalty_max_days),
    loyalty_multiplier: num(l.loyalty_multiplier),
    daily_remaining_usd: num(l.daily_remaining_usd),
    monthly_remaining_usd: num(l.monthly_remaining_usd),
    updated_at: l.updated_at ? String(l.updated_at) : undefined,
  };
}

/**
 * The programme numbers the Go backend ships as its own defaults
 * (backend/internal/stakeledger/params.go `Defaults`, MaxBoostBps→percent,
 * admin/models.go DefaultStakerCashbackBps). Mirrored here so the PUBLIC
 * calculator keeps working with zero backend: a live or cached response
 * always wins, this is only the floor. Keep in sync with the Go side.
 */
const BUNDLED_PROGRAM: StakeCashbackProgram = {
  max_boost_percent: 10, // MaxBoostBps 1000 / 100
  min_stake_nim: 100,
  daily_cap_usd: 50,
  monthly_cap_usd: 500,
  ramp_days: 1825,
  display_basis_usd: 100,
  ledger_max_usd: 10,
  credit_share: 0.8,
  loyalty_start: 0.5,
  carry_share: 0.1,
};

/** The programme description from /api/cashback/rate. Never throws. */
export async function loadStakerProgram(): Promise<StakerProgram> {
  const cached = readCache<StakerProgram>(PROGRAM_KEY, PROGRAM_TTL_MS);
  if (cached) return cached;
  try {
    const r: any = await getCashbackRate();
    const program: StakeCashbackProgram | null = normProgram(r && r.stake_cashback);
    const out: StakerProgram = {
      enabled: !!(r && r.staker_program_enabled) && !!program,
      baseBps: Number(r && r.cashback_bps) || 0,
      stakerBaseBps: (() => {
        const bps = Number(r && r.staker_base_bps);
        if (Number.isFinite(bps) && bps > 0) return bps;
        const pct = Number(r && r.staker_base_percent);
        if (Number.isFinite(pct) && pct > 0) return Math.round(pct * 100);
        // Published default: any positive pool stake earns 1%. A missing or
        // zero pool field must not render the calculator as "0% while staked".
        return 100;
      })(),
      validator: String((r && r.pool_validator_address) || '').trim(),
      program,
    };
    writeCache(PROGRAM_KEY, out);
    return out;
  } catch {
    // Backend unreachable. The calculator's maths is fully determined by the
    // programme params + the static NIM rate, so keep it running: last known
    // programme first (any age), then the published default programme. The
    // in-app stake CTA stays hidden in the bundled branch (empty validator),
    // so no transaction can ever target a guessed address.
    const stale = cached || readCache<StakerProgram>(PROGRAM_KEY, Number.POSITIVE_INFINITY);
    const base = stale && stale.program
      ? stale
      : { enabled: false, baseBps: cachedCashbackBps(), stakerBaseBps: 100, validator: '', program: BUNDLED_PROGRAM };
    return { ...base, loadError: true, degraded: true };
  }
}

/** Drops the cached programme so the next load re-asks the backend. */
export function clearStakerProgramCache() {
  try {
    sessionStorage.removeItem(PROGRAM_KEY);
  } catch {
    /* private mode — nothing cached anyway */
  }
}

function parseMyStake(r: any): MyStake | null {
  if (!r || !r.address) return null;
  return {
    address: String(r.address),
    staked: !!r.staked,
    stake_luna: Number(r.stake_luna) || 0,
    stake_nim: Number(r.stake_nim) || 0,
    base_bps: Number(r.base_bps) || 0,
    base_source: r.base_source ? String(r.base_source) : undefined,
    boost_bps: Number(r.boost_bps) || 0,
    cashback_bps: Number(r.cashback_bps) || 0,
    cashback_percent: Number(r.cashback_percent) || 0,
    boosted: !!r.boosted,
    pool_validator_address: String(r.pool_validator_address || '').trim(),
    staker_program_enabled: r.staker_program_enabled ? true : undefined,
    stake_check_error: r.stake_check_error ? String(r.stake_check_error) : undefined,
    staked_since: r.staked_since ? String(r.staked_since) : undefined,
    loyalty_days: Number.isFinite(Number(r.loyalty_days)) ? Number(r.loyalty_days) : undefined,
    ledger: normLedger(r.ledger) ?? undefined,
  };
}

/** The signed-in buyer's standing. Null when signed out or on a hard error. */
export async function loadMyStake(): Promise<MyStake | null> {
  const cached = readCache<MyStake>(MY_STAKE_KEY, MY_STAKE_TTL_MS);
  if (cached) return cached;
  try {
    const r: any = await getPoolStake();
    const mine = parseMyStake(r);
    if (mine) writeCache(MY_STAKE_KEY, mine);
    return mine;
  } catch {
    return cached;
  }
}

/** Drops the cached answer and re-asks — used right after a stake tx. */
export async function refreshMyStake(): Promise<MyStake | null> {
  try {
    sessionStorage.removeItem(MY_STAKE_KEY);
  } catch {
    /* ignore */
  }
  try {
    const r: any = await refreshPoolStake();
    const mine = parseMyStake(r);
    if (mine) writeCache(MY_STAKE_KEY, mine);
    return mine;
  } catch {
    return null;
  }
}

export function fmtStakeNIM(n: number): string {
  if (!(n > 0)) return '0';
  if (n >= 1000) return Math.round(n).toLocaleString('en-US');
  if (n >= 100) return n.toFixed(0);
  if (n >= 10) return n.toFixed(1);
  const s = n.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  return s || '0';
}

/** "$12.40" for the ledger card; "" when the price is unknown. */
export function fmtUSD(n: number | undefined): string {
  if (!Number.isFinite(Number(n)) || !(Number(n) > 0)) return '';
  const v = Number(n);
  return '$' + (v >= 100 ? v.toFixed(0) : v.toFixed(2).replace(/0$/, '').replace(/\.$/, ''));
}

/** "You'll earn ≈ 3 NIM cashback … (6% — base + staker boost)". */
export function stakerEarnLine(productNIM: number, bps: number, boosted: boolean): string {
  const n = estimateCashbackNIM(productNIM, bps);
  if (!n) return '';
  const pct = bps / 100;
  const pctText = Number.isInteger(pct) ? String(pct) : pct.toFixed(2);
  const suffix = boosted ? tr('cashback.suffixBaseBoost') : '';
  return tr('cashback.earnLine', { nim: fmtCashbackNIM(n), pct: pctText, suffix });
}

/** "0.5%" / "0.55%" — bps as a percent label with no float noise. */
export function pctLabel(bps: number): string {
  const p = (Number(bps) || 0) / 100;
  return (Number.isInteger(p) ? String(p) : String(Number(p.toFixed(2)))) + '%';
}
