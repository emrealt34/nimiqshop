import { siteName } from './config';
import { t as tr } from '../i18n';
/**
 * deliveryCopy.ts — ONE source of truth for two questions the whole app kept
 * answering differently:
 *
 *   1. "How does this order reach the buyer?"  (email / phone / email & phone)
 *   2. "Which rail is being paid?"             (Pay with Nimiq Pay; legacy USDT orders are labelled)
 *
 * WHY THIS FILE EXISTS
 * --------------------
 * A cart may hold gift cards (delivered to an EMAIL) and mobile top-ups
 * (credited to a PHONE NUMBER) at the same time. Screens used to hardcode
 * "your code lands by email", which is:
 *   - wrong for a phone-only cart (there is no code and no email),
 *   - incomplete for a mixed cart (the top-up never touches the inbox).
 *
 * Every surface (checkout pay sheet, orders list, order detail, success beat,
 * redeem steps) now derives its wording from `deliverySummary()` so the same
 * order can never describe itself two different ways.
 *
 * The manifest comes from the SERVER (`quote.lines[]`, see db.QuoteLine) —
 * never re-parsed from a joined label. `linesOf()` falls back to older rows
 * that predate the manifest so history keeps rendering correctly.
 */

export type DeliveryChannel = 'email' | 'phone' | 'both' | 'none';

export type QuoteLine = {
  product_id?: string;
  country?: string;
  denomination?: string;
  product_value?: number;
  product_currency?: string;
  quantity?: number;
  kind?: string;
  delivery_channel?: string;
  delivery_target?: string;
  face_label?: string;
};

export type DeliverySummary = {
  /** Combined channel across every line in the order. */
  channel: DeliveryChannel;
  /** Distinct email destinations (usually one). */
  emails: string[];
  /** Distinct phone destinations (top-up credits). */
  phones: string[];
  /** Numbers the PRODUCT is credited to. */
  creditPhones: string[];
  /** Where the gift note itself is emailed, when that is not the delivery inbox. */
  noteEmail: string;
  /** True when at least one line is a mobile top-up. */
  hasTopUp: boolean;
  /** True when at least one line is delivered to an inbox. */
  hasEmail: boolean;
  /** True when at least one line is an eSIM. */
  hasEsim: boolean;
  /** True when a gift note is sent (separate from the product delivery).
   *  The note's only carrier is an email — there is no SMS sender. */
  giftChannel: '' | 'email';
  /** Short label: "Email", "Phone", "Email & phone". */
  label: string;
  /** Full sentence for the pay sheet / order detail. */
  sentence: string;
  /** Imperative "what happens next" line shown before payment. */
  nextLine: string;
  /** Icon name for the sentence. */
  icon: 'mail' | 'phone' | 'message';
};

function clean(v: unknown): string {
  return String(v ?? '').trim();
}

function uniq(list: string[]): string[] {
  return Array.from(new Set(list.filter(Boolean)));
}

function joinList(items: string[]): string {
  const list = items.filter(Boolean);
  if (list.length <= 1) return list[0] || '';
  if (list.length === 2) return `${list[0]} and ${list[1]}`;
  return `${list.slice(0, -1).join(', ')} and ${list[list.length - 1]}`;
}

/** Is this line a mobile top-up (credit onto a phone number)? */
function isTopUpLine(l: QuoteLine): boolean {
  const kind = clean(l.kind).toLowerCase();
  const ch = clean(l.delivery_channel).toLowerCase();
  return kind === 'topup' || kind === 'phone_refill' || kind === 'mobile_recharge' || ch === 'phone';
}

function isEsimLine(l: QuoteLine): boolean {
  const kind = clean(l.kind).toLowerCase();
  return kind === 'esim' || kind === 'e-sim';
}

/**
 * The server-provided per-line manifest, with a defensive fallback for rows
 * created before `lines[]` existed (and for the local cart, which has the same
 * shape under different key names).
 */
export function linesOf(source: any): QuoteLine[] {
  if (!source) return [];
  const raw = source.lines || source.quote?.lines;
  if (Array.isArray(raw) && raw.length) return raw as QuoteLine[];

  // ---- Legacy / local fallbacks ----------------------------------------
  // Local cart items: { type, id, qty, country }
  if (Array.isArray(source) ) {
    return (source as any[]).map((it) => ({
      product_id: clean(it?.id || it?.name),
      country: clean(it?.country),
      quantity: Number(it?.qty) || 1,
      kind: clean(it?.type) === 'phone_refill' ? 'topup' : clean(it?.type) || 'gift_card',
      delivery_channel: clean(it?.type) === 'phone_refill' ? 'phone' : 'email',
      denomination: clean(it?.denomination),
    }));
  }
  // Public tracking payload: no per-line manifest and no targets, only the
  // reduced channel. Synthesise one line so the summary still resolves.
  const pubCh = clean(source.delivery_channel).toLowerCase();
  if (pubCh === 'email' || pubCh === 'phone' || pubCh === 'both') {
    if (pubCh === 'both') {
      return [
        { product_id: clean(source.product_id), quantity: 1, kind: 'gift_card', delivery_channel: 'email' },
        { product_id: clean(source.product_id), quantity: 1, kind: 'topup', delivery_channel: 'phone' },
      ];
    }
    return [
      {
        product_id: clean(source.product_id),
        quantity: Number(source.quantity) || 1,
        kind: pubCh === 'phone' ? 'topup' : 'gift_card',
        delivery_channel: pubCh,
      },
    ];
  }

  // A single legacy quote/order row.
  const phone = clean(source.phone_number);
  const email = clean(source.customer_email || source.email);
  const kindRaw = clean(source.kind || source._kind).toLowerCase();
  const topUp = !!phone || kindRaw === 'topup' || kindRaw === 'mobile_recharge' || kindRaw === 'phone_refill';
  if (!phone && !email) return [];
  return [
    {
      product_id: clean(source.product_id || source.payload?.product_name),
      country: clean(source.product_country || source.country || source.payload?.country),
      quantity: Number(source.quantity) || 1,
      kind: topUp ? 'topup' : kindRaw === 'esim' ? 'esim' : 'gift_card',
      delivery_channel: topUp ? 'phone' : 'email',
      delivery_target: topUp ? phone : email,
      denomination: clean(source.denomination),
    },
  ];
}

/**
 * Build the delivery summary for a quote/order (or a raw line array).
 * `source` may be the quote object, the API envelope, or the local cart.
 */
export function deliverySummary(source: any): DeliverySummary {
  const lines = linesOf(source);
  const root = Array.isArray(source) ? {} : source?.quote || source || {};

  const emails: string[] = [];
  const phones: string[] = [];
  let hasTopUp = false;
  let hasEmail = false;
  let hasEsim = false;

  const creditPhones: string[] = [];

  for (const l of lines) {
    const target = clean(l.delivery_target);
    if (isTopUpLine(l)) {
      hasTopUp = true;
      if (target) {
        phones.push(target);
        creditPhones.push(target);
      }
    } else {
      hasEmail = true;
      if (isEsimLine(l)) hasEsim = true;
      if (target) emails.push(target);
    }
  }

  // Root-level fallbacks: an order can carry the address even when a legacy
  // line lacks the explicit target.
  const rootEmail = clean(root.customer_email || root.email || root.payload?.email);
  const rootPhone = clean(root.phone_number || root.payload?.phone_number);
  if (hasEmail && !emails.length && rootEmail) emails.push(rootEmail);
  if (hasTopUp && !phones.length && rootPhone) phones.push(rootPhone);
  if (!lines.length) {
    if (rootEmail) {
      hasEmail = true;
      emails.push(rootEmail);
    }
    if (rootPhone) {
      hasTopUp = true;
      phones.push(rootPhone);
      creditPhones.push(rootPhone);
    }
  }

  // The gift NOTE is a separate delivery from the product, and it never carries
  // a code. The note has exactly ONE carrier — an email; the shop has no SMS
  // sender, so no number can ever be a note target (see src/lib/giftNote.ts).
  const giftChannelRaw = clean(root.gift_channel).toLowerCase();
  const giftChannel: '' | 'email' = giftChannelRaw === 'email' ? 'email' : '';
  // An emailed note on an order with no emailed lines IS the order's own
  // email — which is why a top-up gift cart has to collect an address in the
  // first place.
  const noteEmail = giftChannel === 'email' && !hasEmail ? clean(emails[0] || rootEmail) : '';

  const emailList = uniq(emails);
  const phoneList = uniq(phones);
  const creditList = uniq(creditPhones);

  // THE RULE: the gift NOTE never changes how the PRODUCT is delivered. An
  // emailed gift card that is also texted to the recipient is still an
  // "Email" order — calling it "Email & phone" made the pay sheet promise a
  // top-up credit that does not exist.
  const channel: DeliveryChannel =
    hasEmail && hasTopUp
      ? 'both'
      : hasEmail
        ? 'email'
        : hasTopUp || creditList.length
          ? 'phone'
          : 'none';

  const label =
    channel === 'both' ? 'Email & phone' : channel === 'email' ? 'Email' : channel === 'phone' ? 'Phone' : '—';

  const icon: DeliverySummary['icon'] = channel === 'phone' ? 'phone' : channel === 'both' ? 'message' : 'mail';

  // ---- Sentences --------------------------------------------------------
  const emailPart = emailList.length ? joinList(emailList) : '';
  // Only numbers that actually receive product value may be named as the
  // top-up target; a note-only number would read as if money went there.
  const phonePart = creditList.length ? joinList(creditList) : phoneList.length ? joinList(phoneList) : '';

  let sentence = '';
  let nextLine = '';
  if (channel === 'both') {
    sentence = tr('delivery.mixedSentence', {
      email: emailPart || tr('delivery.yourEmail'),
      number: phonePart || tr('delivery.theNumber'),
    });
    nextLine = tr('delivery.mixedNext');
  } else if (channel === 'phone') {
    sentence = tr('delivery.phoneSentence', { number: phonePart || tr('delivery.theNumber') });
    nextLine = tr('delivery.phoneNext');
  } else if (channel === 'email') {
    sentence = hasEsim
      ? tr('delivery.esimSentence', { email: emailPart || tr('delivery.yourEmail') })
      : tr('delivery.codeSentence', { email: emailPart || tr('delivery.yourEmail') });
    nextLine = tr('delivery.emailNext');
  } else {
    sentence = tr('delivery.attached');
    nextLine = tr('delivery.attachedNext');
  }

  // The gift note rides on top of the product delivery and must be stated
  // separately so nobody thinks the CODE itself is the note.
  if (giftChannel) {
    const mailTo = noteEmail || (hasEmail && emailList.length ? joinList(emailList) : '');
    const at = (v: string) => (v ? ` (${v})` : '');
    const rider = hasEmail ? tr('delivery.riderCode') : tr('delivery.riderCredit');
    sentence += tr('delivery.giftNoteSuffix', { at: at(mailTo), rider });
  }

  return {
    channel,
    emails: emailList,
    phones: phoneList,
    creditPhones: creditList,
    noteEmail,
    hasTopUp,
    hasEmail,
    hasEsim,
    giftChannel,
    label,
    sentence,
    nextLine,
    icon,
  };
}

/* ============================ payment rail ============================== */

export type PayRail = {
  /** 'usdt' | 'nim' */
  id: 'usdt' | 'nim';
  /** "USDT · Polygon" / "Pay with Nimiq Pay" */
  label: string;
  /** Short chip text used next to amounts. */
  short: string;
  /** One-line explanation of where the money goes. */
  note: string;
  /** True when the buyer pays USDT on Polygon. */
  isUsdt: boolean;
};

/**
 * Which rail does this quote/order use? One detector for every screen.
 * New orders are always Nimiq Pay. The USDT branch only labels legacy orders
 * that were created before the USDT rail was removed (2026-10-10).
 */
export function payRail(q: any): PayRail {
  const method = String(q?.payment_method || q?.quote?.payment_method || '').toLowerCase();
  const coin = String(q?.coin || q?.quote?.coin || '').toUpperCase();
  const isUsdt = method === 'usdt_polygon' || coin === 'USDT';
  if (isUsdt) {
    return {
      id: 'usdt',
      // Rail name is a proper noun — no locale key needed (the USDT rail
      // is legacy now; only historical orders render this label).
      label: 'USDT · Polygon',
      short: 'USDT',
      note: tr('delivery.usdtNote', { site: siteName() }),
      isUsdt: true,
    };
  }
  return {
    id: 'nim',
    label: tr('checkout.flowPayWithNim'),
    short: tr('checkout.flowMethodNim'),
    note: tr('delivery.nimNote', { site: siteName() }),
    isUsdt: false,
  };
}

/** The Next line: one short "waiting for your payment" sentence. The rail
 *  verb and the delivery narration lived here before and read as instructions
 *  stacked on instructions; the pay button already says how to pay. */
export function payActionLine(q: any, summary?: DeliverySummary): string {
  const del = summary || deliverySummary(q);
  return del.nextLine;
}

/**
 * Stablecoin amount for a quote, as a display string.
 *
 * The backend fills `coin_amount` only once the rail has locked a price; until
 * then the figure lives in `validated_coin_amount`. Reading just `coin_amount`
 * left the pay sheet showing a bare coin with no number, which is exactly the
 * field a payer must copy — so fall back through both, then trim the trailing
 * zeros the fixed-point column carries.
 */
export function coinAmountOf(q: any, decimals = 6): string {
  const raw = q?.coin_amount ?? q?.validated_coin_amount ?? '';
  const n = Number(raw);
  if (!raw || !Number.isFinite(n) || n <= 0) return '';
  // USDT is 6-decimal; BTC needs the FULL satoshi precision (8 dp) or the
  // recap rounds the Lightning invoice away (owner, 2026-10-05). Trailing
  // zeros drop, value stays exact.
  return String(Number(n.toFixed(decimals)));
}

/** "12.34 USDT", or '' when unknown. */
export function coinAmountLabel(q: any): string {
  return coinAmountLabelFor(q, 'USDT');
}

/** The coin column is USDT on the stablecoin rail but the supplier's BTC
 *  invoice amount on the Lightning rail — labelling the latter "USDT" read as
 *  a wrong currency on the order recap (owner, 2026-10-04). */
export function coinAmountLabelFor(q: any, unit: 'USDT' | 'BTC'): string {
  const a = coinAmountOf(q, unit === 'BTC' ? 8 : 6);
  return a ? `${a} ${unit}` : '';
}

/** Payment-screen helpers (moved here from the removed SimulatedPayScreen so
 * the REAL pay screen owns all of its wording). */
import { quoteFaceValue } from './format';

/** Delivery wording is derived from the server's per-line manifest, so a
 * mixed cart says "email & phone" instead of claiming everything arrives in
 * an inbox. */
export function deliveryLine(quote: any): { icon: string; text: string } | null {
  const summary = deliverySummary(quote);
  if (summary.channel === 'none') return null;
  return { icon: summary.icon, text: summary.sentence };
}

export function youGetText(quote: any): string {
  const batchTotals = Array.isArray(quote.face_value_totals) ? quote.face_value_totals.filter(Boolean) : [];
  if (batchTotals.length) return batchTotals.join(' + ');
  const { value: selVal, currency: selCcy } = quoteFaceValue(quote);
  return selVal > 0 && selCcy ? `${selVal} ${selCcy}` : '';
}
