/**
 * orderTrack.ts — ONE source of truth for quote/order lifecycle stages.
 * Ported from order-track.js. quoteStages maps every backend status to the
 * 4-stage picture; stageTimeline (in uiKit) renders the English copy.
 */

export type Stage = { id: string; status: string; timestamp?: string | null };

export function quoteStages(q: any): Stage[] {
  const st = String(q.status || '');
  const mk = (id: string, status: string, ts?: string | null): Stage => ({ id, status, timestamp: ts ?? null });
  const created = q.created_at;
  const updated = q.updated_at || created;

  const s1 = mk('order_placed', 'completed', created);
  let s2: Stage = mk('payment_settled', 'pending');
  let s3: Stage = mk('supplier_processing', 'pending');
  let s4: Stage = mk('delivery_complete', 'pending');

  if (st === 'quoted') s2 = { ...s2, status: 'in_progress' };
  else if (st === 'order_creating') s2 = mk('payment_settled', 'in_progress', updated);
  else if (st === 'awaiting_payment') s2 = mk('payment_settled', 'in_progress', updated);
  else if (st === 'nim_payment_submitted' || st === 'payment_started') s2 = mk('payment_settled', 'in_progress', updated);
  else if (st === 'nim_confirmed' || st === 'payment_received') { s2 = mk('payment_settled', 'completed', updated); s3 = mk('supplier_processing', 'in_progress'); }
  else if (st === 'processing' || st === 'supplier_processing') { s2 = mk('payment_settled', 'completed', updated); s3 = mk('supplier_processing', 'in_progress', updated); }
  else if (st === 'lightning_invoice_created') s2 = mk('payment_settled', 'in_progress', updated);
  else if (['supplier_invoice_created', 'polygon_tx_submitted', 'polygon_confirmed', 'delivering'].includes(st)) { s2 = mk('payment_settled', 'completed', updated); s3 = mk('supplier_processing', 'in_progress', updated); }
  else if (st === 'fulfilled') { s2 = mk('payment_settled', 'completed', updated); s3 = mk('supplier_processing', 'completed', updated); s4 = mk('delivery_complete', 'completed', updated); }
  else if (st === 'failed_supplier' || st === 'refunding') { s2 = mk('payment_settled', 'completed', updated); s3 = mk('supplier_processing', 'failed', updated); s4 = { ...s4, status: 'failed' }; }
  else if (st === 'refunded') { s2 = mk('payment_settled', 'completed', updated); s3 = mk('supplier_processing', 'failed', updated); s4 = mk('delivery_complete', 'failed', updated); }
  else if (st === 'expired') { s2 = { ...s2, status: 'failed' }; s3 = { ...s3, status: 'failed' }; s4 = { ...s4, status: 'failed' }; }
  else if (st === 'manual_review') { s3 = { ...s3, status: 'failed' }; s4 = { ...s4, status: 'failed' }; }
  else if (st === 'failed') { s2 = { ...s2, status: 'failed' }; s3 = { ...s3, status: 'failed' }; s4 = { ...s4, status: 'failed' }; }

  return [s1, s2, s3, s4];
}

export function isTerminalStatus(s?: string | null): boolean {
  return ['delivered', 'complete', 'fulfilled', 'failed', 'refunded', 'expired', 'denied', 'blocked'].includes(String(s).toLowerCase());
}
export function isDeliveredStatus(s?: string | null): boolean {
  return ['delivered', 'complete', 'fulfilled'].includes(String(s).toLowerCase());
}
export function isIssueStatus(s?: string | null): boolean {
  return ['failed', 'refunded', 'expired', 'denied', 'blocked', 'payment_error', 'manual_review', 'failed_supplier', 'refunding'].includes(String(s).toLowerCase());
}

/* ---------------- Rate-your-delivery popup ---------------- */

/** How long after fulfillment the rate popup still fires on the order page. */
export const RATE_POPUP_FRESH_MS = 15 * 60_000;

/** localStorage key under which "Rate later" is remembered per order/quote. */
export function ratingDismissedKey(kind: 'quote' | 'order', id: string): string {
  return `nimshop.rate-later:${kind}:${id}`;
}

/**
 * Should the "Rate your delivery" popup open for this row?
 *
 * Fires ONCE for a FRESH delivery: the status just became delivered-ish
 * (live tracking poll or the buyer opened the order right after), the buyer
 * has not rated yet, and they did not press "Rate later" before (checked by
 * the caller through the dismissed callback). Old delivered orders from
 * history never nag — their rating lives on the order page card and the
 * Orders page row instead.
 */
export function shouldAskRating(
  row: any,
  kind: 'quote' | 'order',
  opts: { now?: number; isDismissed?: (key: string) => boolean } = {}
): boolean {
  const id = String(row?.id || row?.quote_id || '');
  if (!id) return false;
  if (!isDeliveredStatus(row?.status)) return false;
  if (Number(row?.rating) > 0) return false;
  if (opts.isDismissed?.(ratingDismissedKey(kind, id))) return false;
  const updated = Date.parse(row?.updated_at || row?.created_at || '');
  if (!Number.isFinite(updated)) return false;
  return (opts.now ?? Date.now()) - updated <= RATE_POPUP_FRESH_MS;
}
