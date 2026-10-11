/** One stock rule for the hourly snapshot, card prefetch, and product page.
 * An unknown response is never evidence of sold-out inventory. */
export type Availability = 'available' | 'unavailable' | 'unknown';

export function usableProduct(p: any): boolean {
  if (!p || typeof p !== 'object' || p.is_out_of_stock === true) return false;
  if (String(p.delivery_type || '').toLowerCase() === 'by_account') return false;
  if (p.range) {
    return Number(p.range.min) > 0 && Number(p.range.max) >= Number(p.range.min);
  }
  return !!(p.product_id || p.denomination || p.localized_denomination);
}

export function familyAvailability(data: unknown): Availability {
  const families: any[] = Array.isArray(data) ? data : data && typeof data === 'object' ? [data] : [];
  if (!Array.isArray(data) && !families.length) return 'unknown';
  let unknown = false;
  for (const f of families) {
    if (!f || !Array.isArray(f.products)) { unknown = true; continue; }
    if (f.is_out_of_stock !== true && f.products.some(usableProduct)) return 'available';
  }
  return unknown ? 'unknown' : 'unavailable';
}
