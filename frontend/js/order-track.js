/* order-track.js — ONE source of truth for quote/order lifecycle stages.
 *
 * orders.js and order.js used to each build the same 4-stage model
 * (order_placed → payment_settled → supplier_processing → delivery_complete)
 * with slightly different wording. Now the stage model (id/status/timestamp
 * only) lives here and the English copy lives once in ui.js's STAGE_COPY,
 * which stageTimeline() renders — exactly one text source, zero drift.
 */
/* Client-side stage model for direct CryptoRefills-Lightning purchases.
 * Maps every backend status to the 4-stage picture; each stage carries only
 * id + status + timestamp — the COPY lives once in ui.js's STAGE_COPY
 * (stageTimeline renders it), so there is exactly one English text source. */
export function quoteStages(q) {
  const st = String(q.status || '');
  const mk = (id, status, ts) => ({ id, status, timestamp: ts });
  const created = q.created_at;
  const updated = q.updated_at || created;

  const s1 = mk('order_placed', 'completed', created);
  let s2 = mk('payment_settled', 'pending');
  let s3 = mk('supplier_processing', 'pending');
  let s4 = mk('delivery_complete', 'pending');

  if (st === 'quoted') { s2.status = 'in_progress'; }
  else if (st === 'order_creating') { s2.status = 'in_progress'; s2.timestamp = updated; }
  else if (st === 'awaiting_payment') { s2.status = 'in_progress'; s2.timestamp = updated; }
  else if (st === 'nim_payment_submitted' || st === 'payment_started') { s2.status = 'in_progress'; s2.timestamp = updated; }
  else if (st === 'nim_confirmed' || st === 'payment_received') { s2.status = 'completed'; s2.timestamp = updated; s3.status = 'in_progress'; }
  else if (st === 'lightning_invoice_created') { s2.status = 'in_progress'; s2.timestamp = updated; }
  else if (['supplier_invoice_created', 'polygon_tx_submitted', 'polygon_confirmed', 'delivering'].includes(st)) { s2.status = 'completed'; s3.status = 'in_progress'; s3.timestamp = updated; }
  else if (st === 'fulfilled') { s2.status = 'completed'; s3.status = 'completed'; s4.status = 'completed'; s4.timestamp = updated; }
  else if (st === 'failed_supplier' || st === 'refunding') { s2.status = 'completed'; s2.timestamp = updated; s3.status = 'failed'; s3.timestamp = updated; s4.status = 'failed'; }
  else if (st === 'refunded') { s2.status = 'completed'; s2.timestamp = updated; s3.status = 'failed'; s3.timestamp = updated; s4.status = 'failed'; s4.timestamp = updated; }
  else if (st === 'expired') { s2.status = 'failed'; s3.status = 'failed'; s4.status = 'failed'; }
  else if (st === 'manual_review') { s3.status = 'failed'; s4.status = 'failed'; }
  else if (st === 'failed') { s2.status = 'failed'; s3.status = 'failed'; s4.status = 'failed'; }

  return [s1, s2, s3, s4];
}

/* ---------------- status classifiers (single source) ---------------- */

export function isTerminalStatus(s) {
  return ['delivered', 'complete', 'fulfilled', 'failed', 'refunded', 'expired', 'denied', 'blocked'].includes(String(s).toLowerCase());
}
export function isDeliveredStatus(s) {
  return ['delivered', 'complete', 'fulfilled'].includes(String(s).toLowerCase());
}
export function isIssueStatus(s) {
  return ['failed', 'refunded', 'expired', 'denied', 'blocked', 'payment_error', 'manual_review', 'failed_supplier', 'refunding'].includes(String(s).toLowerCase());
}
