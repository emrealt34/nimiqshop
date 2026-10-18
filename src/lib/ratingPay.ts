/**
 * ratingPay.ts — the buyer's side of an on-chain star rating.
 *
 * The buyer's wallet signs and pays 1 Luna plus the network fee to the shop
 * wallet, with the rating memo. Nimiq Pay (inside the app) and Nimiq Hub
 * (in a browser) both do this. After the wallet returns, the shop is asked to
 * save the rating, and that call answers 202 until the chain shows the
 * transaction. This module owns that loop and the resume-after-redirect state.
 */
import { ApiError, submitRating, type RatingBody, type RatingKind } from './api';
import { inNimiqPay, initNimiqMiniApp } from './miniapp';
import { sendRatingWithHub } from './hub';
import { classifyWalletError } from './walletErrors';
import { getAddress, getUid } from './session';
import { cleanComment } from './ratingComment';
import { ratingStars } from './ratingStars';

export class RatingError extends Error {
  /** 'cancelled' | 'unavailable' | 'failed' | 'pending' | 'refused' | 'wrongWallet' */
  code: string;
  hash?: string;
  constructor(code: string, message: string, hash?: string) {
    super(message);
    this.code = code;
    this.hash = hash;
  }
}

/** What the shop asked the wallet to sign. Returned by the rating intent. */
export interface RatingIntent {
  memo: string;
  recipient: string;
  value_luna: number;
  fee_luna: number;
}

const PENDING_KEY = 'nimshop.pendingRating';
const PENDING_MAX_MS = 15 * 60 * 1000;

export interface PendingRating {
  kind: RatingKind;
  id: string;
  stars: number;
  comment: string;
  ts: number;
  owner_uid?: string;
  submitted?: boolean;
  hash?: string;
}

export function rememberPendingRating(p: Omit<PendingRating, 'ts'>): void {
  const stars = ratingStars(p.stars);
  if (stars === null) throw new RatingError('refused', 'rating must be an integer from 1 to 5');
  const owner = getUid();
  const value = { ...p, stars, owner_uid: owner || undefined, ts: Date.now() };
  pendingScope = owner ? PENDING_KEY + ':' + owner + ':' + p.kind + ':' + p.id : undefined;
  try { sessionStorage.setItem(PENDING_KEY, JSON.stringify(value)); } catch { /* Local copy below is independent. */ }
  if (pendingScope) try { localStorage.setItem(pendingScope, JSON.stringify(value)); } catch { /* Session copy still supports redirects. */ }
}

export function readPendingRating(kind: RatingKind, id: string): PendingRating | null {
  try {
    const owner = getUid();
    pendingScope = owner ? PENDING_KEY + ':' + owner + ':' + kind + ':' + id : undefined;
    let raw: string | null = null;
    if (pendingScope) try { raw = localStorage.getItem(pendingScope); } catch { /* Fall back to session storage. */ }
    if (!raw) raw = sessionStorage.getItem(PENDING_KEY);
    if (!raw) return null;
    const p = JSON.parse(raw) as PendingRating;
    if (p?.owner_uid && p.owner_uid !== owner) return null;
    const stars = ratingStars(p?.stars);
    if (!p || !['order', 'quote'].includes(p.kind) || typeof p.id !== 'string' || !p.id
      || stars === null || typeof p.comment !== 'string' || !Number.isFinite(p.ts)
      || p.ts <= 0 || p.ts > Date.now() + 60_000 || (!p.submitted && Date.now() - p.ts > PENDING_MAX_MS)) {
      clearPendingRating();
      return null;
    }
    if (p.kind !== kind || p.id !== id) return null;
    pendingScope = p.owner_uid ? PENDING_KEY + ':' + p.owner_uid + ':' + p.kind + ':' + p.id : undefined;
    return { ...p, stars };
  } catch {
    clearPendingRating();
    return null;
  }
}

let pendingScope: string | undefined;
export function markPendingRatingSubmitted(kind: RatingKind, id: string, hash?: string): void {
  const pending = readPendingRating(kind, id);
  if (!pending) return;
  const value = { ...pending, submitted: true, hash: hash || pending.hash };
  try { sessionStorage.setItem(PENDING_KEY, JSON.stringify(value)); } catch { /* Local durable copy below remains independent. */ }
  if (pendingScope) try { localStorage.setItem(pendingScope, JSON.stringify(value)); } catch { /* Session copy still survives navigation. */ }
}
export function clearPendingRating(): void {
  try { sessionStorage.removeItem(PENDING_KEY); } catch { /* Try the independent local copy too. */ }
  if (pendingScope) try { localStorage.removeItem(pendingScope); } catch { /* A later lookup can safely retry this metadata operation. */ }
  pendingScope = undefined;
}

/** Hands the intent to the buyer's wallet and resolves once the wallet is done. */
export async function payRatingIntent(intent: RatingIntent): Promise<{ hash?: string }> {
  try {
    if (inNimiqPay()) {
      const provider = (await initNimiqMiniApp()) as any;
      if (!provider || typeof provider.sendBasicTransactionWithData !== 'function') {
        throw new RatingError('unavailable', 'update-required');
      }
      const res = await provider.sendBasicTransactionWithData({
        recipient: intent.recipient,
        value: intent.value_luna,
        fee: intent.fee_luna,
        data: intent.memo,
      });
      if (res && typeof res === 'object' && 'error' in (res as object)) throw res;
      // Current providers return a transaction hash. Preserve it so confirmation
      // never depends on finding this payment in a short wallet-history window.
      // Older hosts without a hash can still recover through the memo scan.
      const hash = typeof res === 'string' ? res : res?.hash;
      return { hash: typeof hash === 'string' && /^[0-9a-f]{64}$/i.test(hash) ? hash.toLowerCase() : undefined };
    }
    return await sendRatingWithHub({
      recipient: intent.recipient,
      value: intent.value_luna,
      fee: intent.fee_luna,
      extraData: intent.memo,
      // The shop verifies the payer against the signed-in address; pin Hub to it.
      sender: getAddress(),
    });
  } catch (err) {
    if (err instanceof RatingError) throw err;
    const outcome = err as { type?: string; data?: { hash?: string } } | null;
    if (outcome?.type === 'TRANSACTION_OUTCOME_UNKNOWN') {
      const hash = outcome.data?.hash;
      throw new RatingError('pending', 'uncertain-wallet-submission',
        typeof hash === 'string' && /^[0-9a-f]{64}$/i.test(hash) ? hash.toLowerCase() : undefined);
    }
    // Hub's answer to forceSender when the signed-in address is not in this
    // Hub. Nothing was charged; the buyer must use the wallet they signed in with.
    if (/address not found/i.test(String((err as Error)?.message || ''))) {
      throw new RatingError('wrongWallet', 'wrong-wallet');
    }
    const cls = classifyWalletError(err);
    if (cls === 'denied') throw new RatingError('cancelled', 'cancelled');
    throw new RatingError('failed', String((err as Error)?.message || cls));
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * Asks the shop to save the rating until the chain confirms the buyer's
 * transaction. 202 (pending) is polled; any other answer is final.
 */
export async function awaitRatingSaved(
  kind: RatingKind,
  id: string,
  body: RatingBody,
  { attempts = 30, delayMs = 2000 }: { attempts?: number; delayMs?: number } = {},
): Promise<any> {
  const limit = Math.max(1, Math.min(30, attempts));
  const deadline = Date.now() + 90_000;
  for (let i = 0; i < limit && Date.now() < deadline; i++) {
    let res: any;
    try {
      res = await submitRating(kind, id, body);
    } catch (err) {
      if (err instanceof ApiError && err.status === 503) {
        if (i + 1 < limit && Date.now() < deadline) await sleep(delayMs);
        continue;
      }
      throw err;
    }
    if (res && res.status === 'pending') {
      if (i + 1 < limit && Date.now() < deadline) await sleep(delayMs);
      continue;
    }
    // A successful HTTP response alone must not discard an unsaved review.
    if (ratingStars(res?.rating) !== ratingStars(body.stars)
      || res?.comment !== cleanComment(body.comment || '')
      || typeof res?.rating_tx !== 'string' || !/^[0-9a-f]{64}$/i.test(res.rating_tx)
      || (body.tx_hash && res.rating_tx.toLowerCase() !== body.tx_hash.toLowerCase())) {
      throw new RatingError('pending', 'unverified-acknowledgement');
    }
    return res;
  }
  throw new RatingError('pending', 'pending');
}
