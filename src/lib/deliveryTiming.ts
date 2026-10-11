/** Immutable lifecycle times. A rating/email update cannot extend a delivery. */
function timestamp(raw: unknown): number | null {
  if (typeof raw !== 'string' || !raw) return null;
  const time = Date.parse(raw);
  return Number.isFinite(time) && time > 0 ? time : null;
}

export function deliveryTimestamp(row: any): string | null {
  if (!row) return null;
  const stage = Array.isArray(row.stages) ? row.stages.find((s: any) => s.id === 'delivery_complete' && s.status === 'completed') : null;
  const candidates = [row.delivered_at, stage?.timestamp];
  if (['fulfilled', 'complete', 'delivered'].includes(row.status)) {
    candidates.push(row.supplier_updated_at, row.gift_notified_at);
    if (!row.rated_at) candidates.push(row.updated_at);
  }
  return candidates.find(value => timestamp(value) !== null) || null;
}

export function deliverySeconds(row: any): number | null {
  if (!['fulfilled', 'complete', 'delivered'].includes(row?.status)) return null;
  const paid = timestamp(row.paid_at || row.lightning_receipt?.confirmed_at);
  const end = timestamp(deliveryTimestamp(row));
  const created = timestamp(row.created_at);
  if (paid === null || end === null || end < paid || (created !== null && paid < created) || end > Date.now()) return null;
  return (end - paid) / 1000;
}

export function purchaseElapsedSeconds(row: any): number | null {
  const end = timestamp(deliveryTimestamp(row));
  const start = timestamp(row?.created_at);
  if (start === null || end === null || end < start || end > Date.now()) return null;
  return (end - start) / 1000;
}
