/**
 * walletBalance.ts — "how much NIM do I have right now?"
 *
 * WHY THIS EXISTS (owner, 2026-10-05): the first thing a buyer looks for is the
 * NIM they hold — to know what they can afford before they start a checkout.
 * It used to appear only after choosing an amount inside a product page. This
 * module is the ONE source of that number for the home, product, cart, checkout
 * and orders surfaces.
 *
 * TWO SOURCES, IN THIS ORDER
 *
 *   1. Nimiq Pay, through the host bridge: `getBalance(address)` (Mini App SDK
 *      0.2.1+, see developer-center PR 216). No RPC endpoint is involved, the
 *      lookup follows the host's active network, and no account approval or
 *      ownership check happens — any valid address works. Older hosts have no
 *      `getBalance`, so the documented generic form
 *      `request({ method: 'getBalance', params: { address } })` is tried second.
 *
 *   2. Our own backend (`/api/wallet/balance`, session-scoped) for everything
 *      else: the plain browser, and hosts old enough to reject the bridge call
 *      with `UNKNOWN_REQUEST`. Same constant, same meaning: luna, 1 NIM =
 *      100,000 luna.
 *
 * ERRORS ARE THE FEATURE, NOT AN AFTERTHOUGHT
 *
 * `init()` throws a plain Error when no provider is injected (not inside Pay),
 * the bridge rejects with `NimiqProviderError` on SDK 0.2.x, older bundles
 * RESOLVE with `{ error: { type, message } }`, and a direct `window.nimiq` call
 * keeps the host's raw shape. All four are normalized here into one small
 * vocabulary — the six documented types plus the numeric fallbacks (4001,
 * 4200, -32602, -32000, -32603) — and anything unrecognized stays `unknown`
 * instead of being guessed at (PR 215). A failed lookup NEVER blocks a screen:
 * it resolves to a state value, not a throw.
 *
 * The cached value is deliberately short-lived (25 s): a balance shown next to
 * a buy button must not be minutes old. Paying calls
 * `refreshWalletBalance({ force: true })` so the figure drops immediately
 * instead of waiting for the TTL.
 */
import { getWalletBalance, getNimRate, cachedNimRate } from './api';
import { initNimiqMiniApp, getNimiqProvider, inNimiqPay } from './miniapp';
import { getAddress, isAuthed, subscribeSession } from './session';

/** 1 NIM = 100,000 luna — the constant the SDK documents for getBalance. */
export const LUNA_PER_NIM = 100000;

/** How long a reading stays fresh. Short on purpose: it sits next to prices. */
const TTL_MS = 25000;

export type WalletBalanceFailure =
  /** PERMISSION_DENIED (4001): the user cancelled the host dialog. Normal. */
  | 'denied'
  /** UNKNOWN_REQUEST (4200) / no bridge method: this host cannot do it. */
  | 'unsupported'
  /** INVALID_REQUEST (-32602): the address we asked about is not valid. */
  | 'invalid'
  /** NETWORK_ERROR (-32000, incl. the host's 30 s timeout). */
  | 'network'
  /** INTERNAL_ERROR (-32603). */
  | 'internal'
  /** UNKNOWN_ERROR: an unmapped code or an unlisted host-supplied type. */
  | 'unknown'
  /** Not signed in (and no host wallet): there is no address to read. */
  | 'signed-out'
  /** The account has no wallet address attached. */
  | 'no-wallet';

export interface WalletBalanceState {
  status: 'loading' | 'ready' | 'error' | 'unavailable';
  /** Balance in luna exactly as the chain reports it. */
  luna: number;
  /** Balance in NIM (luna / 100,000). 0 is a real, successful reading. */
  nim: number;
  /** USD equivalent from the live rate, when one is known. 0 = unknown. */
  usd: number;
  /** Where the reading came from. */
  source: 'nimiq-pay' | 'shop' | 'none';
  /** Present when status is 'error' (and for 'unavailable' context). */
  failure?: WalletBalanceFailure;
  /** True while a previous reading is on screen during a refresh. */
  stale?: boolean;
  at?: number;
}

const EMPTY: WalletBalanceState = { status: 'loading', luna: 0, nim: 0, usd: 0, source: 'none' };

let current: WalletBalanceState = { ...EMPTY, status: 'unavailable' };
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

/* ---------------- error normalization (PR 215) ---------------- */

/** An Error carrying the bridge's `type`/`code`, so one reader handles both. */
function bridgeError(type: string, message: string, code?: number): Error {
  const err = new Error(message) as Error & { type?: string; code?: number };
  err.type = type;
  if (typeof code === 'number') err.code = code;
  return err;
}

const BY_CODE: Record<number, WalletBalanceFailure> = {
  4001: 'denied',
  4200: 'unsupported',
  [-32602]: 'invalid',
  [-32000]: 'network',
  [-32603]: 'internal',
};

const BY_TYPE: Record<string, WalletBalanceFailure> = {
  PERMISSION_DENIED: 'denied',
  UNKNOWN_REQUEST: 'unsupported',
  // Both appear in the reference with -32602: INVALID_REQUEST on this method
  // (getBalance) and INVALID_TRANSACTION on the transaction methods. For a
  // balance read the address is what can be invalid.
  INVALID_REQUEST: 'invalid',
  INVALID_TRANSACTION: 'invalid',
  NETWORK_ERROR: 'network',
  INTERNAL_ERROR: 'internal',
  UNKNOWN_ERROR: 'unknown',
};

interface BridgeError {
  type?: string;
  message?: string;
  code?: number;
}

/**
 * Pull { type, message, code } out of whatever the host threw. Covers:
 * NimiqProviderError instances, plain objects, JSON-RPC shaped errors nested
 * under `.data` or `.error`, and legacy resolved `{ error: {...} }` responses.
 */
function readErrorDetail(err: unknown): BridgeError {
  if (!err || typeof err !== 'object') {
    return typeof err === 'string' ? { message: err } : {};
  }
  const e = err as Record<string, any>;
  const legacy = e.error && typeof e.error === 'object' ? e.error : null;
  const data = e.data && typeof e.data === 'object' ? e.data : null;
  const code = [
    e.code,
    legacy?.code,
    data?.code,
  ].find((v) => typeof v === 'number');
  const type = [e.type, legacy?.type, data?.type].find((v) => typeof v === 'string' && v);
  const message = [e.message, legacy?.message, data?.message].find((v) => typeof v === 'string' && v);
  return {
    type: type as string | undefined,
    message: message as string | undefined,
    code: code as number | undefined,
  };
}

/** The documented vocabulary, plus a numeric fallback, plus `unknown`. */
export function classifyBalanceError(err: unknown): WalletBalanceFailure {
  const { type, code } = readErrorDetail(err);
  if (type && BY_TYPE[type]) return BY_TYPE[type];
  if (typeof code === 'number' && BY_CODE[code]) return BY_CODE[code];
  // An unlisted type supplied by the host is preserved, not reinterpreted:
  // we know a failure happened, not what caused it.
  if (type) return 'unknown';
  return 'unknown';
}

/* ---------------- the reading itself ---------------- */

function isNimiqAddress(value: string): boolean {
  const a = String(value || '').replace(/\s+/g, '').toUpperCase();
  return /^NQ\d{2}[0-9A-HJ-NP-VXY]{32}$/.test(a);
}

/** A bridge result may be a raw number or a wrapped payload; unwrap safely. */
function unwrapLuna(raw: unknown): number | null {
  if (typeof raw === 'number' && Number.isFinite(raw)) return raw;
  if (raw && typeof raw === 'object') {
    const o = raw as Record<string, any>;
    for (const k of ['balance', 'balance_luna', 'result']) {
      const v = o[k];
      if (typeof v === 'number' && Number.isFinite(v)) return v;
      if (typeof v === 'string' && v && Number.isFinite(Number(v))) return Number(v);
    }
  }
  return null;
}

/** true when a bridge error means "this host simply cannot do it". */
const isUnsupported = (err: unknown) => classifyBalanceError(err) === 'unsupported';

/**
 * Ask the host for the balance of `address`. Rejects with the host's own error
 * (normalized by classifyBalanceError) so the caller can decide whether to fall
 * back to the shop API.
 */
async function bridgeBalance(address: string): Promise<number> {
  const provider: any = (await initNimiqMiniApp()) || getNimiqProvider();
  if (!provider) throw bridgeError('UNKNOWN_REQUEST', 'no provider');
  // A missing, non-string or malformed address is rejected by the host; check
  // here too so we never send a request the host will refuse anyway.
  if (!isNimiqAddress(address)) throw bridgeError('INVALID_REQUEST', 'invalid address');

  if (typeof provider.getBalance === 'function') {
    const raw = await provider.getBalance(address); // SDK 0.2.1+ / supported host
    const luna = unwrapLuna(raw);
    if (luna === null) throw bridgeError('UNKNOWN_ERROR', 'unreadable balance');
    return luna;
  }
  if (typeof provider.request === 'function') {
    // Documented generic form. Same lookup, same network, no external RPC.
    const raw = await provider.request({ method: 'getBalance', params: { address } });
    const luna = unwrapLuna(raw);
    if (luna === null) throw bridgeError('UNKNOWN_ERROR', 'unreadable balance');
    return luna;
  }
  throw bridgeError('UNKNOWN_REQUEST', 'getBalance not supported by this host');
}

/** Our backend: session-scoped, no address parameter, no open proxy. */
async function shopBalance(): Promise<number> {
  const res = await getWalletBalance();
  if (res && res.available === false) {
    throw bridgeError(res.reason === 'no_wallet' ? 'NO_WALLET' : 'UNKNOWN_REQUEST', String(res.reason || ''));
  }
  const luna = unwrapLuna(res);
  if (luna === null) throw bridgeError('UNKNOWN_ERROR', 'unreadable balance');
  return luna;
}

/** Attach the live USD equivalent when a rate is already cached. */
function withUsd(state: WalletBalanceState): WalletBalanceState {
  const rate = Number(cachedNimRate()?.usd_per_nim) || 0;
  if (!(rate > 0) || !(state.nim >= 0)) return { ...state, usd: 0 };
  return { ...state, usd: state.nim * rate };
}

async function readBalance(): Promise<WalletBalanceState> {
  const address = String(getAddress() || '').trim();
  const signedIn = isAuthed() && !!address;

  // Inside Nimiq Pay the host is the fastest and most correct source: it reads
  // through its own client on its active network.
  if (address && (inNimiqPay() || getNimiqProvider())) {
    try {
      const luna = await bridgeBalance(address);
      // Fire-and-forget: a missing rate must not delay the balance.
      void getNimRate().catch(() => {});
      return withUsd({ status: 'ready', luna, nim: luna / LUNA_PER_NIM, usd: 0, source: 'nimiq-pay', at: Date.now() });
    } catch (err) {
      if (!isUnsupported(err)) {
        // A real answer from the wallet (cancelled, network, internal, unknown)
        // is NOT retried behind the user's back — only "this host cannot do it"
        // falls through to our own endpoint.
        return { status: 'error', luna: 0, nim: 0, usd: 0, source: 'none', failure: classifyBalanceError(err) };
      }
      /* fall through to the shop endpoint */
    }
  }

  if (!signedIn) {
    return { status: 'unavailable', luna: 0, nim: 0, usd: 0, source: 'none', failure: 'signed-out' };
  }

  try {
    const luna = await shopBalance();
    void getNimRate().catch(() => {});
    return withUsd({ status: 'ready', luna, nim: luna / LUNA_PER_NIM, usd: 0, source: 'shop', at: Date.now() });
  } catch (err) {
    const detail = readErrorDetail(err);
    if (detail.type === 'NO_WALLET') {
      return { status: 'unavailable', luna: 0, nim: 0, usd: 0, source: 'none', failure: 'no-wallet' };
    }
    const status = Number((err as any)?.status) || 0;
    if (status === 401) {
      return { status: 'unavailable', luna: 0, nim: 0, usd: 0, source: 'none', failure: 'signed-out' };
    }
    return { status: 'error', luna: 0, nim: 0, usd: 0, source: 'none', failure: 'network' };
  }
}

/**
 * The current reading. `force` skips the TTL cache (post-payment refresh, the
 * retry button, a pull-to-refresh style gesture). Concurrent callers share one
 * request, so the home page, the cart and the product page mounting together
 * still cost exactly one lookup.
 */
export function refreshWalletBalance({ force = false }: { force?: boolean } = {}): Promise<WalletBalanceState> {
  const fresh = current.status === 'ready' && current.at && Date.now() - current.at < TTL_MS;
  if (!force && fresh) return Promise.resolve(current);
  if (inflight) return inflight;

  if (current.status === 'ready') emit({ ...current, stale: true });
  else emit({ ...current, status: 'loading' });

  inflight = readBalance()
    .then((next) => emit(next))
    .catch(() => emit({ status: 'error', luna: 0, nim: 0, usd: 0, source: 'none', failure: 'unknown' }))
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
  emit({ status: 'unavailable', luna: 0, nim: 0, usd: 0, source: 'none' });
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
