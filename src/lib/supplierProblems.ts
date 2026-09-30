import catalog from '../../backend/internal/cryptorefills/problem_catalog.json';
import { t as tr } from '../i18n';

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
    result.push({ code, moreDetails: details, scope: info?.scope || 'unknown', message: info ? tr(`supplier.${canonical}`) : unknownMessage() });
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
