import { deliveryTimestamp } from './deliveryTiming';
import { isDeliveredStatus } from './orderTrack';
import type { RatingKind } from './api';

export const RATING_REMINDER_COOLDOWN_MS = 7 * 24 * 60 * 60_000;
export type UnratedPurchase = { kind: RatingKind; id: string; name: string };
export const ratingReminderKey = (uid: string, purchase?: UnratedPurchase) =>
  `nimshop.rating-reminder.v2:${uid}${purchase ? `:${purchase.kind}:${purchase.id}` : ''}`;

/** Local confirmation shield: after the API confirms a rating, don't nag for
 * this purchase if a subsequent history read is briefly stale. */
export const ratingReminderRatedKey = (kind: RatingKind, id: string) =>
  `nimshop.rating-reminder.rated.v1:${kind}:${id}`;
// Some older histories expose one supplier purchase under both order/quote
// kinds. Keep a type-free marker too so a stale alternate row cannot nag.
export const ratingReminderRatedIdKey = (id: string) =>
  `nimshop.rating-reminder.rated-id.v1:${id}`;

export function rememberRatedPurchase(kind: RatingKind, id: string): void {
  try {
    localStorage.setItem(ratingReminderRatedKey(kind, id), '1');
    localStorage.setItem(ratingReminderRatedIdKey(id), '1');
  } catch {}
}

export function isPurchaseLocallyRated(kind: RatingKind, id: string): boolean {
  try {
    return localStorage.getItem(ratingReminderRatedKey(kind, id)) === '1'
      || localStorage.getItem(ratingReminderRatedIdKey(id)) === '1';
  } catch { return false; }
}

/** Remind on the next storefront visit after delivery, never during payment.
 * A supplier purchase represented in both histories is reminded only once. */
export function unratedPurchases(orders: unknown, quotes: unknown, now = Date.now()): UnratedPurchase[] {
  const groups = new Map<string, { candidate?: UnratedPurchase; rated: boolean }>();
  for (const [kind, source] of [['quote', quotes], ['order', orders]] as const) {
    if (!Array.isArray(source)) continue;
    for (const row of source) {
      if (!row || typeof row !== 'object') continue;
      const id = String(row.id || row.quote_id || '');
      if (!id || !isDeliveredStatus(row.status)) continue;
      const key = row.supplier_order_id ? `supplier:${row.supplier_order_id}` : `${kind}:${id}`;
      const group = groups.get(key) || { rated: false };
      group.rated ||= Number(row.rating) > 0 || !!row.rating_tx || Date.parse(row.rated_at || '') > 0;
      const delivered = Date.parse(deliveryTimestamp(row) || '');
      // Fulfilled legacy purchases may have no delivery timestamp. Their
      // confirmed delivery status is sufficient; do not invent a timestamp.
      if (!group.candidate && (!Number.isFinite(delivered) || now - delivered >= 0)) {
        const name = row.product_name || row.payload?.product_name || row.product_id || '';
        group.candidate = { kind, id, name: typeof name === 'string' ? name : '' };
      }
      groups.set(key, group);
    }
  }
  return [...groups.values()].flatMap(group => !group.rated && group.candidate ? [group.candidate] : []);
}
