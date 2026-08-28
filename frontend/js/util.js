/* util.js — DOM helpers, formatters, icons.
 *
 * XSS policy: this app never assigns innerHTML from data. All DOM is built
 * with `el()` / textContent / attribute setters, so API responses, product
 * names, support messages etc. can never inject markup or scripts.
 */

import { ICON_MARKUP } from './icons.js';

export const CFG = window.APP_CONFIG || { API_BASE: '/api', HUB_URL: 'https://hub.nimiq.com', APP_NAME: 'nim.shop', NETWORK: 'mainnet' };

export const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/** Build a DOM element. `el('div.class#id', {attr: v, on: {click: fn}}, ...children) */
export function el(spec, attrs = {}, ...children) {
  let tag = 'div';
  let className = '';
  let id = '';
  const m = String(spec).match(/^([a-zA-Z0-9-]+)?([.#][\w-]+)*$/);
  if (m) {
    const parts = String(spec).split(/(?=[.#])/);
    for (const p of parts) {
      if (!p) continue;
      if (p.startsWith('.')) className += (className ? ' ' : '') + p.slice(1);
      else if (p.startsWith('#')) id = p.slice(1);
      else tag = p;
    }
  } else {
    tag = String(spec);
  }
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (id) node.id = id;
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'on' && typeof v === 'object') {
      for (const [evt, fn] of Object.entries(v)) node.addEventListener(evt, fn);
    } else if (k === 'style' && typeof v === 'object') {
      Object.assign(node.style, v);
    } else if (k === 'text') {
      node.textContent = v; // safe: never parsed as HTML
    } else if (k === 'html') {
      // Escape hatch for TRUSTED, hard-coded markup only (icons etc.).
      // Never pass user/API data here.
      node.innerHTML = v;
    } else if (k in node && k !== 'list' && k !== 'form' && typeof v !== 'string') {
      try { node[k] = v; } catch { node.setAttribute(k, v); }
    } else {
      node.setAttribute(k, v === true ? '' : v);
    }
  }
  appendChildren(node, children);
  return node;
}

function appendChildren(node, children) {
  // NULL-GUARD: async renders (polls, awaited lists) can resolve after the
  // view was swapped away — the old code crashed here with
  // "Cannot read properties of null (reading 'append')". A missing parent
  // now drops the render silently instead of killing the page.
  if (!node) { console.warn('[ui] appendChildren: parent is null — render skipped'); return; }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
}

/** Fragment helper */
export function clear(node) { if (!node) return; while (node.firstChild) node.removeChild(node.firstChild); }

/* NULL-GUARD: same race as appendChildren — a view swapped mid-await makes
 * the target selector null; skip instead of throwing. */
export function replaceChildren(node, ...children) { if (!node) { console.warn('[ui] replaceChildren: target is null — render skipped'); return; } clear(node); appendChildren(node, children); }

/* ---------- formatting ---------- */

// fmtMoney renders an amount in its OWN currency ("₺250.00", "$25.00") —
// the cart used to print every local value with a $ sign ("$250.00" for a
// 250 TRY package — nonsense).
export function fmtMoney(value, currency) {
  const n = Number(value);
  if (!isFinite(n)) return '—';
  const code = String(currency || 'USD').toUpperCase();
  try {
    return new Intl.NumberFormat('en-US', { style: 'currency', currency: code, currencyDisplay: 'narrowSymbol', maximumFractionDigits: n % 1 === 0 ? 0 : 2 }).format(n);
  } catch { return `${n} ${code}`; }
}

export function fmtUSD(value, { compact = false } = {}) {
  const n = typeof value === 'string' ? parseFloat(value) : Number(value);
  if (!isFinite(n)) return '—';
  if (compact && Math.abs(n) >= 1000) {
    return '$' + n.toLocaleString('en-US', { maximumFractionDigits: 0 });
  }
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 2 });
}

export function formatWalletAddress(addr, group = 4) {
  const raw = String(addr || '').replace(/\s+/g, '');
  if (!raw) return '';
  return raw.match(new RegExp(`.{1,${group}}`, 'g'))?.join(' ') || raw;
}

export function fmtNIM(value, decimals = 5) {
  const n = typeof value === 'string' ? parseFloat(value) : Number(value);
  if (!isFinite(n)) return '—';
  return n.toLocaleString('en-US', { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
}

export function fmtNum(value) {
  const n = Number(value);
  if (!isFinite(n)) return '—';
  return n.toLocaleString('en-US');
}

export function fmtDate(iso, withTime = true) {
  const d = iso instanceof Date ? iso : new Date(iso);
  if (isNaN(d)) return '—';
  const date = d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
  if (!withTime) return date;
  return date + ', ' + d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' });
}

export function timeAgo(iso) {
  const d = iso instanceof Date ? iso : new Date(iso);
  if (isNaN(d)) return '';
  const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
  if (s < 45) return 'just now';
  if (s < 3600) return Math.round(s / 60) + ' min ago';
  if (s < 86400) return Math.round(s / 3600) + ' h ago';
  if (s < 7 * 86400) return Math.round(s / 86400) + ' d ago';
  return fmtDate(iso, false);
}

export function shortAddr(addr, lead = 9, tail = 6) {
  if (!addr) return '';
  const a = String(addr).replace(/\s+/g, ' ').trim();
  if (a.length <= lead + tail + 1) return a;
  return a.slice(0, lead) + '…' + a.slice(-tail);
}

/** ISO 3166-1 alpha-2 → flag emoji (graceful on systems without flag glyphs) */
/** stripHtml — supplier text fields (redeem_geo, product_tc…) sometimes
 * arrive as HTML. Anywhere they are rendered as plain TEXT the tags must go,
 * or the UI shows literal <p>…</p>. Also unescapes the common entities. */
export function stripHtml(s) {
  return String(s || '')
    .replace(/<[^>]*>/g, ' ')
    .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&nbsp;/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

export function flag(country) {
  if (!country || !/^[a-zA-Z]{2}$/.test(country)) return '🌐';
  const base = 0x1F1E6;
  const up = country.toUpperCase();
  return String.fromCodePoint(base + up.charCodeAt(0) - 65, base + up.charCodeAt(1) - 65);
}

export function countryName(code) {
  try {
    const dn = new Intl.DisplayNames(['en'], { type: 'region' });
    return dn.of(String(code || '').toUpperCase()) || String(code || '').toUpperCase();
  } catch {
    return String(code || '').toUpperCase();
  }
}

export function uuid() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export function bytesToHex(bytes) {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}


export function debounce(fn, ms = 250) {
  let t;
  return (...args) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

/** Clipboard with fallback. Returns true on success. */
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    try {
      const ta = el('textarea', { style: { position: 'fixed', opacity: '0' }, value: text });
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      return ok;
    } catch {
      return false;
    }
  }
}

/** Only http(s) URLs may be used as link targets. Third-party data
 * (e.g. supplier redemption links) must pass through this before href. */
export function safeHref(url) {
  try {
    const u = new URL(String(url || ''), location.href);
    if (u.protocol === 'http:' || u.protocol === 'https:') return u.href;
  } catch { /* fall through */ }
  return null;
}

/** Parse a URL query param safely. */
export function queryParam(name) {
  try {
    return new URLSearchParams(location.search).get(name) || '';
  } catch {
    return '';
  }
}

/* ---------- icons: Lucide (https://lucide.dev) ----------
 * Every UI icon is a genuine Lucide icon. The SVG markup lives in
 * js/icons.js — extracted verbatim from the official lucide-static
 * package (ISC license). No hand-drawn SVG paths anywhere. */

export function icon(name, size = 20) {
  const markup = ICON_MARKUP[name] || ICON_MARKUP.info;
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '2');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  // Trusted, hard-coded Lucide markup only — never user/API data (XSS policy above).
  svg.innerHTML = markup;
  return svg;
}

/** Brand mark: the shop's own icon (replaces the old Lucide "bolt" SVG). */
export function brandMark(size = 50) {
  const img = el('img', { src: '/img/brand-icon-96.png', alt: '', width: size, height: size, style: { display: 'block', borderRadius: '6px' } });
  img.setAttribute('aria-hidden', 'true');
  return img;
}

/* ---------------- Page-scoped timers (SPA-safe polling) ----------------
 * The shell swaps page content WITHOUT reloading the document, so a page
 * module's plain setInterval kept firing forever after the visitor navigated
 * away — the Activity page polled /api/activity + /api/presence every 15/30s,
 * Support polled tickets, Orders re-fetched… all in the background, for the
 * rest of the session. pageInterval() registers the timer on the CURRENT
 * page; the shell dispatches 'nimshop:pagechange' right before rendering the
 * next page and every registered interval is cleared with it.
 */
const _pageTimers = new Set();
if (typeof window !== 'undefined') {
  window.addEventListener('nimshop:pagechange', () => {
    for (const t of _pageTimers) clearInterval(t);
    _pageTimers.clear();
  });
}
export function pageInterval(fn, ms) {
  const t = setInterval(fn, ms);
  _pageTimers.add(t);
  return t;
}

/* ---------------- Denomination / money parsing (country-proof) ----------
 * Supplier denomination labels vary by country: "100 USD", "TRY300",
 * "150.000 IDR", "1.000.000 VND", "1 000 AED", "5,000 IQD", "25,50 EUR",
 * "$25", "₩5,000", "120 TL", "٥٠٠ SAR", "Java & Bedrock Ed" (label-only).
 * The old parseFloat-based parsers read "150.000" as 150 and "1 000 AED"
 * as 0 — which dropped whole countries' products and made checkout fail
 * with "denomination is required". Keep these in lockstep with the
 * backend's parseMoneyAmount in internal/handlers/quote_handlers.go.
 */
export function parseMoneyAmount(input) {
  if (input == null) return 0;
  let t = String(input);
  // Unicode digit blocks → ASCII (Arabic-Indic, Extended, Devanagari)
  t = t.replace(/[\u0660-\u0669]/g, (d) => String(d.charCodeAt(0) - 0x0660));
  t = t.replace(/[\u06F0-\u06F9]/g, (d) => String(d.charCodeAt(0) - 0x06F0));
  t = t.replace(/[\u0966-\u096F]/g, (d) => String(d.charCodeAt(0) - 0x0966));
  // Numeric core: first digit … last digit, separators only in between.
  const m = t.match(/\d(?:[\d.,\s\u00A0\u202F]*\d)?/);
  if (!m) return 0;
  t = m[0].replace(/[\s\u00A0\u202F]/g, '');
  const lastDot = t.lastIndexOf('.'), lastComma = t.lastIndexOf(',');
  if (lastDot >= 0 && lastComma >= 0) {
    if (lastDot > lastComma) t = t.replace(/,/g, '');                 // 1,234.56
    else t = t.replace(/\./g, '').replace(/,/g, '.');                 // 1.234,56
  } else if (lastComma >= 0) {
    const parts = t.split(',');
    const grouped = parts.length > 1 && parts.slice(1).every((p) => p.length === 3);
    t = grouped ? parts.join('') : t.replace(',', '.');               // 1,000,000 | 25,50
  } else if (lastDot >= 0) {
    const parts = t.split('.');
    const grouped = parts.length > 1 && parts.slice(1).every((p) => p.length === 3);
    if (grouped) t = parts.join('');                                  // 1.000.000
    // else plain decimal: keep
  }
  const v = parseFloat(t);
  return isFinite(v) && v >= 0 ? v : 0;
}

const _CURRENCY_SYMBOLS = { '$': 'USD', '€': 'EUR', '£': 'GBP', '¥': 'JPY', '₩': 'KRW', '₺': 'TRY', '₹': 'INR', '₽': 'RUB', '﷼': 'SAR' };

export function parseCurrencyValue(str) {
  if (!str) return { currency: '', value: 0, raw: '' };
  const raw = String(str).trim();
  let currency = '';
  let m;
  if ((m = raw.match(/([\$€£¥₩₺₹₽﷼])/))) currency = _CURRENCY_SYMBOLS[m[1]] || m[1];
  else if ((m = raw.match(/^([A-Z]{3})\b/))) currency = m[1];          // "TRY300", "IDR150.000"
  else if ((m = raw.match(/\s([A-Z]{3})\s*$|\s([A-Z]{3})\b/))) currency = m[1] || m[2]; // "100 USD"
  else if ((m = raw.match(/\s([A-Z]{2})\s*$/))) currency = m[1];       // "120 TL"
  return { currency, value: parseMoneyAmount(raw), raw };
}

/* ---------------- Payment countdown ----------------
 * Pay sheets show how long the Lightning invoice stays payable
 * ("⏳ Time left to pay: 29m 12s"), ticking every second. At zero the
 * caller's onExpire runs (disable the pay link / fail the checkout) and
 * the timer stops. The timer also stops when the visitor navigates away.
 */
export function payCountdown(el_, expiresAt, onExpire) {
  if (!expiresAt) return null;
  const exp = new Date(expiresAt).getTime();
  if (!isFinite(exp)) return null;
  const fmt = (ms) => {
    const s = Math.max(0, Math.ceil(ms / 1000));
    const m = Math.floor(s / 60);
    return `${m}m ${String(s % 60).padStart(2, '0')}s`;
  };
  let t = null;
  const update = () => {
    const ms = exp - Date.now();
    if (ms <= 0) {
      el_.textContent = '⏳ Payment window expired — start a new order.';
      el_.style.color = 'var(--orange, #c7481d)';
      el_.style.fontWeight = '800';
      if (t) clearInterval(t);
      if (onExpire) { try { onExpire(); } catch {} }
      return;
    }
    el_.textContent = '⏳ Time left to pay: ' + fmt(ms);
  };
  update();
  t = setInterval(update, 1000);
  if (typeof window !== 'undefined') {
    window.addEventListener('nimshop:pagechange', () => { if (t) clearInterval(t); }, { once: true });
  }
  return t;
}

/** Renders a SANITIZED rich-HTML string (see safeRichHTML) into a real DOM
 *  node. el() turns plain strings into text nodes — which made the sanitized
 *  supplier HTML show up as literal <p>/<li> text on screen. This helper is
 *  the ONLY correct way to mount safeRichHTML output. */
export function richNode(html, cls = 'rich-html') {
  const classes = String(cls || '').split(/[\s.]+/).filter(Boolean);
  const node = el('div' + classes.map((c) => '.' + c).join(''));
  node.innerHTML = safeRichHTML(html);
  return node;
}

/* ---------------- safeRichHTML: supplier rich-content sanitizer ----------
 * CryptoRefills' API returns per-brand HTML for "How to redeem",
 * "Terms and conditions" and the description. We render it (it is the
 * supplier's official wording, links included) AFTER sanitizing:
 *   - <script>/<iframe>/<object>/<embed>/<style> removed
 *   - on* event attributes stripped
 *   - javascript:/data: URLs stripped
 *   - every <a> forced to target=_blank + rel=noopener noreferrer nofollow
 * CSP (script-src 'self') already blocks injected scripts as belt-and-braces.
 */
export function safeRichHTML(html) {
  if (!html || typeof html !== 'string') return '';
  if (typeof DOMParser === 'undefined') return '';
  try {
    const doc = new DOMParser().parseFromString('<div id="__r">' + html + '</div>', 'text/html');
    const root = doc.getElementById('__r');
    for (const bad of root.querySelectorAll('script, iframe, object, embed, style, link, meta')) bad.remove();
    for (const el_ of root.querySelectorAll('*')) {
      for (const attr of [...el_.attributes]) {
        const name = attr.name.toLowerCase();
        if (name.startsWith('on')) el_.removeAttribute(attr.name);
        else if ((name === 'href' || name === 'src') && /^\s*(javascript|data|vbscript):/i.test(attr.value)) el_.removeAttribute(attr.name);
      }
      if (el_.tagName === 'A') {
        el_.setAttribute('target', '_blank');
        el_.setAttribute('rel', 'noopener noreferrer nofollow');
      }
    }
    return root.innerHTML;
  } catch { return ''; }
}

/* ---------------- Shared catalog/order helpers ----------------
 * One canonical copy of helpers that used to be copy-pasted across pages
 * (home/orders cleanFamilyName, order/product cleanSupplierTerms,
 * orders/order/product/home face-value parsing, MAX_QTY). Everything
 * imports from here so a fix lands everywhere at once.
 */

// Backend MaxOrderQuantity — the same ceiling everywhere (product qty
// stepper and cart qty controls).
export const MAX_QTY = 10;

// Fallback currency for the local face-value label, keyed by country
// (range products whose denomination is just "range").
export const COUNTRY_CCY = { TR: 'TRY', US: 'USD', GB: 'GBP', DE: 'EUR', FR: 'EUR', ES: 'EUR', IT: 'EUR', NL: 'EUR', CA: 'CAD', BR: 'BRL', IN: 'INR', AU: 'AUD', JP: 'JPY', PL: 'PLN', MX: 'MXN' };

/* The local-currency face value a quote/order carries, resolved from
 * WHICHEVER field holds it: parsed denomination label ("25 USD",
 * "150.000 IDR"), _currency, product_value, or the country fallback.
 * Returns { value, currency, label }. */
export function quoteFaceValue(q, { country = '' } = {}) {
  if (!q) return { value: 0, currency: '', label: '' };
  const parsed = parseCurrencyValue(String(q.denomination || ''));
  let ccy = parsed.currency;
  if (!ccy) {
    const m = String(q.denomination || '').match(/([A-Z]{3})\s*$/);
    if (m) ccy = m[1].toUpperCase();
  }
  ccy = ccy || q._currency || COUNTRY_CCY[String((q.product_country || q.country || country) || '').toUpperCase()] || '';
  const value = (parsed.value > 0 ? parsed.value : 0) || (Number(q.product_value) > 0 ? Number(q.product_value) : 0);
  const label = (q.denomination && q.denomination !== 'range') ? String(q.denomination) : '';
  return { value, currency: ccy, label };
}

/* Supplier family names sometimes carry markdown decoration. Only unwrap a
 * whole-string markdown link ("[Name](url)" → "Name"); NOTHING else may
 * change — this string doubles as the product ID sent to the catalog, and
 * the supplier lookup is exact-match. */
export function cleanFamilyName(name) {
  if (!name) return '';
  let s = String(name).trim();
  const mdMatch = s.match(/^\[(.+?)\]\(.+?\)$/);
  if (mdMatch) s = mdMatch[1].trim();
  return s;
}

/* Strip supplier terms HTML/markdown down to plain truncated text. */
export function cleanSupplierTerms(tc) {
  let s = stripHtml(String(tc || '')).trim(); // strip supplier HTML before the markdown pass
  if (!s) return '';
  s = s.replace(/\[([^]]*)\]\([^)]*\)/g, '$1');   // [text](url) -> text
  s = s.replace(/!\[[^]]*\]\([^)]*\)/g, '');      // images
  s = s.replace(/https?:\/\/\S+/g, '');            // bare urls
  s = s.replace(/[*_#`>|]+/g, ' ');                // md markers
  s = s.replace(/\s+/g, ' ').trim();
  if (s.length > 420) s = s.slice(0, 420).trim() + '…';
  return s;
}

/* ---------------- duration / countdown labels (shared) ---------------- */
/* One human duration formatter for the activity feed, the orders list and
 * the profile countdown — they used to each format durations with slightly
 * different rules. fmtDuration takes SECONDS (activity/orders data),
 * fmtCountdown takes MILLISECONDS (countdown-to-reset). */
export function fmtDuration(sec) {
  if (!sec || sec <= 0) return '—';
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.round(sec / 60) + ' min';
  return Math.floor(sec / 3600) + 'h ' + Math.round(((sec % 3600) / 60)) + 'm';
}

export function fmtCountdown(ms) {
  if (!isFinite(ms) || ms <= 0) return 'under a minute';
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
