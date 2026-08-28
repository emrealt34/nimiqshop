/* dev/mock-server.js — DEVELOPMENT / PREVIEW ONLY.
 *
 * Serves the static frontend and mocks the CURRENT Go backend API shapes
 * (catalog brands/families, quotes with Lightning invoices, orders,
 * activity, support, limits) so the whole UI can be clicked through
 * without the real stack. NEVER deploy this file.
 *
 * IMPORTANT: nim.shop is non-custodial — there are intentionally NO
 * balance, deposit, credit or treasury endpoints here (or anywhere).
 */
const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const ROOT = path.join(__dirname, '..');
const PORT = process.env.PORT || 8080;
const now = () => new Date().toISOString();
const iso = (msAgo) => new Date(Date.now() - msAgo).toISOString();
const uuid = () => crypto.randomUUID();

/* ---------------- market ---------------- */

const MARKET = { usd_per_nim: 0.0031, usd_per_btc: 110000, updated_at: now() };
// Rough FX used only to derive mock BTC prices from local face values.
const FX = {
  USD: 1, EUR: 1.08, GBP: 1.27, TRY: 0.029, INR: 0.0119, BRL: 0.18, JPY: 0.0065,
  AUD: 0.66, CAD: 0.73, MXN: 0.054, PLN: 0.25, SAR: 0.27, AED: 0.27, EGP: 0.021,
  ZAR: 0.055, IDR: 0.000063, PHP: 0.0175, PKR: 0.0036, BDT: 0.0085, VND: 0.000039,
  THB: 0.028, COP: 0.00024, ARS: 0.0011, CLP: 0.0011, PEN: 0.27, CZK: 0.043,
  HUF: 0.0026, SEK: 0.093, NOK: 0.092, DKK: 0.145, CHF: 1.13, UAH: 0.024,
  MAD: 0.1, TND: 0.32, IQD: 0.00076, JOD: 1.41, KWD: 3.25, QAR: 0.27, OMR: 2.6,
  BHD: 2.65, NGN: 0.00065, KES: 0.0077, RON: 0.22, KRW: 0.00075, SGD: 0.74,
  MYR: 0.21, NZD: 0.61, ILS: 0.27,
};
const usdOf = (value, ccy) => (value || 0) * (FX[ccy] || 1);
const btcOfUsd = (usd) => ((usd * 1.05) / MARKET.usd_per_btc).toFixed(8);
const nimOfUsd = (usd) => Math.ceil((usd * 1.05) / MARKET.usd_per_nim);

/* ---------------- catalog dataset ----------------
 * Global digital brands exist in (almost) every country; each country also
 * carries local retail/grocery cards and its mobile operators. This mirrors
 * CryptoRefills' coverage so the country picker never looks broken.
 */

const SYM = { USD: '$', EUR: '€', GBP: '£', TRY: '₺', INR: '₹', BRL: 'R$', JPY: '¥', AUD: 'A$', CAD: 'C$', MXN: 'MX$', PLN: 'zł', SAR: 'SAR ', AED: 'AED ', EGP: 'E£', ZAR: 'R', IDR: 'Rp', PHP: '₱', PKR: 'Rs ', BDT: '৳', VND: '₫', THB: '฿', COP: 'COL$', ARS: 'AR$', CLP: 'CL$', PEN: 'S/', CZK: 'Kč', HUF: 'Ft', SEK: 'kr', NOK: 'kr', DKK: 'kr', CHF: 'CHF ', UAH: '₴', MAD: 'MAD ', TND: 'TND ', IQD: 'IQD ', JOD: 'JD', KWD: 'KD', QAR: 'QR', OMR: 'OMR ', BHD: 'BD', NGN: '₦', KES: 'KSh', RON: 'lei', KRW: '₩', SGD: 'S$', MYR: 'RM', NZD: 'NZ$', ILS: '₪' };
const CURR = {
  TR: 'TRY', US: 'USD', GB: 'GBP', DE: 'EUR', FR: 'EUR', ES: 'EUR', IT: 'EUR', NL: 'EUR',
  CA: 'CAD', BR: 'BRL', IN: 'INR', AU: 'AUD', JP: 'JPY', PL: 'PLN', MX: 'MXN', AT: 'EUR',
  BE: 'EUR', CH: 'CHF', PT: 'EUR', IE: 'EUR', GR: 'EUR', SE: 'SEK', NO: 'NOK', DK: 'DKK',
  FI: 'EUR', CZ: 'CZK', HU: 'HUF', RO: 'RON', BG: 'EUR', HR: 'EUR', SK: 'EUR', SI: 'EUR',
  LT: 'EUR', LV: 'EUR', EE: 'EUR', UA: 'UAH', SA: 'SAR', AE: 'AED', QA: 'QAR', KW: 'KWD',
  OM: 'OMR', BH: 'BHD', EG: 'EGP', MA: 'MAD', TN: 'TND', DZ: 'EUR', ZA: 'ZAR', NG: 'NGN',
  KE: 'KES', GH: 'USD', CI: 'USD', SN: 'USD', CM: 'USD', ET: 'USD', UG: 'USD', TZ: 'USD',
  RW: 'USD', ZM: 'USD', ZW: 'USD', MZ: 'USD', AO: 'USD', AR: 'ARS', CL: 'CLP', CO: 'COP',
  PE: 'PEN', EC: 'USD', UY: 'USD', PY: 'USD', BO: 'USD', GT: 'USD', CR: 'USD', PA: 'USD',
  DO: 'USD', HN: 'USD', NI: 'USD', SV: 'USD', KR: 'KRW', SG: 'SGD', MY: 'MYR', TH: 'THB',
  ID: 'IDR', PH: 'PHP', VN: 'VND', KH: 'USD', LA: 'USD', MM: 'USD', BD: 'BDT', PK: 'PKR',
  LK: 'USD', NP: 'USD', NZ: 'NZD', FJ: 'USD', IL: 'ILS', JO: 'JOD', LB: 'USD', IQ: 'IQD',
  IR: 'USD', CY: 'EUR', MT: 'EUR', IS: 'USD', AL: 'USD', RS: 'USD', BA: 'USD', MK: 'USD',
  MD: 'USD', GE: 'USD', AM: 'USD', AZ: 'USD', KZ: 'USD', UZ: 'USD', KG: 'USD', MN: 'USD',
};

const COLORS = ['#FF9900', '#1DB954', '#0F9D58', '#E23744', '#0078D7', '#6B4FBB', '#FF5A5F', '#25D366', '#00A4EF', '#FFCE00', '#F0652F', '#4A154B', '#00B7C3', '#D70A0A', '#12B312', '#003087', '#FF2D55', '#1ED760', '#24292F', '#3B4CCA'];
const colorFor = (s) => COLORS[[...String(s)].reduce((a, c) => a + c.charCodeAt(0), 0) % COLORS.length];

// Local monogram logos as data-URI SVGs (CSP-safe, zero network).
function logoFor(name) {
  const initials = String(name).split(/\s+/).slice(0, 2).map((w) => w[0]).join('').toUpperCase();
  const bg = colorFor(name);
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='220' height='140'><rect width='220' height='140' rx='16' fill='${bg}'/><text x='110' y='86' font-family='Georgia,serif' font-size='56' font-weight='bold' fill='white' text-anchor='middle'>${initials}</text></svg>`;
  return 'data:image/svg+xml;utf8,' + encodeURIComponent(svg);
}

// Global digital brands, priced in USD, present in every shop country.
const GLOBAL_GIFTCARDS = [
  ['Steam USD', [5, 10, 20, 50, 100]],
  ['Google Play', [10, 25, 50, 100]],
  ['Apple', [15, 25, 50, 100]],
  ['Roblox', [10, 25, 50]],
  ['Razer Gold', null, [10, 200, 5]],
  ['Amazon (Global USD)', null, [10, 500, 5]],
  ['Netflix', [30, 60]],
  ['Spotify Premium', [11, 33, 66]],
  ['PlayStation Store', [10, 25, 50, 100]],
  ['Xbox', [10, 25, 50]],
  ['Microsoft 365 (Personal & Family)', [49, 99]],
  ['Nintendo eShop', [10, 20, 35, 50]],
  ['Uber (Global)', null, [10, 200, 5]],
  ['Airbnb (Global)', null, [25, 500, 5]],
];

// Local anchors: [family, kind('gc'|'tu'|'esim'), currency, denoms or range, category]
const LOCAL = {
  TR: [
    ['Trendyol', 'gc', 'TRY', null, [250, 10000, 50], 'retail'],
    ['Hepsiburada', 'gc', 'TRY', null, [250, 10000, 50], 'retail'],
    ['Getir', 'gc', 'TRY', [250, 500, 1000], 'groceries'],
    ['A101', 'gc', 'TRY', [200, 500, 1000], 'groceries'],
    ['Amazon.com.tr', 'gc', 'TRY', null, [200, 20000, 100], 'retail'],
    ['IKEA Türkiye', 'gc', 'TRY', null, [500, 15000, 100], 'home'],
    ['Turkcell', 'tu', 'TRY', [350, 600, 900], 'mobile'],
    ['Vodafone Türkiye', 'tu', 'TRY', [300, 500, 750], 'mobile'],
    ['Türk Telekom', 'tu', 'TRY', [300, 500], 'mobile'],
    ['eSIM Türkiye', 'esim', 'USD', [7, 19, 35], 'e-sim'],
  ],
  US: [
    ['Amazon.com', 'gc', 'USD', null, [5, 2000, 1], 'retail'],
    ['Walmart', 'gc', 'USD', null, [5, 500, 1], 'retail'],
    ['Target', 'gc', 'USD', null, [10, 500, 1], 'retail'],
    ['DoorDash', 'gc', 'USD', [15, 25, 50, 100], 'food'],
    ['Starbucks USA', 'gc', 'USD', [5, 10, 25, 50], 'food'],
    ['T-Mobile USA', 'tu', 'USD', null, [5, 200, 1], 'mobile'],
    ['AT&T', 'tu', 'USD', null, [10, 300, 5], 'mobile'],
    ['Verizon', 'tu', 'USD', null, [5, 500, 5], 'mobile'],
    ['eSIM USA', 'esim', 'USD', [9, 25, 45], 'e-sim'],
  ],
  GB: [
    ['Amazon.co.uk', 'gc', 'GBP', null, [5, 1000, 1], 'retail'],
    ['Deliveroo', 'gc', 'GBP', [10, 25, 50], 'food'],
    ['Just Eat', 'gc', 'GBP', [10, 25, 50, 100], 'food'],
    ['Tesco', 'gc', 'GBP', null, [5, 250, 1], 'groceries'],
    ['EE', 'tu', 'GBP', [10, 20, 50], 'mobile'],
    ['Vodafone UK', 'tu', 'GBP', [10, 25, 50], 'mobile'],
    ['O2 UK', 'tu', 'GBP', [10, 25, 50], 'mobile'],
    ['eSIM United Kingdom', 'esim', 'USD', [9, 25, 45], 'e-sim'],
  ],
  DE: [
    ['Amazon.de', 'gc', 'EUR', null, [5, 1500, 1], 'retail'],
    ['MediaMarkt', 'gc', 'EUR', [10, 20, 50, 100], 'electronics'],
    ['Lieferando', 'gc', 'EUR', [10, 20, 50], 'food'],
    ['Rewe', 'gc', 'EUR', [10, 25, 50], 'groceries'],
    ['Telekom Deutschland', 'tu', 'EUR', [15, 30, 50], 'mobile'],
    ['Vodafone Deutschland', 'tu', 'EUR', [15, 25, 50], 'mobile'],
    ['O2 Deutschland', 'tu', 'EUR', [15, 30], 'mobile'],
    ['eSIM Deutschland', 'esim', 'USD', [9, 25, 45], 'e-sim'],
  ],
  FR: [
    ['Amazon.fr', 'gc', 'EUR', null, [5, 1500, 1], 'retail'],
    ['Carrefour', 'gc', 'EUR', [25, 50, 100, 250], 'groceries'],
    ['Fnac', 'gc', 'EUR', null, [10, 250, 1], 'retail'],
    ['Orange France', 'tu', 'EUR', [10, 20, 50], 'mobile'],
    ['SFR', 'tu', 'EUR', [10, 25, 50], 'mobile'],
    ['Bouygues Telecom', 'tu', 'EUR', [10, 25], 'mobile'],
    ['eSIM France', 'esim', 'USD', [9, 25, 45], 'e-sim'],
  ],
  ES: [
    ['Amazon.es', 'gc', 'EUR', null, [5, 1500, 1], 'retail'],
    ['El Corte Inglés', 'gc', 'EUR', null, [10, 300, 1], 'retail'],
    ['Glovo España', 'gc', 'EUR', [10, 25, 50], 'food'],
    ['Movistar España', 'tu', 'EUR', [10, 20, 50], 'mobile'],
    ['Vodafone España', 'tu', 'EUR', [10, 25, 50], 'mobile'],
    ['Orange España', 'tu', 'EUR', [10, 25], 'mobile'],
  ],
  IT: [
    ['Amazon.it', 'gc', 'EUR', null, [5, 1500, 1], 'retail'],
    ['Esselunga', 'gc', 'EUR', [25, 50, 100], 'groceries'],
    ['TIM', 'tu', 'EUR', [10, 25, 50], 'mobile'],
    ['Vodafone Italia', 'tu', 'EUR', [10, 25, 50], 'mobile'],
    ['Wind Tre', 'tu', 'EUR', [10, 25], 'mobile'],
  ],
  NL: [
    ['Bol.com', 'gc', 'EUR', [10, 20, 50, 100], 'retail'],
    ['Albert Heijn', 'gc', 'EUR', [10, 20, 50], 'groceries'],
    ['KPN', 'tu', 'EUR', [10, 20, 50], 'mobile'],
    ['Vodafone Nederland', 'tu', 'EUR', [10, 25], 'mobile'],
  ],
  CA: [
    ['Amazon.ca', 'gc', 'CAD', null, [5, 1000, 1], 'retail'],
    ['Walmart Canada', 'gc', 'CAD', null, [5, 500, 1], 'retail'],
    ['Fido', 'tu', 'CAD', [15, 25, 50], 'mobile'],
    ['Rogers', 'tu', 'CAD', [15, 30, 50], 'mobile'],
  ],
  BR: [
    ['Amazon.com.br', 'gc', 'BRL', null, [20, 2000, 5], 'retail'],
    ['iFood', 'gc', 'BRL', [25, 50, 100], 'food'],
    ['Magazine Luiza', 'gc', 'BRL', null, [20, 1000, 10], 'retail'],
    ['Vivo', 'tu', 'BRL', [20, 30, 50, 100], 'mobile'],
    ['Claro Brasil', 'tu', 'BRL', [20, 30, 50], 'mobile'],
    ['TIM Brasil', 'tu', 'BRL', [20, 30, 50], 'mobile'],
  ],
  IN: [
    ['Amazon.in', 'gc', 'INR', null, [100, 10000, 10], 'retail'],
    ['Flipkart', 'gc', 'INR', null, [100, 10000, 10], 'retail'],
    ['Myntra', 'gc', 'INR', [250, 500, 1000, 2000], 'retail'],
    ['Airtel', 'tu', 'INR', [239, 479, 666, 2999], 'mobile'],
    ['Jio', 'tu', 'INR', [239, 479, 666, 2999], 'mobile'],
    ['Vi (Vodafone Idea)', 'tu', 'INR', [239, 479, 2999], 'mobile'],
  ],
  AU: [
    ['Amazon.com.au', 'gc', 'AUD', null, [10, 1000, 1], 'retail'],
    ['Woolworths', 'gc', 'AUD', [50, 100, 200], 'groceries'],
    ['Telstra', 'tu', 'AUD', [20, 30, 50], 'mobile'],
    ['Optus', 'tu', 'AUD', [20, 30, 50], 'mobile'],
  ],
  JP: [
    ['Amazon.co.jp', 'gc', 'JPY', [500, 1000, 3000, 5000, 10000], 'retail'],
    ['7-Eleven Japan', 'gc', 'JPY', [1000, 3000, 5000], 'retail'],
    ['NTT Docomo', 'tu', 'JPY', [3000, 5000, 10000], 'mobile'],
    ['SoftBank', 'tu', 'JPY', [3000, 5000, 10000], 'mobile'],
  ],
  PL: [
    ['Allegro', 'gc', 'PLN', [50, 100, 200, 500], 'retail'],
    ['Biedronka', 'gc', 'PLN', [50, 100, 200], 'groceries'],
    ['Orange Polska', 'tu', 'PLN', [30, 50, 100], 'mobile'],
    ['Play', 'tu', 'PLN', [30, 50, 100], 'mobile'],
  ],
  MX: [
    ['Amazon.com.mx', 'gc', 'MXN', null, [100, 5000, 10], 'retail'],
    ['Mercado Libre México', 'gc', 'MXN', null, [100, 5000, 10], 'retail'],
    ['Telcel', 'tu', 'MXN', [100, 200, 300, 500], 'mobile'],
    ['AT&T México', 'tu', 'MXN', [100, 200, 500], 'mobile'],
  ],
  SA: [
    ['Amazon.sa', 'gc', 'SAR', null, [50, 2000, 5], 'retail'],
    ['Noon Saudi Arabia', 'gc', 'SAR', null, [50, 2000, 5], 'retail'],
    ['STC', 'tu', 'SAR', [50, 100, 200], 'mobile'],
    ['Mobily', 'tu', 'SAR', [50, 100, 150], 'mobile'],
  ],
  AE: [
    ['Amazon.ae', 'gc', 'AED', null, [50, 2000, 5], 'retail'],
    ['Noon UAE', 'gc', 'AED', null, [50, 2000, 5], 'retail'],
    ['Carrefour UAE', 'gc', 'AED', [50, 100, 250], 'groceries'],
    ['Etisalat', 'tu', 'AED', [50, 100, 200], 'mobile'],
    ['du', 'tu', 'AED', [50, 100, 150], 'mobile'],
  ],
  EG: [
    ['Amazon.eg', 'gc', 'EGP', null, [200, 5000, 10], 'retail'],
    ['Vodafone Egypt', 'tu', 'EGP', [100, 250, 500], 'mobile'],
    ['Orange Egypt', 'tu', 'EGP', [100, 250, 500], 'mobile'],
    ['Etisalat Egypt', 'tu', 'EGP', [100, 250], 'mobile'],
  ],
  ZA: [
    ['Takealot', 'gc', 'ZAR', null, [100, 5000, 10], 'retail'],
    ['Checkers', 'gc', 'ZAR', [100, 250, 500], 'groceries'],
    ['Vodacom', 'tu', 'ZAR', [60, 120, 250], 'mobile'],
    ['MTN South Africa', 'tu', 'ZAR', [60, 120, 250], 'mobile'],
  ],
  NG: [
    ['MTN Nigeria', 'tu', 'NGN', [1000, 2000, 5000], 'mobile'],
    ['Airtel Nigeria', 'tu', 'NGN', [1000, 2000, 5000], 'mobile'],
    ['Glo', 'tu', 'NGN', [1000, 2000], 'mobile'],
  ],
  KE: [
    ['Safaricom', 'tu', 'KES', [500, 1000, 2500], 'mobile'],
    ['Airtel Kenya', 'tu', 'KES', [500, 1000], 'mobile'],
  ],
  ID: [
    ['Tokopedia', 'gc', 'IDR', [50000, 100000, 250000, 500000], 'retail'],
    ['GoPay', 'gc', 'IDR', [50000, 100000, 200000], 'digital-goods'],
    ['Telkomsel', 'tu', 'IDR', [50000, 100000, 200000], 'mobile'],
    ['Indosat', 'tu', 'IDR', [50000, 100000], 'mobile'],
  ],
  PH: [
    ['Globe Telecom', 'tu', 'PHP', [100, 300, 500], 'mobile'],
    ['Smart', 'tu', 'PHP', [100, 300, 500], 'mobile'],
    ['Grab Philippines', 'gc', 'PHP', [200, 500, 1000], 'food'],
  ],
  PK: [
    ['Jazz', 'tu', 'PKR', [500, 1000, 2000], 'mobile'],
    ['Telenor Pakistan', 'tu', 'PKR', [500, 1000, 2000], 'mobile'],
    ['Zong', 'tu', 'PKR', [500, 1000], 'mobile'],
  ],
  BD: [['Grameenphone', 'tu', 'BDT', [200, 500, 1000], 'mobile']],
  VN: [['Viettel', 'tu', 'VND', [100000, 200000, 500000], 'mobile']],
  TH: [
    ['AIS', 'tu', 'THB', [100, 200, 300], 'mobile'],
    ['TrueMove H', 'tu', 'THB', [100, 200, 300], 'mobile'],
    ['Lazada Thailand', 'gc', 'THB', [200, 500, 1000], 'retail'],
  ],
  CO: [
    ['Claro Colombia', 'tu', 'COP', [20000, 50000], 'mobile'],
    ['Movistar Colombia', 'tu', 'COP', [20000, 50000], 'mobile'],
    ['Rappi', 'gc', 'COP', [30000, 60000, 100000], 'food'],
  ],
  AR: [
    ['Claro Argentina', 'tu', 'ARS', [1000, 3000, 6000], 'mobile'],
    ['Personal', 'tu', 'ARS', [1000, 3000], 'mobile'],
    ['Mercado Libre Argentina', 'gc', 'ARS', null, [1000, 100000, 100], 'retail'],
  ],
  CL: [
    ['Entel', 'tu', 'CLP', [5000, 10000, 20000], 'mobile'],
    ['Movistar Chile', 'tu', 'CLP', [5000, 10000], 'mobile'],
  ],
  PE: [['Claro Perú', 'tu', 'PEN', [20, 50, 100], 'mobile']],
  RO: [
    ['Vodafone Romania', 'tu', 'EUR', [10, 20, 50], 'mobile'],
    ['Orange Romania', 'tu', 'EUR', [10, 20, 50], 'mobile'],
  ],
  CZ: [
    ['T-Mobile Czech', 'tu', 'CZK', [300, 500, 1000], 'mobile'],
    ['O2 Czech', 'tu', 'CZK', [300, 500], 'mobile'],
  ],
  HU: [['Magyar Telekom', 'tu', 'HUF', [3000, 5000, 10000], 'mobile']],
  GR: [['Cosmote', 'tu', 'EUR', [10, 20, 50], 'mobile']],
  PT: [
    ['MEO', 'tu', 'EUR', [10, 20, 50], 'mobile'],
    ['Vodafone Portugal', 'tu', 'EUR', [10, 25], 'mobile'],
  ],
  SE: [['Telia', 'tu', 'SEK', [100, 200, 500], 'mobile']],
  NO: [['Telenor', 'tu', 'NOK', [100, 200, 500], 'mobile']],
  DK: [['TDC', 'tu', 'DKK', [100, 200, 500], 'mobile']],
  FI: [['Elisa', 'tu', 'EUR', [10, 20, 50], 'mobile']],
  AT: [['A1', 'tu', 'EUR', [10, 20, 50], 'mobile']],
  CH: [['Swisscom', 'tu', 'CHF', [10, 20, 50], 'mobile']],
  BE: [['Proximus', 'tu', 'EUR', [10, 20, 50], 'mobile']],
  IE: [['Three Ireland', 'tu', 'EUR', [10, 20, 50], 'mobile']],
  UA: [['Kyivstar', 'tu', 'UAH', [300, 600, 1200], 'mobile']],
  MA: [['Maroc Telecom', 'tu', 'MAD', [50, 100, 200], 'mobile']],
  TN: [['Ooredoo Tunisia', 'tu', 'TND', [10, 20, 50], 'mobile']],
  IQ: [
    ['Zain Iraq', 'tu', 'IQD', [10000, 25000, 50000], 'mobile'],
    ['Asiacell', 'tu', 'IQD', [10000, 25000], 'mobile'],
  ],
  JO: [['Zain Jordan', 'tu', 'JOD', [5, 10, 20], 'mobile']],
  LB: [
    ['Alfa', 'tu', 'USD', [5, 10, 25], 'mobile'],
    ['Touch', 'tu', 'USD', [5, 10, 25], 'mobile'],
  ],
  KW: [['Zain Kuwait', 'tu', 'KWD', [2, 5, 10], 'mobile']],
  QA: [['Ooredoo Qatar', 'tu', 'QAR', [25, 50, 100], 'mobile']],
  OM: [['Omantel', 'tu', 'OMR', [5, 10, 20], 'mobile']],
  BH: [['Batelco', 'tu', 'BHD', [5, 10, 20], 'mobile']],
  KR: [
    ['Google Play Korea', 'gc', 'KRW', [10000, 30000, 50000], 'games'],
    ['Kakao', 'gc', 'KRW', [10000, 30000], 'digital-goods'],
  ],
  SG: [['Grab Singapore', 'gc', 'SGD', [10, 25, 50], 'food']],
  MY: [
    ['Grab Malaysia', 'gc', 'MYR', [25, 50, 100], 'food'],
    ['Maxis', 'tu', 'MYR', [30, 50, 100], 'mobile'],
  ],
  NZ: [['Spark New Zealand', 'tu', 'NZD', [20, 50], 'mobile']],
  IL: [['Google Play Israel', 'gc', 'ILS', [50, 100, 200], 'games']],
};

// Build brand entries for a country: global cards + local anchors.
function brandsFor(cc) {
  const ccy = CURR[cc] || 'USD';
  const out = [];
  for (const [family, denoms, range] of GLOBAL_GIFTCARDS) {
    const [min, max] = range ? [range[0], range[1]] : [denoms[0], denoms[denoms.length - 1]];
    out.push({
      family, brand_id: 'glb-' + family.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      kind: 'giftcard', category: 'digital-goods', country_code: cc,
      logo_url: logoFor(family), bg_color: colorFor(family), /* REAL brand background */
      min: `${min} USD`, max: `${max} USD`,
      is_out_of_stock: false, product_type: 'digital',
      _ccy: 'USD', _denoms: denoms, _range: range,
    });
  }
  for (const [family, kind, famCcy, denoms, rangeOrCat, cat] of (LOCAL[cc] || [])) {
    const isRange = Array.isArray(rangeOrCat);
    const category = isRange ? (cat || 'retail') : (rangeOrCat === 'esim' ? 'e-sim' : (rangeOrCat || 'mobile'));
    const k = kind === 'gc' ? 'giftcard' : 'mobile_recharge';
    const den = isRange ? null : denoms;
    const [min, max] = isRange ? [rangeOrCat[0], rangeOrCat[1]] : [denoms[0], denoms[denoms.length - 1]];
    out.push({
      family, brand_id: cc.toLowerCase() + '-' + family.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      kind: k, category, country_code: cc,
      logo_url: logoFor(family), bg_color: colorFor(family), /* REAL brand background */
      min: `${min} ${famCcy}`, max: `${max} ${famCcy}`,
      is_out_of_stock: false, product_type: kind === 'tu' ? 'mobile' : 'digital',
      _ccy: famCcy, _denoms: den, _range: isRange ? rangeOrCat : null,
    });
  }
  // Travel eSIMs are listed in every country (they are destination products).
  if (cc !== 'TR' && cc !== 'US' && cc !== 'GB' && cc !== 'DE') {
    out.push({
      family: 'eSIM Global (100+ countries)', brand_id: 'esim-global',
      kind: 'mobile_recharge', category: 'e-sim', country_code: cc,
      logo_url: logoFor('eSIM Global'), bg_color: colorFor('eSIM Global'),
      min: '15 USD', max: '85 USD', is_out_of_stock: false, product_type: 'esim',
      _ccy: 'USD', _denoms: [15, 45, 85], _range: null,
    });
  }
  return out;
}

function catalogResponse(cc) {
  const brands = brandsFor(cc).map(({ _ccy, _denoms, _range, ...b }) => b);
  const groups = new Map();
  for (const b of brands) {
    const key = b.kind + '|' + b.category;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(b);
  }
  return {
    country_code: cc,
    categories: [...groups.entries()].map(([key, list]) => {
      const [kind, category] = key.split('|');
      return { kind, category, brands: list };
    }),
  };
}

function findBrand(cc, familyName) {
  const q = String(familyName || '').trim().toLowerCase();
  return brandsFor(cc).find((b) => b.family.toLowerCase() === q) || null;
}

function familyResponse(cc, familyName) {
  const b = findBrand(cc, familyName);
  if (!b) return null;
  const ccy = b._ccy;
  const products = [];
  if (b._range) {
    products.push({
      product_id: b.brand_id + '-dyn', is_dynamic: true,
      range: { min: b._range[0], max: b._range[1], currency: ccy, step_size: b._range[2] || 1 },
      coin: 'BTC', delivery_type: b.kind === 'mobile_recharge' && b.category !== 'e-sim' ? 'by_phone' : 'by_email',
      product_type: b.product_type,
    });
  } else {
    for (const v of b._denoms) {
      // Localized label like "$25", "₺300", "¥500" — symbol before the value
      // for symbol currencies, "50 SAR" style for code currencies.
      const sym = SYM[ccy] || '';
      const isCodeSym = sym === '' || sym.trim() === ccy;
      const localized = isCodeSym ? `${v} ${ccy}` : `${sym}${v}`;
      products.push({
        product_id: `${b.brand_id}-${v}`,
        denomination: `${v} ${ccy}`,
        localized_denomination: localized,
        amount: v, currency_code: ccy, coin: 'BTC',
        coin_amount: btcOfUsd(usdOf(v, ccy)),
        delivery_type: b.kind === 'mobile_recharge' && b.category !== 'e-sim' ? 'by_phone' : 'by_email',
        product_type: b.product_type,
      });
    }
  }
  const isTopup = b.kind === 'mobile_recharge' && b.category !== 'e-sim';
  return [{
    country_code: cc, category: b.category, additional_categories: [],
    kind: b.kind, default_denomination: products[0] ? (products[0].denomination || 'range') : '',
    family: b.family, brand_id: b.brand_id, brand: b.family,
    is_out_of_stock: false, logo_url: b.logo_url, bg_color: b.bg_color || colorFor(b.family),
    product_tc: isTopup
      ? `Top-up for ${b.family} (${countryNameOf(cc)}). The credit is applied directly to the beneficiary phone number in E.164 format. Delivery is instant in most cases; the operator may take up to a few minutes. Wrong numbers cannot be reversed once the operator applies the credit — double-check the number before paying.`
      : `Redeem your ${b.family} code on the official ${b.family} website or app. Enter the code at checkout or in the account/balance section; the full face value is credited there. Codes are delivered by email instantly after the Lightning payment confirms. The brand's own terms and conditions apply.`,
    /* RICH-CONTENT parity: the real supplier family payload carries
     * rich_description (how_to_redeem / terms / redeem_geo) — the order and
     * product pages render it. Mock the same shape. */
    rich_description: {
      how_to_redeem: isTopup
        ? `<ol><li>The credit is sent directly to the <strong>${b.family}</strong> number you entered at checkout.</li><li>It usually lands within minutes; the operator can take longer at busy times.</li><li>Check the balance in the operator's app or via USSD.</li></ol>`
        : `<ol><li>Open the <strong>${b.family}</strong> website or app and sign in.</li><li>Go to <em>Gift cards &rarr; Redeem a code</em> (or paste it at checkout).</li><li>Enter the code from <em>Your delivery</em> — the full face value is credited instantly.</li></ol>`,
      term_and_conditions: `<p>The ${b.family} code is delivered by CryptoRefills and subject to the brand's own terms. Codes cannot be refunded once redeemed. Verify the redemption region before purchasing.</p>`,
      redeem_geo: b._ccy === 'TRY' ? 'in Türkiye only' : '',
    },
    products,
  }];
}

function countryNameOf(cc) {
  try { return new Intl.DisplayNames(['en'], { type: 'region' }).of(cc) || cc; } catch { return cc; }
}

/* ---------------- state: quotes / orders / tickets ---------------- */

const quotes = [];
const orders = [];
const tickets = [];
const activity = [];

// Seed history (public activity feed + one delivered order per session).
(function seed() {  const seedOrders = [
    { product: 'Steam USD', country: 'US', currency: 'USD', value: 20, kind: 'gift_card', days: 2, fulfilled: true, code: 'QX7M-2RLK-9WVP', pin: '4821', instructions: 'Open Steam → Games → Redeem a Steam Gift Card or Wallet Code. The balance is credited to your Steam Wallet instantly.' },
    { product: 'Trendyol', country: 'TR', currency: 'TRY', value: 500, kind: 'gift_card', days: 5, fulfilled: true, code: 'TRND-8841-2210-7734', pin: '', instructions: 'sepetim.trendyol.com adresinde "Hediye Çeki" alanına kodu girin.' },
    { product: 'Turkcell', country: 'TR', currency: 'TRY', value: 600, kind: 'topup', days: 9, fulfilled: true, beneficiary: '+905551234567', instructions: 'Top-up completed — credit applied to the phone number.' },
    { product: 'Google Play', country: 'DE', currency: 'EUR', value: 25, kind: 'gift_card', days: 12, fulfilled: true, code: 'GP-DE-5521-8874', pin: '', instructions: 'Google Play Store → Profil → Zahlungen & Abos → Geschenkkarte einlösen.' },
  ];
  for (const s of seedOrders) {
    const created = iso(s.days * 864e5);
    const usd = usdOf(s.value, s.currency);
    const o = {
      id: uuid(), kind: s.kind, category_id: s.kind, product_id: s.product,
      quantity: 1, price_usd: (usd * 1.05).toFixed(6),
      status: 'delivered', supplier_order_id: 'CR-' + Math.floor(Math.random() * 9e4 + 1e4),
      created_at: created, updated_at: created,
      payload: { product_name: s.product, country: s.country, currency: s.currency, value: s.value, beneficiary: s.beneficiary || 'demo@example.com', product_image: logoFor(s.product), product_bg: colorFor(s.product) },
      fulfillment: s.fulfilled ? { code: s.code || '', pin: s.pin || '', how_to_redeem: s.instructions } : null,
    };
    orders.push(o);
    activity.push({ type: 'purchase', id: o.id, kind: o.kind, title: s.product, country: s.country, quantity: 1,
      address: 'NQ86 ' + Math.random().toString(16).slice(2, 6).toUpperCase() + ' ' + Math.random().toString(16).slice(2, 6).toUpperCase(),
      status: 'delivered', time: created, usd: usd * 1.05, nim: (usd * 1.05) / MARKET.usd_per_nim,
      local_amount: s.value, local_currency: s.currency, rating: 4 + (s.days % 2) });
  }

  /* One LIVE direct-payment quote in "awaiting payment" — the exact orders-
   * page scenario: real brand thumb + local face value + USD equivalent
   * (product_usd in micro-USD, like the fixed backend). */
  const liveUSD = usdOf(500, 'TRY');
  const liveID = uuid();
  quotes.push({
    id: liveID, quote_id: liveID, status: 'awaiting_payment',
    product_id: 'Amazon.com.tr', product_country: 'TR',
    denomination: 'TRY500', product_value: 500, quantity: 1,
    product_usd: Math.round(liveUSD * 1e6),
    customer_email: 'demo@example.com', beneficiary_account: 'demo@example.com',
    coin: 'BTC', network: 'Lightning', coin_amount: btcOfUsd(liveUSD),
    lightning_invoice: bolt11(), wallet_address: bolt11(),
    payment_expires_at: new Date(Date.now() + 18 * 60e3).toISOString(),
    expires_at: new Date(Date.now() + 18 * 60e3).toISOString(),
    created_at: iso(6 * 60e3), updated_at: iso(6 * 60e3),
    _product: 'Amazon.com.tr', _country: 'TR', _currency: 'TRY', _value: 500, _kind: 'gift_card',
  });
})();

const stagesFor = (status, created, updated) => {
  const s = [
    { id: 'order_placed', status: 'completed', timestamp: created },
    { id: 'payment_settled', status: 'completed', timestamp: created },
    { id: 'supplier_processing', status: 'pending' },
    { id: 'delivery_complete', status: 'pending' },
  ];
  let cur = 1;
  if (['pending', 'created', 'processing', 'payment_detected'].includes(status)) { s[2].status = 'in_progress'; s[2].timestamp = updated; cur = 2; }
  if (['delivered', 'complete', 'fulfilled'].includes(status)) { s[2].status = 'completed'; s[2].timestamp = updated; s[3].status = 'completed'; s[3].timestamp = updated; cur = 3; }
  if (['failed', 'refunded'].includes(status)) { s[2].status = 'failed'; s[2].timestamp = updated; s[3].status = 'failed'; cur = 3; }
  return { stages: s, current_stage: cur };
};

function bolt11() {
  // Shape-valid BOLT11 for the UI (lnbc + bech32-ish body). Preview only.
  const alphabet = '023456789acdefghjklmnpqrstuvwxyz';
  let body = '';
  for (let i = 0; i < 180; i++) body += alphabet[Math.floor(Math.random() * alphabet.length)];
  return 'lnbc' + body;
}

function makeQuote(b) {
  const usd = (b.unitUSD || 10) * (b.quantity || 1);
  const created = now();
  const qid = uuid();
  const q = {
    id: qid, // LIST-PARITY FIX: GET /quotes items expose `id` (db.Quote JSON) — orders links /order?type=quote&id=… now resolve in preview too
    quote_id: qid, status: 'lightning_invoice_created',
    product_id: b.product_id, product_country: b.country || 'US',
    denomination: b.denomination || 'range', quantity: b.quantity || 1,
    product_value: b._faceLocal || 0, /* SELECTED local amount (slider/range parity with the real backend) */
    product_usd: Math.round(usd * 1e6),
    customer_email: b.email || 'demo@example.com',
    gift_channel: b.gift_channel || '', gift_recipient_phone: b.gift_recipient_phone || '', gift_message: b.gift_message || '',
    phone_number: b.phone_number || '',
    beneficiary_account: b.phone_number || b.email || 'demo@example.com',
    coin: 'BTC', network: 'Lightning',
    lightning_amount_btc: btcOfUsd(usd), coin_amount: btcOfUsd(usd),
    estimated_nim: nimOfUsd(usd), required_nim: nimOfUsd(usd),
    lightning_invoice: bolt11(),
    cryptorefills_payment_request: null,
    expires_at: new Date(Date.now() + 30 * 60e3).toISOString(),
    created_at: created, updated_at: created,
    _product: b._productName || b.product_id, _country: b.country || 'US',
    _currency: b._currency || 'USD', _value: b._value || usd, _kind: b._kind || 'gift_card',
    _instructions: b._instructions || '',
  };
  quotes.push(q);
  // Simulate the customer paying and the supplier webhook completing the order.
  setTimeout(() => { q.status = 'payment_received'; q.updated_at = now(); }, 6000);
  setTimeout(() => {
    q.status = 'fulfilled'; q.updated_at = now();
    const delivered = {
      id: uuid(), kind: q._kind, category_id: q._kind, product_id: q.product_id,
      quantity: q.quantity, price_usd: (usd * 1.05).toFixed(6), status: 'delivered',
      supplier_order_id: 'CR-' + Math.floor(Math.random() * 9e4 + 1e4),
      created_at: q.created_at, updated_at: now(),
      customer_email: q.customer_email || '', phone_number: q.phone_number || '', gift_channel: q.gift_channel || '', gift_recipient_phone: q.gift_recipient_phone || '', gift_message: q.gift_message || '',
      payload: { product_name: q._product, country: q._country, currency: q._currency, value: q._value, beneficiary: q.beneficiary_account, customer_email: q.customer_email || '', phone_number: q.phone_number || '' },
      fulfillment: q._kind === 'topup'
        ? { code: '', pin: '', how_to_redeem: `Top-up completed for ${q.beneficiary_account}.` }
        : { code: 'DEMO-' + crypto.randomBytes(4).toString('hex').toUpperCase(), pin: '', how_to_redeem: q._instructions || `Redeem on the ${q._product} website or app.` },
    };
    orders.push(delivered);
    activity.unshift({ type: 'purchase', id: uuid(), kind: q._kind, title: q._product + (q.denomination && q.denomination !== 'range' ? ' ' + q.denomination : ''), country: q._country, quantity: q.quantity,
      address: 'NQ86 ' + Math.random().toString(16).slice(2, 6).toUpperCase() + ' ' + Math.random().toString(16).slice(2, 6).toUpperCase(),
      status: 'delivered', time: now(), usd, nim: usd / MARKET.usd_per_nim,
      local_amount: q._faceLocal || q.product_value || 0, local_currency: q._currency || '', rating: 0 });
  }, 12000);
  return q;
}

/* ---------------- http plumbing ---------------- */

function send(res, code, obj, headers = {}) {
  const body = typeof obj === 'string' ? obj : JSON.stringify(obj);
  res.writeHead(code, { 'Content-Type': typeof obj === 'string' ? 'text/plain' : 'application/json', 'Access-Control-Allow-Origin': '*', ...headers });
  res.end(body);
}

function readBody(req) {
  return new Promise((resolve) => {
    let data = '';
    req.on('data', (c) => { data += c; });
    req.on('end', () => { try { resolve(data ? JSON.parse(data) : {}); } catch { resolve({}); } });
  });
}
function staticFile(res, p, urlPath) { // urlPath: the ORIGINAL request path, for cache scoping
  fs.readFile(p, (err, buf) => {
    if (err) { res.writeHead(404); return res.end('not found'); }
    const ext = path.extname(p);
    const types = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.xml': 'application/xml', '.txt': 'text/plain' };
    /* STALE-CACHE FIX: same as live-proxy — never let browsers keep old JS/CSS across deploys. */
    const codeExts = ['.html', '.js', '.mjs', '.css', '.json', '.xml', '.txt'];
    const headers = { 'Content-Type': types[ext] || 'application/octet-stream' };
    // Cache policy: HTML/JSON revalidate; fonts/vendor are immutable
    // (1 year), images a day, code an hour — revisits cost ~nothing.
    const up = urlPath || ('/' + path.relative(ROOT, p).split(path.sep).join('/'));
    if (up.endsWith('.html')) Object.assign(headers, { 'Cache-Control': 'no-cache' }); // revalidate the shell, cache the rest
    else if (up.startsWith('/fonts/') || up.startsWith('/vendor/')) Object.assign(headers, { 'Cache-Control': 'public, max-age=31536000, immutable' });
    else if (up.startsWith('/img/')) Object.assign(headers, { 'Cache-Control': 'public, max-age=86400' });
    else if (up.startsWith('/js/') || up.startsWith('/css/')) Object.assign(headers, { 'Cache-Control': 'public, max-age=3600' });
    else Object.assign(headers, { 'Cache-Control': 'public, max-age=3600' });
    res.writeHead(200, headers);
    res.end(buf);
  });
}
function authed(req) {
  const h = req.headers['authorization'] || '';
  return h.startsWith('Bearer ') && h.length > 12;
}

/* ---------------- api ---------------- */

async function api(req, res, parts) {
  // parts = ['', 'api', ...segments] — the route path is everything after /api.
  const route = `${req.method} /${parts.slice(2).join('/')}`;
  const url = new URL(req.url, 'http://localhost');

  if (route === 'GET /health') return send(res, 200, { status: 'ok', time: now(), non_custodial: true });
  if (route === 'GET /geo') return send(res, 200, { country: 'TR', cloudflare: false });
  if (route === 'GET /market/nim-rate') return send(res, 200, MARKET);
  if (route === 'GET /market/fx') return send(res, 200, { usd_per_unit: FX, updated_at: now() }); // parity with the real backend's FX table

  if (route === 'POST /auth/challenge') return send(res, 200, { challenge_token: 'mock-' + uuid(), message: 'nim.shop preview login ' + uuid(), expires_at: new Date(Date.now() + 5 * 60e3).toISOString() });
  if (route === 'POST /auth/hub-login') {
    const b = await readBody(req);
    return send(res, 200, { token: 'mock-preview-jwt.' + Buffer.from(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 86400 })).toString('base64url') + '.sig', user: { nimiq_address: b.address || 'NQ86 0000 0000 0000 0000 0000 0000 0000 0000', created_at: now() } });
  }

  /* ---- catalog ---- */
  const cc = (url.searchParams.get('country') || 'TR').toUpperCase();
  if (route === 'GET /catalog/giftcards') {
    const full = catalogResponse(cc);
    full.categories = full.categories.filter((c) => c.kind === 'giftcard');
    return send(res, 200, full);
  }
  if (route === 'GET /catalog/topups') {
    const full = catalogResponse(cc);
    full.categories = full.categories.filter((c) => c.kind === 'mobile_recharge' && c.category !== 'e-sim');
    return send(res, 200, full);
  }
  if (route === 'GET /catalog/esims') {
    const full = catalogResponse(cc);
    full.categories = full.categories.filter((c) => c.category === 'e-sim');
    return send(res, 200, full);
  }
  if (route === 'GET /catalog/search') {
    const q = (url.searchParams.get('q') || '').toLowerCase();
    const full = catalogResponse(cc);
    for (const c of full.categories) c.brands = c.brands.filter((b) => b.family.toLowerCase().includes(q));
    full.categories = full.categories.filter((c) => c.brands.length);
    return send(res, 200, full);
  }
  if (route === 'GET /catalog/check-phone') {
    const raw = url.searchParams.get('phone_number') || '';
    const country = (url.searchParams.get('country') || '').toUpperCase();
    const dial = { TR: '90', US: '1', DE: '49', GB: '44', FR: '33', ES: '34', IT: '39', NL: '31', BR: '55', IN: '91', PL: '48', MX: '52', EG: '20', SA: '966', AE: '971', NG: '234', ID: '62', PH: '63', PK: '92', TH: '66' }[country];
    let digits = '', plus = false, bad = false;
    for (let i = 0; i < raw.length; i++) {
      const c = raw[i];
      if (c === '+' && i === 0) plus = true;
      else if (c >= '0' && c <= '9') digits += c;
      else if (!' \t-./()'.includes(c)) bad = true;
    }
    let e164 = null;
    if (!bad && digits) {
      if (plus) e164 = '+' + digits;
      else if (digits.startsWith('00')) e164 = '+' + digits.slice(2);
      else if (digits.startsWith('0') && dial) e164 = '+' + dial + digits.slice(1);
    }
    if (!e164 || !/^\+[1-9]\d{7,14}$/.test(e164)) {
      return send(res, 400, { error: 'phone_number must be in E.164 format, e.g. +905551234567 (leading +, country code, 8-15 digits)' });
    }
    return send(res, 200, { phone_number: e164, country, valid: true });
  }
  if (req.method === 'GET' && parts[2] === 'catalog' && parts[3] === 'products' && parts[4]) {
    const fam = decodeURIComponent(parts[4]);
    const fams = familyResponse(cc, fam);
    if (!fams) return send(res, 404, { error: 'product not found' });
    return send(res, 200, fams);
  }

  /* ---- quotes ---- */
  if (route === 'POST /quotes') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const idem = req.headers['idempotency-key'];
    if (!idem || idem.length < 16) return send(res, 400, { error: 'a 16-128 character Idempotency-Key is required' });
    const b = await readBody(req);
    if (!b.product_id || !b.country) return send(res, 400, { error: 'product_id and country are required' });
    if (!b.email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(b.email)) return send(res, 400, { error: 'a valid delivery email is required (the product is delivered to it)' });
    const brand = findBrand(b.country.toUpperCase(), b.product_id);
    const fam = brand ? familyResponse(b.country.toUpperCase(), brand.family)[0] : null;
    const isTopup = fam && fam.kind === 'mobile_recharge' && fam.category !== 'e-sim';
    if (isTopup && !/^\+[1-9]\d{7,14}$/.test(b.phone_number || '')) return send(res, 400, { error: 'phone_number is required for mobile top-ups (E.164, e.g. +905551234567)' });
    // Resolve unit face value from denomination/range.
    let unitUSD = 0, unitCcy = 'USD', faceLabel = b.denomination || 'range';
    if (fam) {
      const fixed = fam.products.find((p) => p.denomination === b.denomination);
      if (fixed) { unitUSD = usdOf(fixed.amount, fixed.currency_code); unitCcy = fixed.currency_code; }
      else if (fam.products[0] && fam.products[0].range) { unitUSD = usdOf(b.product_value || fam.products[0].range.min, fam.products[0].range.currency); unitCcy = fam.products[0].range.currency; }
      else if (fam.products[0]) { unitUSD = usdOf(fam.products[0].amount, fam.products[0].currency_code); unitCcy = fam.products[0].currency_code; }
    } else { unitUSD = Number(b.product_value) || 10; }
    // The SELECTED local-currency amount (what the slider chose): fixed → the
    // denomination's own amount, range → the buyer's product_value.
    let faceLocal = 0;
    if (fam) {
      const fixed = fam.products.find((p) => p.denomination === b.denomination);
      if (fixed) faceLocal = fixed.amount;
      else if (fam.products[0] && fam.products[0].range) faceLocal = Number(b.product_value) || fam.products[0].range.min;
      else if (fam.products[0]) faceLocal = fam.products[0].amount;
    } else faceLocal = Number(b.product_value) || 0;
    const q = makeQuote({
      product_id: b.product_id, country: b.country.toUpperCase(), denomination: faceLabel,
      quantity: b.quantity || 1, email: b.email, phone_number: isTopup ? b.phone_number : '',
      unitUSD, _faceLocal: faceLocal, _productName: brand ? brand.family : b.product_id,
      _country: b.country.toUpperCase(), _currency: unitCcy, _value: unitUSD * (b.quantity || 1),
      _kind: isTopup ? 'topup' : (fam && fam.category === 'e-sim' ? 'esim' : 'gift_card'),
      _instructions: fam ? fam.product_tc : '',
    });
    return send(res, 201, q);
  }
  if (route === 'GET /quotes') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    return send(res, 200, quotes.slice().reverse()); // MOCK-PARITY FIX: bare array like the real backend
  }
  if (req.method === 'GET' && parts[2] === 'quotes' && parts[3]) {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const q = quotes.find((x) => (x.id || x.quote_id) === parts[3]);
    if (!q) return send(res, 404, { error: 'quote not found' });
    return send(res, 200, { quote: q });
  }

  /* ---- orders ---- */
  if (route === 'GET /orders') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    return send(res, 200, orders.slice().reverse()); // MOCK-PARITY FIX: real backend returns a BARE array; the {orders:[]} envelope broke the orders page in preview
  }
  if (req.method === 'GET' && parts[2] === 'orders' && parts[3]) {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const o = orders.find((x) => x.id === parts[3]);
    if (!o) return send(res, 404, { error: 'order not found' });
    const { stages, current_stage } = stagesFor(o.status, o.created_at, o.updated_at);
    return send(res, 200, { ...o, stages, current_stage });
  }
  if (req.method === 'POST' && parts[2] === 'orders' && parts[4] === 'refresh') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const o = orders.find((x) => x.id === parts[3]);
    if (!o) return send(res, 404, { error: 'order not found' });
    return send(res, 200, { ok: true, status: o.status });
  }
  if (req.method === 'POST' && parts[2] === 'orders' && parts[4] === 'rate') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const o = orders.find((x) => x.id === parts[3]);
    if (o) { const b = await readBody(req); o.rating = b.rating || 5; }
    return send(res, 200, { ok: true });
  }
  if (req.method === 'POST' && parts[2] === 'quotes' && parts[4] === 'rate') return send(res, 200, { ok: true });

  /* ---- public activity & ratings ---- */
  if (route === 'GET /activity') {
    // MOCK-PARITY FIX: mirror the real backend's {items, summary} shape.
    const items = activity.slice(0, Number(url.searchParams.get('limit')) || 50);
    const rated = activity.filter((a) => a.rating > 0);
    const dist = {};
    for (const a of rated) dist[String(a.rating)] = (dist[String(a.rating)] || 0) + 1;
    return send(res, 200, {
      items,
      summary: {
        average: rated.length ? rated.reduce((x, a) => x + a.rating, 0) / rated.length : 0,
        count: rated.length,
        dist,
        delivered_count: activity.length,
        avg_delivery_seconds: 37,
        active_users: 1 + Math.floor(Math.random() * 4),
      },
    });
  }
  if (route === 'GET /ratings/summary') {
    const rated = activity.filter((a) => a.rating > 0);
    const avg = rated.length ? rated.reduce((s, a) => s + a.rating, 0) / rated.length : 0;
    return send(res, 200, { average: avg, count: rated.length });
  }
  if (req.method === 'GET' && parts[2] === 'track' && parts[3]) {
    const o = orders.find((x) => x.id === parts[3]);
    if (!o) return send(res, 404, { error: 'not found' });
    const { stages, current_stage } = stagesFor(o.status, o.created_at, o.updated_at);
    return send(res, 200, { id: o.id, status: o.status, stages, current_stage, product: o.payload.product_name, country: o.payload.country });
  }

  /* ---- account (limits only — no balance: nim.shop is non-custodial) ---- */
  if (route === 'GET /account/limits') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    // Mirrors the real GET /api/account/limits shape (see activity_handlers.go):
    // rolling 24h purchase limits only — there is no balance.
    const usedOrders = quotes.filter((q) => q.status === 'fulfilled').length + 1;
    const usedUSD = quotes.filter((q) => q.status === 'fulfilled')
      .reduce((s, q) => s + q.product_usd / 1e6, 0) + 12.5;
    return send(res, 200, {
      used_orders: usedOrders,
      max_orders: 25,
      used_usd: usedUSD.toFixed(6),
      max_usd: 500,
      resets_at: new Date(Date.now() + 9 * 3600e3).toISOString(),
    });
  }
  if (route === 'POST /account/email/start') return send(res, 200, { ok: true, message: 'Code sent (preview: use 000000).' });
  if (route === 'POST /account/email/verify') {
    const b = await readBody(req);
    return b.code === '000000' ? send(res, 200, { ok: true, verified: true }) : send(res, 400, { error: 'wrong code' });
  }

  /* ---- support ---- */
  if (route === 'POST /support/tickets') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const b = await readBody(req);
    const t = { id: uuid(), subject: b.subject || 'Support request', status: 'open', order_id: b.order_id || null, created_at: now(), updated_at: now(), messages: [{ from: 'user', message: b.message || '', created_at: now() }] };
    tickets.push(t);
    setTimeout(() => { t.messages.push({ from: 'admin', message: 'Thanks — this is the preview mock support agent. On the live site a human replies here.', created_at: now() }); t.status = 'waiting_user'; t.updated_at = now(); }, 5000);
    return send(res, 201, t);
  }
  if (route === 'GET /support/tickets') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    return send(res, 200, { tickets: tickets.slice().reverse() });
  }
  if (req.method === 'GET' && parts[2] === 'support' && parts[3] === 'tickets' && parts[4]) {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const t = tickets.find((x) => x.id === parts[4]);
    return t ? send(res, 200, t) : send(res, 404, { error: 'ticket not found' });
  }
  if (req.method === 'POST' && parts[2] === 'support' && parts[3] === 'tickets' && parts[5] === 'messages') {
    if (!authed(req)) return send(res, 401, { error: 'not signed in' });
    const t = tickets.find((x) => x.id === parts[4]);
    if (!t) return send(res, 404, { error: 'ticket not found' });
    const b = await readBody(req);
    t.messages.push({ from: 'user', message: b.message || '', created_at: now() });
    t.status = 'waiting_admin'; t.updated_at = now();
    return send(res, 200, t);
  }

  return send(res, 404, { error: 'unknown endpoint: ' + route });
}

/* ---------------- server ---------------- */

http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const parts = url.pathname.split('/').filter(Boolean);

  if (parts[0] === 'api') {
    try { return await api(req, res, ['', 'api', ...parts.slice(1)]); }
    catch (e) { return send(res, 500, { error: 'mock error: ' + e.message }); }
  }

  let file = url.pathname === '/' ? '/index.html' : url.pathname;
  if (!path.extname(file)) file += '.html';
  const p = path.join(ROOT, path.normalize(file).replace(/^([.][.][/\\])+/, ''));
  if (!p.startsWith(ROOT)) { res.writeHead(403); return res.end('forbidden'); }
  staticFile(res, p, file);
}).listen(PORT, '0.0.0.0', () => console.log(`nim.shop preview (mock API + static) on http://0.0.0.0:${PORT}`));
