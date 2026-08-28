/* quote-pay.js — SHARED quote → payment sheet for EVERY purchase path.
 *
 * product.js, home.js and cart.js used to each render the same payment UI
 * (countdown, "You get", NIM amount, Lightning pay block) with subtly
 * different copies, and cart had its own webhook-poll loop. This module is
 * the one source of truth:
 *
 *   createQuoteStep(body, req)          spinner + createQuote + error toast
 *   renderQuotePayment(body, quote)     the payment sheet itself
 *   waitForLightningPayment(body, ...)  poll a quote until it fulfills
 */
import { el, icon, replaceChildren, clear, fmtNum, quoteFaceValue, payCountdown } from './util.js';
import { toast, alertBox, lightningPayBlock } from './ui.js';
import { createQuote, getQuote } from './api.js';
import { getAddress } from './session.js';
import { rememberLightningPayment, lightningPaymentURI } from './hub.js';
import { nimIcon, nimAmountNode } from './nim.js';

/* Show the "Getting a live NIM quote…" spinner, create the quote, toast on
 * failure. Returns the quote or null (caller closes the sheet). */
export async function createQuoteStep(body, req) {
  replaceChildren(body, el('div.center.mt-2', {}, el('div.spinner', { style: { width: '28px', height: '28px', margin: '0 auto 10px' } }), el('div.strong', { text: 'Getting a live NIM quote…' })));
  try {
    return await createQuote(req);
  } catch (err) {
    toast(err.message || 'Could not create quote', 'error');
    return null;
  }
}

/* Render the payment sheet for a quote: countdown, "You get" (local face
 * value), the NIM amount, the Lightning pay block and the trust note.
 * opts.qtyNote / opts.footerNote append optional alert nodes. Returns true
 * when a payable invoice was rendered, false on invalid invoice. */
export function renderQuotePayment(body, quote, opts = {}) {
  replaceChildren(body);
  const invoice = quote.lightning_invoice || quote.cryptorefills_payment_request || '';
  let paymentURI;
  try { paymentURI = lightningPaymentURI(invoice); } catch (e) { body.appendChild(alertBox('error', e.message || 'Invalid invoice')); return false; }
  // "You get": the local-currency face value chosen at checkout — the buyer
  // never sees a BTC figure anywhere.
  const { value: selVal, currency: selCcy } = quoteFaceValue(quote);
  const youGet = (selVal > 0 && selCcy) ? `${fmtNum(selVal)} ${selCcy}` : '';
  const qty = Number(quote.quantity) || 1;
  const countdown = el('div.strong', { style: { fontSize: '1.02rem' } });
  const pay = lightningPayBlock({
    invoice,
    uri: paymentURI,
    onLaunch: () => rememberLightningPayment(invoice, { kind: 'quote', ref: quote.quote_id }),
    avatarAddress: getAddress(), /* QR-AVATAR FIX: cart & order flows passed the payer identicon, direct-buy did not */
  });
  payCountdown(countdown, quote.payment_expires_at, () => {
    pay.link.style.opacity = '0.45';
    pay.link.removeAttribute('href');
    pay.link.title = 'Payment window expired';
  });
  body.append(
    countdown,
    // NULL-TEXT FIX: native .append(null) renders a literal "null" text
    // node — spread conditionals instead of passing null children.
    ...(opts.qtyNote ? [opts.qtyNote] : []),
    el('div.row.between.mb-1', { style: { flexWrap: 'nowrap' } },
      el('div', {}, el('div.xs.faint', { text: 'Pay directly to CryptoRefills' }), el('div.big-nim', {}, nimIcon(16), nimAmountNode(quote, { fallback: 'amount in Nimiq Pay' }))),
      el('div', { style: { textAlign: 'right' } }, el('div.xs.faint', { text: 'You get' }), el('div.strong', { text: youGet || 'Instant delivery' })),
    ),
    el('div.alert.info.mt-1', { style: { marginBottom: 0 } }, icon('bolt', 18), el('div.small', { text: 'Nimiq Pay converts your NIM and pays CryptoRefills directly. nim.shop never holds funds.' })),
    pay.wrap,
    ...(opts.footerNote ? [opts.footerNote] : []),
  );
  return true;
}

/* Show a quote's payment sheet and poll the backend until it fulfills.
 * Resolves true on fulfilled, false on failed/expired/user-backout. Used by
 * the cart's sequential multi-quote checkout. */
export function waitForLightningPayment(body, quote, name) {
  return new Promise((resolve) => {
    const invoice = quote.lightning_invoice || quote.cryptorefills_payment_request || '';
    let uri;
    try { uri = rememberLightningPayment(invoice, { kind: 'quote', ref: quote.quote_id }); }
    catch (e) { toast(`Invalid Lightning invoice for ${name}: ${e.message}`, 'error'); resolve(false); return; }
    let attempts = 0;
    let timer;
    const status = el('div.strong', { text: `Pay ${name} directly to CryptoRefills` });
    const sub = el('div.small.muted.mt-1', { text: 'Nimiq Pay converts your NIM and pays CryptoRefills directly. Waiting for webhook…' });
    const check = async () => {
      attempts++;
      try {
        const result = await getQuote(quote.quote_id);
        const q = result.quote || result;
        if (q.status === 'fulfilled') { clearTimeout(timer); resolve(true); return; }
        if (['failed', 'manual_review', 'expired'].includes(String(q.status))) { clearTimeout(timer); resolve(false); return; }
      } catch {}
      if (attempts >= 90) { toast(`Payment for ${name} still processing; check Orders.`, 'info'); resolve(false); return; }
      timer = setTimeout(check, 4000);
    };
    const pay = lightningPayBlock({
      invoice,
      uri,
      onLaunch: () => { sub.textContent = 'Wallet opened — waiting for supplier webhook…'; setTimeout(check, 1000); },
      avatarAddress: getAddress(),
    });
    const countdown = el('div.strong', { style: { fontSize: '1.02rem' } });
    payCountdown(countdown, quote.payment_expires_at, () => {
      pay.link.style.opacity = '0.45';
      pay.link.removeAttribute('href');
      toast('Payment window expired — the order was not paid in time.', 'error');
      resolve(false);
    });
    clear(body);
    body.append(el('div.center', { style: { padding: '30px 10px' } }, status, sub, countdown,
      el('div', {}, pay.wrap),
      el('div.card.mt-2', { style: { textAlign: 'left' } }, el('div.xs.faint', { text: 'Lightning invoice — paid in NIM' }), el('div.mono.small', { text: invoice, style: { wordBreak: 'break-all' } })),
    ));
  });
}
