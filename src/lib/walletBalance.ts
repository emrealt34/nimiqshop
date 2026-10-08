/**
 * walletBalance.ts — "how much NIM do I have right now?"
 *
 * WHY THIS EXISTS (owner, 2026-10-05): the first thing a buyer looks for is the
 * NIM they hold — to know what they can afford before they start a checkout.
 *
 * FIXED 2026-10-05: "your wallet'deki NIM miktarım yanlış". One number was the
 * bug, in two ways:
 *
 *   1. `getAccountByAddress` (and the SDK's `getBalance`, which reads the same
 *      chain state) returns the LIQUID balance. A buyer who stakes — the shop's
 *      own cashback programme asks them to — holds far more than that. The
 *      module now carries `available`, `staked` and `total`, so the strip can
 *      be reconciled with the wallet while the affordability maths still uses
 *      only what a payment can actually spend.
 *   2. Two sources could disagree without anyone noticing (a stale cached
 *      address, a host reporting a different unit, a different account). Both
 *      are now read whenever possible and reconciled: a factor-of-100,000 gap
 *      is a unit mismatch and is corrected, anything else keeps the SESSION
 *      wallet's figure — the one the shop will actually charge — and is
 *      flagged so the UI can say so.
 *
 * SOURCES
 *   1. Nimiq Pay host bridge: `getBalance(address)` (Mini App SDK 0.2.1+, see
 *      developer-center PR 216), with the documented generic
 *      `request({ method: 'getBalance', params: { address } })` form for hosts
 *      that do not expose the method directly. No RPC endpoint is involved and
 *      it follows the host's active network.
 *   2. `GET /api/wallet/balance` — session-scoped, stake-aware, and the only
 *      source that can answer when the shop is opened in a plain browser.
 *
 * ERRORS (PR 215): every shape the wallet throws is normalized by
 * `walletErrors.ts` into one small vocabulary, and a failed lookup resolves to
 * a STATE, never a throw — a cancelled dialog, a 30-second bridge timeout or
 * "not signed in" all end in a quiet row that keeps the page usable. The USD
 * figure is refreshed as soon as the live rate arrives, so it is never a stale
 * conversion of a fresh number.
 */
import { getWalletBalance, getNimRate, cachedNimRate } from './api';
import { initNimiqMiniApp, getNimiqProvider, inNimiqPay, nimiqPayBalanceSupport } from './miniapp';
import { getAddress, isAuthed, subscribeSession } from './session';
import { classifyWalletError, readWalletError, type WalletErrorClass } from './walletErrors';

/** 1 NIM = 100,000 luna — the constant the SDK documents for getBalance. */
export const LUNA_PER_NIM = 100000;

/**
 * SPEND_MARGIN — the percentage cushion every payment keeps on top of its price.
 *
 * WHY: a Lightning payment in Nimiq Pay is not a plain NIM transfer. Pay swaps
 * NIM into BTC, and that swap carries its own fee plus a Bitcoin network fee.
 * Pay does not publish those fees, and the rate moves between the quote and the
 * signature. A payment that is short by even one luna is REFUSED, so the cushion
 * has to cover that cost. Owner (2026-10-08) set it back to 1%.
 *
 * The DISPLAYED balance is never adjusted by it: the buyer sees what they hold.
 * Only verdicts ("you can afford this", "you are short") use the cushion.
 */
export const SPEND_MARGIN = 0.01;

/**
 * SPEND_FLOOR_NIM — a fixed minimum cushion, in NIM, added on top of the
 * percentage. It is 0 for now: the Bitcoin network fee is a fixed cost per
 * payment, and Pay does not publish its size. Set this to the amount observed in
 * real refused payments once it is known.
 *
 * It is deliberately NOT a USD amount. NIM trades at a few hundredths of a cent,
 * so "$1" is thousands of NIM and would wrongly block ordinary purchases.
 */
export const SPEND_FLOOR_NIM = 0;

/** The cushion in NIM: the larger of the percentage and the fixed floor. */
function cushionNim(targetNim: number): number {
  return Math.max(targetNim * SPEND_MARGIN, SPEND_FLOOR_NIM);
}

/**
 * requiredNim — the NIM a payment of `targetNim` needs in the wallet: the price
 * plus the cushion. Every affordability decision in the app goes through this
 * one function, so the strip, the card, the sheet and the pay button can never
 * disagree about how much is needed.
 */
export function requiredNim(targetNim: number): number {
  const target = Number(targetNim || 0);
  if (!(target > 0)) return 0;
  return target + cushionNim(target);
}

/** True when the spendable balance covers `targetNim` plus the cushion. */
export function coversTarget(targetNim: number, availableNim: number): boolean {
  const target = Number(targetNim || 0);
  if (!(target > 0)) return true;
  return Number(availableNim || 0) >= requiredNim(target);
}

/**
 * The whole NIM figures the warning sentences use — the buyer sees the same
 * integers on the strip, in the card and in the toast.
 *   need   → UP   (never understate what the payment will ask for)
 *   have   → DOWN (never claim more than the wallet really holds)
 */
export function neededWholeNim(targetNim: number): number {
  const need = requiredNim(targetNim);
  return need > 0 ? Math.ceil(need) : 0;
}

export function shortByWholeNim(targetNim: number, availableNim: number): number {
  const need = requiredNim(targetNim);
  const have = Number(availableNim || 0);
  if (!(need > 0) || have >= need) return 0;
  return Math.max(1, Math.ceil(need - have));
}

/**
 * How many `unitNim`-priced units the balance can buy, cushion included.
 *
 * Solved directly from requiredNim so it can never disagree with coversTarget.
 * For n units the requirement is max(n·u·(1+m), n·u + F), which fits the balance
 * only when BOTH n·u·(1+m) ≤ have AND n·u + F ≤ have.
 */
export function affordableUnits(availableNim: number, unitNim: number): number {
  const unit = Number(unitNim || 0);
  const have = Number(availableNim || 0);
  if (!(unit > 0) || !(have > 0)) return 0;
  const byPercent = Math.floor(have / (unit * (1 + SPEND_MARGIN)));
  const byFloor = Math.floor((have - SPEND_FLOOR_NIM) / unit);
  return Math.max(0, Math.min(byPercent, byFloor));
}

/** How long a reading stays fresh. Short on purpose: it sits next to prices. */
const TTL_MS = 25000;

/** A unit mix-up looks like this and only this: five orders of magnitude. */
const UNIT_FACTOR = LUNA_PER_NIM;
const looksLikeUnitMixUp = (a: number, b: number) => {
  if (!(a > 0) || !(b > 0)) return false;
  const r = a / b;
  return r > UNIT_FACTOR / 10 && r < UNIT_FACTOR * 10;
};

export type WalletBalanceFailure = WalletErrorClass | 'signed-out' | 'no-wallet';

export interface WalletBalanceState {
  status: 'loading' | 'ready' | 'error' | 'unavailable';
  /** Spendable NIM — the address balance. This is what a payment can use. */
  availableNim: number;
  /** Active stake delegated to a validator. Not spendable. */
  stakedNim: number;
  /** Stake that is no longer active. Not spendable by itself. */
  inactiveNim: number;
  /** Retired stake tracked by the wallet, not directly spendable. */
  retiredNim: number;
  /** What a wallet shows: available + active, inactive, and retired stake. */
  totalNim: number;
  /** Alias for `availableNim`, kept so affordability maths reads naturally. */
  nim: number;
  /** Balance in luna exactly as the chain reports it. */
  luna: number;
  /** USD equivalent of the spendable balance, when a rate is known. */
  usd: number;
  source: 'nimiq-pay' | 'shop' | 'both' | 'none';
  /** The address the figures belong to ('' when unknown). */
  address: string;
  /** Which chain the reading came from, as reported by the shop endpoint. */
  network: string;
  /**
   * What the Nimiq Pay host can do (PR 216). 'update-required' is its own
   * state because it is the one case only the BUYER can fix — the SDK cannot
   * add a method to an old wallet, so the strip says so instead of quietly
   * falling back to the shop's figure.
   */
  hostBalance?: 'ready' | 'via-request' | 'update-required' | 'no-provider';
  /** The host and the chain disagreed beyond a unit fix: the chain value won. */
  mismatch?: boolean;
  /**
   * What each source said, in NIM, when BOTH answered. Owner (2026-10-06):
   * "kesinlikle hata var, spendable NIM yazıyor Nimiq Pay'de ama…" — the buyer
   * compares our figure with the number their wallet shows, so the card now
   * carries the comparison instead of a verdict, and the total below is
   * reconciled from both, which is what makes the two agree.
   */
  hostNim?: number;
  shopNim?: number;
  /** A unit mismatch was detected and corrected (host reported NIM, not luna). */
  unitCorrected?: boolean;
  /** True while a previous reading is on screen during a refresh. */
  stale?: boolean;
  at?: number;
  failure?: WalletBalanceFailure;
  /** On-screen diagnostics for Nimiq Pay balance troubleshooting. */
  debugLines?: string[];
}

const EMPTY: WalletBalanceState = {
  status: 'unavailable',
  availableNim: 0,
  stakedNim: 0,
  inactiveNim: 0,
  retiredNim: 0,
  totalNim: 0,
  nim: 0,
  luna: 0,
  usd: 0,
  source: 'none',
  address: '',
  network: '',
};

let current: WalletBalanceState = EMPTY;
let inflight: Promise<WalletBalanceState> | null = null;
const listeners = new Set<(s: WalletBalanceState) => void>();

function emit(next: WalletBalanceState): WalletBalanceState {
  current = next;
  listeners.forEach((fn) => {
    try {
      fn(next);
    } catch {
      /* one bad subscriber must never break the others */
    }
  });
  return next;
}

/* ---------------- the reading itself ---------------- */

interface ShopReading {
  luna: number;
  address: string;
  stakedNim: number;
  inactiveNim: number;
  retiredNim: number;
  totalNim: number;
  network: string;
}

function isNimiqAddress(value: string): boolean {
  const a = String(value || '').replace(/\s+/g, '').toUpperCase();
  return /^NQ\d{2}[0-9A-HJ-NP-VXY]{32}$/.test(a);
}

/** A result may be a raw number or a wrapped payload; unwrap safely. */
function unwrapLuna(raw: unknown): number | null {
  if (typeof raw === 'number' && Number.isFinite(raw)) return raw;
  if (raw && typeof raw === 'object') {
    const o = raw as Record<string, any>;
    for (const k of ['balance_luna', 'balance', 'result']) {
      const v = o[k];
      if (typeof v === 'number' && Number.isFinite(v)) return v;
      if (typeof v === 'string' && v && Number.isFinite(Number(v))) return Number(v);
    }
  }
  return null;
}

const num = (v: unknown) => (Number.isFinite(Number(v)) ? Number(v) : 0);

/** Our backend: session-scoped, stake-aware, no address parameter. */
async function shopReading(fresh = false): Promise<ShopReading> {
  // `fresh` also bypasses the server's 20-second cache, so the manual refresh
  // on the card reads the chain rather than a value this tab may have caused.
  const res = await getWalletBalance({ fresh });
  if (res && res.available === false) {
    const err = new Error(String(res.reason || 'unavailable'));
    (err as any).type = res.reason === 'no_wallet' ? 'NO_WALLET' : 'UNKNOWN_REQUEST';
    throw err;
  }
  const luna = unwrapLuna(res);
  if (luna === null) {
    const err = new Error('unreadable balance');
    (err as any).type = 'UNKNOWN_ERROR';
    throw err;
  }
  const stakedNim = num(res.staked_nim);
  const inactiveNim = num(res.inactive_nim);
  const retiredNim = num(res.retired_nim);
  const availableNim = luna / LUNA_PER_NIM;
  return {
    luna,
    address: String(res.address || ''),
    stakedNim,
    inactiveNim,
    retiredNim,
    totalNim: availableNim + stakedNim + inactiveNim + retiredNim,
    network: String(res.network || ''),
  };
}

/** Ask the host for the balance of `address`, in luna. */
/**
 * A hard cap on the bridge call, longer than the host's own documented
 * 30-second timeout on purpose: the wallet's NETWORK_ERROR (-32000) is the
 * answer we want to render, and this only fires when the bridge itself is
 * wedged (a backgrounded WebView, a killed app) and nothing will ever come
 * back. Without it the strip could spin forever.
 */
const BRIDGE_TIMEOUT_MS = 32000;

function withTimeout<T>(work: Promise<T>, ms: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      const err = new Error('the wallet did not answer the balance lookup in time');
      (err as any).type = 'NETWORK_ERROR';
      (err as any).code = -32000;
      reject(err);
    }, ms);
    work.then(
      (v) => { clearTimeout(timer); resolve(v); },
      (e) => { clearTimeout(timer); reject(e); }
    );
  });
}

async function bridgeLuna(address: string): Promise<number> {
  // PR 216's availability rule, applied before anything is called: the method
  // is a property of the HOST version, not of the SDK the shop ships.
  const support = await nimiqPayBalanceSupport();
  if (support === 'no-provider' || support === 'update-required') {
    const err = new Error(support === 'update-required' ? 'update Nimiq Pay to read balances' : 'no provider');
    (err as any).type = 'UNKNOWN_REQUEST';
    throw err;
  }
  const provider: any = (await initNimiqMiniApp()) || getNimiqProvider();
  if (!provider) {
    const err = new Error('no provider');
    (err as any).type = 'UNKNOWN_REQUEST';
    throw err;
  }
  // A missing, non-string or malformed address is rejected by the host; check
  // here too so we never send a request the host will refuse anyway.
  if (!isNimiqAddress(address)) {
    const err = new Error('invalid address');
    (err as any).type = 'INVALID_REQUEST';
    throw err;
  }
  const call = async (fn: () => Promise<unknown>) => {
    const luna = unwrapLuna(await withTimeout(Promise.resolve(fn()), BRIDGE_TIMEOUT_MS));
    if (luna === null) {
      const err = new Error('unreadable balance');
      (err as any).type = 'UNKNOWN_ERROR';
      throw err;
    }
    return luna;
  };
  if (typeof provider.getBalance === 'function') {
    // The documented direct method (SDK 0.2.1+ on a host that exposes it).
    return call(() => provider.getBalance(address));
  }
  if (typeof provider.request === 'function') {
    // Documented generic form: same lookup, same network, no external RPC.
    return call(() => provider.request({ method: 'getBalance', params: { address } }));
  }
  const err = new Error('getBalance not supported by this host');
  (err as any).type = 'UNKNOWN_REQUEST';
  throw err;
}

/** Attach the live USD equivalent from the cached rate. */
function withUsd(state: WalletBalanceState): WalletBalanceState {
  const rate = num(cachedNimRate()?.usd_per_nim);
  return { ...state, usd: rate > 0 ? state.availableNim * rate : 0 };
}

/** Fire-and-forget rate fetch, then REPAINT with the USD figure it produced. */
function refreshUsdLater(mark: WalletBalanceState): void {
  const seenAt = mark.at;
  void getNimRate()
    .then(() => {
      // Only update if the reading has not been replaced meanwhile.
      if (current.at === seenAt && current.status === 'ready') emit(withUsd(current));
    })
    .catch(() => {});
}

async function readBalance(force = false): Promise<WalletBalanceState> {
  let address = String(getAddress() || '').trim();
  /* The SHOP read is session-scoped and needs no address: it is the only
     source left when the cached one is missing (a WebView with storage
     disabled, a hard reload mid-session). Requiring an address here used to
     blank the whole strip — "balance reading is very wrong" included reading
     NOTHING while the server could have answered. The host bridge still needs
     one, so it is gated separately below. */
  const signedIn = isAuthed();
  const insidePay = inNimiqPay();
  const hostAvailable = !!(insidePay || getNimiqProvider());
  const debugLines: string[] = [
    `inPay=${insidePay} signedIn=${signedIn}`,
    `session=${String(address || '(none)')}`,
  ];

  // In Nimiq Pay the active account is authoritative. It may itself be an
  // HTLC account, while the shop session still contains the ordinary wallet
  // address used during login. Querying only the session address makes an
  // active HTLC appear as 0 NIM and incorrectly opens the low-balance sheet.
  // listAccounts() is already the account-selection primitive used by Pay
  // login; use its active address for the host-side balance lookup only.
  let hostAddresses = address ? [address] : [];
  if (hostAvailable) {
    try {
      const provider: any = (await initNimiqMiniApp()) || getNimiqProvider();
      if (provider && typeof provider.listAccounts === 'function') {
        const accounts = await provider.listAccounts();
        const listed = Array.isArray(accounts)
          ? accounts.map((value: unknown) => String(value || '').trim()).filter(isNimiqAddress)
          : [];
        // Some Pay hosts expose more than one selected account. Include every
        // account the host explicitly returned: an HTLC account may be present
        // beside the ordinary account, and looking only at accounts[0] leaves
        // its balance out of the affordability check.
        hostAddresses = Array.from(new Set(listed.length ? listed : hostAddresses));
        debugLines.push(`providerAccounts=${hostAddresses.length ? hostAddresses.join(',') : '(none)'}`);
      }
    } catch (error) {
      debugLines.push(`listAccountsError=${error instanceof Error ? error.message : String(error)}`);
      // Account listing can be unavailable on older hosts; fall back to the
      // signed-in address and let the normal balance-support path report it.
    }
  }

  if (!hostAddresses.length) debugLines.push('providerAccounts=(none)');
  let shop: ShopReading | null = null;
  let shopError: unknown = null;
  if (signedIn) {
    try {
      shop = await shopReading(force);
      debugLines.push(`shop=${shop.luna} luna; stake=${shop.stakedNim} NIM; addr=${shop.address || '(none)'}`);
    } catch (e) {
      shopError = e;
    }
  }

  let host: number | null = null;
  let hostError: unknown = null;
  let hostSupport: WalletBalanceState['hostBalance'] | undefined;
  if (hostAvailable) {
    try {
      hostSupport = await nimiqPayBalanceSupport();
    } catch {
      hostSupport = undefined;
    }
  }
  if (hostAddresses.length && hostAvailable) {
    try {
      // One account failing (an HTLC/contract address the host cannot read,
      // a single timeout) must NOT wipe out the balance of the others. Sum the
      // accounts that answered and record the ones that did not.
      const settled = await Promise.allSettled(hostAddresses.map((account) => bridgeLuna(account)));
      const readings: number[] = [];
      let firstError: unknown = null;
      settled.forEach((r, i) => {
        if (r.status === 'fulfilled') {
          readings.push(r.value);
          debugLines.push(`acct${i}=${r.value} luna`);
        } else {
          if (firstError === null) firstError = r.reason;
          debugLines.push(`acct${i}Error=${r.reason instanceof Error ? r.reason.message : String(r.reason)}`);
        }
      });
      if (!readings.length) throw firstError;
      // Keep the host's account balances together. This is still display-only;
      // the payment provider remains the authority on whether an HTLC can be
      // spent under its conditions.
      host = readings.reduce((sum, luna) => sum + luna, 0);
      debugLines.push(`host=${host} luna across ${readings.length}/${hostAddresses.length} account(s)`);
    } catch (e) {
      hostError = e;
      // A host may expose generic request() without implementing getBalance.
      // Treat its explicit unsupported-method response as an update requirement,
      // not as an ordinary transient balance failure.
      if (classifyWalletError(e) === 'unsupported') hostSupport = 'update-required';
      debugLines.push(`hostError=${e instanceof Error ? e.message : String(e)}`);
    }
  }

  if (host !== null && hostAddresses.length) address = hostAddresses[0];
  if (!address && shop?.address) address = shop.address;
  debugLines.push(`finalAddress=${address || '(none)'}`);
  const base = { address, network: shop?.network || '', hostBalance: hostSupport, debugLines };

  // BOTH: reconcile. The session wallet is the one the shop charges, so it wins
  // any disagreement — but a clean five-orders-of-magnitude gap is a unit
  // mistake, not a disagreement, and is corrected silently.
  if (shop && host !== null) {
    /* NORMALISE THE UNIT FIRST. A host that reports NIM where the docs promise
       luna is off by 100,000 — the one disagreement that can be corrected from
       the data alone. */
    const unitCorrected = looksLikeUnitMixUp(host, shop.luna);
    const hostLuna = unitCorrected ? Math.round(host * UNIT_FACTOR) : host;

    /* WHICH FIGURE IS SPENDABLE. A Nimiq Pay payment (payLightningInvoice) is
       signed and funded by the Pay host from its own active account(s), not
       from the shop's session address. So the host's reading is the spendable
       figure. The shop's reading of the session address can legitimately be 0
       while the host shows the funds (e.g. NIM sitting in an HTLC/contract
       account the session address does not point at); that must not hide the
       balance or open the low-balance sheet.

       What is NOT spendable is stake. Stake comes only from what the shop's
       backend reports as staked/inactive/retired. It is never inferred from the
       gap between two reads: that gap is what produced a "staked" label for
       funds that were not staked. */
    const spendLuna = hostLuna;
    const reportedStakeNim = shop.stakedNim + shop.inactiveNim + shop.retiredNim;
    const availableNim = spendLuna / LUNA_PER_NIM;
    /* The total is spendable plus the stake the backend reports, each once. */
    const totalNim = availableNim + reportedStakeNim;
    const differs = Math.abs(hostLuna - shop.luna) > Math.max(1, Math.min(hostLuna, shop.luna) * 0.01);
    const state: WalletBalanceState = {
      status: 'ready',
      luna: spendLuna,
      availableNim,
      stakedNim: shop.stakedNim,
      inactiveNim: shop.inactiveNim,
      retiredNim: shop.retiredNim,
      totalNim,
      nim: availableNim,
      usd: 0,
      source: 'both',
      unitCorrected,
      mismatch: differs,
      hostNim: hostLuna / LUNA_PER_NIM,
      shopNim: shop.luna / LUNA_PER_NIM,
      at: Date.now(),
      ...base,
    };
    refreshUsdLater(state);
    return withUsd(state);
  }

  // HOST ONLY: not signed in to the shop, but the wallet can still answer. The
  // host reports one figure (the address balance); staking is not visible here.
  if (host !== null) {
    const state: WalletBalanceState = {
      status: 'ready',
      luna: host,
      availableNim: host / LUNA_PER_NIM,
      stakedNim: 0,
      inactiveNim: 0,
      retiredNim: 0,
      totalNim: host / LUNA_PER_NIM,
      nim: host / LUNA_PER_NIM,
      usd: 0,
      source: 'nimiq-pay',
      at: Date.now(),
      ...base,
    };
    refreshUsdLater(state);
    return withUsd(state);
  }

  // SHOP ONLY — the plain-browser case, and the one that carries the stake.
  if (shop) {
    const state: WalletBalanceState = {
      status: 'ready',
      luna: shop.luna,
      availableNim: shop.luna / LUNA_PER_NIM,
      stakedNim: shop.stakedNim,
      inactiveNim: shop.inactiveNim,
      retiredNim: shop.retiredNim,
      totalNim: shop.totalNim,
      nim: shop.luna / LUNA_PER_NIM,
      usd: 0,
      source: 'shop',
      at: Date.now(),
      ...base,
    };
    refreshUsdLater(state);
    return withUsd(state);
  }

  // Nothing answered. Report WHY, precisely.
  const detail = readWalletError(hostError || shopError);
  if (detail.type === 'NO_WALLET') {
    return { ...EMPTY, ...base, failure: 'no-wallet' };
  }
  if (!signedIn && (!hostError || classifyWalletError(hostError) === 'unsupported')) {
    return { ...EMPTY, ...base, failure: 'signed-out' };
  }
  const status = Number((shopError as any)?.status) || 0;
  if (status === 401) return { ...EMPTY, ...base, failure: 'signed-out' };
  const failure: WalletBalanceFailure = hostError
    ? (classifyWalletError(hostError) as WalletBalanceFailure)
    : signedIn
      ? 'network'
      : 'signed-out';
  return { ...EMPTY, ...base, status: 'error', failure };
}

/**
 * The current reading. `force` skips the TTL cache (post-payment refresh, the
 * retry/refresh button). Concurrent callers share one request, so the home
 * page, the cart and a product page mounting together cost one lookup.
 */
export function refreshWalletBalance({ force = false }: { force?: boolean } = {}): Promise<WalletBalanceState> {
  const fresh = current.status === 'ready' && current.at && Date.now() - current.at < TTL_MS;
  if (!force && fresh) return Promise.resolve(current);
  if (inflight) return inflight;

  if (current.status === 'ready') emit({ ...current, stale: true });
  else emit({ ...current, status: 'loading' });

  inflight = readBalance(force)
    .then((next) => emit(next))
    .catch(() => emit({ ...EMPTY, status: 'error', failure: 'unknown' }))
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

export function getWalletBalanceState(): WalletBalanceState {
  return current;
}

export function subscribeWalletBalance(fn: (s: WalletBalanceState) => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Forget the reading. Used by tests and by a full sign-out. */
export function resetWalletBalance(): void {
  inflight = null;
  emit(EMPTY);
}

/* Auto-refresh wiring: a sign-in, a sign-out or coming back to the tab must not
 * leave yesterday's number on screen. Lazily installed — importing this module
 * is enough, and nothing runs on the server (no window). */
if (typeof window !== 'undefined') {
  subscribeSession(() => {
    void refreshWalletBalance({ force: true });
  });
  // Coming back to the tab, or back to the app (Nimiq Pay switches away for
  // the wallet dialog), must never show the number from before. Both events
  // are used because a mobile WebView can fire only one of them.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && current.status !== 'loading') {
      void refreshWalletBalance();
    }
  });
  window.addEventListener('focus', () => {
    if (current.status !== 'loading') void refreshWalletBalance();
  });
  // A long-lived page (the home shelf kept open, the checkout left waiting for
  // a bank transfer) drifts otherwise. One cheap read a minute, visible tabs
  // only — the server's own 20 s cache absorbs the rest. Owner (2026-10-06):
  // "balance okuma baya yanlış yapıyor".
  window.setInterval(() => {
    if (document.visibilityState === 'visible' && current.status !== 'loading') {
      void refreshWalletBalance();
    }
  }, 60000);
}
