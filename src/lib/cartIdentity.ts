/** Identity and legacy-cart reconciliation. Supplier labels are opaque: never
 * infer a SKU from a display price or combine two known different brands. */
export type CartIdentity = {
  id: string; country?: string; pkg?: string; denomination?: string;
  value?: number | string; currency?: string; brand?: string; brand_id?: string;
  delivery_type?: string; cartSlot?: number;
};
const text = (s: unknown) => String(s || '').trim();

export function sameCartSelection(a: CartIdentity, b: CartIdentity): boolean {
  if (text(a.id) !== text(b.id) || text(a.country).toUpperCase() !== text(b.country).toUpperCase()) return false;
  const ap = text(a.pkg), bp = text(b.pkg);
  const ad = text(a.denomination), bd = text(b.denomination);
  // Old rebuys lacked pkg. Only bridge that gap with an exact denomination.
  if (ap && bp && ap !== bp) return false;
  if ((!ap || !bp) && (!ad || !bd || ad !== bd)) {
    // Range selections have no package; the exact numeric value is the SKU.
    if (ad.toLowerCase() !== 'range' || bd.toLowerCase() !== 'range') return false;
  }
  if (ad && bd && ad !== bd) return false;
  if (ad.toLowerCase() === 'range' || bd.toLowerCase() === 'range' || (!ad && !bd && !ap && !bp)) {
    if (Number(a.value) !== Number(b.value)) return false;
  }
  if ((!text(a.brand_id) || !text(b.brand_id)) && text(a.brand) && text(b.brand) && text(a.brand).toUpperCase() !== text(b.brand).toUpperCase()) return false;
  for (const k of ['brand_id', 'delivery_type', 'currency'] as const) {
    const x = text(a[k]).toUpperCase(), y = text(b[k]).toUpperCase();
    if (x && y && x !== y) return false;
  }
  return true;
}

export function cartItemKey(it: CartIdentity): string {
  return JSON.stringify([text(it.id), text(it.country).toUpperCase(), text(it.pkg), text(it.denomination),
    text(it.denomination).toLowerCase() === 'range' || (!it.pkg && !it.denomination) ? Number(it.value) || 0 : null,
    text(it.brand_id), text(it.brand), text(it.delivery_type), it.cartSlot || 0]);
}

/** Merge only unambiguous equivalent lines; never lose units over the cap. */
export function reconcileCart<T extends CartIdentity & { qty: number }>(rows: T[], max: number): T[] {
  const result: T[] = [];
  const isAmbiguous = (row: T) => {
    const matches = rows.filter(other => sameCartSelection(other, row));
    return matches.some((a, i) => matches.slice(i + 1).some(b => !sameCartSelection(a, b)));
  };
  for (const row of rows) {
    // Missing metadata can match several different real SKUs. Do not guess,
    // including when the ambiguous old row appears before the specific ones.
    const candidates = result.filter(other => sameCartSelection(other, row) && !isAmbiguous(other));
    const ambiguous = isAmbiguous(row);
    let remaining = row.qty;
    if (!ambiguous) {
      for (const existing of candidates) {
        const count = Math.min(remaining, max - existing.qty);
        if (count <= 0) continue;
        existing.qty += count;
        // Fill only missing identifiers; retain the original line's other data.
        for (const key of ['pkg', 'denomination', 'brand_id', 'brand', 'delivery_type', 'currency'] as const) {
          if (!existing[key] && row[key]) (existing as CartIdentity)[key] = row[key];
        }
        remaining -= count;
        if (!remaining) break;
      }
    }
    if (remaining > 0) result.push({ ...row, qty: remaining, cartSlot: 0 });
  }
  const keys = new Set<string>();
  for (const row of result) {
    row.cartSlot = 0;
    while (keys.has(cartItemKey(row))) row.cartSlot++;
    keys.add(cartItemKey(row));
  }
  return result;
}
