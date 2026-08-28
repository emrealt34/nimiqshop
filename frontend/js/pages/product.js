/* pages/product.js — FIXED: No refund, only email. Country required. Correct currency handling, no duplicate, side-by-side denominations. */
import { bootShell, openLogin, navigate } from '../shell.js';
import { el, icon, $, replaceChildren, fmtNIM, fmtMoney, queryParam, flag, countryName, richNode, stripHtml, cleanSupplierTerms, MAX_QTY, parseCurrencyValue } from '../util.js';
import { getProduct, getNimRate, getFXRates } from '../api.js';
import { brandMetaFor } from '../catalog-meta.js';
import { isAuthed } from '../session.js';
import { toast, openSheet, closeSheet, alertBox } from '../ui.js';
import { nimIcon } from '../nim.js';
import { addToCart } from '../cart.js';
import { askSingleDelivery, buildOrderRequest } from '../delivery.js';
import { mapKind, extractLogo } from '../catalog.js';
import { createQuoteStep, renderQuotePayment } from '../quote-pay.js';

bootShell('');
const id = queryParam('id');
const countryParam = queryParam('country');
const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on
// top of the first (the duplicate locked-card bug).
replaceChildren(main);

main.appendChild(el('div.container', {},
  el('a.btn.btn-ghost.btn-sm', { href: '/' }, icon('back', 16), el('span.btn-label', { text: 'Shop' })),
  el('div#detail.mt-2', {}, el('div.card', {}, el('div', { text: 'Loading product…' }))),
));
const detail = $('#detail');
let nimUsdRate = null;
let btcUsdRate = null;
let fxRates = null; // USD-per-unit table from the backend (same as admin cap filter)

/* ---------------- brand meta (original background) ----------------
 * Resolves a family's OWN logo + bg_color from the catalog brand lists
 * (giftcards/topups/esims) — the same source the home grid renders from.
 * The family DETAIL payload often carries no bg_color, which used to leave
 * the product hero on a hardcoded white plate. Country lists are fetched
 * once per session and shared by every lookup.
 */


// Set by render(): redraws chips + buy label once late-arriving rate data lands.
let refreshEstimates = null;

function cleanName(name) {
  if (!name) return '';
  let s = String(name).trim();
  // Only unwrap markdown links; keep the name otherwise intact — it is the
  // exact-match lookup id for the supplier API (parentheses included).
  const md = s.match(/^\[(.+?)\]\(.+?\)$/);
  if (md) s = md[1].trim();
  return s;
}

function cleanDescription(desc) {
  if (!desc) return '';
  let s = stripHtml(String(desc)); // supplier descriptions can carry <p>/<br>
  s = s.replace(/Pay with Bitcoin[\s\S]*?Arbitrum\.?/gi, '');
  s = s.replace(/,?\s*and Arbitrum\.?/gi, '');
  s = s.replace(/,?\s*on Lightning Network[\s\S]*?Arbitrum\.?/gi, '');
  s = s.replace(/Buy now a .*? with Bitcoin and other Crypto\./gi, '');
  s = s.replace(/Pay with Bitcoin[\s\S]*/gi, '');
  s = s.trim().replace(/^,+\s*/, '').replace(/\s{2,}/g, ' ').trim();
  s = s.replace(/ ,/g, ',').replace(/,\s*,/g, ',').trim();
  if (!s || s.length < 10) return `Instant email delivery. Pay with Nimiq Pay in NIM. Valid for ${countryName(countryParam)}.`;
  return s;
}


function unavailableCard() {
  return el('div.card.fade-in', {},
    el('div.empty', {},
      el('div.empty-ico', {}, icon('search', 32)),
      el('h3', { text: 'Not available right now' }),
      el('p.small.muted', { text: `This product is not purchasable in ${countryName(countryParam) || countryParam || 'this country'} at the moment — it may be sold out, region-locked or above the current order limit.` }),
      el('div.row', { style: { gap: '10px', justifyContent: 'center', flexWrap: 'wrap' } },
        el('a.btn.btn-gold', { href: '/' }, icon('bag', 16), el('span.btn-label', { text: 'Browse the shop' })),
        el('button.btn.btn-outline', { on: { click: () => load() } }, icon('refresh', 16), el('span.btn-label', { text: 'Try again' })),
      ),
    ),
  );
}

async function load() {
  if (!id) { replaceChildren(detail, alertBox('error', 'No product selected.')); return; }
  if (!countryParam || countryParam.length !== 2) {
    replaceChildren(detail, alertBox('error', 'Missing country param'));
    return;
  }
  try {
    // SPEED: only the CATALOG fetch blocks the render. The NIM rate (oracle
    // can take seconds when cold) and the FX table load IN PARALLEL in the
    // background; when they land, the estimates on the already-visible page
    // refresh themselves. The old code awaited all three SEQUENTIALLY —
    // the page stayed on "Loading product…" for up to ~12s.
    Promise.allSettled([
      getNimRate().then((m) => {
        nimUsdRate = Number(m.usd_per_nim) || null;
        btcUsdRate = Number(m.usd_per_btc) || null;
      }),
      getFXRates().then((fx) => { fxRates = (fx && fx.usd_per_unit) || null; }),
    ]).then(() => { if (refreshEstimates) refreshEstimates(); });

    const data = await getProduct(id, countryParam);
    let productDetail = null;
    if (Array.isArray(data) && data.length > 0) productDetail = familyToProduct(data[0], id);
    else if (data && data.products) productDetail = familyToProduct(data, id);
    else {
      // Record for the home list's self-healing filter: this card stops
      // being shown to other visitors of this session.
      try {
        const arr = JSON.parse(sessionStorage.getItem('nimshop_missing') || '[]');
        if (!arr.some((m) => m.id === id && m.country === countryParam)) {
          arr.push({ id, country: countryParam, at: Date.now() });
          sessionStorage.setItem('nimshop_missing', JSON.stringify(arr.slice(-100)));
        }
      } catch {}
    }
    if (!productDetail) {
      // NOT a hard error: admin cap rules, country rules or a supplier
      // hiccup can legitimately remove a family from the catalog. Show the
      // friendly unavailable state instead of the dead-end "Product not
      // found" message.
      replaceChildren(detail, unavailableCard());
      return;
    }
    if (!productDetail || !productDetail.id) { replaceChildren(detail, unavailableCard()); return; }
    // ORIGINAL BRAND BACKGROUND: the family detail payload does not always
    // carry bg_color — resolve the brand's OWN color (and logo, if missing)
    // from the same catalog lists the shop grid uses, then render.
    if (!productDetail.bg_color || !productDetail.logo_url) {
      const meta = await brandMetaFor(productDetail.name || id, countryParam);
      if (!productDetail.bg_color && meta.bg) productDetail.bg_color = meta.bg;
      if (!productDetail.logo_url && meta.logo) productDetail.logo_url = meta.logo;
    }
    render(productDetail);
  } catch (err) {
    const notFound = /not found|unavailable/i.test(String(err.message || ''));
    if (notFound) {
      try {
        const arr = JSON.parse(sessionStorage.getItem('nimshop_missing') || '[]');
        if (!arr.some((m) => m.id === id && m.country === countryParam)) {
          arr.push({ id, country: countryParam, at: Date.now() });
          sessionStorage.setItem('nimshop_missing', JSON.stringify(arr.slice(-100)));
        }
      } catch {}
    }
    replaceChildren(detail, notFound ? unavailableCard() : alertBox('error', err.message || 'Could not load product'));
  }
}

function familyToProduct(family, fallbackId) {
  const familyName = cleanName(family.family || family.brand || fallbackId);
  const countryCode = (family.country_code || countryParam).toUpperCase();
  const logo = extractLogo(family);
  const products = family.products || [];
  const packages = [];
  let range = null;
  let currency = 'USD';
  for (const p of products) {
    if (p.range) {
      range = { min: p.range.min, max: p.range.max, step: p.range.step_size || p.range.step || 1, currency: p.range.currency || 'USD' };
      currency = p.range.currency || currency;
      continue;
    }
    let faceValue = 0;
    let faceCurrency = currency;
    let denomLabel = p.denomination || p.localized_denomination || '';
    // Parse from the label — handles every country's format now
    // ("25 TRY", "150.000 IDR", "1.000.000 VND", "1 000 AED", "₩5,000").
    // The old 10,000 cap DROPPED high-denomination currencies entirely.
    if (denomLabel) {
      const parsed = parseCurrencyValue(denomLabel);
      if (parsed.value > 0 && parsed.value < 100000000) { faceValue = parsed.value; faceCurrency = parsed.currency || faceCurrency; }
    }
    if (!faceValue && p.amount && p.amount > 0 && p.amount < 100000000) {
      faceValue = p.amount;
      faceCurrency = p.currency_code || faceCurrency;
      denomLabel = `${faceValue} ${faceCurrency}`.trim();
    }
    if (!faceValue && p.face_value) {
      try {
        const fv = typeof p.face_value === 'string' ? JSON.parse(p.face_value) : p.face_value;
        if (fv) {
          if (typeof fv === 'number' && fv > 0 && fv < 100000000) faceValue = fv;
          else if (fv.amount && typeof fv.amount === 'number' && fv.amount < 100000000) faceValue = fv.amount;
          else if (fv.amount && typeof fv.amount === 'object' && fv.amount.value) {
            const pr = parseFloat(fv.amount.value);
            if (pr && pr < 100000000) faceValue = pr;
          }
          else if (fv.amount && fv.amount.price) {
            const pr = parseFloat(fv.amount.price);
            if (pr && pr < 100000000) faceValue = pr;
          }
          if (fv.currency_code) faceCurrency = fv.currency_code;
        }
      } catch {}
    }
    // Absolutely no label anywhere: fall back to the supplier's product id
    // (the most meaningful identifier we have) so the SKU still renders and
    // can be purchased — the supplier validates the exact label either way.
    if (!denomLabel) denomLabel = p.product_id || '';
    if (faceValue > 0 || denomLabel) {
      // KEEP the package even when the label has no usable number
      // ("Java & Bedrock Ed") or the value is huge ("150.000 IDR",
      // "1.000.000 VND"): the old `faceValue < 10000` filter DROPPED these
      // packages, leaving the family with zero buy options — checkout then
      // sent denomination:'' and died with "denomination is required".
      // Label-only packages send the exact supplier label; the supplier
      // prices fixed products by the label itself.
      packages.push({
        package_id: p.product_id,
        value: faceValue,
        currency: faceCurrency,
        denomination: denomLabel || `${faceValue} ${faceCurrency}`.trim(),
        localized_denomination: p.localized_denomination || denomLabel,
        coin_amount: p.coin_amount || p.original_coin_amount || '',
        coin: p.coin || 'BTC',
      });
    }
  }
  const bgColor = family.bg_color || ''; /* HERO-BG: supplier brand background, same as the home grid */
  const richRaw = family.rich_description || null;
  return {
    id: familyName,
    family: familyName,
    name: familyName,
    // Supplier's own rich content (HTML): how-to-redeem, T&C, region note.
    bg_color: bgColor,
    rich: richRaw ? {
      description: richRaw.description || '',
      howToRedeem: richRaw.how_to_redeem || '',
      terms: richRaw.term_and_conditions || richRaw.product_tc_hint || '',
      redeemGeo: richRaw.redeem_geo || '',
      note: richRaw.note || '',
      brandUrl: richRaw.brand_url || '',
    } : null,
    type: mapKind(family.kind, family.category),
    country: countryCode,
    currency: currency,
    packages: packages.length ? packages : null,
    range: range,
    in_stock: !family.is_out_of_stock,
    logo_url: logo,
    images: logo ? { large: logo } : {},
    description: cleanDescription(family.product_tc || ''),
    _family: family,
  };
}

// parseCurrencyValue moved to ../util.js (shared, country-proof parser).
// The old local copy mis-parsed "150.000 IDR" as 150 and "1 000 AED" as 0.

// mapKind + extractLogo live in ../catalog.js (shared with the shop grid).

function nimPriceFromBTC(btcAmount) {
  if (!btcAmount) return '';
  const btc = parseFloat(String(btcAmount));
  if (!btc || btc <= 0) return '';
  if (nimUsdRate && btcUsdRate && nimUsdRate > 0 && btcUsdRate > 0) {
    const nim = btc * btcUsdRate / nimUsdRate;
    if (nim >= 1000000) return `${(nim/1000000).toFixed(1)}M NIM`;
    if (nim >= 1000) return `${(nim/1000).toFixed(1)}K NIM`;
    return `${fmtNIM(nim, 0)} NIM`;
  }
  return '';
}

// localToUSD converts a LOCAL-currency face value to its USD-equivalent
// using the backend's curated table. Unknown/missing currency falls back
// 1:1 (supplier default is USD) — estimates stay sane either way.
function localToUSD(amount, code) {
  if (!amount || amount <= 0) return 0;
  const rate = fxRates && code ? fxRates[String(code).toUpperCase()] : null;
  if (!rate || rate <= 0) return Number(amount); // 1:1 fallback
  return Number(amount) * rate;
}

function nimPriceFromUSD(usd) {
  // NOTE: `usd` may actually be the LOCAL face value (IDR/VND ranges);
  // the 10,000 upper bound used to blank every high-denomination country's
  // estimate. The NIM division normalizes the display anyway — only guard
  // against non-positive garbage.
  if (!usd || usd <= 0) return '';
  if (nimUsdRate && nimUsdRate > 0) {
    const nim = Number(usd) / nimUsdRate;
    if (nim >= 1000000) return `${(nim/1000000).toFixed(1)}M NIM`;
    if (nim >= 1000) return `${(nim/1000).toFixed(1)}K NIM`;
    return `${fmtNIM(nim, 0)} NIM`;
  }
  return '';
}

/* ---------------- How to redeem (CryptoRefills delivery info) ----------- */
/* The supplier (CryptoRefills) defines how each product type is delivered
 * and redeemed; this card surfaces those rules on the buy page BEFORE
 * purchase, the same way cryptorefills.com shows "how to redeem" on its
 * own product pages. After payment the exact code/PIN/QR plus any
 * supplier-specific redeem_instructions arrive with the delivery. */

const REDEEM_STEPS = {
  gift_card: {
    title: 'How to redeem',
    steps: [
      'Pay with Nimiq Pay — the Lightning payment settles in ~5 seconds.',
      'Your gift card code arrives instantly by email and on the Orders page.',
      'Enter the code at the brand’s checkout, website or app — the full face value is credited there.',
    ],
    note: 'Codes never expire on our side; redeem whenever you like. The supplier’s own terms apply.',
  },
  phone_refill: {
    title: 'How it works',
    steps: [
      'Enter the recipient’s mobile number in international format (+90…).',
      'Pay with Nimiq Pay — the Lightning payment settles in ~5 seconds.',
      'The top-up credit is applied directly to that number, usually within minutes. Check the balance in the operator’s app or via USSD.',
    ],
    note: 'The number you enter is validated live with the supplier before you pay — a wrong number cannot slip through.',
  },
  esim: {
    title: 'How to install',
    steps: [
      'Pay with Nimiq Pay — the Lightning payment settles in ~5 seconds.',
      'Your eSIM QR code and activation details arrive instantly by email and on the Orders page.',
      'On your phone: Settings → Cellular / Mobile data → Add eSIM → scan the QR. Stay on Wi‑Fi while installing.',
    ],
    note: 'Most eSIMs activate on first network connection in the destination country. Your device must be eSIM-capable and unlocked.',
  },
};

// Strip markdown noise from the supplier's product_tc terms so only plain,
// readable text remains; truncated to keep the buy page compact.
function howToRedeemCard(p) {
  const info = REDEEM_STEPS[p.type] || REDEEM_STEPS.gift_card;
  const rich = p.rich || null;
  const list = el('ol.howto-steps', {}, ...info.steps.map((st) => el('li', {}, el('span', { text: st }))));
  const terms = cleanSupplierTerms(p._family ? p._family.product_tc : '');

  // Supplier's OWN rich content (from the CryptoRefills API) wins over our
  // generic steps: exact official "How to redeem" wording, links included
  // (sanitized: scripts/iframe/on*/javascript: stripped, links new-tab).
  let howTo = list;
  if (rich && rich.howToRedeem) {
    howTo = richNode(rich.howToRedeem, 'rich-html howto-rich'); /* RICH-HTML FIX: el() string children become TEXT nodes — the sanitized HTML was rendering as literal <p>/<li> */
  }

  // Region restriction banner: "May only be redeemable in Türkiye" with
  // the country flag, straight from the supplier's redeem_geo field.
  const geoBanner = rich && rich.redeemGeo
    ? el('div.alert.info.mt-1', { style: { marginBottom: 0 } }, icon('info', 16),
        el('div.small', {},
          el('span', { text: `${flag(p.country)} May only be redeemable ${stripHtml(rich.redeemGeo)}. ` }), // redeem_geo arrives as HTML — strip for the text banner
          el('a', { href: '/', style: { fontWeight: '800' }, on: { click: (e) => { e.preventDefault(); navigate('/'); } } }, 'Not in ' + (p.country || '') + '? Find your country'),
        ),
      )
    : null;

  const termsBlock = (rich && rich.terms)
    ? el('details.howto-terms.mt-1', {},
        el('summary.xs', { text: 'Terms and conditions (from CryptoRefills)' }),
        richNode(rich.terms, 'xs muted rich-html'),
      )
    : terms ? el('details.howto-terms.mt-1', {},
        el('summary.xs', { text: 'Supplier terms (CryptoRefills)' }),
        el('div.xs.muted', { text: terms }),
      ) : null;

  return el('div.card.mt-2.howto', {},
    el('div.card-title', {}, icon('gift', 16), el('span', { text: info.title + ' — delivered by CryptoRefills' })),
    geoBanner,
    howTo,
    el('div.howto-note.small', {}, icon('info', 15), el('span', { text: info.note })),
    el('div.howto-mini.row.mt-1', {},
      p && p.type === 'phone_refill'
        ? el('span.chip', {}, icon('bolt', 13), 'Instant phone top-up')
        : el('span.chip', {}, icon('bolt', 13), 'Instant email delivery'),
      p && p.type === 'phone_refill'
        ? el('span.chip', {}, icon('lock', 13), 'Credit applied to your number')
        : el('span.chip', {}, icon('lock', 13), 'Code shown on email'),
    ),
    termsBlock,
  );
}

function render(p) {
  const hasPackages = !!(p.packages && p.packages.length);
  // Dead state: no SKUs and no range — nothing purchasable right now (e.g.
  // the supplier returned an empty product list for this country). The buy
  // buttons are DISABLED with a clear reason instead of letting the user
  // click into a checkout that dies with "denomination is required".
  const dead = !hasPackages && !p.range;
  const state = { pkg: hasPackages ? p.packages[0].package_id : '', value: p.range ? p.range.min : 0, qty: 1, denomination: '' };
  if (hasPackages) {
    state.denomination = p.packages[0].denomination || `${p.packages[0].value} ${p.packages[0].currency || p.currency}`.trim();
  } else if (p.range) {
    state.denomination = 'range';
    state.value = p.range.min;
  }

  const img = p.logo_url || '';
  // CUSTOM ICON: shop's own mark replaces the old flag fallback on the product hero.
  const heroFallback = () => el('img', { src: '/img/brand-icon.png', alt: p.name || 'Product', style: { width: '120px', height: '120px', objectFit: 'contain', borderRadius: '12px', padding: '10px' } });
  const heroImg = el('div.pd-image', {},
    img ? (() => { const i = el('img', { src: img, alt: p.name, fetchpriority: 'high', decoding: 'async', style: { maxWidth: '100%', maxHeight: '220px', objectFit: 'contain', background: p.bg_color || '#fff', borderRadius: '12px' } }); /* HERO-BG FIX: was hardcoded #fff — use the supplier brand color like the home cards */ i.addEventListener('error', () => i.replaceWith(heroFallback())); return i; })()
        : heroFallback());

  // Dynamic buy-button label: shows the LIVE total NIM estimate for the
  // current selection × quantity ("Buy 2 × — ≈ 1.2M NIM"). Used to be a
  // static "Buy with NIM" that told the buyer nothing about price and
  // never reacted to the quantity stepper.
  const buyLabel = el('span.btn-label', { text: 'Buy with NIM — Pay with Nimiq Pay' });
  const buy = el('button.btn.btn-gold.btn-block.btn-lg', { on: { click: () => startBuy(p, state) } }, nimIcon(14), buyLabel);
  function updateBuyLabel() {
    const qty = state.qty || 1;
    let nim = '';
    const pkg = (p.packages || []).find((k) => k.package_id === state.pkg);
    if (pkg && pkg.coin_amount) nim = nimPriceFromBTC(parseFloat(pkg.coin_amount) * qty);
    if (!nim) nim = nimPriceFromUSD(localToUSD((state.value || 0) * qty, pkg ? pkg.currency : p.currency));
    if (dead) { buyLabel.textContent = 'Not available here'; return; }
    // BUY-LABEL FIX: "Buy 2 × —" read as broken punctuation. Name the
    // product instead: "Buy 2 × Getir — ≈ 80.9K NIM" / "Buy Getir ≈ …".
    const nm = (p.name || '').length > 16 ? (p.name || '').slice(0, 15) + '…' : (p.name || 'this item');
    if (nim) buyLabel.textContent = qty > 1 ? `Buy ${qty} × ${nm} — ≈ ${nim}` : `Buy ${nm} ≈ ${nim}`;
    else buyLabel.textContent = qty > 1 ? `Buy ${qty} × ${nm}` : `Buy ${nm} — Pay with Nimiq Pay`;
  }
  const addCart = el('button.btn.btn-outline.btn-block.btn-lg', { on: { click: () => addToCart(p, { pkg: state.pkg, value: state.value, qty: state.qty, denomination: state.denomination }) } }, icon('bag', 14), el('span.btn-label', { text: 'Add to cart' }));
  if (dead) {
    buy.disabled = true;
    addCart.disabled = true;
    buy.title = 'Not purchasable in ' + (p.country || 'this country') + ' right now';
    addCart.title = buy.title;
    buy.style.opacity = '0.5';
    addCart.style.opacity = '0.5';
  }

  const qtyVal = el('span.qty-val', { text: String(state.qty) });
  const qtyMinus = el('button.qty-btn', { text: '−', on: { click: () => { state.qty = Math.max(1, state.qty - 1); qtyVal.textContent = String(state.qty); updateQtyBtns(); } } });
  const qtyPlus = el('button.qty-btn', { text: '+', on: { click: () => { if (state.qty < MAX_QTY) { state.qty = Math.min(MAX_QTY, state.qty + 1); qtyVal.textContent = String(state.qty); updateQtyBtns(); } } } });
  function updateQtyBtns() {
    qtyMinus.disabled = state.qty <= 1;
    qtyMinus.style.opacity = state.qty <= 1 ? '0.4' : '1';
    qtyPlus.disabled = state.qty >= MAX_QTY;
    qtyPlus.style.opacity = state.qty >= MAX_QTY ? '0.4' : '1';
    updateBuyLabel();
  }
  updateBuyLabel();
  updateQtyBtns();
  const qtyStepper = el('div.qty-stepper', {}, el('span.xs.faint', { text: 'Quantity', style: { fontWeight: '800' } }), el('div.stepper', {}, qtyMinus, qtyVal, qtyPlus));

  // Force 2+ columns side-by-side via inline style (no CSS specificity fight).
const chooser = el('div.packages-grid.mt-2', {
  style: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
    gap: '12px',
    width: '100%',
    maxWidth: '100%',
  },
});
  function drawChooser() {
    replaceChildren(chooser);
    if (p.packages && p.packages.length) {
      for (const k of p.packages) {
        const isActive = k.package_id === state.pkg;
        const usdValue = k.value || 0;
        // NIM estimate (never raw BTC on the storefront): convert the
        // supplier's coin_amount to NIM; when rates are unavailable show
        // "NIM at checkout" instead of a confusing BTC number.
        const nimPrice = (k.coin_amount ? nimPriceFromBTC(k.coin_amount) : '') || nimPriceFromUSD(localToUSD(usdValue, k.currency || p.currency));
        // Show correct denomination, not $ for TRY — use localized_denomination like "TRY25" or "25 TRY"
        const faceLabel = k.localized_denomination || k.denomination || (usdValue ? `$${usdValue}` : 'Package');
        const btn = el('button.pd-pkg' + (isActive ? '.active' : ''), {
          on: { click: () => { state.pkg = k.package_id; state.denomination = k.denomination || `${k.value} ${k.currency || p.currency}`.trim(); state.value = k.value; drawChooser(); updateBuyLabel(); } }, /* LIVE-PAY-AMOUNT FIX: the buy button's NIM estimate was never refreshed on package switch */
        },
          el('div.pd-pkg-face', { text: faceLabel }),
          nimPrice ? el('div.pd-pkg-nim', {}, nimIcon(12), el('span', { text: nimPrice })) : el('div.xs.faint', { text: 'NIM at checkout' }),
        );
        chooser.appendChild(btn);
      }
    } else if (p.range) {
      const rangeCur = p.range.currency || p.currency;
      const nimMin = nimPriceFromUSD(localToUSD(p.range.min, rangeCur));
      const nimMax = nimPriceFromUSD(localToUSD(p.range.max, rangeCur));
      // LIVE AMOUNT: the buyer must always SEE which local amount they are on
      // (e.g. 1.250 ₺ inside 5 – 5.000 ₺), not just the NIM estimate.
      const amountText = el('div', { text: fmtMoney(state.value, rangeCur), style: { fontFamily: 'var(--font-serif)', fontWeight: '900', fontSize: 'clamp(1.15rem, 1rem + 1vw, 1.5rem)', lineHeight: '1.2', color: 'var(--ink)', whiteSpace: 'nowrap' } });
      const nimText = el('span', { text: nimPriceFromUSD(localToUSD(state.value, rangeCur)) || nimMin || 'NIM at checkout' });
      // DRAG FIX: the old `input` handler called drawChooser() on every tick,
      // and drawChooser() does replaceChildren() — the slider element was
      // destroyed and recreated mid-drag, so dragging broke after the first
      // movement and only tap-to-position "worked". The slider now stays
      // alive; only the amount text, the NIM text and the buy label refresh.
      const slider = el('input', { type: 'range', min: p.range.min, max: p.range.max, step: p.range.step || 1, value: state.value, style: { width: '100%', marginTop: '12px' }, on: { input: (e) => {
        applyValue(Number(e.target.value), true);
      } } });
      // TYPE-IN AMOUNT: sliders are hopeless for 4-5 digit ranges (₺57 -
      // ₺5.000). A synced number box lets the buyer just TYPE the amount; the
      // slider follows along (and vice versa). Steps to the range step, clamps
      // to min/max, and accepts plain digits only.
      const rangeStep = p.range.step || p.range.step_size || 1;
      const snap = (v) => {
        let n = Math.round(v / rangeStep) * rangeStep;
        n = Math.max(p.range.min, Math.min(p.range.max, n));
        return n;
      };
      const applyValue = (v, fromSlider) => {
        state.value = v;
        state.denomination = 'range';
        amountText.textContent = fmtMoney(v, rangeCur);
        nimText.textContent = nimPriceFromUSD(localToUSD(v, rangeCur)) || nimMin || 'NIM at checkout';
        if (!fromSlider) slider.value = v;
        amountInput.value = v;
        updateBuyLabel();
      };
      const amountInput = el('input.input', {
        type: 'text', inputmode: 'numeric', pattern: '[0-9]*',
        placeholder: String(p.range.min),
        'aria-label': 'Custom amount in ' + rangeCur,
        style: { maxWidth: '160px', minHeight: '44px', fontWeight: 800, textAlign: 'center', fontFamily: 'var(--font-serif)', fontSize: '1.05rem' },
      });
      amountInput.value = state.value;
      amountInput.addEventListener('input', () => {
        const digits = amountInput.value.replace(/[^0-9]/g, '');
        if (digits !== amountInput.value) amountInput.value = digits;
        if (!digits) return; // keep typing; slider stays on the last valid value
        applyValue(snap(Number(digits)));
      });
      amountInput.addEventListener('blur', () => { amountInput.value = state.value; });

      chooser.appendChild(el('div.range-card', { style: { gridColumn: '1 / -1', width: '100%', boxSizing: 'border-box' } },
        el('div.row.between', { style: { flexWrap: 'wrap', gap: '8px' } },
          el('div', {}, el('div.xs.faint', { text: 'Amount range' }), el('div.strong', { text: `${fmtMoney(p.range.min, rangeCur)} - ${fmtMoney(p.range.max, rangeCur)}` })),
          el('div', { style: { textAlign: 'right' } }, el('div.xs.faint', { text: 'NIM estimate' }), el('div.strong.nim-price', { style: { display: 'inline-flex', alignItems: 'center', gap: '6px', whiteSpace: 'nowrap' } }, nimIcon(14), el('span', { text: `${nimMin} - ${nimMax}` })))
        ),
        el('div.row', { style: { alignItems: 'flex-end', gap: '10px', flexWrap: 'wrap', marginTop: '10px' } },
          el('div.field', { style: { marginBottom: '0', flex: '0 0 auto' } },
            el('label.xs.faint', { text: 'Or type the amount (' + rangeCur + ')' }),
            amountInput,
          ),
          el('div.xs.faint', { style: { paddingBottom: '14px', flex: '1 1 160px', minWidth: '0' } }, `between ${fmtMoney(p.range.min, rangeCur)} and ${fmtMoney(p.range.max, rangeCur)}`),
        ),
        slider,
        el('div.range-ends.mt-1', { style: { display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' } },
          el('span.small', { text: fmtMoney(p.range.min, rangeCur), style: { flexShrink: 0 } }),
          el('div', { style: { flex: '1 1 auto', minWidth: '0', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '3px', textAlign: 'center' } },
            amountText,
            el('span.big-nim', { style: { display: 'inline-flex', alignItems: 'center', justifyContent: 'center', gap: '6px', whiteSpace: 'normal', textAlign: 'center' } }, nimIcon(14), nimText),
          ),
          el('span.small', { text: fmtMoney(p.range.max, rangeCur), style: { flexShrink: 0, marginLeft: 'auto' } }),
        ),
      ));
    } else {
      // No SKUs and no range: a clear unavailability notice in the buyer's
      // country — NOT the old misleading "Live denominations at checkout"
      // line that sat above a checkout which then failed.
      chooser.appendChild(el('div.alert.info', {}, icon('info', 18), el('div.small', {
        text: `This product is currently unavailable in ${countryName(p.country) || p.country}. Please pick another country or product.`,
      })));
    }
  }
  drawChooser();
  refreshEstimates = () => { drawChooser(); updateBuyLabel(); };

  const chipsRow = el('div.chips-row', { style: { display: 'flex', gap: '8px', flexWrap: 'wrap', alignItems: 'center' } },
    el('span.chip', { style: { display: 'inline-flex', alignItems: 'center', gap: '6px' } }, p.type === 'phone_refill' ? '⚡ Instant phone top-up' : '⚡ Instant email delivery'),
    el('span.chip', { style: { display: 'inline-flex', alignItems: 'center', gap: '6px' } }, nimIcon(14), 'Pay with NIM'),
    el('span.chip', { style: { display: 'inline-flex', alignItems: 'center', gap: '6px' } }, el('span', { text: flag(p.country) }), `${p.country} ${countryName(p.country)}`),
  );

  replaceChildren(detail, el('div.pd.fade-in', {},
    el('div.pd-top', {}, heroImg,
      el('div.pd-info', {},
        el('h1', { style: { margin: '0 0 8px', fontSize: 'clamp(1.3rem, 1.2rem + 1vw, 1.8rem)', lineHeight: '1.2', wordBreak: 'break-word' }, text: p.name }),
        chipsRow,
        el('p.mt-2.small', { text: p.description || (p.type === 'phone_refill' ? `Top up ${p.name} with NIM via Nimiq Pay.` : `Buy ${p.name} gift card with NIM via Nimiq Pay. Instant email delivery.`) }),
      ),
    ),
    el('div.card.mt-2', {}, el('div.card-title', { text: 'Choose denomination — NIM price' }), chooser),
    el('div.card.mt-2', {}, el('div.card-title', { text: 'Quantity' }), qtyStepper),
    howToRedeemCard(p),
    el('div.mt-3.row', { style: { gap: '12px' } }, addCart, buy),
  ));
}

function startBuy(p, state) {
  const { body } = openSheet({ title: 'Buy — ' + p.name, wide: true });
  const go = async () => {
    const info = await askSingleDelivery(body, p);
    if (!info) { closeSheet(); return; }
    createAndPay(p, state, body, info);
  };
  if (!isAuthed()) {
    body.append(
      el('div.center.mt-2.mb-2', {}, el('div.strong', { text: 'Connect your wallet to pay' })),
      el('button.btn.btn-gold.btn-block.btn-lg', { on: { click: () => openLogin(() => go()) } }, icon('wallet', 18), el('span.btn-label', { text: 'Connect Nimiq wallet' })),
    );
    return;
  }
  go();
}

async function createAndPay(p, state, body, info) {
  const email = (info && info.email) || '';
  let phone = (info && info.phone) || '';
  let denomination = state.denomination;
  let product_value = state.value;
  if (!denomination) {
    if (p.range) { denomination = 'range'; product_value = state.value; }
    else if (p.packages && p.packages.length) { const first = p.packages[0]; denomination = first.denomination || `${first.value} ${first.currency || p.currency}`.trim(); product_value = first.value; }
  }
  if (denomination === 'range' && (!product_value || product_value <= 0)) product_value = p.range ? p.range.min : state.value;
  // Last-resort guard: never send an empty cart to the supplier. With the
  // country-proof parser this should be unreachable (label-only packages
  // always carry a denomination), but a blank request is exactly what used
  // to produce the confusing "denomination is required" toast.
  if (!denomination && !(product_value > 0)) {
    toast(`This product is temporarily unavailable in ${p.country || 'this country'}.`, 'error');
    closeSheet();
    return;
  }
  // TOP-UP FIX: the number was already collected (and live-validated with
  // the supplier) in the shared delivery step — no second email/phone
  // round-trip here anymore.
  if (p.type === 'phone_refill' && !phone) { toast('Enter the number to top up.', 'error'); closeSheet(); return; }
  const req = buildOrderRequest(
    { id: p.id, type: p.type, country: p.country, qty: state.qty, denomination, value: product_value },
    { email, phone, gift: true }, // single-item flow: gift extras (if any) belong to this purchase
  );
  const quote = await createQuoteStep(body, req);
  if (!quote) { closeSheet(); return; }
  drawQuote(body, quote);
}

function drawQuote(body, quote) {
  const qty = Number(quote.quantity) || 1;
  renderQuotePayment(body, quote, {
    qtyNote: qty > 1 ? el('div.alert.info.mt-1', { style: { marginBottom: 0 } }, icon('gift', 16), el('div.small', { text: `${qty} codes will be delivered — the NIM amount below covers all ${qty} (≈ ${fmtNIM((quote.estimated_nim || 0) / qty, 0)} NIM each).` })) : null,
    footerNote: el('div.small.muted.mt-1', { style: { display: 'flex', gap: '8px', alignItems: 'center' } }, icon('gift', 50), el('span', { text: 'After payment: your code / top-up confirmation arrives by email and on the Orders page — see “How to redeem” on this product.' })),
  });
}
load();
