/**
 * catalog-bounds.mjs — the REAL purchasable face range of one family, taken
 * from its live products payload, and the label rewriting that puts it on the
 * brand card.
 *
 * WHY THIS EXISTS
 * A brand card's `min`/`max` labels come from the supplier's /v2/brands
 * directory — an aspirational range ("€5 – €2,000" for Amazon.de) that can
 * disagree with what the family actually sells (its SKUs are €5/€10/€50/€75,
 * its dynamic range stops at the Lightning ceiling). The product page computes
 * the truth from the products list; the home page card repeated the directory
 * label, so one product showed two different maxima and the card promised
 * amounts the Lightning rail can never sell. The catalog sync already fetches
 * every family's products for the stock check — these helpers read the same
 * payload and rewrite the card labels to the product page's own numbers.
 *
 * The Lightning per-order ceiling (SUPPLIER_ORDER_LIMIT_EUR, the supplier's
 * total incl. its fee) is applied the same way the backend applies it
 * (catalog.OrderLimit): face cap = total / (1 + margin/100), converted with
 * the market FX table, fixed denominations above the cap hidden, dynamic
 * ranges clamped to it.
 */

/** Structured supplier values only — mirrors src/lib/productMoney.ts. */
export function positiveAmount(raw) {
  if (typeof raw !== 'number' && typeof raw !== 'string') return 0;
  if (typeof raw === 'string' && !/^[+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(raw.trim())) return 0;
  const value = Number(raw);
  return Number.isFinite(value) && value > 0 && value < 100_000_000 ? value : 0;
}

export function productMoney(p) {
  let currency = String(p?.currency_code || '').trim().toUpperCase();
  const amount = positiveAmount(p?.amount);
  if (amount) return { value: amount, currency };
  let fv = p?.face_value;
  if (typeof fv === 'string') {
    try { fv = JSON.parse(fv); } catch { return { value: 0, currency }; }
  }
  if (fv && typeof fv === 'object') {
    currency = String(fv.currency_code || currency).trim().toUpperCase();
    fv = fv.amount;
    if (fv && typeof fv === 'object') fv = fv.value ?? fv.price;
  }
  return { value: positiveAmount(fv), currency };
}

/**
 * parseMoneyLabel — a whole money label ("€2,000", "50 USD", "150.000 IDR").
 * Mirrors the backend's catalog.ParseDenominationLabel: display only, never
 * used to price an order. Also returns the label's STYLE (prefix symbol /
 * suffix code / thousands separator) so a rewritten label keeps it.
 */
const SYMBOLS = { $: 'USD', '€': 'EUR', '£': 'GBP', '¥': 'JPY', '₩': 'KRW', '₺': 'TRY', '₹': 'INR', '₽': 'RUB', '﷼': 'SAR', TL: 'TRY' };
const moneyPrefix = /^([$€£¥₩₺₹₽﷼]|[A-Z]{3}|TL)\s*/;
const moneySuffix = /\s*([$€£¥₩₺₹₽﷼]|[A-Z]{3}|TL)$/;
const moneyNumber = /^[0-9](?:[0-9.,\s\u00A0\u202F]*[0-9])?$/;

export function parseMoneyLabel(label) {
  const raw = String(label ?? '').trim();
  let token = '';
  let body = raw;
  let where = 'none';
  let m = raw.match(moneyPrefix);
  if (m) {
    token = m[1];
    body = raw.slice(m[0].length).trim();
    where = 'prefix';
  } else if ((m = raw.match(moneySuffix))) {
    token = m[1];
    body = raw.slice(0, raw.length - m[0].length).trim();
    where = 'suffix';
  }
  if (!moneyNumber.test(body)) return { value: 0, currency: '', style: null };
  const code = SYMBOLS[token] || (token.length === 3 ? token : '');
  const digits = body;
  let sep = '';
  for (const cand of [',', '.', ' ', '\u00A0', '\u202F']) {
    const i = digits.indexOf(cand);
    if (i > 0 && digits.length - i - cand.length === 3) { sep = cand; break; }
  }
  return { value: parseAmount(digits), currency: code, style: { where, token, sep } };
}

function parseAmount(s) {
  let t = String(s).replace(/[\s\u00A0\u202F]/g, '');
  const lastDot = t.lastIndexOf('.');
  const lastComma = t.lastIndexOf(',');
  if (lastDot >= 0 && lastComma >= 0) {
    t = lastDot > lastComma ? t.replace(/,/g, '') : t.replace(/\./g, '').replace(/,/g, '.');
  } else if (lastComma >= 0) {
    t = t.split(',').length > 2 || (t.length - lastComma - 1) === 3 ? t.replace(/,/g, '') : t.replace(/,/g, '.');
  }
  const v = Number(t);
  return Number.isFinite(v) && v > 0 && v < 100_000_000 ? v : 0;
}

/** Group digits with the label's own thousands separator ("2,000"). */
function groupDigits(value, sep) {
  const digits = String(Math.round(value));
  if (!sep) return digits;
  let out = '';
  for (let i = 0; i < digits.length; i += 1) {
    if (i > 0 && (digits.length - i) % 3 === 0) out += sep;
    out += digits[i];
  }
  return out;
}

/** Write `value` in the original label's style ("€2,000" → "€106"). */
export function formatMoneyLike(value, code, style) {
  if (!style || !(value > 0)) return '';
  const body = groupDigits(value, style.sep);
  if (style.where === 'prefix') return `${style.token}${style.token === 'TL' ? ' ' : ''}${body}`;
  if (style.where === 'suffix') return `${body} ${style.token}`;
  return `${body} ${code}`.trim();
}

/** Mirrors backend catalog.OrderLimit: face cap = total / (1 + margin/100). */
export function faceCapEur(limits = {}) {
  const total = Number(limits.totalEur ?? limits.SUPPLIER_ORDER_LIMIT_EUR ?? 100);
  const margin = Number(limits.marginPct ?? limits.SUPPLIER_ORDER_MARGIN_PCT ?? 5);
  if (!(total > 0)) return 0;
  const m = Number.isFinite(margin) && margin >= 0 ? margin : 0;
  return total / (1 + m / 100);
}

/** The face cap in `code`, rounded DOWN to whole units (0 = unknown currency).
 *  Mirrors catalog.OrderLimit.FaceCapIn. */
export function faceCapIn(fx, code, limits = {}) {
  const capEur = faceCapEur(limits);
  if (!(capEur > 0)) return 0;
  const c = String(code || '').trim().toUpperCase();
  const usd = fx && fx[c];
  const usdEur = fx && fx.EUR;
  if (!Number.isFinite(usd) || !(usd > 0) || !Number.isFinite(usdEur) || !(usdEur > 0)) return 0;
  if (c === 'EUR') return Math.floor(capEur);
  return Math.floor((capEur * usdEur) / usd);
}

/** Same exclusions as the product page (src/lib/catalogAvailability.ts). */
function usableProduct(p) {
  if (!p || typeof p !== 'object') return false;
  if (p.is_out_of_stock === true) return false;
  if (String(p.delivery_type || '').toLowerCase() === 'by_account') return false;
  if (p.range) return Number(p.range.min) > 0 && Number(p.range.max) >= Number(p.range.min);
  return !!(p.product_id || p.denomination || p.localized_denomination);
}

/**
 * familyFaceBounds — what the product page would show for this family:
 * the extreme face values of its usable SKUs / dynamic range, in the family's
 * ONE money. `fixed` keeps every denomination with its display label (the
 * product page's own chip), `range` the raw dynamic bounds. Null when the
 * family carries no money the card could honestly quote.
 *
 * Accepts BOTH wire shapes: a flat SKU list, or the /v5/products payload —
 * an array of family objects each carrying its SKUs under `products` (the
 * product page merges those; the card must see the same union).
 */
export function familyFaceBounds(products) {
  if (!Array.isArray(products)) return null;
  const flat = [];
  for (const p of products) {
    if (p && Array.isArray(p.products)) flat.push(...p.products);
    else if (p && typeof p === 'object') flat.push(p);
  }
  const fixed = [];
  let range = null;
  let currency = '';
  for (const p of flat) {
    if (!usableProduct(p)) continue;
    if (p.range) {
      const c = String(p.range.currency || '').trim().toUpperCase();
      const lo = Number(p.range.min);
      const hi = Number(p.range.max);
      if (!(lo > 0) || !(hi >= lo) || !c) continue;
      if (currency && currency !== c) return null; // mixed money: leave the label alone
      currency = c;
      range = range ? { min: Math.min(range.min, lo), max: Math.max(range.max, hi) } : { min: lo, max: hi };
      continue;
    }
    let money = productMoney(p);
    let label = String(p.localized_denomination || p.denomination || '').trim();
    if (!(money.value > 0)) {
      const parsed = parseMoneyLabel(label);
      if (parsed.value > 0) money = parsed;
    }
    if (!(money.value > 0)) continue;
    const c = String(money.currency || '').trim().toUpperCase();
    if (!c) continue;
    if (currency && currency !== c) return null;
    currency = c;
    fixed.push({ v: money.value, label });
  }
  if (!currency || (!fixed.length && !range)) return null;
  fixed.sort((a, b) => a.v - b.v);
  return { currency, fixed, range };
}

/**
 * applyFaceBoundsToBrand — rewrite one brand card's min/max labels to the
 * family's REAL bounds, with the Lightning face cap applied exactly like the
 * product page (denominations above the cap are hidden, ranges clamp to it).
 * Keeps the original labels' style. Returns true when the card changed.
 */
export function applyFaceBoundsToBrand(b, bounds, { fx, limits } = {}) {
  if (!b || !bounds || !fx) return false;
  const cap = faceCapIn(fx, bounds.currency, limits);
  const fixed = cap > 0 ? bounds.fixed.filter((f) => f.v <= cap) : bounds.fixed;
  const loPoints = fixed.map((f) => f.v);
  const hiPoints = fixed.map((f) => f.v);
  // A dynamic range that starts above the cap is unbuyable at this ceiling —
  // drop it, but keep any fixed denomination that still fits (mixed family).
  if (bounds.range && (cap <= 0 || bounds.range.min <= cap)) {
    loPoints.push(bounds.range.min);
    hiPoints.push(cap > 0 ? Math.min(bounds.range.max, cap) : bounds.range.max);
  }
  if (!loPoints.length) return false;
  const lo = Math.min(...loPoints);
  const hi = Math.max(...hiPoints);
  if (!(lo > 0) || !(hi >= lo)) return false;

  const loFixed = fixed.find((f) => f.v === lo);
  const hiFixed = fixed.find((f) => f.v === hi);
  const minStyle = parseMoneyLabel(b.min).style;
  const maxStyle = parseMoneyLabel(b.max).style;
  const loLabel = (loFixed && loFixed.label) || formatMoneyLike(lo, bounds.currency, minStyle) || `${lo} ${bounds.currency}`;
  const hiLabel = (hiFixed && hiFixed.label) || formatMoneyLike(hi, bounds.currency, maxStyle) || `${hi} ${bounds.currency}`;
  // Only MONEY cards get rewritten. A family whose display labels are data
  // allowances ("1 GB 3 Days" eSIMs) keeps the supplier's own wording — the
  // card's min/max there describe plans, not prices.
  if (!parseMoneyLabel(loLabel).currency || !parseMoneyLabel(hiLabel).currency) return false;
  if (b.min === loLabel && b.max === hiLabel) return false;
  b.min = loLabel;
  b.max = hiLabel;
  return true;
}
