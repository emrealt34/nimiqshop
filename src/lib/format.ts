import { positiveAmount } from './productMoney';
import { countryLabel } from './countries';
import { getLang, t as tr } from '../i18n';

/** Locale tag for Intl number/date formatting. English keeps the historical
 *  en-US output byte-for-byte; every other language formats natively. */
function localeTag(): string {
  const tags: Record<string, string> = { en: 'en-US', es: 'es-ES', de: 'de-DE', fr: 'fr-FR', pt: 'pt-BR', tr: 'tr-TR' };
  return tags[getLang()] || 'en-US';
}

/**
 * format.ts — pure formatting / parsing helpers ported verbatim from the
 * original util.js. Everything here is DOM-free so it is safe on the server
 * and in React render functions.
 */

/* ---------- money / address formatting ---------- */

// fmtMoney renders an amount in its OWN currency ("₺250.00", "$25.00").
export function fmtMoney(value: number | string | null | undefined, currency?: string): string {
  const n = Number(value);
  if (!isFinite(n)) return '—';
  const code = String(currency || 'USD').toUpperCase();
  try {
    return new Intl.NumberFormat(localeTag(), {
      style: 'currency',
      currency: code,
      currencyDisplay: 'narrowSymbol',
      maximumFractionDigits: n % 1 === 0 ? 0 : 2,
    }).format(n);
  } catch {
    return `${n} ${code}`;
  }
}

export function fmtUSD(value: number | string | null | undefined, { compact = false }: { compact?: boolean } = {}): string {
  const n = typeof value === 'string' ? parseFloat(value) : Number(value);
  if (!isFinite(n)) return '—';
  if (compact && Math.abs(n) >= 1000) {
    return '$' + n.toLocaleString(localeTag(), { maximumFractionDigits: 0 });
  }
  return n.toLocaleString(localeTag(), { style: 'currency', currency: 'USD', maximumFractionDigits: 2 });
}

export function formatWalletAddress(addr?: string | null, group = 4): string {
  const raw = String(addr || '').replace(/\s+/g, '');
  if (!raw) return '';
  return raw.match(new RegExp(`.{1,${group}}`, 'g'))?.join(' ') || raw;
}

export function fmtNIM(value: number | string | null | undefined, decimals = 0): string {
  const n = typeof value === 'string' ? parseFloat(value) : Number(value);
  if (!isFinite(n)) return '—';
  return n.toLocaleString(localeTag(), { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
}

export function fmtNum(value: number | string | null | undefined): string {
  const n = Number(value);
  if (!isFinite(n)) return '—';
  return n.toLocaleString(localeTag());
}

export function fmtDate(iso: string | Date, withTime = true): string {
  const d = iso instanceof Date ? iso : new Date(iso);
  if (isNaN(d as unknown as number)) return '—';
  const date = d.toLocaleDateString(localeTag(), { month: 'short', day: 'numeric', year: 'numeric' });
  if (!withTime) return date;
  return date + ', ' + d.toLocaleTimeString(localeTag(), { hour: '2-digit', minute: '2-digit' });
}

/**
 * fmtClock — the EXACT time, with seconds.
 *
 * Owner (2026-10-06): "ben saniyede azsın derken 6 saat önce değil hani 10 am
 * tam saniyesi yazsın ki anladın mı". A relative age answers "how long ago" and
 * hides the moment; what he wants is the moment itself, to the second:
 * "10:32:07". Today shows the clock alone, another day prefixes the date. The
 * seconds are what make a refresh (a live read, a new stage) visibly land.
 */
export function fmtClock(iso?: string | number | Date | null): string {
  if (iso === null || iso === undefined || iso === '') return '';
  const d = iso instanceof Date ? iso : new Date(iso as any);
  if (isNaN(d as unknown as number)) return '';
  const time = d.toLocaleTimeString(localeTag(), { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  const now = new Date();
  const sameDay = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? time : d.toLocaleDateString(localeTag(), { day: 'numeric', month: 'short' }) + ' ' + time;
}

export function timeAgo(iso: string | Date): string {
  const d = iso instanceof Date ? iso : new Date(iso);
  if (isNaN(d as unknown as number)) return '';
  const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
  // Owner (2026-10-06): "live trackingde saniyede gösterirsen sevinirim,
  // dakika gösteriyorsun". Under a minute the age is now stated in SECONDS
  // ("42 saniye önce") instead of a flat "az önce", so a count that is refreshed
  // every second visibly moves. Above a minute nothing changed.
  if (s < 5) return tr('fmt.justNow');
  if (s < 60) return tr('fmt.secAgo', { n: Math.floor(s) });
  if (s < 3600) return tr('fmt.minAgo', { n: Math.round(s / 60) });
  if (s < 86400) return tr('fmt.hAgo', { n: Math.round(s / 3600) });
  if (s < 7 * 86400) return tr('fmt.dAgo', { n: Math.round(s / 86400) });
  return fmtDate(iso, false);
}

/** Nimiq addresses are ALWAYS shown in 4-char groups separated by spaces
 *  ("NQ77 AYF1 TRV7 …"), everywhere in the app — full or shortened.
 *  Short form shows the leading characters only, then an ellipsis at the end
 *  (e.g. "NQ12 CPDG 4KKU…"), never the "prefix…suffix" form. */
export function shortAddr(addr?: string | null, lead = 9, _tail = 6): string {
  if (!addr) return '';
  const a = String(addr).replace(/\s+/g, '');
  if (!a) return '';
  if (a.length <= lead + 1) return formatWalletAddress(a);
  return formatWalletAddress(a.slice(0, lead)) + '…';
}

/** ISO 3166-1 alpha-2 → flag emoji (graceful on systems without flag glyphs). */
export function flag(country?: string | null): string {
  if (!country || !/^[a-zA-Z]{2}$/.test(country)) return '🌐';
  const base = 0x1f1e6;
  const up = country.toUpperCase();
  return String.fromCodePoint(base + up.charCodeAt(0) - 65, base + up.charCodeAt(1) - 65);
}

export function countryName(code?: string | null): string {
  // Our own list wins — always return "Türkiye" for TR, never "Turkey".
  const own = countryLabel(code);
  if (own) return own;
  try {
    const dn = new Intl.DisplayNames([getLang() || 'en'], { type: 'region' });
    return dn.of(String(code || '').toUpperCase()) || String(code || '').toUpperCase();
  } catch {
    return String(code || '').toUpperCase();
  }
}

/** Supplier copy (descriptions, terms, region notes) can contain localised words.
 *  The shop is English-only, so display text is normalised before it is shown. */
export function enCopy(s: unknown): string {
  return String(s || '')
    .replace(/Turkey/g, 'Türkiye')
    .replace(/Turkish/g, 'Türkçe')
    .replace(/Turk Telecom/g, 'Türk Telekom');
}

/* ---------- text sanitization ---------- */

/** stripHtml — supplier text fields (redeem_geo, product_tc…) arrive as HTML. */
export function stripHtml(s: unknown): string {
  return String(s || '')
    .replace(/<[^>]*>/g, ' ')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&nbsp;/g, ' ')
    // &amp; last so "&amp;lt;" decodes to "&lt;" (one level), not "<".
    .replace(/&amp;/g, '&')
    .replace(/\s+/g, ' ')
    .trim();
}

/**
 * safeRichHTML — supplier rich-content sanitizer. Returns SANITIZED HTML
 * string (react dangerouslySetInnerHTML only — never user/API data in it).
 * CSP (script-src 'self') already blocks injected scripts as belt-and-braces.
 */
export function safeRichHTML(html: unknown): string {
  if (!html || typeof html !== 'string' || typeof DOMParser === 'undefined') return '';
  try {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const allowed = new Set(['P', 'BR', 'DIV', 'SPAN', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6',
      'UL', 'OL', 'LI', 'STRONG', 'B', 'EM', 'I', 'U', 'S', 'SMALL', 'SUP', 'SUB',
      'BLOCKQUOTE', 'CODE', 'PRE', 'HR', 'TABLE', 'THEAD', 'TBODY', 'TFOOT', 'TR', 'TH',
      'TD', 'DL', 'DT', 'DD', 'A', 'IMG']);
    const drop = new Set(['SCRIPT', 'STYLE', 'IFRAME', 'OBJECT', 'EMBED', 'SVG', 'MATH',
      'TEMPLATE', 'FORM', 'INPUT', 'BUTTON', 'TEXTAREA', 'SELECT', 'LINK', 'META', 'BASE']);
    const clean = (parent: Element) => {
      for (const node of Array.from(parent.children)) {
        if (drop.has(node.tagName)) { node.remove(); continue; }
        clean(node);
        if (!allowed.has(node.tagName)) { node.replaceWith(...Array.from(node.childNodes)); continue; }
        for (const attr of Array.from(node.attributes)) {
          const name = attr.name.toLowerCase();
          let keep = ['title', 'alt'].includes(name);
          if (['width', 'height', 'colspan', 'rowspan'].includes(name)) keep = /^\d{1,4}$/.test(attr.value);
          if ((node.tagName === 'A' && name === 'href') || (node.tagName === 'IMG' && name === 'src')) {
            try {
              // URL parsing also normalizes embedded tabs/newlines in a scheme.
              const url = new URL(attr.value, typeof location === 'undefined' ? 'https://shop.nimiqbase.com' : location.href);
              keep = ['http:', 'https:'].includes(url.protocol) || (node.tagName === 'A' && url.protocol === 'mailto:');
            } catch { keep = false; }
          }
          if (!keep) node.removeAttribute(attr.name);
        }
        if (node.tagName === 'A') {
          node.setAttribute('target', '_blank');
          node.setAttribute('rel', 'noopener noreferrer nofollow');
        }
        if (node.tagName === 'IMG') node.setAttribute('loading', 'lazy');
      }
    };
    clean(doc.body);
    return doc.body.innerHTML;
  } catch { return ''; }
}

/* ---------- shared catalog / order helpers ---------- */

// Backend MaxOrderQuantity — the same ceiling everywhere.
export const MAX_QTY = 100;

// Fallback currency for local face-value labels, keyed by country.
export const COUNTRY_CCY: Record<string, string> = {
  TR: 'TRY', US: 'USD', GB: 'GBP', DE: 'EUR', FR: 'EUR', ES: 'EUR', IT: 'EUR',
  NL: 'EUR', CA: 'CAD', BR: 'BRL', IN: 'INR', AU: 'AUD', JP: 'JPY', PL: 'PLN',
  MX: 'MXN',
};

/* ---------- denomination / money parsing (country-proof) ---------- */
export function parseMoneyAmount(input: unknown): number {
  if (input == null) return 0;
  let t = String(input).trim();
  t = t.replace(/[\u0660-\u0669]/g, (d) => String(d.charCodeAt(0) - 0x0660));
  t = t.replace(/[\u06f0-\u06f9]/g, (d) => String(d.charCodeAt(0) - 0x06f0));
  t = t.replace(/[\u0966-\u096f]/g, (d) => String(d.charCodeAt(0) - 0x0966));
  const m = t.match(/^\d(?:[\d.,\s\u00A0\u202F]*\d)?$/);
  if (!m) return 0;
  t = m[0].replace(/[\s\u00A0\u202F]/g, '');
  const lastDot = t.lastIndexOf('.');
  const lastComma = t.lastIndexOf(',');
  if (lastDot >= 0 && lastComma >= 0) {
    if (lastDot > lastComma) t = t.replace(/,/g, '');
    else t = t.replace(/\./g, '').replace(/,/g, '.');
  } else if (lastComma >= 0) {
    const parts = t.split(',');
    const grouped = parts.length > 1 && parts.slice(1).every((p) => p.length === 3);
    t = grouped ? parts.join('') : t.replace(',', '.');
  } else if (lastDot >= 0) {
    const parts = t.split('.');
    const grouped = parts.length > 1 && parts.slice(1).every((p) => p.length === 3);
    if (grouped) t = parts.join('');
  }
  const v = parseFloat(t);
  return isFinite(v) && v >= 0 ? v : 0;
}

const CURRENCY_SYMBOLS: Record<string, string> = {
  $: 'USD', '€': 'EUR', '£': 'GBP', '¥': 'JPY', '₩': 'KRW', '₺': 'TRY',
  '₹': 'INR', '₽': 'RUB', '﷼': 'SAR',
};

const CURRENCY_ALIASES_2: Record<string, string> = {
  TL: 'TRY',
};

/** Only for supplier monetary min/max fields, never SKU/name selection. */
export function parseCurrencyValue(str: unknown): { currency: string; value: number; raw: string } {
  const raw = String(str ?? '').trim();
  let number = raw;
  let currency = '';
  const prefix = number.match(/^([$€£¥₩₺₹₽﷼]|[A-Z]{3}|TL)\s*/);
  const suffix = number.match(/\s*([$€£¥₩₺₹₽﷼]|[A-Z]{3}|TL)$/);
  const token = prefix?.[1] || suffix?.[1];
  if (token) {
    currency = CURRENCY_SYMBOLS[token] || CURRENCY_ALIASES_2[token] || token;
    number = prefix ? number.slice(prefix[0].length) : number.slice(0, -suffix![0].length);
  }
  const value = parseMoneyAmount(number.trim());
  return { currency: value > 0 ? currency : '', value, raw };
}

/** The local-currency face value a quote/order carries. */
export function quoteFaceValue(
  q: { denomination?: unknown; product_currency?: unknown; _currency?: unknown; product_value?: unknown; product_country?: unknown; country?: unknown } | null | undefined,
  _opts: { country?: string } = {}
): { value: number; currency: string; label: string } {
  if (!q) return { value: 0, currency: '', label: '' };
  // Denomination is a display/SKU label, not an amount (e.g. 50 GB 90 days).
  const ccy = String(q.product_currency || q._currency || '').toUpperCase();
  const value = positiveAmount(q.product_value);
  const label = q.denomination && q.denomination !== 'range' ? String(q.denomination) : '';
  return { value, currency: ccy, label };
}

/** Supplier family names carry markdown decoration; only unwrap whole-string markdown links. */
export function cleanFamilyName(name: unknown): string {
  if (!name) return '';
  let s = String(name).trim();
  const mdMatch = s.match(/^\[(.+?)\]\(.+?\)$/);
  if (mdMatch) s = mdMatch[1].trim();
  return s;
}

/**
 * Human-facing product/family label. Supplier and legacy quote records can
 * contain Markdown links, a denomination in parentheses, a quantity, or a
 * country-specific domain suffix (for example `Amazon.com.tr`). None of that
 * belongs in an order/activity title or a brand-logo lookup.
 */
export function cleanProductLabel(value: unknown): string {
  const raw = typeof value === 'string'
    ? value
    : (value as any)?.product_id || (value as any)?.product || (value as any)?.product_name || (value as any)?.title || (value as any)?.name || '';
  let s = String(raw || '').trim();
  if (!s) return '';

  // A whole Markdown link is the product name. When a link is embedded in a
  // malformed joined title it is usually duplicated decoration, so remove the
  // link rather than leaving its label attached to the neighbouring brand.
  const wholeLink = s.match(/^\s*\*{0,2}\[([^\]]+)\]\([^)]*\)\*{0,2}\s*$/);
  if (wholeLink) s = wholeLink[1];
  else s = s.replace(/\[[^\]]+\]\([^)]*\)/g, '');

  s = s
    .replace(/\*{2}/g, '')
    .replace(/`/g, '')
    .replace(/\bhttps?:\/\/\S+/gi, '')
    // Denominations and quantities are suffixes, not brand names.
    .replace(/\s*\([^()]*\)\s*$/g, '')
    .replace(/\s*[×x]\s*\d+\s*$/i, '')
    .replace(/\s+\d+(?:[.,]\d+)?\s*(?:USD|EUR|GBP|TRY|JPY|CNY|CAD|AUD|CHF|INR|BRL|MXN|PLN|SEK|NOK|DKK|AED|SAR)\s*$/i, '')
    .replace(/\s+(?:USD|EUR|GBP|TRY|JPY|CNY|CAD|AUD|CHF|INR|BRL|MXN|PLN|SEK|NOK|DKK|AED|SAR)\s*\d+(?:[.,]\d+)?\s*$/i, '')
    .replace(/\s+/g, ' ')
    .trim();

  // Catalog families often use a country domain while the UI should show the
  // brand itself: Amazon.com.tr → Amazon, Amazon.co.uk → Amazon.
  s = s.replace(/(?:\.com|\.co|\.org|\.net)(?:\.[a-z]{2})?$/i, '').trim();
  return s;
}

/** Split a joined legacy title into clean, de-duplicated brand labels. */
export function cleanBatchProductLabels(value: unknown): string[] {
  const values = Array.isArray(value) ? value : [value];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const v of values) {
    const pieces = String(v ?? '').split(/\s*\+\s*/);
    for (const piece of pieces) {
      const label = cleanProductLabel(piece);
      const key = label.toLocaleLowerCase();
      if (label && !seen.has(key)) {
        seen.add(key);
        out.push(label);
      }
    }
  }
  return out;
}

/* ---------- supplier crypto-rail copy removal ---------- */

/**
 * The supplier (CryptoRefills) advertises its OWN payment rails inside its
 * product copy, e.g.:
 *
 *   "Buy now a Getir gift card with Bitcoin and other Crypto. … Pay with
 *    Bitcoin, Litecoin, Ethereum, USDC, USDT, MIM, EUROC, FRAX, EUROC, BUSD,
 *    and DAI on Lightning Network, Avalanche, Polygon, Fantom, Binance Chain,
 *    and Arbitrum."
 *
 * nim.shop only ever pays with Nimiq Pay, so every one of those sentences is
 * removed before supplier text reaches the screen. Applied to BOTH the
 * supplier terms (`cleanSupplierTerms`) and the product blurb
 * (`cleanDescription` in ProductPage), so the copy can never leak a coin or
 * chain this shop does not accept.
 */
const CRYPTO_RAIL_PATTERNS: RegExp[] = [
  /\(?\s*Pay with Bitcoin[\s\S]*?Arbitrum\s*\.?/gi,
  /\(?\s*Pay with Bitcoin[\s\S]*/gi,
  /Buy now (?:a|an)[\s\S]{0,160}?with Bitcoin and other Crypto\s*\.?/gi,
  /Pay with Bitcoin and other Crypto\s*\.?/gi,
  /,?\s*on (?:the )?Lightning Network[\s\S]*?Arbitrum\s*\.?/gi,
  /,?\s*and Arbitrum\s*\.?/gi,
];

/** Leftover coin/chain names that can survive a partial sentence match. */
const CRYPTO_NAME_RE =
  /\b(?:Lightning Network|Avalanche|Polygon|Fantom|Binance Smart Chain|Binance Chain|Arbitrum|Optimism|Tron|Solana|Litecoin)\b/gi;

/** Coin names the supplier lists in its rail advertising. */
const COIN_LIST_RE = /\b(?:Bitcoin|Ethereum|USDC|USDT|MIM|EUROC|FRAX|BUSD|DAI|Litecoin)\b/gi;

/** Supplier leftovers that are rail advertising in any language: the coin
 *  list sentence ("… ve DAI ile … ödeme yapın", "… and DAI on …") and the
 *  English delivery/checkout fragments CryptoRefills appends untranslated. */
const RAIL_LEFTOVER_RE = /instant e-?mail delivery|pay with nimiq pay/i;

/** Remove the supplier's own crypto-rail advertising from any supplier text.
 *  `scrubNames` also deletes stray coin/chain words — keep it ON for plain
 *  text, OFF for HTML (a chain name could otherwise be clipped out of a URL). */
export function stripCryptoCopy(s: unknown, { scrubNames = true }: { scrubNames?: boolean } = {}): string {
  let out = String(s || '');
  for (const re of CRYPTO_RAIL_PATTERNS) out = out.replace(re, '');
  if (scrubNames) out = out.replace(CRYPTO_NAME_RE, '');
  // The English patterns above cannot match a LOCALISED rail sentence — once
  // CryptoRefills translates it ("Bitcoin, Ethereum, USDC, … ve DAI ile …
  // ödeme yapın") it sailed straight through. Filter whole sentences that
  // carry two or more coin names (a single incidental mention survives), plus
  // the untranslated delivery/checkout fragments. Sentences keep their own
  // spacing, so joining with '' rebuilds the text minus the removed ones.
  out = (out.match(/[^.!?]+[.!?]*/g) || [out])
    .filter((sentence) => {
      const coins = sentence.match(COIN_LIST_RE);
      return (!coins || coins.length < 2) && !RAIL_LEFTOVER_RE.test(sentence);
    })
    .join('');
  return (
    out
      .replace(/\s{2,}/g, ' ')
      .replace(/\s*,\s*,/g, ',')
      .replace(/,\s*([.;:])/g, '$1')
      .replace(/\s+([.,;:])/g, '$1')
      .replace(/^[\s,;:]+/, '')
      .replace(/[\s,;:]+$/, '')
      .trim()
  );
}

/** Strip supplier terms HTML/markdown down to plain truncated text.
 *  The supplier's "Pay with Bitcoin, Litecoin … Arbitrum" advertising (and its
 *  localised coin-list twins) is removed; what remains is the supplier's own
 *  terms and nothing else — no coin list, no appended rail sentence, so the
 *  text ends where the supplier's real terms end. */
export function cleanSupplierTerms(tc: unknown): string {
  let s = stripHtml(String(tc || '')).trim();
  if (!s) return '';
  s = s.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1');
  s = s.replace(/!\[[^\]]*\]\([^)]*\)/g, '');
  s = s.replace(/https?:\/\/\S+/g, '');
  s = s.replace(/[*_#`>|]+/g, ' ');
  s = s.replace(/\s+/g, ' ').trim();
  s = stripCryptoCopy(s);
  if (!s) return '';
  if (s.length > 420) s = s.slice(0, 420).trim() + '…';
  return s;
}

/* ---------- duration / countdown labels ---------- */
export function fmtDuration(sec?: number | null): string {
  if (!sec || sec <= 0) return '—';
  if (sec < 60) return tr('fmt.secondsShort', { n: sec });
  if (sec < 3600) return tr('fmt.minShort', { n: Math.round(sec / 60) });
  return tr('fmt.hoursMinutes', { h: Math.floor(sec / 3600), m: Math.round((sec % 3600) / 60) });
}

export function fmtCountdown(ms?: number | null): string {
  if (!isFinite(ms as number) || (ms as number) <= 0) return tr('fmt.underMinute');
  const s = Math.round((ms as number) / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return tr('fmt.hoursMinutes', { h, m });
  if (m > 0) return tr('fmt.minutesSeconds', { m, s: s % 60 });
  return tr('fmt.secondsShort', { n: s });
}

/* ---------- misc ---------- */
export function uuid(): string {
  if (typeof crypto !== 'undefined' && (crypto as { randomUUID?: () => string }).randomUUID)
    return (crypto as { randomUUID: () => string }).randomUUID();
  const b = (crypto as Crypto).getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

export function debounce<A extends unknown[]>(fn: (...args: A) => void, ms = 250): (...args: A) => void {
  let t: ReturnType<typeof setTimeout> | undefined;
  return (...args: A) => {
    clearTimeout(t);
    t = setTimeout(fn, ms, ...args);
  };
}

/** Only http(s) URLs may be used as link targets. */
export function safeHref(url?: string | null): string | null {
  try {
    const u = new URL(String(url || ''), location.href);
    if (u.protocol === 'http:' || u.protocol === 'https:') return u.href;
  } catch {
    /* fall through */
  }
  return null;
}

/** Parse a URL query param safely. */
export function queryParam(name: string): string {
  if (typeof window === 'undefined') return '';
  try {
    return new URLSearchParams(window.location.search).get(name) || '';
  } catch {
    return '';
  }
}
