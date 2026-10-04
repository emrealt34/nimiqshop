/**
 * cashback.ts — buyer-facing cashback estimate copy and rate cache (ported
 * from cashback.js).
 */
import { getCashbackRate } from './api';
import { t as tr } from '../i18n';

const CB_KEY = 'nim_cashback_rate';
const CB_TTL_MS = 5 * 60 * 1000;
const DEFAULT_BPS = 0; // 0.00% — same as backend DefaultCashbackBps

/**
 * Cashback for a product, in NIM. Mirrors the backend EXACTLY
 * (internal/cashback.AmountLuna): bps/10_000 of the product price, converted
 * to Luna, rounded to whole Luna, then back to NIM for display.
 *
 * The Luna step is not decoration — it is why a sub-Luna payout reads as 0
 * here and as "skipped" on the server instead of the two disagreeing.
 */
export function estimateCashbackNIM(productNIM: number, bps: number): number {
  if (!(productNIM > 0) || !(bps > 0)) return 0;
  const luna = Math.round((productNIM * bps * 100_000) / 10_000);
  if (luna < 1) return 0;
  return luna / 100_000;
}

export function fmtCashbackNIM(n: number): string {
  if (!(n > 0)) return '0';
  if (n >= 100) return n.toFixed(0);
  if (n >= 10) return n.toFixed(1);
  const s = n.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  return s || '0';
}

export function cashbackPercentLabel(bps: number): string {
  const pct = Number(bps) / 100;
  if (!Number.isFinite(pct) || pct <= 0) return '';
  return Number.isInteger(pct) ? String(pct) : String(Number(pct.toFixed(2)));
}

export function cashbackLabelSuffix(meta?: { source?: string; code?: string }): string {
  const source = String((meta && meta.source) || '');
  const code = String((meta && meta.code) || '').trim();
  if (source === 'code') return code ? tr('cashback.suffixPromoCode', { code }) : tr('cashback.suffixPromoCodeBare');
  if (source === 'ledger' || source === 'staker') return tr('cashback.suffixBaseBoost');
  return '';
}

export function cashbackExclusiveNote(meta?: { source?: string; code?: string }): string {
  const source = String((meta && meta.source) || '');
  if (source !== 'code') return '';
  const code = String((meta && meta.code) || '').trim();
  return code
    ? tr('cashback.promoActiveCode', { code })
    : tr('cashback.promoOverrides');
}

export function cashbackEarnLine(productNIM: number, bps: number, meta?: { source?: string; code?: string }): string {
  const n = estimateCashbackNIM(productNIM, bps);
  if (!n) return '';
  const pct = cashbackPercentLabel(bps);
  return tr('cashback.earnLine', { nim: fmtCashbackNIM(n), pct, suffix: cashbackLabelSuffix(meta) });
}

/** The cashback NIM a quote will pay: the locked quote value when the server
 *  stamped one, else the live estimate at the quote's (or cached) rate. */
export function cashbackNimFromQuote(quote: any): number {
  const bps = Number.isFinite(Number(quote && quote.cashback_bps)) ? Number(quote.cashback_bps) : cachedCashbackBps();
  const locked = Number(quote && quote.estimated_cashback_nim);
  if (locked > 0) return locked;
  return estimateCashbackNIM(Number(quote && quote.estimated_nim) || 0, bps);
}

export function cashbackEarnLineFromQuote(quote: any): string {
  const bps = Number.isFinite(Number(quote && quote.cashback_bps)) ? Number(quote.cashback_bps) : cachedCashbackBps();
  const meta = {
    source: String((quote && quote.cashback_source) || ''),
    code: String((quote && quote.cashback_code) || ''),
  };
  const n = cashbackNimFromQuote(quote);
  if (!n) return '';
  const pct = cashbackPercentLabel(bps);
  if (!pct) return '';
  return tr('cashback.earnLine', { nim: fmtCashbackNIM(n), pct, suffix: cashbackLabelSuffix(meta) });
}

export function cachedCashbackBps(): number {
  try {
    const j = JSON.parse(sessionStorage.getItem(CB_KEY) || '');
    if (j && Number.isFinite(Number(j.cashback_bps))) return Number(j.cashback_bps);
  } catch {
    /* ignore */
  }
  return DEFAULT_BPS;
}

export async function loadCashbackBps(): Promise<number> {
  const now = Date.now();
  try {
    const raw = sessionStorage.getItem(CB_KEY);
    if (raw) {
      const j = JSON.parse(raw);
      if (j && Number.isFinite(Number(j.cashback_bps)) && now - j.fetched_at < CB_TTL_MS) {
        return Number(j.cashback_bps);
      }
    }
  } catch {
    /* ignore */
  }
  try {
    const r = await getCashbackRate();
    const bps = Number(r && r.cashback_bps);
    if (Number.isFinite(bps) && bps >= 0) {
      try {
        sessionStorage.setItem(CB_KEY, JSON.stringify({ cashback_bps: bps, fetched_at: now }));
      } catch {}
      return bps;
    }
  } catch {
    /* network — fall through */
  }
  return cachedCashbackBps();
}
