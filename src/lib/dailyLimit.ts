/**
 * dailyLimit.ts — shared daily/monthly purchase-budget parsing and copy.
 * The legacy exported names remain compatible with existing checkout callers.
 *
 * The refusal is HTTP 429 with DAILY_LIMIT_EXCEEDED or MONTHLY_LIMIT_EXCEEDED, plus the buyer's
 * own numbers: what the window holds, how much is used, what this order would
 * have cost and when the oldest purchase drops off. Parsing and wording live
 * here so every surface (checkout sheet, cart, product page) says the same
 * thing about the same state — and none of them can fall back to the generic
 * "too many requests, retry in a few seconds", which is wrong advice for a
 * budget that refills over hours, not seconds.
 */
import { fmtCountdown, fmtUSD } from './format';
import { t as tr } from '../i18n';

/** Matches `CodeDailyLimit` in backend/internal/handlers/daily_limit.go. */
export const DAILY_LIMIT_CODE = 'DAILY_LIMIT_EXCEEDED';
export const MONTHLY_LIMIT_CODE = 'MONTHLY_LIMIT_EXCEEDED';

export type DailyLimitReason = 'orders' | 'spend';

export type DailyLimit = {
  period?: 'daily' | 'monthly';
  /** The backend's own sentence — always safe to show verbatim. */
  message: string;
  /** Which ceiling refused the purchase: the order count or the spend. */
  reason: DailyLimitReason;
  attemptedUSD: number;
  usedOrders: number;
  /** 0 = that ceiling is disabled by the operator. */
  maxOrders: number;
  /** -1 = the order-count ceiling is disabled. */
  remainingOrders: number;
  usedUSD: number;
  maxUSD: number;
  remainingUSD: number;
  /** Epoch ms, already corrected for device-clock skew. 0 = unknown. */
  resetsAt: number;
};

const fallbackDaily = () => tr('limit.fallbackDaily');

function num(v: unknown, fallback = 0): number {
  const n = typeof v === 'string' ? parseFloat(v) : Number(v);
  return Number.isFinite(n) ? n : fallback;
}

/** Server timestamps are anchored to the server clock: a wrong device clock
 *  would otherwise show a countdown that is hours off (or already "reset"). */
function toClientEpoch(iso: unknown, serverNowMs: number): number {
  const t = Date.parse(String(iso || ''));
  if (!Number.isFinite(t)) return 0;
  if (Number.isFinite(serverNowMs) && serverNowMs > 0) return t - (serverNowMs - Date.now());
  return t;
}

/** True when either purchase budget refuses an order (not request throttling). */
export function isDailyLimitError(err: unknown): boolean {
  const e = err as { code?: string; data?: { code?: unknown } } | null;
  return [e?.code, e?.data?.code].some(code => code === DAILY_LIMIT_CODE || code === MONTHLY_LIMIT_CODE);
}

/** Buyer-facing sentence for a limit refusal — never a raw status line. */
export function dailyLimitMessage(err: unknown): string {
  return dailyLimitFromError(err)?.message || fallbackDaily();
}

/** Full parse of a limit refusal; null for any other error. */
export function dailyLimitFromError(err: unknown): DailyLimit | null {
  if (!isDailyLimitError(err)) return null;
  const e = err as { code?: string; message?: string; data?: Record<string, any> } | null;
  const d = (e?.data || {}) as Record<string, any>;
  const message = String(d.detail || d.error || e?.message || '').trim();
  const period = e?.code === MONTHLY_LIMIT_CODE || d.code === MONTHLY_LIMIT_CODE ? 'monthly' : 'daily';
  return {
    period,
    message: message || (period === 'monthly' ? tr('limit.fallbackMonthly') : fallbackDaily()),
    reason: d.limit_reason === 'orders' ? 'orders' : 'spend',
    attemptedUSD: num(d.attempted_usd),
    usedOrders: num(d.used_orders),
    maxOrders: num(d.max_orders),
    remainingOrders: num(d.remaining_orders, -1),
    usedUSD: num(d.used_usd),
    maxUSD: num(d.max_usd),
    remainingUSD: num(d.remaining_usd),
    resetsAt: toClientEpoch(d.resets_at, Date.parse(String(d.server_now || ''))),
  };
}

/** The screen title. "Too many requests" would send the buyer the wrong way. */
export function limitHeadline(l: DailyLimit): string {
  if (l.period === 'monthly') return tr('limit.headlineMonthly');
  return l.reason === 'orders' ? tr('limit.headlineOrders') : tr('limit.headlineSpend');
}

/** The one sentence that explains the refusal for THIS item or cart. */
export function limitSentence(l: DailyLimit, subject: string): string {
  const what = subject && subject.trim() ? subject.trim() : tr('limit.subjectFallback');
  const price = l.attemptedUSD > 0 ? tr('limit.subjectCosts', { subject: what, usd: fmtUSD(l.attemptedUSD) }) : tr('limit.subjectNoPrice', { subject: what });
  if (l.reason === 'orders') {
    return tr('limit.ordersUsedAll', { price, max: String(l.maxOrders) });
  }
  if (l.period === 'monthly') return tr('limit.monthlyLeft', { price, left: fmtUSD(l.remainingUSD), max: fmtUSD(l.maxUSD) });
  const left = l.maxUSD > 0
    ? tr('limit.dailyLeftOfMax', { left: fmtUSD(l.remainingUSD), max: fmtUSD(l.maxUSD) })
    : tr('limit.dailyLeft', { left: fmtUSD(l.remainingUSD) });
  return tr('limit.spendLine', { price, left });
}

/** "Orders 2 of 3 · Spending $46.00 of $50.00" — the counters line. */
export function limitCounters(l: DailyLimit): string {
  const parts: string[] = [];
  if (l.maxOrders > 0) parts.push(tr('limit.countersOrders', { used: String(Math.min(l.usedOrders, l.maxOrders)), max: String(l.maxOrders) }));
  if (l.maxUSD > 0) parts.push(tr('limit.countersSpending', { used: fmtUSD(Math.min(l.usedUSD, l.maxUSD)), max: fmtUSD(l.maxUSD) }));
  return parts.join(' · ');
}

/** What the buyer can actually do about it right now. */
export function limitHint(l: DailyLimit): string {
  if (l.period === 'monthly') return tr('limit.hintMonthly', { left: fmtUSD(l.remainingUSD) });
  if (l.reason === 'orders') return tr('limit.hintOrders');
  if (l.remainingUSD <= 0) return tr('limit.hintNothingFits');
  return tr('limit.hintChooseUpTo', { left: fmtUSD(l.remainingUSD) });
}

/** "Resets in 6h 12m — your oldest purchase drops off the window then." */
export function limitResetLine(l: DailyLimit, now = Date.now()): string {
  if (l.period === 'monthly') {
    if (!l.resetsAt) return tr('limit.resetMonthlyUnknown');
    if (l.resetsAt <= now) return tr('limit.resetNewMonth');
    return tr('limit.resetMonthlyIn', { time: fmtCountdown(l.resetsAt - now) });
  }
  if (!l.resetsAt) return tr('limit.resetDailyUnknown');
  const ms = l.resetsAt - now;
  if (ms <= 0) return tr('limit.resetDailyNow');
  return tr('limit.resetDailyIn', { time: fmtCountdown(ms) });
}
