import catalog from '../../backend/internal/cryptorefills/problem_catalog.json';
import { t as tr } from '../i18n';
import { fmtMoney } from './format';

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
    const message = (canonical === 'LIGHTNING_ORDER_LIMIT_EXCEEDED' && lightningLimitMessage(details)) || (info ? tr(`supplier.${canonical}`) : unknownMessage());
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

/* ---- LIGHTNING_ORDER_LIMIT_EXCEEDED ------------------------------------ */

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
  /** Which ceiling the order crossed, when the numbers say so. */
  exceeded: 'per_order' | 'daily' | 'monthly' | null;
};

export function parseLightningLimit(details: unknown): LightningLimit | null {
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
  return out;
}

/** Buyer-facing sentence with the REAL numbers ("at most €100 per order;
 *  this order would be €208.07"), or null when the payload carries none —
 *  the caller then falls back to the generic catalogue sentence. */
export function lightningLimitMessage(details: unknown): string | null {
  const l = parseLightningLimit(details);
  if (!l) return null;
  const f = (m: Money | null) => (m ? fmtMoney(m.amount, m.currency) : '');
  const total = f(l.orderTotal);
  let lead = '';
  if (l.exceeded === 'per_order' && l.perOrder) lead = tr('supplier.lightningLimitPerOrder', { limit: f(l.perOrder), total });
  else if (l.exceeded === 'daily' && l.daily) lead = tr('supplier.lightningLimitDaily', { limit: f(l.daily), spent: f(l.spentToday) || fmtMoney(0, l.daily.currency), total });
  else if (l.exceeded === 'monthly' && l.monthly) lead = tr('supplier.lightningLimitMonthly', { limit: f(l.monthly), spent: f(l.spentThisMonth) || fmtMoney(0, l.monthly.currency), total });
  else lead = tr('supplier.LIGHTNING_ORDER_LIMIT_EXCEEDED');
  const tiers: string[] = [];
  if (l.perOrder) tiers.push(tr('supplier.lightningTierPerOrder', { amount: f(l.perOrder) }));
  if (l.daily) tiers.push(tr('supplier.lightningTierDaily', { amount: f(l.daily) }));
  if (l.monthly) tiers.push(tr('supplier.lightningTierMonthly', { amount: f(l.monthly) }));
  return tiers.length ? `${lead} ${tr('supplier.lightningTiers', { tiers: tiers.join(' · ') })}` : lead;
}
