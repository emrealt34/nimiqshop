/**
 * validate.ts — strict client-side validation (ported from validate.js).
 * Kept at the same severity as the backend. Every buyer-facing message is
 * translated at CALL time, so a language switch is honoured immediately.
 */
import { t as tr } from '../i18n';

/* ----------------------------- email ------------------------------------ */
const EMAIL_LOCAL_RE = /^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$/;
const EMAIL_DOMAIN_RE = /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}$/;

export function isValidEmail(s: unknown): boolean {
  const v = String(s == null ? '' : s).trim();
  if (v.length < 6 || v.length > 254) return false;
  if (v.includes('..')) return false;
  const at = v.lastIndexOf('@');
  if (at <= 0 || at !== v.indexOf('@')) return false;
  const local = v.slice(0, at);
  const domain = v.slice(at + 1);
  if (local.length === 0 || local.length > 64) return false;
  if (!EMAIL_LOCAL_RE.test(local)) return false;
  if (domain.length < 4 || domain.length > 253) return false;
  return EMAIL_DOMAIN_RE.test(domain);
}

export function emailError(s: unknown): string {
  const v = String(s == null ? '' : s).trim();
  if (!v) return tr('validate.emailRequired');
  if (v.indexOf('@') === -1) return tr('validate.emailNoAt');
  if (v.length > 254) return tr('validate.emailTooLong');
  return tr('validate.emailInvalid');
}

/* ----------------------------- phone ------------------------------------- */
function isSeparator(ch: string): boolean {
  return ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r' || ch === '-' || ch === '.' || ch === '(' || ch === ')' || ch === '/';
}

const DIAL_CODES: Record<string, string> = {
  AL: '355', AD: '376', AT: '43', BY: '375', BE: '32', BA: '387',
  BG: '359', HR: '385', CY: '357', CZ: '420', DK: '45', EE: '372',
  FO: '298', FI: '358', FR: '33', GE: '995', DE: '49', HU: '36',
  IS: '354', IE: '353', IM: '44', IT: '39', LV: '371', LI: '423',
  LT: '370', LU: '352', MT: '356', ME: '382', MK: '389', NL: '31',
  NO: '47', PL: '48', PT: '351', RO: '40', RU: '7', SM: '378',
  RS: '381', SK: '421', SI: '386', ES: '34', SE: '46', CH: '41',
  TR: '90', UA: '380', GB: '44', MD: '373',
  AE: '971', AF: '93', AZ: '994', BH: '973', DJ: '253', EG: '20',
  IL: '972', IQ: '964', IR: '98', JO: '962', KW: '965', LB: '961',
  LY: '218', MA: '212', MR: '222', OM: '968', PS: '970', QA: '974',
  SA: '966', SD: '249', SY: '963', TN: '216', YE: '967', DZ: '213',
  AO: '244', BF: '226', BI: '257', BJ: '229', BW: '267', CD: '243',
  CF: '236', CG: '242', CI: '225', CM: '237', CV: '238', ER: '291',
  ET: '251', GA: '241', GH: '233', GM: '220', GN: '224', GQ: '240',
  GW: '245', KE: '254', LS: '266', LR: '231', MG: '261', ML: '223',
  MN: '976', MU: '230', MW: '265', MZ: '258', NA: '264', NE: '227',
  NG: '234', RW: '250', SC: '248', SL: '232', SN: '221', SO: '252',
  SS: '211', ST: '239', SZ: '268', TD: '235', TG: '228', TZ: '255',
  UG: '256', ZA: '27', ZM: '260', ZW: '263',
  AM: '374', BT: '975', BD: '880', BN: '673', KH: '855', CN: '86',
  IN: '91', ID: '62', JP: '81', KZ: '7', KP: '850', KG: '996',
  LA: '856', LK: '94', MM: '95', MY: '60', MV: '960', NP: '977',
  PH: '63', PK: '92', SG: '65', KR: '82', TH: '66', TJ: '992',
  TL: '670', UZ: '998', VN: '84',
  AU: '61', FJ: '679', NZ: '64', PG: '675', WS: '685',
  AR: '54', BO: '591', BR: '55', CL: '56', CO: '57', CR: '506',
  CU: '53', EC: '593', GT: '502', HN: '504', NI: '505', PA: '507',
  PE: '51', PY: '595', SV: '503', UY: '598', VE: '58',
};

const KEEP_ZERO: Record<string, boolean> = { IT: true };

function validDigits(d: string): boolean {
  if (d.length < 8 || d.length > 15 || d[0] === '0') return false;
  for (let i = 0; i < d.length; i++) {
    const c = d.charCodeAt(i);
    if (c < 48 || c > 57) return false;
  }
  return true;
}


export function normalizePhone(raw: unknown, countryISO?: string | null): { phone: string | null; error: string | null } {
  const s = String(raw == null ? '' : raw).trim();
  if (!s) return { phone: null, error: tr('validate.phoneRequired') };
  let digits = '';
  let hasPlus = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (ch === '+' && i === 0 && !hasPlus) {
      hasPlus = true;
      continue;
    }
    if (ch >= '0' && ch <= '9') {
      digits += ch;
      continue;
    }
    if (isSeparator(ch)) continue;
    return { phone: null, error: tr('validate.phoneInvalidChars') };
  }
  const d = digits;
  if (!d) return { phone: null, error: tr('validate.phoneRequired') };

  if (hasPlus) {
    if (!validDigits(d)) return { phone: null, error: tr('validate.phoneIntl') };
    return { phone: '+' + d, error: null };
  }
  if (d.startsWith('00')) {
    const rest = d.slice(2);
    if (!validDigits(rest)) return { phone: null, error: tr('validate.phoneIntl') };
    return { phone: '+' + rest, error: null };
  }
  if (d.startsWith('0')) {
    const country = String(countryISO == null ? '' : countryISO).toUpperCase().trim();
    const dial = DIAL_CODES[country];
    if (!dial) return { phone: null, error: tr('validate.phoneNeedCountry') };
    const num = KEEP_ZERO[country] ? d : d.slice(1);
    const combined = dial + num;
    if (!validDigits(combined)) return { phone: null, error: tr('validate.phoneIntl') };
    return { phone: '+' + combined, error: null };
  }
  return { phone: null, error: tr('validate.phoneNeedCountry') };
}
