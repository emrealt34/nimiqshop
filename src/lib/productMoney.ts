/** Structured supplier values only. Names, denominations and IDs are opaque. */
export function positiveAmount(raw: unknown): number {
  if (typeof raw !== 'number' && typeof raw !== 'string') return 0;
  if (typeof raw === 'string' && !/^[+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(raw.trim())) return 0;
  const value = Number(raw);
  return Number.isFinite(value) && value > 0 && value < 100_000_000 ? value : 0;
}

export function productMoney(p: any): { value: number; currency: string } {
  let currency = String(p?.currency_code || '').trim().toUpperCase();
  const amount = positiveAmount(p?.amount);
  if (amount) return { value: amount, currency };
  let fv = p?.face_value;
  if (typeof fv === 'string') {
    try { fv = JSON.parse(fv); } catch { return { value: 0, currency }; }
  }
  if (fv && typeof fv === 'object') {
    currency = String(fv.currency_code || currency).trim().toUpperCase();
    fv = fv.amount;
    if (fv && typeof fv === 'object') fv = fv.value ?? fv.price;
  }
  return { value: positiveAmount(fv), currency };
}

/** Fixed SKUs are ordered by the exact denomination, never a guessed price. */
export function selectionPayload(denomination: string, value: unknown) {
  return String(denomination).trim().toLowerCase() === 'range'
    ? { denomination: 'range', product_value: positiveAmount(value) }
    : { denomination };
}
