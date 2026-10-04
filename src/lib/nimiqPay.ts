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

export type PayLightningOutcome =
  | { status: 'submitted'; hash: string; swapId: string }
  // Terminal, do NOT resubmit the same invoice — check merchant status first:
  | { status: 'duplicate'; message: string; hash?: string; swapId?: string }
  | { status: 'unknown'; message: string; hash?: string; swapId?: string } // TRANSACTION_OUTCOME_UNKNOWN
  // Recoverable — the user may try again (e.g. after approving, or a fresh quote):
  | { status: 'declined'; message: string }     // PERMISSION_DENIED
  | { status: 'invalid'; message: string }       // INVALID_REQUEST / INVALID_TRANSACTION
  | { status: 'unavailable'; message: string }   // not inside Nimiq Pay / no invoice
  // The WALLET reported a network problem of its own (provider code -32000).
  // It says nothing about the shop being reachable, so it must not be shown
  // as such either — it gets its own copy.
  | { status: 'network'; message: string }
  // The SDK never reached the wallet at all: window.nimiq was missing or the
  // handshake timed out (live repro 2026-10-04: the 8-second init). A
  // wallet-connection state, never a shop outage.
  | { status: 'noProvider'; message: string }
  | { status: 'error'; message: string };

/** Outcomes after which the SAME invoice must never be submitted again. */
export function isTerminalOutcome(o: PayLightningOutcome | null | undefined): boolean {
  return !!o && (o.status === 'submitted' || o.status === 'duplicate' || o.status === 'unknown');
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
    const { hash, swapId } = await nimiq.payLightningInvoice({ invoice: inv });
    return { status: 'submitted', hash, swapId };
  } catch (error: unknown) {
    if (NimiqProviderError.is(error)) {
      const data = (error.data && typeof error.data === 'object' ? error.data : {}) as { hash?: string; swapId?: string };
      switch (error.type) {
        case 'DUPLICATE_PAYMENT':
          return { status: 'duplicate', message: error.message, hash: data.hash, swapId: data.swapId };
        case 'TRANSACTION_OUTCOME_UNKNOWN':
          return { status: 'unknown', message: error.message, hash: data.hash, swapId: data.swapId };
        case 'PERMISSION_DENIED':
          return { status: 'declined', message: error.message };
        case 'NETWORK_ERROR':
          return { status: 'network', message: error.message };
        case 'INVALID_REQUEST':
        case 'INVALID_TRANSACTION':
          return { status: 'invalid', message: error.message };
        default:
          return { status: 'error', message: error.message };
      }
    }
    const text = error instanceof Error ? error.message : String(error);
    // The SDK never reached the wallet: window.nimiq was absent or the init
    // handshake timed out ("Nimiq provider was not injected. Are you running
    // inside a Nimiq app?"). That is a wallet-connection state; the shop was
    // up the whole time (payment-launch answered 200 during the live repro).
    if (/was not injected|not injected|timed out|timeout/i.test(text)) {
      return { status: 'noProvider', message: text };
    }
    return { status: 'error', message: text };
  } finally {
    inFlight.delete(inv);
  }
}
