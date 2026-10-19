import { validateCashbackCode } from './api';
import { t as tr } from '../i18n';

export const CASHBACK_CODE_KEY = 'nimshop_cashback_code';
export const CASHBACK_CODE_EVENT = 'nimshop:cashback-code';
const TTL_MS = 5 * 60 * 1000;

export type AppliedCashbackCode = {
  code: string;
  cashback_bps: number;
  cashback_percent: number;
  cashback_source?: string;
  cashback_exclusive?: boolean;
  overrides_staker?: boolean;
  message?: string;
  fetched_at: number;
};

export function normalizeCashbackCode(raw: unknown): string {
  return String(raw || '').trim().toUpperCase();
}

export function readAppliedCashbackCode(): AppliedCashbackCode | null {
  if (typeof sessionStorage === 'undefined') return null;
  try {
    const raw = sessionStorage.getItem(CASHBACK_CODE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    const code = normalizeCashbackCode(parsed && parsed.code);
    const bps = Number(parsed && parsed.cashback_bps);
    if (!code || !Number.isFinite(bps) || !(bps > 0)) return null;
    return {
      code,
      cashback_bps: bps,
      cashback_percent: Number(parsed && parsed.cashback_percent) || bps / 100,
      cashback_source: String(parsed && parsed.cashback_source || 'code'),
      cashback_exclusive: parsed && parsed.cashback_exclusive !== false,
      overrides_staker: parsed && parsed.overrides_staker !== false,
      message: String(parsed && parsed.message || ''),
      fetched_at: Number(parsed && parsed.fetched_at) || 0,
    };
  } catch {
    return null;
  }
}

export function cashbackCodeIsFresh(value: AppliedCashbackCode | null | undefined): boolean {
  return !!value && value.fetched_at > 0 && Date.now() - value.fetched_at < TTL_MS;
}

function broadcastCashbackCodeChange() {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new CustomEvent(CASHBACK_CODE_EVENT));
}

export function clearAppliedCashbackCode(): void {
  if (typeof sessionStorage !== 'undefined') {
    try {
      sessionStorage.removeItem(CASHBACK_CODE_KEY);
    } catch {
      /* ignore */
    }
  }
  broadcastCashbackCodeChange();
}

export function storeAppliedCashbackCode(value: Omit<AppliedCashbackCode, 'fetched_at'> & { fetched_at?: number }): AppliedCashbackCode {
  const stored: AppliedCashbackCode = {
    code: normalizeCashbackCode(value.code),
    cashback_bps: Number(value.cashback_bps) || 0,
    cashback_percent: Number(value.cashback_percent) || (Number(value.cashback_bps) || 0) / 100,
    cashback_source: value.cashback_source || 'code',
    cashback_exclusive: value.cashback_exclusive !== false,
    overrides_staker: value.overrides_staker !== false,
    message: value.message || '',
    fetched_at: value.fetched_at || Date.now(),
  };
  if (typeof sessionStorage !== 'undefined') {
    try {
      sessionStorage.setItem(CASHBACK_CODE_KEY, JSON.stringify(stored));
    } catch {
      /* ignore */
    }
  }
  broadcastCashbackCodeChange();
  return stored;
}

export function currentCashbackCode(): string {
  return readAppliedCashbackCode()?.code || '';
}

export async function applyCashbackCode(raw: string, orderUSD = 0): Promise<AppliedCashbackCode> {
  const code = normalizeCashbackCode(raw);
  if (!code) throw new Error(tr('cashback.codeEnterErr'));
  const res = await validateCashbackCode(code, orderUSD);
  return storeAppliedCashbackCode({
    code: String(res.code || code),
    cashback_bps: Number(res.cashback_bps) || 0,
    cashback_percent: Number(res.cashback_percent) || Number(res.cashback_bps || 0) / 100,
    cashback_source: String(res.cashback_source || 'code'),
    cashback_exclusive: res.cashback_exclusive !== false,
    overrides_staker: res.overrides_staker !== false,
    message: String(res.message || ''),
    fetched_at: Date.now(),
  });
}

export type PromoVerdict =
  | { state: 'none' } // nothing applied — nothing to prove
  | { state: 'fresh'; code: AppliedCashbackCode } // the shop just said yes
  | { state: 'rejected'; code: string; status: number } // the shop just said no
  | { state: 'unknown' }; // we could not ask — not a verdict either way

/**
 * Ask the shop about the promo code the buyer is carrying, RIGHT NOW.
 *
 * A code is proven once when it is typed in and then kept in sessionStorage
 * for five minutes, and in that window it can stop being true: it expires,
 * its uses run out, an admin retires it, or the cart grew past its cap. What
 * used to happen then is the worst possible outcome — the cart kept showing
 * the promo cashback, the order was created, and the code was quietly dropped
 * at the till, so the buyer found out only when the cashback never arrived.
 *
 * Callers must act on `rejected` (say so, drop the code) and must NOT block
 * on `unknown`: only the shop's own refusal is proof.
 */
export async function verifyAppliedCashbackCode(orderUSD = 0): Promise<PromoVerdict> {
  const current = readAppliedCashbackCode();
  if (!current) return { state: 'none' };
  try {
    return { state: 'fresh', code: await applyCashbackCode(current.code, orderUSD) };
  } catch (err) {
    const status = Number((err as { status?: number } | null)?.status) || 0;
    // A dropped connection or a 5xx is not proof the code is dead — blocking a
    // buyer on it would punish them for our own outage. Only a 4xx is the shop
    // saying "no", and only that may stop a checkout.
    if (status >= 400 && status < 500) return { state: 'rejected', code: current.code, status };
    return { state: 'unknown' };
  }
}

export async function refreshAppliedCashbackCode(): Promise<AppliedCashbackCode | null> {
  const current = readAppliedCashbackCode();
  if (!current) return null;
  if (cashbackCodeIsFresh(current)) return current;
  try {
    return await applyCashbackCode(current.code);
  } catch {
    clearAppliedCashbackCode();
    return null;
  }
}
