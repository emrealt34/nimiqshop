/**
 * lib/nimiqPay.ts — Nimiq Pay Mini App payment trigger.
 *
 * Thin, SSR-safe wrapper around the Nimiq Pay Mini App SDK `payLightningInvoice()`
 * so the shop can let a buyer pay a merchant BOLT11 / LNURL invoice with NIM or
 * USDT from inside Nimiq Pay. Nimiq Pay owns asset choice, fees and final
 * approval; the shop only hands over the invoice and reacts to the outcome.
 *
 * IMPORTANT: a resolved submission means the SPENDING transaction was submitted
 * to NIM/Polygon — it does NOT mean the merchant received BTC over Lightning.
 * The order must stay pending until the backend confirms Lightning settlement
 * (the order page already polls for that and hides the pay card when it does).
 *
 * Per the SDK contract, `DUPLICATE_PAYMENT` and `TRANSACTION_OUTCOME_UNKNOWN`
 * must never be auto-retried with the same invoice. This module surfaces those
 * as terminal outcomes (see `isTerminalOutcome`) so the UI stops and asks the
 * user to check payment status instead of resubmitting.
 *
 * See: Nimiq Provider API — payLightningInvoice; "Bitcoin Lightning Payments in
 * Mini Apps".
 */

import { init as initMiniAppSdk, NimiqProviderError, type NimiqProvider } from '@nimiq/mini-app-sdk';
import { inNimiqPay } from './miniapp';
import { classifyWalletError, readWalletError, walletErrorCode } from './walletErrors';

// miniapp.ts already imports the SDK for host-language and account bootstrap.
// Importing it dynamically here cannot split the bundle; only initialize the
// payment provider when the buyer actually submits an invoice.
let providerPromise: Promise<NimiqProvider> | null = null;
async function getProvider(): Promise<NimiqProvider> {
  if (!providerPromise) {
    providerPromise = initMiniAppSdk({ timeout: 8000 });
    providerPromise.catch(() => { providerPromise = null; });
  }
  return providerPromise;
}

/** True when the shop is inside Nimiq Pay (injected window.nimiq / window.nimiqPay). */
export function isInNimiqPay(): boolean {
  return inNimiqPay();
}

/** The wallet's own account of what happened, carried alongside the status. */
export interface PayWalletError {
  /** e.g. INVALID_TRANSACTION, NETWORK_ERROR, or a type the host invented. */
  type: string;
  /** The provider's numeric code, when it supplied one. */
  code: number;
  /** The wallet's own message, verbatim — never replaced by our copy. */
  message: string;
  /** "INVALID_TRANSACTION (-32602)" for the technical-detail line. */
  label: string;
}

export type PayLightningOutcome =
  | { status: 'submitted'; hash: string; swapId: string }
  // Terminal, do NOT resubmit the same invoice — check merchant status first:
  | { status: 'duplicate'; message: string; hash?: string; swapId?: string; wallet?: PayWalletError }
  | { status: 'unknown'; message: string; hash?: string; swapId?: string; wallet?: PayWalletError } // TRANSACTION_OUTCOME_UNKNOWN
  // Recoverable — the user may try again (e.g. after approving, or a fresh quote):
  | { status: 'declined'; message: string; wallet?: PayWalletError }      // PERMISSION_DENIED
  /* The wallet refused the spend and said why. The commonest real cause is an
     insufficient balance for the amount plus the network fee — a buyer who
     reads "this payment request is invalid or expired" (what INVALID_TRANSACTION
     used to render as) is being told the wrong thing. */
  | { status: 'insufficient'; message: string; wallet?: PayWalletError }
  | { status: 'invalid'; message: string; wallet?: PayWalletError }        // INVALID_REQUEST / INVALID_TRANSACTION
  | { status: 'unavailable'; message: string }                            // not inside Nimiq Pay / no invoice
  | { status: 'updateRequired'; message: string; wallet?: PayWalletError } // host does not support this Mini App method
  // The WALLET reported a network problem of its own (provider code -32000).
  // It says nothing about the shop being reachable, so it must not be shown
  // as such either — it gets its own copy.
  | { status: 'network'; message: string; wallet?: PayWalletError }
  // The SDK never reached the wallet at all: window.nimiq was missing or the
  // handshake timed out (live repro 2026-10-04: the 8-second init). A
  // wallet-connection state, never a shop outage.
  | { status: 'noProvider'; message: string }
  | { status: 'error'; message: string; wallet?: PayWalletError };

/**
 * Everything the wallet threw, in one shape. `classifyWalletError` owns the
 * documented vocabulary + numeric fallbacks (see walletErrors.ts); this maps
 * the class onto a pay outcome so the button never has to guess.
 */
function outcomeFromWalletError(error: unknown): PayLightningOutcome {
  const detail = readWalletError(error);
  const cls = classifyWalletError(error);
  const wallet: PayWalletError = {
    type: detail.type || (cls === 'unknown' ? 'UNKNOWN_ERROR' : cls.toUpperCase()),
    code: typeof detail.code === 'number' ? detail.code : 0,
    message: detail.message || '',
    label: walletErrorCode(error),
  };
  const message = wallet.message;
  switch (cls) {
    case 'denied':
      return { status: 'declined', message, wallet };
    case 'network':
      return { status: 'network', message, wallet };
    case 'insufficient':
      // The wallet named the balance: say so, with the numbers.
      return { status: 'insufficient', message, wallet };
    case 'invalid':
      return { status: 'invalid', message, wallet };
    case 'unsupported':
      // The app is running in Nimiq Pay, but this host does not support the
      // Lightning invoice method. Keep it distinct from an app-not-installed
      // case so the UI can tell the buyer to update Nimiq Pay.
      return { status: 'updateRequired', message, wallet };
    default:
      return { status: 'error', message, wallet };
  }
}

/** Outcomes after which the SAME invoice must never be submitted again. */
export function isTerminalOutcome(o: PayLightningOutcome | null | undefined): boolean {
  return !!o && (o.status === 'submitted' || o.status === 'duplicate' || o.status === 'unknown');
}

function walletFacts(error: unknown): PayWalletError {
  const detail = readWalletError(error);
  return {
    type: detail.type || 'UNKNOWN_ERROR',
    code: typeof detail.code === 'number' ? detail.code : 0,
    message: detail.message || '',
    label: walletErrorCode(error),
  };
}

// Guards a rapid double-click / concurrent submit of one invoice in this tab.
const inFlight = new Set<string>();

/**
 * Trigger the Nimiq Pay payment for `invoice`. Never throws: every path (success
 * and every documented error) is mapped to a `PayLightningOutcome` the UI can
 * render deterministically.
 */
export async function payLightningInvoice(invoice: string): Promise<PayLightningOutcome> {
  const inv = (invoice || '').trim();
  if (!inv) return { status: 'invalid', message: 'No Lightning invoice to pay yet.' };
  if (!isInNimiqPay()) {
    return { status: 'unavailable', message: 'Open this shop inside Nimiq Pay to pay with NIM or USDT.' };
  }
  if (inFlight.has(inv)) {
    return { status: 'unknown', message: 'This invoice is already being submitted — check payment status before retrying.' };
  }
  inFlight.add(inv);
  try {
    const nimiq = await getProvider();
    // SDK typings may include a method that the injected host has not shipped
    // yet. Check the runtime provider before calling it so an older Nimiq Pay
    // version gets an actionable update message instead of a generic TypeError.
    if (typeof nimiq.payLightningInvoice !== 'function') {
      return {
        status: 'updateRequired',
        message: 'Update Nimiq Pay to use Lightning invoice payments.',
      };
    }
    const res: any = await nimiq.payLightningInvoice({ invoice: inv });
    // PR 215: SDK 0.2.x converts legacy `{ error: { type, message } }` answers
    // into NimiqProviderError, but an older host bundle still RESOLVES with
    // that envelope. Reading it as a success would report a payment that never
    // happened, so the envelope is checked before the happy path.
    if (res && typeof res === 'object' && res.error) {
      return outcomeFromWalletError(res.error);
    }
    const hash = String(res?.hash || '');
    const swapId = String(res?.swapId || '');
    if (!hash && !swapId) {
      // A resolved call with neither identifier cannot be treated as a
      // submitted payment: the merchant side would never hear about it.
      return {
        status: 'unknown',
        message: 'The wallet returned no transaction id.',
        wallet: { type: 'UNKNOWN_ERROR', code: 0, message: 'no hash/swapId in the result', label: 'UNKNOWN_ERROR' },
      };
    }
    return { status: 'submitted', hash, swapId };
  } catch (error: unknown) {
    if (NimiqProviderError.is(error)) {
      const data = (error.data && typeof error.data === 'object' ? error.data : {}) as { hash?: string; swapId?: string };
      // The two terminal outcomes stay first: their identifiers are what support
      // needs, and neither may ever be resubmitted.
      if (error.type === 'DUPLICATE_PAYMENT') {
        const wallet = walletFacts(error);
        return { status: 'duplicate', message: error.message, hash: data.hash, swapId: data.swapId, wallet };
      }
      if (error.type === 'TRANSACTION_OUTCOME_UNKNOWN') {
        const wallet = walletFacts(error);
        return { status: 'unknown', message: error.message, hash: data.hash, swapId: data.swapId, wallet };
      }
      return outcomeFromWalletError(error);
    }
    const text = error instanceof Error ? error.message : String(error);
    // The SDK never reached the wallet: window.nimiq was absent or the init
    // handshake timed out ("Nimiq provider was not injected. Are you running
    // inside a Nimiq app?"). That is a wallet-connection state; the shop was
    // up the whole time (payment-launch answered 200 during the live repro).
    if (/was not injected|not injected|timed out|timeout/i.test(text)) {
      return { status: 'noProvider', message: text };
    }
    return outcomeFromWalletError(error);
  } finally {
    inFlight.delete(inv);
  }
}
