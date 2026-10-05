/**
 * walletErrors.ts — ONE vocabulary for everything the Nimiq Pay wallet can
 * throw at us.
 *
 * Written against developer-center PR 215 ("clarify mini-app wallet behavior
 * and error handling"): from SDK 0.2.0 the provider rejects with
 * `NimiqProviderError` (a `type`, a `message`, an optional numeric `code`), the
 * SDK normalizes legacy `{ error: { type, message } }` responses into the same
 * class, and unlisted host-supplied types are preserved rather than remapped.
 * `init()` on the other hand throws a plain Error when no provider is injected,
 * and a direct `window.nimiq` call keeps the host's raw error shape.
 *
 * All four shapes pass through `readWalletError` here, and everything else
 * about them — the copy, the retry advice, the "was it my balance?" question —
 * is decided from the normalized result. Unrecognized codes stay `unknown`:
 * guessing a cause from a number we were never given is how a shop ends up
 * telling a buyer the wrong thing (PR 215's whole point).
 */

export type WalletErrorClass =
  /** PERMISSION_DENIED (4001) — the buyer cancelled. A normal outcome. */
  | 'denied'
  /** UNKNOWN_REQUEST (4200) — this host cannot do the thing at all. */
  | 'unsupported'
  /** INVALID_REQUEST / INVALID_TRANSACTION (-32602). */
  | 'invalid'
  /** NETWORK_ERROR (-32000), including the host's 30-second timeout. */
  | 'network'
  /** INTERNAL_ERROR (-32603). */
  | 'internal'
  /** UNKNOWN_ERROR, or any type the host invented and we must not reinterpret. */
  | 'unknown'
  /** An INVALID_TRANSACTION whose own message names the balance. */
  | 'insufficient';

export interface WalletErrorDetail {
  type?: string;
  code?: number;
  message?: string;
}

const BY_CODE: Record<number, WalletErrorClass> = {
  4001: 'denied',
  4200: 'unsupported',
  [-32602]: 'invalid',
  [-32000]: 'network',
  [-32603]: 'internal',
};

const BY_TYPE: Record<string, WalletErrorClass> = {
  PERMISSION_DENIED: 'denied',
  UNKNOWN_REQUEST: 'unsupported',
  INVALID_REQUEST: 'invalid',
  INVALID_TRANSACTION: 'invalid',
  NETWORK_ERROR: 'network',
  INTERNAL_ERROR: 'internal',
  UNKNOWN_ERROR: 'unknown',
};

/**
 * The wallet's own words matter: when a transaction is refused the host usually
 * says WHY, and "not enough NIM for the amount plus the network fee" is a
 * different problem from "this invoice is malformed". Anything that names the
 * balance is classified as such instead of being flattened into "invalid".
 */
const INSUFFICIENT = /insufficient|not enough|too little|below the minimum|balance|funds|yetersiz|bakiye/i;

/** Pull { type, message, code } out of any shape the wallet may hand us. */
export function readWalletError(err: unknown): WalletErrorDetail {
  if (err == null) return {};
  if (typeof err === 'string') return { message: err };
  if (typeof err !== 'object') return { message: String(err) };

  const e = err as Record<string, any>;
  const legacy = e.error && typeof e.error === 'object' ? (e.error as Record<string, any>) : null;
  const data = e.data && typeof e.data === 'object' ? (e.data as Record<string, any>) : null;

  const code = [e.code, legacy?.code, data?.code].find((v) => typeof v === 'number' && Number.isFinite(v));
  const type = [e.type, legacy?.type, data?.type].find((v) => typeof v === 'string' && v);
  const message = [e.message, legacy?.message, data?.message, e.reason]
    .find((v) => typeof v === 'string' && v.trim());

  return {
    type: type as string | undefined,
    message: message as string | undefined,
    code: typeof code === 'number' ? code : undefined,
  };
}

/** The documented vocabulary → the numeric fallback → `unknown`, in that order. */
export function classifyWalletError(err: unknown): WalletErrorClass {
  const { type, code, message } = readWalletError(err);
  const byType = type ? BY_TYPE[type] : undefined;
  const byCode = typeof code === 'number' ? BY_CODE[code] : undefined;
  const cls = byType || byCode || 'unknown';
  // A refusal that says the balance is the reason is more useful to the buyer
  // than the generic "invalid transaction" bucket it arrives in.
  if (cls === 'invalid' && message && INSUFFICIENT.test(message)) return 'insufficient';
  return cls;
}

/** "INVALID_TRANSACTION (-32602)" — for the small technical-detail line. */
export function walletErrorCode(err: unknown): string {
  const { type, code } = readWalletError(err);
  if (type && typeof code === 'number') return `${type} (${code})`;
  if (type) return type;
  if (typeof code === 'number') return String(code);
  return '';
}
