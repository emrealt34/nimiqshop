import { needsPhone } from './catalog';
import { selectionPayload } from './productMoney';
/**
 * delivery.ts — shared checkout delivery logic (ported from delivery.js).
 * Pure parts: live phone validation + the /api/quotes payload builder. The
 * interactive email/phone "steps" are React components (see components/checkout).
 *
 * Gift extras (channel / message) are carried in MEMORY only — never
 * persisted, never stored on this device. The channel is always "email":
 * the shop has no SMS sender.
 */
import { normalizePhone } from './validate';
import { checkPhone } from './api';
import { currentCashbackCode } from './cashbackCode';
import { t as tr } from '../i18n';

let memGiftChannel = '';
let memGiftMsg = '';
let memGiftIdenticon = '';
/** The buyer's own identicon, prepared at checkout for EVERY order so the
 *  order mail can show the wallet that paid, gift or not. Kept apart from the
 *  gift extras, which are cleared whenever the gift option is switched off. */
let buyerIdenticon = '';
export function setBuyerIdenticon(png: string) {
  buyerIdenticon = png;
}

export function setGiftExtras(channel: string, message: string) {
  memGiftChannel = channel;
  memGiftMsg = message;
}
/** The buyer's identicon PNG (see lib/identicon.ts), pre-rasterized for the
 *  gift email; set whenever a named gift note is on. */
export function setGiftIdenticon(png: string) {
  memGiftIdenticon = png;
}
export function clearGiftExtras() {
  memGiftChannel = '';
  memGiftMsg = '';
  memGiftIdenticon = '';
}
export function getGiftExtras() {
  return { channel: memGiftChannel, message: memGiftMsg, identicon: memGiftIdenticon };
}

/** Normalize + live-supplier validation of a phone number. */
export async function collectPhoneValue(raw: unknown, country?: string): Promise<{ ok: boolean; phone?: string; error?: string }> {
  const local = normalizePhone(raw, country);
  if (local.error) return { ok: false, error: local.error };
  try {
    const res = await checkPhone(local.phone || '', country);
    return { ok: true, phone: res && res.phone_number ? res.phone_number : local.phone || undefined };
  } catch (e) {
    return { ok: false, error: (e as Error).message || tr('delivery.phoneRejected') };
  }
}

/** Build the /api/quotes request for ONE item from shared delivery info. */
export function buildOrderRequest(
  item: any,
  { email, phone, gift = false, paymentMethod, cashbackDestination, anonymous = false }: { email?: string; phone?: string; gift?: boolean; paymentMethod?: string; cashbackDestination?: string; anonymous?: boolean } = {}
): Record<string, unknown> {
  const isTopUp = needsPhone(item);
  const ch = memGiftChannel;
  const gMsg = memGiftMsg;
  const req: Record<string, unknown> = {
    product_id: item.id,
    quantity: item.qty || 1,
    country: item.country,
    email: '',
    ...selectionPayload(item.denomination || '', item.value),
    payment_method: paymentMethod || 'nimiq_pay',
    cashback_destination: cashbackDestination || 'cashback',
  };
  // Carry originating supplier brand when one family fans out to multiple brands (e.g. Turk Telecom).
  // The backend prefers this exact brand if provided, otherwise resolves via denomination->brand map.
  if (item.brand) req.brand = item.brand;
  if (item.brand_id) req.brand_id = item.brand_id;
  if (item.category) req.category = item.category;
  if (anonymous) req.anonymous = true;
  const cashbackCode = currentCashbackCode();
  if (cashbackCode) req.cashback_code = cashbackCode;
  if (gift && ch) {
    req.gift_channel = ch;
    if (gMsg) req.gift_message = gMsg;
  }
  // The buyer's identicon for the order mail: gift or plain purchase alike.
  // Never sent on an anonymous order (the mail must not identify the buyer).
  const avatar = memGiftIdenticon || buyerIdenticon;
  if (avatar && !anonymous) req.gifter_identicon = avatar;
  if (isTopUp) {
    req.phone_number = phone || '';
    if (gift && ch && (email || '').trim()) {
      req.email = (email || '').trim();
    }
  } else {
    req.email = (email || '').trim();
  }
  return req;
}

export type DeliveryInfo = {
  email: string;
  phones: Map<unknown, string>;
  paymentMethod?: 'nimiq_pay' | 'usdt_polygon';
  cashbackDestination?: 'cashback' | 'burn';
  anonymous?: boolean;
};
