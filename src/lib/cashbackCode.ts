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
