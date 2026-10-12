import { deliveryTimestamp } from './deliveryTiming';
import { isDeliveredStatus } from './orderTrack';
import type { RatingKind } from './api';

export const RATING_REMINDER_COOLDOWN_MS = 7 * 24 * 60 * 60_000;
export type UnratedPurchase = { kind: RatingKind; id: string; name: string };
export const ratingReminderKey = (uid: string) => `nimshop.rating-reminder.v1:${uid}`;

/** Remind on the next storefront visit after delivery, never during payment.
 * A supplier purchase represented in both histories is reminded only once. */
export function unratedPurchases(orders: unknown, quotes: unknown, now = Date.now()): UnratedPurchase[] {
  const groups = new Map<string, { candidate?: UnratedPurchase; rated: boolean }>();
  for (const [kind, source] of [['quote', quotes], ['order', orders]] as const) {
    if (!Array.isArray(source)) continue;
    for (const row of source) {
      if (!row || typeof row !== 'object' || row.test_mode) continue;
      const id = String(row.id || row.quote_id || '');
      if (!id || !isDeliveredStatus(row.status)) continue;
      const key = row.supplier_order_id ? `supplier:${row.supplier_order_id}` : `${kind}:${id}`;
      const group = groups.get(key) || { rated: false };
      group.rated ||= Number(row.rating) > 0 || !!row.rating_tx || !!row.rated_at;
      const delivered = Date.parse(deliveryTimestamp(row) || '');
      if (!group.candidate && Number.isFinite(delivered) && now - delivered >= 0) {
        const name = row.product_name || row.payload?.product_name || row.product_id || '';
        group.candidate = { kind, id, name: typeof name === 'string' ? name : '' };
      }
      groups.set(key, group);
    }
  }
  return [...groups.values()].flatMap(group => !group.rated && group.candidate ? [group.candidate] : []);
}
