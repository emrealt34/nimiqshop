import { familyAvailability } from '../src/lib/catalogAvailability.ts';

// v1 only proved product presence, even when the family was sold out.
export const STOCK_VERSION = 2;
export const RECHECK_AFTER_MS = 60 * 60_000;
export function validStockRecord(rec, now = Date.now()) {
  return rec?.version === STOCK_VERSION && ['ok', 'empty'].includes(rec.state)
    && Number.isFinite(rec.at) && rec.at > 0 && rec.at <= now + 60_000;
}
export function stockState(data) {
  const result = familyAvailability(data);
  return result === 'available' ? 'ok' : result === 'unavailable' ? 'empty' : 'unknown';
}
export function applyStockState(data, cc, state, now = Date.now()) {
  for (const cat of data.categories) {
    for (const b of cat.brands) {
      const rec = state[`${cc}|${b.family}`];
      if (!validStockRecord(rec, now) || now - rec.at >= RECHECK_AFTER_MS) continue;
      // The family endpoint merges supplier brands just like the product
      // page. Its stock result takes precedence over directory-only flags.
      b.is_out_of_stock = rec.state === 'empty';
      b.stock_checked_at = new Date(rec.at).toISOString();
    }
  }
  return data;
}
