import { t as tr } from '../i18n';
/**
 * giftNote.ts — ONE rule for the "Send as gift" note, used by every delivery
 * step (EmailStep · TopUpStep · MixedStep) instead of three copies of the same
 * ifs that drifted apart.
 *
 * THE RULE (the whole point of this file)
 * --------------------------------------
 * A gift NOTE is a separate delivery from the PRODUCT, and it has exactly one
 * carrier: an EMAIL.
 *
 *   · the note is the buyer's own text, formatted, with the item and the
 *     reference in it — a 160-character SMS cannot hold that, and cutting it to
 *     one segment was the thing customers complained about;
 *   · the note must never contain the code, PIN or QR: those go to the
 *     recipient's inbox (card / eSIM) or straight onto their number (top-up)
 *     from CryptoRefills. So the note only ever explains and personalises.
 *
 * There used to be a channel picker next to this rule (email / SMS / both) and
 * the fields a channel needed. It is gone, and the reason is not taste:
 *
 *   1. every combination was a promise the order could not always keep — an SMS
 *      note needed a number, so a card cart either had to demand one or drop
 *      the note; a top-up cart needed an address for an emailed note. Both
 *      "solutions" produced silent failures, which is what the whole gift
 *      rework is about;
 *   2. a top-up's number is already taken by the product: the credit lands
 *      there, so a "note by SMS to the same number" was a second message with
 *      nothing in it the buyer could not read on their order page;
 *   3. revealing a field under a chosen chip moved the chips (the layout rule at
 *      the top of DeliverySteps). With one channel there is nothing to choose
 *      and nothing to move.
 *
 * WHAT THAT COSTS, SAID OUT LOUD: a cart of only top-ups now REQUIRES a
 * recipient email when the buyer sends it as a gift — the number carries the
 * credit, and the note needs somewhere to live. That is a field more than the
 * old "SMS to the topped-up number" path asked for, and it is deliberate:
 * requiring it is honest, offering a channel we do not send is not.
 * (`validateGiftNote` is where it is enforced; `requireGiftContacts` on the
 * server says the same thing, so an old client cannot skip it.)
 *
 * There is nothing to re-interpret either: the shop has no SMS sender, no
 * stored order carries a texted note, and the API refuses any channel that is
 * not "email".
 */

/** The only channel an order can ask for. */
export const GIFT_CHANNEL = 'email';

/** What the cart's LINES need, independent of the gift note. */
export type ProductRoute = {
  /** At least one line is delivered to an inbox (gift card / eSIM). */
  hasEmail: boolean;
  /** At least one line is credited onto a phone number (top-up). */
  hasTopUp: boolean;
};

/** The email body carries the buyer's text in full. The server caps it at the
 *  same number (giftMessageMax in quote_handlers.go), so the field can use
 *  maxLength instead of truncating or refusing after the fact. */
export const GIFT_EMAIL_MAX = 2000;

export type GiftNoteRules = {
  route: ProductRoute;
  /** The note's address IS the product's delivery address → one email field,
   *  not two. False for a top-up-only cart, which has no emailed line at all
   *  and therefore has to ask for an address. */
  emailShared: boolean;
  /** Whether a gift note can exist for this cart at all. Always true: an
   *  email is the one thing every order can carry. */
  available: boolean;
  /** Label for the address the note goes to, when the step has to show one. */
  emailLabel: string;
  /** Where the note lands, in one line, under the field. */
  noteWhere: string;
  /** The standing promise: the note never contains a code. */
  codeNote: string;
  /** Sentence for the toggle itself, per route. */
  toggleSub: string;
};

/**
 * The rules for a given cart. `hasEmail`/`hasTopUp` come from the cart lines
 * (or from `deliverySummary()` on stored orders) — never from a hardcoded
 * per-screen assumption, which is how a top-up step ended up demanding an
 * email for a note it was going to text.
 */
export function giftNoteRules(route: ProductRoute): GiftNoteRules {
  const hasEmail = !!route.hasEmail;
  const hasTopUp = !!route.hasTopUp;
  return {
    route: { hasEmail, hasTopUp },
    emailShared: hasEmail,
    available: true,
    emailLabel: hasTopUp
      ? tr('gift.emailLabelTopup')
      : tr('gift.emailLabelCode'),
    noteWhere: hasEmail
      ? tr('gift.noteWhereEmail')
      : tr('gift.noteWhereNumber'),
    codeNote: hasEmail
      ? tr('gift.codeNoteEmail')
      : hasTopUp
        ? tr('gift.codeNoteTopup')
        : tr('gift.codeNoteNever'),
    toggleSub: hasEmail
      ? tr('gift.toggleSubEmail')
      : tr('gift.toggleSubTopup'),
  };
}

/* ------------------------------- the note -------------------------------- */

export type GiftNoteInput = {
  /** Is this order a gift at all? Off → every gift field is emptied. */
  on: boolean;
  /** The address the note goes to: the product's own when the route has an
   *  emailed line, otherwise the extra field the step shows. */
  email: string;
  message: string;
  isValidEmail: (v: string) => boolean;
  emailError?: (v: string) => string;
};

export type GiftNoteResult = {
  ok: boolean;
  error: string;
  /** Which field the error belongs to — the step can highlight it. */
  field: 'email' | 'message' | '';
  channel: string;
  email: string;
  message: string;
  /** Non-blocking, e.g. "no personal message was written". */
  warnings: string[];
};

const EMPTY: GiftNoteResult = {
  ok: false,
  error: '',
  field: '',
  channel: '',
  email: '',
  message: '',
  warnings: [],
};

/**
 * The one validator. Every gift path funnels through here, so the three steps
 * can never disagree about what a note needs — and so the API's
 * `requireGiftContacts` has a mirror image instead of a second opinion.
 */
export function validateGiftNote(input: GiftNoteInput): GiftNoteResult {
  const email = String(input.email || '').trim();
  const message = String(input.message || '').trim();

  // Not a gift → blank every gift field, so a value typed and then hidden can
  // never ride along on a self-purchase.
  if (!input.on) return { ...EMPTY, ok: true };

  if (!email) {
    return {
      ...EMPTY,
      error: tr('gift.emailRequired'),
      field: 'email',
      channel: GIFT_CHANNEL,
      message,
    };
  }
  if (!input.isValidEmail(email)) {
    const custom = input.emailError ? input.emailError(email) : '';
    return { ...EMPTY, error: custom || tr('gift.emailInvalid'), field: 'email', channel: GIFT_CHANNEL, message, email };
  }
  if (message.length > GIFT_EMAIL_MAX) {
    return {
      ...EMPTY,
      error: tr('gift.messageTooLong', { count: String(message.length), max: String(GIFT_EMAIL_MAX) }),
      field: 'message',
      channel: GIFT_CHANNEL,
      email,
      message,
    };
  }

  const out: GiftNoteResult = { ok: true, error: '', field: '', channel: GIFT_CHANNEL, email, message, warnings: [] };
  if (!message) out.warnings.push(tr('gift.noMessage'));
  return out;
}

/* ------------------------- request-row payload -------------------------- */

/**
 * The gift fields as they must appear on a quote request row. The cart collects
 * the note ONCE (one toggle, one recipient), and every row of that cart rides
 * with it — the batch is persisted as one order, so one note; the server hoists
 * it off the first row that carries it and clears it from the items.
 *
 * No `gift_recipient_phone` exists any more: the note has one carrier, the
 * email in the order's own Email field, and the shop has no SMS sender.
 */
export function giftRowFields(g: { channel?: unknown; message?: unknown; identicon?: unknown }): {
  gift_channel?: string;
  gift_message?: string;
  gifter_identicon?: string;
} {
  if (String(g?.channel || '').trim().toLowerCase() !== GIFT_CHANNEL) return {};
  const message = String(g?.message || '').trim();
  const identicon = String(g?.identicon || '');
  const out: { gift_channel?: string; gift_message?: string; gifter_identicon?: string } = { gift_channel: GIFT_CHANNEL };
  if (message) out.gift_message = message.slice(0, GIFT_EMAIL_MAX);
  if (identicon) out.gifter_identicon = identicon;
  return out;
}
