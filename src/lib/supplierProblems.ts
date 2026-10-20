import catalog from '../../backend/internal/cryptorefills/problem_catalog.json';
import { t as tr } from '../i18n';
import { fmtClock, fmtMoney } from './format';

export type SupplierIssue = { code: string; scope: string; message: string; moreDetails?: unknown };
const messages: Record<string, { scope: string; message: string }> = catalog;
const unknownMessage = () => tr('supplier.unknown');
const machineCode = /^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$/;
function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

/** Keep EVERY problem and its structured details. Never execute or display raw
 * provider reasons, links, HTML or unsupported payment alternatives. */
export function supplierIssues(error: unknown): SupplierIssue[] {
  const err = record(error);
  const data = record(err.data);
  const body = Object.keys(data).length ? data : err;
  const result: SupplierIssue[] = [];
  const add = (rawCode: unknown, details?: unknown) => {
    const code = typeof rawCode === 'string' && machineCode.test(rawCode) ? rawCode : 'UNKNOWN_SUPPLIER_PROBLEM';
    const canonical = code === 'PRODUCT_NOT_AVAILABLE' ? 'NOT_AVAILABLE_PRODUCT' : code;
    const info = Object.prototype.hasOwnProperty.call(messages, canonical) ? messages[canonical] : undefined;
    const period = lightningLimitPeriodOf(canonical);
    const message = (period && lightningLimitMessage(details, period)) || (info ? tr(`supplier.${canonical}`) : unknownMessage());
    result.push({ code, moreDetails: details, scope: info?.scope || 'unknown', message });
  };
  if (Array.isArray(body.problems)) {
    for (const entry of body.problems) {
      const p = record(entry);
      add(p.problem, p.moreDetails);
    }
  }
  // Some providers put the suspension in detail, with numeric status. Handle
  // that as well as our normalized code, without dropping a simultaneous list.
  for (const candidate of [body.code, err.code, body.detail]) {
    if (typeof candidate !== 'string') continue;
    const known = Object.prototype.hasOwnProperty.call(messages, candidate);
    const isSupplierCode = body.supplier_error === true && machineCode.test(candidate) && !['SUPPLIER_PROBLEMS', 'SUPPLIER_UNAVAILABLE', 'SUPPLIER_RATE_LIMITED'].includes(candidate);
    if ((known || isSupplierCode) && !result.some(p => p.code === candidate)) add(candidate, body.moreDetails);
  }
  if (!result.length && body.supplier_error === true) add('UNKNOWN_SUPPLIER_PROBLEM');
  const seconds = Number(body.retry_after_seconds);
  if (result.length && (err.status === 429 || body.code === 'SUPPLIER_RATE_LIMITED') && Number.isFinite(seconds) && seconds > 0) {
    result.push({code: 'SUPPLIER_COOLDOWN', scope: 'rate', message: tr('supplier.cooldown', { seconds: Math.ceil(seconds) })});
  }
  return result;
}

export function supplierProblemMessage(error: unknown): string {
  return supplierIssues(error).map(p => p.message).join('\n\n');
}

/* ---- LIGHTNING_*_LIMIT_EXCEEDED ---------------------------------------
 * The payment partner's Lightning rule for buyers without an account has
 * three parts, each with its own code: a ceiling on the order total (fee
 * included), then a daily and a monthly budget per DELIVERY EMAIL that every
 * created order uses up, paid or not. Live, 2026-10-20: a 104 USD Walmart
 * order left unpaid made the next one with the same email fail with
 * LIGHTNING_DAILY_LIMIT_EXCEEDED, which rendered as "could not approve this
 * request". The backend now adds `moreDetails.shop` — the buyer's own
 * orders that are holding the budget — so the sentence can say which order
 * to pay instead of opening another. */

export type LightningPeriod = 'per_order' | 'daily' | 'monthly';

const lightningCodes: Record<string, LightningPeriod> = {
  LIGHTNING_ORDER_LIMIT_EXCEEDED: 'per_order',
  LIGHTNING_DAILY_LIMIT_EXCEEDED: 'daily',
  LIGHTNING_MONTHLY_LIMIT_EXCEEDED: 'monthly',
};

export function lightningLimitPeriodOf(code: unknown): LightningPeriod | null {
  return typeof code === 'string' && Object.prototype.hasOwnProperty.call(lightningCodes, code) ? lightningCodes[code] : null;
}

/** The first Lightning-limit period among a refusal's problems, or null. */
export function lightningLimitPeriod(issues: SupplierIssue[]): LightningPeriod | null {
  for (const issue of issues) {
    const period = lightningLimitPeriodOf(issue.code);
    if (period) return period;
  }
  return null;
}

type Money = { amount: number; currency: string };

function money(value: unknown): Money | null {
  const m = record(value);
  const amount = Number(m.amount);
  if (!Number.isFinite(amount) || amount < 0) return null;
  const currency = typeof m.currency === 'string' && /^[A-Za-z]{3}$/.test(m.currency) ? m.currency.toUpperCase() : 'EUR';
  return { amount, currency };
}

/** The supplier's per-email ceilings on Lightning orders, as parsed from
 *  moreDetails (live shape of 2026-10-20: limits.{per_order,daily,monthly},
 *  order_total, spent_today, spent_this_month — each {amount, currency}). */
export type LightningLimit = {
  perOrder: Money | null; daily: Money | null; monthly: Money | null;
  orderTotal: Money | null; spentToday: Money | null; spentThisMonth: Money | null;
  /** Which ceiling the order crossed: what the numbers say (they are the
   *  supplier's own and decide even when its code is the generic per-order
   *  one), else the period the code names. */
  exceeded: LightningPeriod | null;
};

export function parseLightningLimit(details: unknown, period?: LightningPeriod | null): LightningLimit | null {
  const d = record(details);
  const limits = record(d.limits);
  const out: LightningLimit = {
    perOrder: money(limits.per_order), daily: money(limits.daily), monthly: money(limits.monthly),
    orderTotal: money(d.order_total), spentToday: money(d.spent_today), spentThisMonth: money(d.spent_this_month),
    exceeded: null,
  };
  if (!out.perOrder && !out.daily && !out.monthly) return null;
  const total = out.orderTotal?.amount || 0;
  if (total > 0) {
    if (out.perOrder && total > out.perOrder.amount) out.exceeded = 'per_order';
    else if (out.daily && (out.spentToday?.amount || 0) + total > out.daily.amount) out.exceeded = 'daily';
    else if (out.monthly && (out.spentThisMonth?.amount || 0) + total > out.monthly.amount) out.exceeded = 'monthly';
  }
  if (!out.exceeded) out.exceeded = period || null;
  return out;
}

/** One of the buyer's own orders that the shop knows is using the budget
 *  (moreDetails.shop.orders, written by the backend from its own ledger). */
export type EmailBudgetOrder = {
  quoteId: string; productId: string; country: string; denomination: string;
  productValue: number; productCurrency: string; quantity: number; status: string;
  createdAt: string; paymentExpiry: string; totalUsd: number;
  /** Still payable: paying it uses the budget it already holds. */
  pending: boolean;
};

export type EmailBudget = {
  email: string; period: LightningPeriod | null;
  orders: EmailBudgetOrder[]; pendingCount: number; countedTotalUsd: number;
};

export function parseEmailBudget(details: unknown): EmailBudget | null {
  const shop = record(record(details).shop);
  if (!Object.keys(shop).length) return null;
  const orders: EmailBudgetOrder[] = [];
  if (Array.isArray(shop.orders)) {
    for (const entry of shop.orders) {
      const o = record(entry);
      if (typeof o.quote_id !== 'string' || !o.quote_id) continue;
      orders.push({
        quoteId: o.quote_id, productId: typeof o.product_id === 'string' ? o.product_id : '',
        country: typeof o.country === 'string' ? o.country : '', denomination: typeof o.denomination === 'string' ? o.denomination : '',
        productValue: Number(o.product_value) || 0, productCurrency: typeof o.product_currency === 'string' ? o.product_currency : '',
        quantity: Number(o.quantity) || 1, status: typeof o.status === 'string' ? o.status : '',
        createdAt: typeof o.created_at === 'string' ? o.created_at : '', paymentExpiry: typeof o.payment_expiry === 'string' ? o.payment_expiry : '',
        totalUsd: Number(o.total_usd) || 0, pending: o.pending === true,
      });
    }
  }
  return {
    email: typeof shop.email === 'string' ? shop.email : '',
    period: lightningLimitPeriodOf(shop.period === 'per_order' ? 'LIGHTNING_ORDER_LIMIT_EXCEEDED' : shop.period === 'daily' ? 'LIGHTNING_DAILY_LIMIT_EXCEEDED' : shop.period === 'monthly' ? 'LIGHTNING_MONTHLY_LIMIT_EXCEEDED' : ''),
    orders, pendingCount: Number(shop.pending_count) || orders.filter(o => o.pending).length,
    countedTotalUsd: Number(shop.counted_total_usd) || 0,
  };
}

/** Face label of a counted order ("$104", "2 × €50"). */
function orderFace(o: EmailBudgetOrder): string {
  const unit = o.productValue > 0 && o.productCurrency ? fmtMoney(o.productValue, o.productCurrency) : o.denomination;
  return o.quantity > 1 ? `${o.quantity} × ${unit}` : unit;
}

/** The sentence about the buyer's own pending order (or counted orders),
 *  when the shop knows of any. */
export function emailBudgetSentence(details: unknown): string | null {
  const budget = parseEmailBudget(details);
  if (!budget || !budget.orders.length) return null;
  const pending = budget.orders.find(o => o.pending);
  if (pending) {
    return tr('supplier.lightningPendingOrder', {
      name: pending.productId || tr('supplier.lightningLimitEmailFallback'),
      amount: orderFace(pending),
      total: fmtMoney(pending.totalUsd, 'USD'),
      time: pending.paymentExpiry ? fmtClock(pending.paymentExpiry) : '—',
    });
  }
  return tr('supplier.lightningCountedOrders', { count: budget.orders.length });
}

/** Buyer-facing sentence with the REAL numbers ("daily limit reached for
 *  x@y: €100 per day, €97.70 already counted, this order would add
 *  €97.70"), the buyer's own pending order when the shop knows it, and the
 *  tiers; null when the payload carries no numbers — the caller then falls
 *  back to the catalogue sentence for the code. */
export function lightningLimitMessage(details: unknown, period?: LightningPeriod | null): string | null {
  const l = parseLightningLimit(details, period);
  if (!l) return null;
  const f = (m: Money | null) => (m ? fmtMoney(m.amount, m.currency) : '');
  const total = f(l.orderTotal);
  const email = parseEmailBudget(details)?.email || tr('supplier.lightningLimitEmailFallback');
  let lead = '';
  if (l.exceeded === 'per_order' && l.perOrder) lead = tr('supplier.lightningLimitPerOrder', { limit: f(l.perOrder), total });
  else if (l.exceeded === 'daily' && l.daily) lead = tr('supplier.lightningLimitDaily', { email, limit: f(l.daily), spent: f(l.spentToday) || fmtMoney(0, l.daily.currency), total });
  else if (l.exceeded === 'monthly' && l.monthly) lead = tr('supplier.lightningLimitMonthly', { email, limit: f(l.monthly), spent: f(l.spentThisMonth) || fmtMoney(0, l.monthly.currency), total });
  else lead = tr(period === 'daily' ? 'supplier.LIGHTNING_DAILY_LIMIT_EXCEEDED' : period === 'monthly' ? 'supplier.LIGHTNING_MONTHLY_LIMIT_EXCEEDED' : 'supplier.LIGHTNING_ORDER_LIMIT_EXCEEDED');
  const parts = [lead];
  const own = emailBudgetSentence(details);
  if (own && l.exceeded !== 'per_order') parts.push(own);
  const tiers: string[] = [];
  if (l.perOrder) tiers.push(tr('supplier.lightningTierPerOrder', { amount: f(l.perOrder) }));
  if (l.daily) tiers.push(tr('supplier.lightningTierDaily', { amount: f(l.daily) }));
  if (l.monthly) tiers.push(tr('supplier.lightningTierMonthly', { amount: f(l.monthly) }));
  if (tiers.length) parts.push(tr('supplier.lightningTiers', { tiers: tiers.join(' · ') }));
  // One line per thought: the refusal, the buyer's own order, the tiers.
  return parts.join('\n');
}
