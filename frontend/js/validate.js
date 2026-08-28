/* validate.js — STRICT client-side validation, kept at the same severity as
 * the backend so a customer never gets through the UI with input the server
 * would reject.
 *
 *   - email: same field the backend validates before talking to the supplier
 *     (Go net/mail.ParseAddress + length bounds) — this checker is stricter,
 *     which is safe: anything accepted here is accepted server-side too.
 *   - phone: a 1:1 JavaScript port of backend/internal/phone/phone.go —
 *     the same separator rules, the same dial-code table, the same
 *     fail-closed behavior for bare national numbers, and the same error
 *     messages. The backend re-validates anyway (defense in depth), and the
 *     /catalog/check-phone endpoint still performs the live supplier check.
 */

/* ----------------------------- email ------------------------------------ */

// Strict, pragmatic email shape: local@domain.tld with sane charsets, no
// leading/trailing/consecutive dots, TLD of at least 2 letters. Total
// length bounded exactly like the backend (6..254), local part <= 64.
const EMAIL_LOCAL_RE = /^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$/;
const EMAIL_DOMAIN_RE = /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}$/;

export function isValidEmail(s) {
  const v = String(s == null ? '' : s).trim();
  if (v.length < 6 || v.length > 254) return false;
  if (v.includes('..')) return false;
  const at = v.lastIndexOf('@');
  if (at <= 0 || at !== v.indexOf('@')) return false; // exactly one @, not first
  const local = v.slice(0, at);
  const domain = v.slice(at + 1);
  if (local.length === 0 || local.length > 64) return false;
  if (!EMAIL_LOCAL_RE.test(local)) return false;
  if (domain.length < 4 || domain.length > 253) return false;
  return EMAIL_DOMAIN_RE.test(domain);
}

export function emailError(s) {
  const v = String(s == null ? '' : s).trim();
  if (!v) return 'Email is required — your code is delivered to it.';
  if (v.indexOf('@') === -1) return 'That email has no @ — enter a full address like you@gmail.com.';
  if (v.length > 254) return 'That email is too long to be valid.';
  return 'Enter a valid email, e.g. you@gmail.com — the code is delivered there.';
}

/* ----------------------------- phone ------------------------------------- */

// Strict E.164: leading +, non-zero first country-code digit, 8-15 digits
// in total (ITU E.164) — identical to backend phone.Validate.
const E164_RE = /^\+[1-9]\d{7,14}$/;
function isSeparator(ch) {
  return ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r' || ch === '-' || ch === '.' || ch === '(' || ch === ')' || ch === '/';
}

// 1:1 copy of backend internal/phone dialCodes — countries where "strip ONE
// leading trunk 0, then prepend the dial code" is the correct E.164
// conversion. Countries with special access prefixes are intentionally
// ABSENT (fail closed with a clear message instead of guessing).
const DIAL_CODES = {
  // Europe
  AL: '355', AD: '376', AT: '43', BY: '375', BE: '32', BA: '387',
  BG: '359', HR: '385', CY: '357', CZ: '420', DK: '45', EE: '372',
  FO: '298', FI: '358', FR: '33', GE: '995', DE: '49', HU: '36',
  IS: '354', IE: '353', IM: '44', IT: '39', LV: '371', LI: '423',
  LT: '370', LU: '352', MT: '356', ME: '382', MK: '389', NL: '31',
  NO: '47', PL: '48', PT: '351', RO: '40', RU: '7', SM: '378',
  RS: '381', SK: '421', SI: '386', ES: '34', SE: '46', CH: '41',
  TR: '90', UA: '380', GB: '44', MD: '373',
  // Middle East & North Africa
  AE: '971', AF: '93', AZ: '994', BH: '973', DJ: '253', EG: '20',
  IL: '972', IQ: '964', IR: '98', JO: '962', KW: '965', LB: '961',
  LY: '218', MA: '212', MR: '222', OM: '968', PS: '970', QA: '974',
  SA: '966', SD: '249', SY: '963', TN: '216', YE: '967', DZ: '213',
  // Africa
  AO: '244', BF: '226', BI: '257', BJ: '229', BW: '267', CD: '243',
  CF: '236', CG: '242', CI: '225', CM: '237', CV: '238', ER: '291',
  ET: '251', GA: '241', GH: '233', GM: '220', GN: '224', GQ: '240',
  GW: '245', KE: '254', LS: '266', LR: '231', MG: '261', ML: '223',
  MN: '976', MU: '230', MW: '265', MZ: '258', NA: '264', NE: '227',
  NG: '234', RW: '250', SC: '248', SL: '232', SN: '221', SO: '252',
  SS: '211', ST: '239', SZ: '268', TD: '235', TG: '228', TZ: '255',
  UG: '256', ZA: '27', ZM: '260', ZW: '263',
  // Asia
  AM: '374', BT: '975', BD: '880', BN: '673', KH: '855', CN: '86',
  IN: '91', ID: '62', JP: '81', KZ: '7', KP: '850', KG: '996',
  LA: '856', LK: '94', MM: '95', MY: '60', MV: '960', NP: '977',
  PH: '63', PK: '92', SG: '65', KR: '82', TH: '66', TJ: '992',
  TL: '670', UZ: '998', VN: '84',
  // Oceania
  AU: '61', FJ: '679', NZ: '64', PG: '675', WS: '685',
  // Latin America
  AR: '54', BO: '591', BR: '55', CL: '56', CO: '57', CR: '506',
  CU: '53', EC: '593', GT: '502', HN: '504', NI: '505', PA: '507',
  PE: '51', PY: '595', SV: '503', UY: '598', VE: '58',
};

// Countries whose E.164 numbers KEEP the national leading 0 (Italy).
const KEEP_ZERO = { IT: true };

function validDigits(d) {
  if (d.length < 8 || d.length > 15 || d[0] === '0') return false;
  for (let i = 0; i < d.length; i++) {
    const c = d.charCodeAt(i);
    if (c < 48 || c > 57) return false;
  }
  return true;
}

const ERR_E164 = 'Enter a valid phone number in international format, e.g. +905551234567 (leading +, country code, 8-15 digits).';
const ERR_NEED_COUNTRY = 'The number must include the country code — use the international format with a leading +, e.g. +905551234567.';
const ERR_REQUIRED = 'Phone number is required (international format, e.g. +905551234567).';
const ERR_INVALID_CHARS = 'Phone number contains invalid characters — only digits, separators (space . - ( ) /) and a leading + are allowed.';

// normalizePhone mirrors backend phone.Normalize. Returns
// { phone: '+905551234567', error: null } on success or
// { phone: null, error: 'message' } on failure. Never guesses a country
// for a bare number: a wrong guess delivers the top-up to someone else.
export function normalizePhone(raw, countryISO) {
  const s = String(raw == null ? '' : raw).trim();
  if (!s) return { phone: null, error: ERR_REQUIRED };
  let digits = '';
  let hasPlus = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (ch === '+' && i === 0 && !hasPlus) { hasPlus = true; continue; }
    if (ch >= '0' && ch <= '9') { digits += ch; continue; }
    if (isSeparator(ch)) continue;
    return { phone: null, error: ERR_INVALID_CHARS };
  }
  const d = digits;
  if (!d) return { phone: null, error: ERR_REQUIRED };

  if (hasPlus) {
    if (!validDigits(d)) return { phone: null, error: ERR_E164 };
    return { phone: '+' + d, error: null };
  }
  if (d.startsWith('00')) {
    const rest = d.slice(2);
    if (!validDigits(rest)) return { phone: null, error: ERR_E164 };
    return { phone: '+' + rest, error: null };
  }
  if (d.startsWith('0')) {
    const country = String(countryISO == null ? '' : countryISO).toUpperCase().trim();
    const dial = DIAL_CODES[country];
    if (!dial) return { phone: null, error: ERR_NEED_COUNTRY };
    const num = KEEP_ZERO[country] ? d : d.slice(1);
    const combined = dial + num;
    if (!validDigits(combined)) return { phone: null, error: ERR_E164 };
    return { phone: '+' + combined, error: null };
  }
  // Bare national number without trunk 0 — fail closed, same as backend.
  return { phone: null, error: ERR_NEED_COUNTRY };
}
