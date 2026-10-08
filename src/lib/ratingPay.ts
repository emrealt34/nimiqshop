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

export class RatingError extends Error {
  /** 'cancelled' | 'unavailable' | 'failed' | 'pending' | 'refused' */
  code: string;
  constructor(code: string, message: string) {
    super(message);
    this.code = code;
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
}

export function rememberPendingRating(p: Omit<PendingRating, 'ts'>): void {
  try {
    sessionStorage.setItem(PENDING_KEY, JSON.stringify({ ...p, ts: Date.now() }));
  } catch {
    /* private mode: the flow still works, it just cannot resume */
  }
}

export function readPendingRating(kind: RatingKind, id: string): PendingRating | null {
  try {
    const raw = sessionStorage.getItem(PENDING_KEY);
    if (!raw) return null;
    const p = JSON.parse(raw) as PendingRating;
    if (!p || p.kind !== kind || p.id !== id || Date.now() - p.ts > PENDING_MAX_MS) return null;
    return p;
  } catch {
    return null;
  }
}

export function clearPendingRating(): void {
  try {
    sessionStorage.removeItem(PENDING_KEY);
  } catch {
    /* ignore */
  }
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
      // Nimiq Pay returns the serialized transaction, not a hash. The shop finds it by memo.
      return {};
    }
    return await sendRatingWithHub({
      recipient: intent.recipient,
      value: intent.value_luna,
      fee: intent.fee_luna,
      extraData: intent.memo,
    });
  } catch (err) {
    if (err instanceof RatingError) throw err;
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
  { attempts = 45, delayMs = 4000 }: { attempts?: number; delayMs?: number } = {},
): Promise<any> {
  for (let i = 0; i < attempts; i++) {
    let res: any;
    try {
      res = await submitRating(kind, id, body);
    } catch (err) {
      if (err instanceof ApiError && err.status === 503) {
        await sleep(delayMs);
        continue;
      }
      throw err;
    }
    if (res && res.status === 'pending') {
      await sleep(delayMs);
      continue;
    }
    return res;
  }
  throw new RatingError('pending', 'pending');
}
