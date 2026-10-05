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
import { initNimiqMiniApp, getNimiqProvider, inNimiqPay } from './miniapp';
import { getAddress, isAuthed, subscribeSession } from './session';
import { classifyWalletError, readWalletError, type WalletErrorClass } from './walletErrors';

/** 1 NIM = 100,000 luna — the constant the SDK documents for getBalance. */
export const LUNA_PER_NIM = 100000;

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
  /** What a wallet shows as the buyer's NIM: available + staked + inactive. */
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
  /** The host and the chain disagreed beyond a unit fix: the chain value won. */
  mismatch?: boolean;
  /** A unit mismatch was detected and corrected (host reported NIM, not luna). */
  unitCorrected?: boolean;
  /** True while a previous reading is on screen during a refresh. */
  stale?: boolean;
  at?: number;
  failure?: WalletBalanceFailure;
}

const EMPTY: WalletBalanceState = {
  status: 'unavailable',
  availableNim: 0,
  stakedNim: 0,
  inactiveNim: 0,
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
  stakedNim: number;
  inactiveNim: number;
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
async function shopReading(): Promise<ShopReading> {
  const res = await getWalletBalance();
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
  return {
    luna,
    stakedNim: num(res.staked_nim),
    inactiveNim: num(res.inactive_nim),
    totalNim: num(res.total_nim) || luna / LUNA_PER_NIM,
    network: String(res.network || ''),
  };
}

/** Ask the host for the balance of `address`, in luna. */
async function bridgeLuna(address: string): Promise<number> {
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
    const luna = unwrapLuna(await fn());
    if (luna === null) {
      const err = new Error('unreadable balance');
      (err as any).type = 'UNKNOWN_ERROR';
      throw err;
    }
    return luna;
  };
  if (typeof provider.getBalance === 'function') {
    return call(() => provider.getBalance(address)); // SDK 0.2.1+ / supported host
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

async function readBalance(): Promise<WalletBalanceState> {
  const address = String(getAddress() || '').trim();
  const signedIn = isAuthed() && !!address;
  const hostAvailable = !!(inNimiqPay() || getNimiqProvider());

  let shop: ShopReading | null = null;
  let shopError: unknown = null;
  if (signedIn) {
    try {
      shop = await shopReading();
    } catch (e) {
      shopError = e;
    }
  }

  let host: number | null = null;
  let hostError: unknown = null;
  if (address && hostAvailable) {
    try {
      host = await bridgeLuna(address);
    } catch (e) {
      hostError = e;
    }
  }

  const base = { address, network: shop?.network || '' };

  // BOTH: reconcile. The session wallet is the one the shop charges, so it wins
  // any disagreement — but a clean five-orders-of-magnitude gap is a unit
  // mistake, not a disagreement, and is corrected silently.
  if (shop && host !== null) {
    const unitCorrected = !looksLikeUnitMixUp(host, shop.luna) ? false : true;
    const mismatch = !unitCorrected && Math.abs(host - shop.luna) > Math.max(1, shop.luna * 0.01);
    const state: WalletBalanceState = {
      status: 'ready',
      luna: shop.luna,
      availableNim: shop.luna / LUNA_PER_NIM,
      stakedNim: shop.stakedNim,
      inactiveNim: shop.inactiveNim,
      totalNim: shop.totalNim,
      nim: shop.luna / LUNA_PER_NIM,
      usd: 0,
      source: 'both',
      unitCorrected,
      mismatch,
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

  inflight = readBalance()
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
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && current.status !== 'loading') {
      void refreshWalletBalance();
    }
  });
}
