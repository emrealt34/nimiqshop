/* pages/order.js — single purchase detail: live tracking timeline,
 * delivery contents (code/PIN/link), summary, inline support thread.
 * Handles legacy order rows and direct CryptoRefills-Lightning quotes.
 */
import { bootShell } from '../shell.js';
import { el, icon, $, replaceChildren, fmtUSD, fmtNum, fmtDate, queryParam, shortAddr, countryName, flag, safeHref, pageInterval, copyText, payCountdown, richNode, stripHtml, cleanFamilyName, cleanSupplierTerms, quoteFaceValue } from '../util.js';
import { getOrder, refreshOrder, getOrderSupport, getQuote, createTicket, replyTicket, rateOrder, rateQuote, getProduct } from '../api.js';

import { quoteStages, isTerminalStatus } from '../order-track.js';
import { resolveMetaLogo } from '../catalog.js';
import { nimAmountNode } from '../nim.js';
import { isAuthed, getAddress } from '../session.js';
import { toast, statusBadge, stageTimeline, copyButton, kv, kvCard, lockedSignInCard, emptyState, errorState, skeletonLines, alertBox, kindMeta, starsDisplay, starPicker, lightningPayBlock, chatComposer, chatMessageRow } from '../ui.js';
import { openLogin } from '../shell.js';
import { lightningPaymentURI, rememberLightningPayment } from '../hub.js';
import { inNimiqPay } from '../miniapp.js';

bootShell('orders');

/* Country → local currency lives in ../util.js (COUNTRY_CCY, shared). */

/* ---------------- Real product thumb on the order header ----------------
 * The header used to show a generic category icon (bolt/gift). It now shows
 * the product's OWN photo: payload image first, else the brand logo
 * resolved from the catalog (same source as the shop grid), painted on the
 * brand's original background color.
 */
function orderThumb({ family, country, image = '', bgColor = '', thumbCls, iconName, iconSize = 22 }) {
  const thumb = el('div.thumb.thumb-detail.' + thumbCls, { style: { width: '46px', height: '46px', borderRadius: '14px', display: 'grid', placeItems: 'center' } });
  if (image) {
    replaceChildren(thumb, el('img.product-img', { src: image, alt: family || 'Product', loading: 'lazy', decoding: 'async', style: bgColor ? { background: bgColor } : {} }));
    return thumb;
  }
  thumb.appendChild(icon(iconName, iconSize));
  const clean = cleanFamilyName(family);
  if (clean) resolveMetaLogo(thumb, clean, country, { alt: family });
  return thumb;
}

/* NIM estimates live in ../nim.js (shared, multi-source, retried). */

/* The local-currency amount the buyer SELECTED at checkout (slider range or
 * fixed denomination label) — shared quoteFaceValue in ../util.js. Returns
 * '' when nothing numeric is known. */
function selectedAmountLabel(q) {
  const qty = Number(q.quantity) || 1;
  const { value: perUnit, currency: ccy } = quoteFaceValue(q);
  if (!(perUnit > 0) || !ccy) return '';
  return qty > 1 ? `${fmtNum(perUnit * qty)} ${ccy} (${fmtNum(perUnit)} × ${qty})` : `${fmtNum(perUnit)} ${ccy}`;
}


function giftChannelLabel(ch) {
  return { email: 'ByEmail', sms: 'By SMS', both: 'Email + SMS' }[ch] || String(ch || '');
}

/* ---------------- How to redeem + Terms (on the order itself) -----------
 * The product page shows the supplier's official "How to redeem" / terms
 * BEFORE purchase; the order page now shows the SAME content AFTER purchase
 * so the buyer can redeem straight from the order without hunting down the
 * product page. The rich HTML comes from the family detail's
 * rich_description and is mounted through the same sanitizer as on the
 * product page (richNode → safeRichHTML). Fallback chain per section:
 *   how-to:  delivery instructions → supplier rich how_to_redeem → generic steps
 *   terms:   supplier rich term_and_conditions → cleaned product_tc → hidden
 */

const ORDER_REDEEM_STEPS = {
  gift_card: [
    'Your code (and PIN, if any) is shown in “Your delivery” above and arrives by email.',
    'Open the brand’s website or app and go to gift card / balance redemption.',
    'Enter the code exactly as shown — the full face value is credited to your account.',
  ],
  topup: [
    'The credit is applied automatically to the phone number on the order.',
    'No code to enter — usually lands within minutes.',
    'Check the balance in the operator’s app or via USSD if you are unsure.',
  ],
  esim: [
    'Your eSIM QR / activation details are in “Your delivery” above and in your email.',
    'On your phone: Settings → Cellular / Mobile data → Add eSIM → scan the QR.',
    'Stay on Wi‑Fi while the eSIM downloads and activates.',
  ],
};

// cleanSupplierTerms lives in ../util.js (shared with the product page).
function redeemInfoCard({ family, country, kind, deliveredNote = '' }) {
  const body = el('div');
  const card = el('div.card', {},
    el('div.card-title', {}, icon('gift', 16), el('span', { text: 'How to redeem' })),
    body,
  );

  const genericSteps = ORDER_REDEEM_STEPS[kind] || ORDER_REDEEM_STEPS.gift_card;
  const draw = ({ howTo, geoBanner = null, termsHtml = '', termsText = '' }) => {
    replaceChildren(body,
      geoBanner,
      howTo,
      deliveredNote ? el('div.howto-note.small', {}, icon('info', 15), el('span', { text: deliveredNote })) : null,
      termsHtml ? el('details.howto-terms.mt-1', {},
        el('summary.xs', { text: 'Terms and conditions (from CryptoRefills)' }),
        richNode(termsHtml, 'xs muted rich-html'),
      ) : termsText ? el('details.howto-terms.mt-1', {},
        el('summary.xs', { text: 'Supplier terms (CryptoRefills)' }),
        el('div.xs.muted', { text: termsText }),
      ) : null,
    );
  };

  // Immediate render: generic steps (+ the delivery's own instructions).
  draw({ howTo: el('ol.howto-steps', {}, ...genericSteps.map((st) => el('li', {}, el('span', { text: st })))) });

  // Enrich with the supplier's OWN rich content when the family resolves.
  (async () => {
    try {
      if (!family || !country || String(country).length !== 2) return;
      const data = await getProduct(family, country);
      const fam = Array.isArray(data) ? data[0] : (data && data.products ? data : null);
      if (!fam) return;
      const rich = fam.rich_description || {};
      const howTo = rich.how_to_redeem
        ? richNode(rich.how_to_redeem, 'rich-html howto-rich')
        : el('ol.howto-steps', {}, ...genericSteps.map((st) => el('li', {}, el('span', { text: st }))));
      const geoBanner = rich.redeem_geo
        ? el('div.alert.info.mt-1', { style: { marginBottom: 0 } }, icon('info', 16),
            el('div.small', { text: `${flag(country)} May only be redeemable ${stripHtml(rich.redeem_geo)}.` })) // redeem_geo arrives as HTML — strip for the text banner
        : null;
      draw({
        howTo,
        geoBanner,
        termsHtml: rich.term_and_conditions || '',
        termsText: cleanSupplierTerms(fam.product_tc || ''),
      });
    } catch { /* family unreachable → generic steps stay on the card */ }
  })();

  return card;
}


const id = queryParam('id');
const isQuote = queryParam('type') === 'quote';
const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on
// top of the first (the duplicate locked-card bug).
replaceChildren(main);

main.appendChild(el('div.container', {},
  el('div.row.mb-2', { style: { gap: '10px' } },
    el('a.btn.btn-ghost.btn-sm', { href: '/orders' }, icon('back', 16), el('span.btn-label', { text: 'All orders' })),
    el('button.btn.btn-ghost.btn-sm#refreshBtn', {}, icon('refresh', 16), el('span.btn-label', { text: 'Refresh status' })),
  ),
  el('div#detail', {}, el('div.card', {}, skeletonLines(5))),
));

const detail = $('#detail');
let autoTimer = null;

// terminal → isTerminalStatus in ../order-track.js

async function load(manual = false) {
  if (!id) {
    replaceChildren(detail, errorState('No order id provided.', () => { navigate('/orders'); }));
    return;
  }
  if (!isAuthed()) {
    replaceChildren(detail, lockedSignInCard({
      title: 'Sign in to view this order',
      text: 'Orders are private to the wallet that placed them.',
      onConnect: () => openLogin(() => load()),
    }));
    return;
  }
  try {
    if (isQuote) {
      const res = await getQuote(id);
      renderQuote(res);
    } else {
      const o = manual ? await refreshOrder(id) : await getOrder(id);
      renderOrder(o);
    }
  } catch (err) {
    if (err.status === 404) {
      replaceChildren(detail, emptyState({ iconName: 'search', title: 'Not found', text: 'This order does not exist or belongs to another wallet.' }));
    } else {
      replaceChildren(detail, errorState(err.message, () => load()));
    }
  }
}

$('#refreshBtn').addEventListener('click', () => { toast('Checking latest status…', 'info', 1500); load(true); });

/* ---------------- order rendering ---------------- */

/* One rating card for both purchase kinds. The old code had two near-identical
 * copies — ratingSection (legacy orders, rateOrder) and quoteRatingCard
 * (quotes, rateQuote) — differing only in the API call and the gate. */
function ratingCard({ status, rating, rate }) {
  // Only terminal, successful purchases are rateable — never failures.
  if (!isTerminalStatus(status)) return null;
  if (['failed', 'refunded', 'expired', 'denied', 'blocked'].includes(String(status).toLowerCase())) return null;
  const card = el('div.card', {}, el('div.card-title', { text: 'Rate your purchase' }));
  if (rating && rating > 0) {
    card.appendChild(el('div', {},
      el('div.small.muted.mb-1', { text: 'Your rating (public):' }),
      el('div.row', { style: { gap: '10px', alignItems: 'center' } },
        starsDisplay(rating, { size: 26 }),
        el('span.small.faint', { text: 'Thanks!' }),
      ),
    ));
    return card;
  }
  card.appendChild(el('div.small.muted.mb-1', { text: 'How was it? Your rating is public on the activity feed.' }));
  let submitting = false;
  const picker = starPicker({
    onSelect: async (r) => {
      if (submitting) return;
      submitting = true;
      picker._disable();
      try {
        await rate(r);
        picker._setValue(r);
        toast('Thanks for your rating!', 'success');
      } catch (err) {
        toast(err.message || 'Could not save rating', 'error');
      } finally {
        submitting = false;
      }
    },
  });
  card.appendChild(picker);
  return card;
}

function renderOrder(o) {
  clearInterval(autoTimer);
  const meta = kindMeta(o.kind);
  const payload = o.payload || {};

  const statusRow = el('div.row.between.mb-2', { style: { flexWrap: 'wrap', gap: '12px' } },
    el('div', {},
      el('div.row', { style: { gap: '10px' } },
        orderThumb({
          family: payload.product_name || o.product_id,
          country: payload.country || '',
          image: payload.product_image || payload.logo_url || '',
          bgColor: payload.product_bg || '',
          thumbCls: meta.thumb,
          iconName: meta.icon,
        }),
        el('div', {},
          el('h2', { style: { margin: 0 }, text: payload.product_name || o.product_id }),
          el('div.xs.faint', { text: `${meta.label} · placed ${fmtDate(o.created_at)}` }),
        ),
      ),
    ),
    statusBadge(o.status),
  );

  const left = el('div.card', {},
    el('div.card-title', { text: 'Live tracking' }),
    stageTimeline(o.stages || []),
  );

  const rightItems = [];

  /* delivery */
  const f = o.fulfillment;
  if (f && (f.code || f.link || f.pin || f.instructions || f.barcode_value)) {
    const card = el('div.card', {}, el('div.card-title', { text: 'Your delivery' }));
    if (f.code) card.appendChild(el('div.code-box', {}, el('span.code', { text: f.code }), copyButton(f.code)));
    if (f.pin) card.appendChild(el('div.code-box', {}, el('span.code', { text: f.pin }), copyButton(f.pin, 'Copy PIN')));
    if (f.link && safeHref(f.link)) {
      card.appendChild(el('a.btn.btn-outline.btn-block.mb-1', { href: safeHref(f.link), target: '_blank', rel: 'noopener noreferrer' }, icon('external', 18), el('span', { text: 'Open redemption link' })));
    } else if (f.link) {
      card.appendChild(el('div.code-box', {}, el('span.code.small', { text: f.link }), copyButton(f.link, 'Copy link')));
    }
    if (f.instructions) card.appendChild(el('div.small.muted', { text: f.instructions, style: { whiteSpace: 'pre-wrap' } }));
    rightItems.push(card);
  }

  /* How to redeem + supplier terms */
  rightItems.push(redeemInfoCard({
    family: payload.product_name || o.product_id,
    country: payload.country || '',
    kind: o.kind,
    deliveredNote: (f && (f.how_to_redeem || f.instructions)) || '',
  }));

  /* summary */
  rightItems.push(el('div.card', {},
    el('div.card-title', { text: 'Summary' }),
    kv([
      ['Product', payload.product_name || o.product_id],
      // Local-currency amount actually purchased ("500 TRY"), when the
      // payload carries it (range/slider purchases).
      (Number(payload.value) > 0 && payload.currency)
        ? ['Selected amount', el('span.strong', { text: `${fmtNum(Number(payload.value) * (Number(o.quantity) || 1))} ${payload.currency}` }) ]
        : null,
      ['Type', meta.label],
      payload.country ? ['Country', `${flag(payload.country)} ${countryName(payload.country)}`] : null,
      /* RECIPIENT INFO (private to the order owner): the delivery email and/or
       * top-up phone — gifted orders show the gift recipient too. */
      (o.customer_email || payload.customer_email || (String(payload.beneficiary || '').includes('@') ? payload.beneficiary : '')) ? ['Delivery email', el('span.mono.small', { text: o.customer_email || payload.customer_email || payload.beneficiary })] : null,
      (payload.phone_number || o.phone_number || (String(payload.beneficiary || '').startsWith('+') ? payload.beneficiary : '')) ? ['Phone number', el('span.mono.small', { text: payload.phone_number || o.phone_number || payload.beneficiary })] : null,
      o.gift_recipient_phone || payload.gift_recipient_phone ? ['Gift recipient', el('span.mono.small', { text: (o.gift_recipient_phone || payload.gift_recipient_phone) })] : null,
      o.gift_channel || payload.gift_channel ? ['Gift notification', giftChannelLabel(o.gift_channel || payload.gift_channel)] : null,
      o.gift_message || payload.gift_message ? ['Gift message', el('span.small', { text: String(o.gift_message || payload.gift_message).slice(0, 140) })] : null,
      ['Quantity', String(o.quantity)],
      ['Total', Number(o.price_usd) > 0 ? fmtUSD(o.price_usd) : '—'],
      ['Order ID', copyIdNode(o.id)],
      o.supplier_order_id ? ['Supplier order', el('span.mono.small', { text: shortAddr(o.supplier_order_id, 10, 8) })] : null,
      ['Updated', fmtDate(o.updated_at)],
    ].filter(Boolean)),
  ));

  /* refund — honest supplier refund record when the supplier refunded the order */
  const refundCard = refundCardForOrder(o.refund);
  if (refundCard) rightItems.push(refundCard);

  /* rating (only for delivered orders; the buyer's rating is public) */
  const rateCard = ratingCard({ status: o.status, rating: o.rating, rate: (r) => rateOrder(o.id, r) });
  if (rateCard) rightItems.push(rateCard);

  /* support */
  rightItems.push(supportSection(o.id, o.support_ticket, o.support_messages));

  replaceChildren(detail, el('div.fade-in', {},
    statusRow,
    el('div.detail-grid.cols', {}, left, el('div.col', {}, rightItems)),
  ));

  if (!isTerminalStatus(o.status)) {
    autoTimer = pageInterval(() => {
      // NEVER auto-refresh while the buyer is writing a support ticket —
      // a refresh re-renders the page and would throw the draft away.
      if (supportComposing(o.id)) return;
      load(false);
    }, 12000);
  }
}

function copyIdNode(text) {
  return el('span.row', { style: { gap: '6px', justifyContent: 'flex-end' } },
    el('span.mono.small', { text: shortAddr(text, 10, 8), title: text }),
    copyButton(text, ''),
  );
}

/* Supplier-side refund record (order.refund from CryptoRefills): the supplier
 * returned its own fee/balance for a failed or refunded order. Rendered
 * verbatim so the buyer sees exactly what the supplier reported. */
function refundCardForOrder(refund) {
  if (!refund) return null;
  const amount = (refund.amount !== undefined && refund.amount !== null && refund.amount !== '')
    ? `${refund.amount} ${refund.currency || ''}`.trim()
    : 'recorded by supplier';
  const card = el('div.card', {}, el('div.card-title', { text: 'Supplier refund' }));
  card.appendChild(kv([
    ['Amount', amount],
    refund.method ? ['Method', String(refund.method)] : null,
    refund.address ? ['Refund address', el('span.row', { style: { gap: '6px', justifyContent: 'flex-end' } }, el('span.mono.small', { text: shortAddr(refund.address, 10, 8), title: refund.address }), copyButton(refund.address, 'Address'))] : null,
  ].filter(Boolean)));
  card.appendChild(el('div.small.muted.mt-1', { text: 'This is the supplier\'s own refund record for the purchase, shown exactly as reported.' }));
  return card;
}

/* ---------------- quote rendering ---------------- */

function renderQuote(res) {
  clearInterval(autoTimer);
  const q = res.quote || res;
  const f = res.fulfillment;
  const refund = res.refund;

  // reuse the quote stage model from orders.js (kept in sync manually)
  const stages = quoteStages(q);

  const statusRow = el('div.row.between.mb-2', { style: { flexWrap: 'wrap', gap: '12px' } },
    el('div', {},
      el('div.row', { style: { gap: '10px' } },
        orderThumb({
          family: q.product_id,
          country: q.product_country || q.country || '',
          thumbCls: 'thumb-bp',
          iconName: 'bolt',
          iconSize: 40, /* ×2 thumb → ×2 fallback icon */
        }),
        el('div', {},
          el('h2', { style: { margin: 0 }, text: 'Purchase' }),
          el('div.xs.faint', { text: `${q.product_id}${selectedAmountLabel(q) ? ' · ' + selectedAmountLabel(q) : ''} · placed ${fmtDate(q.created_at)}` }),
        ),
      ),
    ),
    statusBadge(q.status),
  );

  const left = el('div.card', {},
    el('div.card-title', { text: 'Live tracking' }),
    stageTimeline(stages),
	q.status === 'lightning_invoice_created' ? el('div.alert.info.mt-2', { style: { marginBottom: 0 } }, icon('bolt', 18), el('div', { text: 'Open the saved Lightning invoice in Nimiq Pay. Payment goes directly to CryptoRefills; nim.shop never receives the funds.' })) : null,
  );

  const rightItems = [];

  const lightningInvoice = q.cryptorefills_payment_request || '';
  if (lightningInvoice) {
    try {
      const paymentURI = lightningPaymentURI(lightningInvoice);
      rightItems.push(el('div.card', {},
        el('div.card-title', { text: 'Pay with Nimiq Pay' }),
        el('div.small.muted', { text: 'This BOLT11 invoice is paid directly to CryptoRefills. nim.shop never receives the payment.' }),
        el('a.btn.btn-gold.btn-block.mt-1', { href: paymentURI, on: { click: (e) => {
          rememberLightningPayment(lightningInvoice, { kind: 'quote', ref: q.id });
          if (inNimiqPay()) {
            // Inside Nimiq Pay the lightning: href is dead — copy instead;
            // the app detects the invoice on the clipboard automatically.
            e.preventDefault();
            copyText(lightningInvoice).then(() => toast('Invoice copied — open ☰ → Scan in Nimiq Pay to pay it', 'success')).catch(() => {});
          }
        } } }, icon('bolt', 18), el('span', { text: inNimiqPay() ? 'Copy invoice & pay' : 'Open Lightning payment' })),
        el('div.mono.xs.faint.mt-1.pay-selectall', { text: lightningInvoice, title: 'Long-press to select & copy', style: { wordBreak: 'break-all' } }),
        copyButton(lightningInvoice, 'Copy invoice'),
      ));
    } catch { /* malformed supplier data is never made clickable */ }
  }

  if (f && (f.code || f.link || f.pin || f.instructions)) {
    const card = el('div.card', {}, el('div.card-title', { text: 'Your delivery' }));
    if (f.code) card.appendChild(el('div.code-box', {}, el('span.code', { text: f.code }), copyButton(f.code)));
    if (f.pin) card.appendChild(el('div.code-box', {}, el('span.code', { text: f.pin }), copyButton(f.pin, 'Copy PIN')));
    if (f.link && safeHref(f.link)) card.appendChild(el('a.btn.btn-outline.btn-block.mb-1', { href: safeHref(f.link), target: '_blank', rel: 'noopener noreferrer' }, icon('external', 18), el('span', { text: 'Open redemption link' })));
    else if (f.link) card.appendChild(el('div.code-box', {}, el('span.code.small', { text: f.link }), copyButton(f.link, 'Copy link')));
    if (f.instructions) card.appendChild(el('div.small.muted', { text: f.instructions, style: { whiteSpace: 'pre-wrap' } }));
    rightItems.push(card);
  }

  /* How to redeem + supplier terms for direct Lightning purchases too. */
  rightItems.push(redeemInfoCard({
    family: q.product_id,
    country: q.product_country || q.country || '',
    kind: 'gift_card',
    deliveredNote: (f && (f.how_to_redeem || f.instructions)) || '',
  }));

  rightItems.push(el('div.card', {},
    el('div.card-title', { text: 'Summary' }),
    kv([
      ['Product', q.product_id],
      /* RECIPIENT INFO (private to the quote owner) */
      q.customer_email ? ['Delivery email', el('span.mono.small', { text: q.customer_email })] : null,
      q.phone_number ? ['Phone number', el('span.mono.small', { text: q.phone_number })] : null,
      q.gift_recipient_phone ? ['Gift recipient', el('span.mono.small', { text: q.gift_recipient_phone })] : null,
      q.gift_channel ? ['Gift notification', giftChannelLabel(q.gift_channel)] : null,
      q.gift_message ? ['Gift message', el('span.small', { text: String(q.gift_message).slice(0, 140) })] : null,
      ['Quantity', String(q.quantity)],
      // SELECTED AMOUNT: the local-currency value picked at checkout (slider
      // range stores it in product_value; fixed labels carry it in the
      // denomination). The old row showed a literal "range" here, which told
      // the buyer nothing about what they had chosen.
      selectedAmountLabel(q)
        ? ['Selected amount', el('span.strong', { text: selectedAmountLabel(q) })]
        : (q.denomination && q.denomination !== 'range') ? ['Selected amount', String(q.denomination)] : null,
      // USD equivalent (product_usd is micro-USD).
      (Number(q.product_usd) || 0) > 0
        ? ['≈ USD value', fmtUSD((Number(q.product_usd) || 0) / 1e6)]
        : null,
      // NIM amount (estimated): the supplier stores the invoice as BTC;
      // the buyer only ever sees NIM. Converted live from the oracle rates.
      ['NIM amount', nimAmountNode(q)],
      q.oracle_usd_per_nim > 0 ? ['Informational rate', fmtUSD(q.oracle_usd_per_nim) + ' / NIM'] : null,
      ['Fee', 'Included in CryptoRefills Lightning invoice'],
      ['Quote ID', copyIdNode(q.id)],
      q.nimiq_tx_hash ? ['NIM tx', el('span.mono.small', { text: shortAddr(q.nimiq_tx_hash, 10, 8) })] : null,
      ['Expires', fmtDate(q.expires_at)],
    ].filter(Boolean)),
  ));

  /* Your refund — the exact NIM you paid, back to the wallet that paid.
   * Shown while the automatic on-chain refund is in flight and once done. */
  if (refund) rightItems.push(refundCardForQuote(refund));

  /* transactions — everything public: your NIM payment, the USDC we sent the
   * supplier, and the supplier invoice. End-to-end transparency. */
  const txPairs = [];
  if (q.lightning_payment_hash) txPairs.push(['Lightning payment', copyIdNode(q.lightning_payment_hash)]);
  if (q.nimiq_tx_hash) txPairs.push(['NIM payment (legacy)', copyIdNode(q.nimiq_tx_hash)]);
  if (q.polygon_tx_hash) txPairs.push(['Supplier settlement (legacy USDC)', copyIdNode(q.polygon_tx_hash)]);
  if (q.supplier_invoice_id) txPairs.push(['CryptoRefills invoice', copyIdNode(q.supplier_invoice_id)]);
  if (txPairs.length) rightItems.push(kvCard('Transactions (public)', txPairs));

  // PAY NOW — reload-safe: the Lightning invoice lives on the stored quote
  // (wallet_address), so even after a full reload the buyer can pay until
  // the countdown ends. The old page offered NO way to pay after a reload.
  if (String(q.status) === 'awaiting_payment' && q.wallet_address) {
    const invoice = q.wallet_address;
    let payURI = '';
    try { payURI = lightningPaymentURI(invoice); } catch {}
    const countdown = el('div.strong', { style: { fontSize: '1.05rem' } });
    const pay = payURI
      ? lightningPayBlock({ invoice, uri: payURI, onLaunch: () => rememberLightningPayment(invoice, { kind: 'quote', ref: q.id }), avatarAddress: getAddress() })
      : null;
    if (pay) {
      payCountdown(countdown, q.payment_expiry || q.payment_expires_at, () => {
        pay.link.style.opacity = '0.45';
        pay.link.removeAttribute('href');
        pay.link.title = 'Payment window expired';
        countdown.textContent = '⏳ Payment window expired — start a new order.';
        countdown.style.color = 'var(--orange, #c7481d)';
      });
    } else {
      countdown.textContent = '⏳ Payment window expired — start a new order.';
      countdown.style.color = 'var(--orange, #c7481d)';
    }
    const payCard = el('div.card', { style: { borderColor: 'var(--stamp)', borderWidth: '2px', boxShadow: '3px 3px 0 rgba(199, 72, 29, 0.25)' } },
      el('div.card-title', { text: '⏳ Pay now — invoice is live' }),
      countdown,
      pay ? pay.wrap : null,
      el('div.xs.faint.mt-1', { text: 'Reload-safe: this invoice stays payable until the timer ends.' }),
    );
    rightItems.unshift(payCard);
  }

  const qRate = ratingCard({ status: q.status, rating: q.rating, rate: (r) => rateQuote(q.id, r) });
  if (qRate) rightItems.push(qRate);

  rightItems.push(supportSection(q.id, null, null));

  replaceChildren(detail, el('div.fade-in', {},
    statusRow,
    el('div.detail-grid.cols', {}, left, el('div.col', {}, rightItems)),
  ));

  if (!isTerminalStatus(q.status)) {
    autoTimer = pageInterval(() => load(false), 12000);
  }
}

// quoteStages lives in ../order-track.js (shared with the orders list).

/* Customer-facing refund block for direct-NIM purchases. Two shapes from
 * the API: {status:"refunding", detail} while the on-chain transfer is in
 * flight, and {status:"refunded", amount_nim, refund_address, tx_hash, detail}
 * once it is mined. */
function refundCardForQuote(refund) {
  const card = el('div.card', {}, el('div.card-title', { text: 'Your refund' }));
  const s = String(refund.status || '');
  if (s === 'refunding') {
    card.appendChild(el('div.row', { style: { gap: '10px', alignItems: 'center' } },
      el('div.spinner', { style: { width: '20px', height: '20px' } }),
      el('div.strong', { text: 'Automatic refund in progress' }),
    ));
    card.appendChild(el('div.small.muted.mt-1', { text: refund.detail || 'The exact NIM you paid is being sent back to the wallet that paid.' }));
    card.appendChild(el('div.alert.info.mt-1', { style: { marginBottom: 0 } }, icon('info', 16),
      el('div.small', { text: 'This happens automatically — no support ticket needed. The page refreshes itself until the refund is on-chain.' })));
    return card;
  }
  card.appendChild(kv([
    ['Amount', `${refund.amount_nim} NIM`],
    refund.refund_address ? ['Back to your wallet', el('span.row', { style: { gap: '6px', justifyContent: 'flex-end' } }, el('span.mono.small', { text: shortAddr(refund.refund_address, 12, 10), title: refund.refund_address }), copyButton(refund.refund_address, 'Wallet'))] : null,
    refund.tx_hash ? ['Refund tx', el('span.row', { style: { gap: '6px', justifyContent: 'flex-end' } }, el('span.mono.small', { text: shortAddr(refund.tx_hash, 10, 8), title: refund.tx_hash }), copyButton(refund.tx_hash, 'Tx hash'))] : null,
  ].filter(Boolean)));
  card.appendChild(el('div.small.muted.mt-1', { text: refund.detail || 'The exact NIM you paid was sent back to the wallet that paid.' }));
  return card;
}

/* ---------------- support (shared) ---------------- */

// Draft preservation: the 12s auto-refresh must NEVER wipe a half-written
// ticket ("ticket gitmemeli asla"). Drafts are stored per order id and
// restored on every re-render; while a draft exists or the visitor is
// typing inside the support card, the auto-refresh tick is skipped.
const supportDrafts = new Map();
function supportDraft(orderId) {
  let d = supportDrafts.get(orderId);
  if (!d) { d = { subject: '', msg: '', reply: '' }; supportDrafts.set(orderId, d); }
  return d;
}
function supportComposing(orderId) {
  const card = document.getElementById('supportCard');
  if (!card) return false;
  const ae = document.activeElement;
  if (ae && card.contains(ae)) return true; // actively typing in support
  const d = supportDrafts.get(orderId);
  if (d && ((d.subject || '').trim() || (d.msg || '').trim() || (d.reply || '').trim())) return true;
  for (const t of card.querySelectorAll('textarea,input')) {
    if ((t.value || '').trim()) return true;
  }
  return false;
}
// Chat bubble with avatars: the BUYER's messages sit on the RIGHT with
// their Nimiq identicon; SUPPORT's messages sit on the LEFT with the
// headset avatar.
// chatMessageRow lives in ../ui.js (shared with the support page).

function supportSection(orderId, ticket, messages) {
  const card = el('div.card', { id: 'supportCard' });
  const title = el('div.card-title', { text: 'Support' });
  card.appendChild(title);

  async function drawThread() {
    card.textContent = '';
    card.appendChild(el('div.card-title', { text: 'Support' }));
    try {
      const res = await getOrderSupport(orderId);
      if (!res.ticket) {
        drawCreateForm(card);
        return;
      }
      drawExisting(card, res.ticket, res.messages || []);
    } catch (err) {
      card.appendChild(alertBox('error', err.message || 'Could not load support thread.'));
      card.appendChild(createOpenButton(card));
    }
  }

  function createOpenButton(container) {
    return el('button.btn.btn-outline.btn-block', { on: { click: () => { container.textContent = ''; container.appendChild(el('div.card-title', { text: 'Support' })); drawCreateForm(container); } } },
      icon('headset', 18), el('span.btn-label', { text: 'Contact support' }));
  }

  function drawCreateForm(container) {
    const draft = supportDraft(orderId);
    const subject = el('input.input', { placeholder: 'e.g. Code not working', maxlength: 160, 'aria-label': 'Subject', value: draft.subject });
    const msg = el('textarea.input', { placeholder: 'Describe the problem — include any error text you saw.', maxlength: 4000, 'aria-label': 'Message' });
    msg.value = draft.msg;
    subject.addEventListener('input', () => { draft.subject = subject.value; });
    msg.addEventListener('input', () => { draft.msg = msg.value; });
    const btn = el('button.btn.btn-gold.btn-block', {
      on: {
        click: async () => {
          if (!subject.value.trim() || !msg.value.trim()) { toast('Please fill in the subject and a message.', 'error'); return; }
          btn.disabled = true;
          try {
            const res = await createTicket({ order_id: orderId, subject: subject.value.trim(), message: msg.value.trim() });
            toast('Ticket created — our team will reply here.', 'success');
            drawExisting(container, res.ticket, [res.message].filter(Boolean));
          } catch (err) {
            toast(err.message || 'Could not create the ticket', 'error');
            btn.disabled = false;
          }
        },
      },
    }, icon('send', 18), el('span.btn-label', { text: 'Submit ticket' }));

    container.append(
      el('div.field', {}, el('label', { text: 'Subject' }), subject),
      el('div.field', {}, el('label', { text: 'Message' }), msg),
      btn,
    );
  }

  function drawExisting(container, ticket, msgs) {
    container.append(
      el('div.row.between.mb-1', {},
        el('div.strong.small.truncate', { text: ticket.subject, style: { maxWidth: '60%' } }),
        statusBadge(ticket.status),
      ),
    );
    const chat = el('div.chat.chat-window.mb-2');
    const myAddress = getAddress();
    for (const m of msgs || []) chat.appendChild(chatMessageRow(m, myAddress));
    container.appendChild(chat);
    requestAnimationFrame(() => { chat.scrollTop = chat.scrollHeight; });

    if (['closed', 'resolved'].includes(ticket.status)) {
      container.appendChild(alertBox('info', 'This ticket is ' + ticket.status + '. Open a new ticket if you need more help.'));
      return;
    }
    if (ticket.status === 'waiting_user') {
      container.appendChild(el('div.alert.info.waiting-banner', { style: { marginBottom: '10px' } }, icon('headset', 16),
        el('div.small.strong', { text: 'Support replied — answer below to keep the ticket moving.' })));
    }

    const draft = supportDraft(orderId);
    const composer = chatComposer({
      placeholder: 'Write a reply…',
      sendLabel: 'Send reply',
      onSend: async (text) => {
        try {
          await replyTicket(ticket.id, text);
          supportDrafts.delete(orderId);
          drawThread();
        } catch (err) {
          toast(err.message || 'Could not send the reply', 'error');
          draft.reply = text; // keep the typed text for the retry
          throw err;
        }
      },
    });
    if (draft.reply) composer.ta.value = draft.reply;
    composer.ta.addEventListener('input', () => { draft.reply = composer.ta.value; });
    container.appendChild(composer.box);
  }

  if (ticket) drawExisting(card, ticket, messages || []);
  else drawThread(); // fetch the live state: an EXISTING ticket must never be hidden behind "Open a support ticket"

  return card;
}

window.addEventListener('nimshop:session', () => load());
load();
