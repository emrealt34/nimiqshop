/**
 * phoneCountry.ts — the phone number is a NUMBER OF A COUNTRY, not a string of
 * digits, and for a top-up that country is decided by the PRODUCT.
 *
 * Two jobs:
 *
 *  1. ONE table of dial codes (it used to live inside giftNote.ts, where it
 *     served only the gift-SMS shortcut). `dialCodeFor` / `normalizeE164` keep
 *     the old behaviour for any caller that has a free-form number.
 *
 *  2. `parseCountryPhone` — the rule the checkout's number fields are built on:
 *     the field already knows the country, so the buyer must not have to type
 *     "+90". They type the national digits, the prefix is supplied. That makes
 *     the country LOCK structural instead of a validation message:
 *
 *       · a foreign "+49…" / "0049…" typed or PASTED into a TR field is
 *         refused with the country named, never re-prefixed into "+9049…"
 *         (which would be a different, undeliverable number);
 *       · digits that merely happen to start with another country's code are
 *         judged on local plausibility FIRST, so a valid national number is
 *         never blocked by a prefix coincidence;
 *       · a paste of "+90 555 123 45 67" into the +90 field is fine: the
 *         country code that matches the locked country is stripped;
 *       · a national length that does not exist for that country is a WARNING,
 *         not a block — the supplier's own lookup (collectPhoneValue /
 *         phone.Normalize server-side) is the authority, and it also answers
 *         "is this number real", which no format check can.
 *
 * Nothing here talks to the network: it decides SHAPE. The supplier decides
 * EXISTENCE.
 */
import { getLang, t as tr } from '../i18n';

export type PhoneCountry = {
  /** ISO-3166 alpha-2, uppercase — the code the product carries. */
  code: string;
  /** Calling code without the plus, e.g. "90". */
  dial: string;
  /** English name, used in error copy. */
  name: string;
  /** National (trunk-less) digit lengths that exist here. */
  national: number[];
  /** Does a leading 0 belong to the national format? (TR: 0555…, DE: no.) */
  trunk: boolean;
};

/**
 * The countries nim.shop can sell a number into. `national` is the length of
 * the number WITHOUT the country code, trunk prefix removed — deliberately a
 * range: it exists to catch "5 digits" and "20 digits", not to out-lawyer a
 * carrier's new numbering plan.
 */
export const PHONE_COUNTRIES: PhoneCountry[] = [
  { code: 'TR', dial: '90', name: 'Türkiye', national: [10, 10], trunk: true },
  { code: 'DE', dial: '49', name: 'Germany', national: [6, 11], trunk: false },
  { code: 'FR', dial: '33', name: 'France', national: [9, 9], trunk: true },
  { code: 'ES', dial: '34', name: 'Spain', national: [9, 9], trunk: false },
  { code: 'IT', dial: '39', name: 'Italy', national: [9, 11], trunk: false },
  { code: 'NL', dial: '31', name: 'Netherlands', national: [9, 9], trunk: false },
  { code: 'BE', dial: '32', name: 'Belgium', national: [8, 9], trunk: false },
  { code: 'AT', dial: '43', name: 'Austria', national: [4, 12], trunk: false },
  { code: 'CH', dial: '41', name: 'Switzerland', national: [9, 9], trunk: false },
  { code: 'SE', dial: '46', name: 'Sweden', national: [7, 9], trunk: false },
  { code: 'NO', dial: '47', name: 'Norway', national: [8, 8], trunk: false },
  { code: 'DK', dial: '45', name: 'Denmark', national: [8, 8], trunk: false },
  { code: 'FI', dial: '358', name: 'Finland', national: [5, 11], trunk: false },
  { code: 'IE', dial: '353', name: 'Ireland', national: [7, 9], trunk: false },
  { code: 'GB', dial: '44', name: 'United Kingdom', national: [9, 10], trunk: true },
  { code: 'PT', dial: '351', name: 'Portugal', national: [9, 9], trunk: false },
  { code: 'PL', dial: '48', name: 'Poland', national: [9, 9], trunk: false },
  { code: 'CZ', dial: '420', name: 'Czechia', national: [8, 9], trunk: false },
  { code: 'SK', dial: '421', name: 'Slovakia', national: [7, 9], trunk: false },
  { code: 'HU', dial: '36', name: 'Hungary', national: [6, 9], trunk: true },
  { code: 'RO', dial: '40', name: 'Romania', national: [8, 9], trunk: true },
  { code: 'BG', dial: '359', name: 'Bulgaria', national: [6, 8], trunk: true },
  { code: 'GR', dial: '30', name: 'Greece', national: [9, 10], trunk: false },
  { code: 'HR', dial: '385', name: 'Croatia', national: [6, 9], trunk: true },
  { code: 'UA', dial: '380', name: 'Ukraine', national: [9, 9], trunk: true },
  { code: 'US', dial: '1', name: 'the United States', national: [10, 10], trunk: true },
  { code: 'CA', dial: '1', name: 'Canada', national: [10, 10], trunk: true },
  { code: 'MX', dial: '52', name: 'Mexico', national: [10, 10], trunk: false },
  { code: 'BR', dial: '55', name: 'Brazil', national: [10, 11], trunk: false },
  { code: 'AR', dial: '54', name: 'Argentina', national: [10, 11], trunk: true },
  { code: 'AE', dial: '971', name: 'the UAE', national: [8, 9], trunk: true },
  { code: 'SA', dial: '966', name: 'Saudi Arabia', national: [9, 9], trunk: true },
  { code: 'QA', dial: '974', name: 'Qatar', national: [8, 8], trunk: false },
  { code: 'KW', dial: '965', name: 'Kuwait', national: [7, 8], trunk: false },
  { code: 'IL', dial: '972', name: 'Israel', national: [9, 9], trunk: true },
  { code: 'EG', dial: '20', name: 'Egypt', national: [10, 10], trunk: true },
  { code: 'MA', dial: '212', name: 'Morocco', national: [9, 9], trunk: true },
  { code: 'NG', dial: '234', name: 'Nigeria', national: [10, 10], trunk: true },
  { code: 'KE', dial: '254', name: 'Kenya', national: [9, 10], trunk: true },
  { code: 'ZA', dial: '27', name: 'South Africa', national: [9, 9], trunk: true },
  { code: 'IN', dial: '91', name: 'India', national: [10, 10], trunk: true },
  { code: 'PK', dial: '92', name: 'Pakistan', national: [10, 10], trunk: true },
  { code: 'ID', dial: '62', name: 'Indonesia', national: [9, 12], trunk: true },
  { code: 'MY', dial: '60', name: 'Malaysia', national: [8, 10], trunk: true },
  { code: 'PH', dial: '63', name: 'the Philippines', national: [9, 10], trunk: true },
  { code: 'TH', dial: '66', name: 'Thailand', national: [9, 9], trunk: true },
  { code: 'VN', dial: '84', name: 'Vietnam', national: [7, 10], trunk: true },
  { code: 'SG', dial: '65', name: 'Singapore', national: [8, 8], trunk: false },
  { code: 'JP', dial: '81', name: 'Japan', national: [9, 10], trunk: true },
  { code: 'KR', dial: '82', name: 'South Korea', national: [9, 10], trunk: true },
  { code: 'CN', dial: '86', name: 'China', national: [11, 11], trunk: true },
  { code: 'AU', dial: '61', name: 'Australia', national: [9, 9], trunk: true },
  { code: 'NZ', dial: '64', name: 'New Zealand', national: [8, 10], trunk: false },
];

const BY_CODE: Record<string, PhoneCountry> = {};
for (const c of PHONE_COUNTRIES) {
  // US and CA share +1; the first entry wins for a DIAL lookup, but the code
  // lookup below is always exact, so nothing is guessed.
  BY_CODE[c.code] = c;
}

/** Some catalog rows carry a marketing region instead of an ISO code. */
const CODE_ALIASES: Record<string, string> = {
  TR_EU: 'TR',
  EN: 'US',
  UK: 'GB',
  USA: 'US',
};

/** The catalog entry for a product's country, or null when we have no table
 *  entry — the caller must then fall back to a free-form field, never guess. */
export function phoneCountry(code?: string): PhoneCountry | null {
  const raw = String(code || '').trim().toUpperCase();
  if (!raw) return null;
  return BY_CODE[CODE_ALIASES[raw] || raw] || null;
}

/** Calling code for a country, "" when unknown. */
export function dialCodeFor(code?: string): string {
  return phoneCountry(code)?.dial || '';
}

/** 🇹🇷 from "TR". Two ASCII letters or nothing — a wrong flag is worse than a
 *  missing one, and "TR_EU"/"" must not paint a random country. */
export function flagEmoji(code?: string): string {
  const c = String(code || '').trim().toUpperCase();
  if (!/^[A-Z]{2}$/.test(c)) return '';
  let out = '';
  for (const ch of c) out += String.fromCodePoint(0x1f1e6 + (ch.charCodeAt(0) - 65));
  return out;
}

/** The country's number spelled the way a buyer writes it: 0555 123 45 67. */
export function nationalExample(c: PhoneCountry): string {
  if (c.code === 'TR') return '555 123 45 67';
  if (c.code === 'US' || c.code === 'CA') return '555 123 4567';
  if (c.code === 'DE') return '151 2345678';
  if (c.code === 'GB') return '7400 123456';
  return `${'5'.repeat(Math.max(7, c.national[0]))} ${'1'.repeat(2)}`;
}

export type ParsedPhone = {
  /** Digits the buyer owns, without trunk prefix or country code. */
  national: string;
  /** What gets stored and sent: +<dial><national>. "" until it is complete. */
  e164: string;
  /** Blocks the field: wrong country, letters, nothing to dial. */
  error: string;
  /** Says "that length does not exist here" without blocking. */
  warning: string;
  /** The locked country's dial code, so callers can echo it in copy. */
  dial: string;
};

const NO_INPUT: ParsedPhone = { national: '', e164: '', error: '', warning: '', dial: '' };

/**
 * Parse a number typed into a field that ALREADY knows the country.
 *
 * `raw` is whatever the field holds: national digits, "+90 555…", "0090…", a
 * pasted foreign number. `code` is the PRODUCT's country.
 */
export function parseCountryPhone(raw: string, code?: string): ParsedPhone {
  const c = phoneCountry(code);
  const s = String(raw || '').trim();
  if (!c) {
    // No table entry: refuse to invent a prefix. The caller shows a plain field.
    return { ...NO_INPUT, national: s.replace(/[^\d]/g, '') };
  }
  const locked: ParsedPhone = { national: '', e164: '', error: '', warning: '', dial: c.dial };
  if (!s) return locked;
  if (/[^\d+()\-\s.]/.test(s)) {
    return { ...locked, error: tr('phone.digitsOnly') };
  }

  const hasPlus = s.trim().startsWith('+');
  let d = s.replace(/[^\d]/g, '');
  if (!d) return { ...locked, error: hasPlus ? tr('phone.addAfterCode') : tr('phone.enterNumber') };

  // "00" is how a landline-era buyer writes an international call. Inside a
  // country-locked field it can only mean "I am giving you a country code" —
  // so judge it as one, and refuse it if it is not OURS.
  const international = hasPlus || d.startsWith('00');
  if (d.startsWith('00')) d = d.slice(2);

  if (international) {
    if (d.startsWith(c.dial)) {
      // Our OWN country code: unwrap it even when the rest is still missing —
      // someone typing "+90" in a +90 box deserves "now the number", not a
      // wrong-country refusal.
      return finish(d.slice(c.dial.length).replace(/^0+/, ''), c, true);
    }
    return { ...locked, error: wrongCountry(c) };
  }

  // National input. A leading trunk 0 belongs to the way the country writes it
  // domestically, so it is dropped for the stored form.
  let nat = d;
  if (nat.startsWith(c.dial) && nat.length > c.dial.length + 5) {
    // "+90" left out of the paste but the code still typed: tolerate it.
    nat = nat.slice(c.dial.length);
  }
  // A leading 0 is the domestic trunk prefix — "0555…" in TR, "0151…" from
  // someone who learnt German mobiles the old way. It can never be part of a
  // stored subscriber number, so it is dropped whatever the country claims.
  nat = nat.replace(/^0+/, '');
  if (!nat) return { ...locked, error: wrongCountry(c) };

  // A foreign number typed WITHOUT +/00 — "4915123456789" into a TR field.
  // Only called a foreign country when it cannot be read locally at all: a
  // prefix coincidence must never block a real national number.
  if (!looksLocal(nat, c)) {
    const other = matchForeign(nat);
    if (other) {
      return { ...locked, error: tr('phone.notTheCountry', { other: nameOf(other.code), body: wrongCountry(c) }) };
    }
  }
  return finish(nat, c, false);
}

/** Is this a length that exists for the country, with a usable leading digit? */
function looksLocal(nat: string, c: PhoneCountry): boolean {
  if (!/^[1-9]\d+$/.test(nat)) return false;
  return nat.length >= c.national[0] && nat.length <= c.national[1];
}

/** Longest dial code that fits the front of these digits, with a plausible
 *  remainder. Sorted once, longest first, so +1 vs +1809 cannot mis-split. */
let FOREIGN: PhoneCountry[] | null = null;
function matchForeign(nat: string): PhoneCountry | null {
  if (!FOREIGN) {
    FOREIGN = [...PHONE_COUNTRIES].sort((a, b) => b.dial.length - a.dial.length);
  }
  for (const c of FOREIGN) {
    if (!nat.startsWith(c.dial) || nat.length <= c.dial.length) continue;
    const rest = nat.slice(c.dial.length);
    if (rest.length >= c.national[0] && rest.length <= c.national[1] && /^[1-9]/.test(rest)) return c;
  }
  return null;
}

function nameOf(code: string): string {
  return countryLabel(code);
}

/**
 * Country name in the ACTIVE language for everything the buyer reads (the
 * country picker and the validation messages). Intl.DisplayNames ships with the
 * browser, so 200 countries come out translated without 200 keys; the table's
 * own English name is the fallback when Intl is missing.
 */
export function countryLabel(code: string): string {
  const iso = String(code || '').trim().toUpperCase();
  if (iso.length !== 2) return phoneCountry(iso)?.name || iso;
  try {
    const Display = (Intl as any).DisplayNames;
    if (Display) {
      const names = new Display([getLang()], { type: 'region' });
      const label = names.of(iso);
      if (label && label !== iso) return label;
    }
  } catch {
    /* older engine — fall through to the table */
  }
  return phoneCountry(iso)?.name || iso;
}

function wrongCountry(c: PhoneCountry): string {
  return tr('phone.wrongCountry', {
    country: countryLabel(c.code),
    dial: c.dial,
    example: nationalExample(c),
  });
}

/** Compose the stored value. `typedFull` marks a number that came in with its
 *  own country code, which is the one case where a too-short remainder is a
 *  hard error instead of a warning (there is nothing left to type). */
function finish(national: string, c: PhoneCountry, typedFull: boolean): ParsedPhone {
  if (!/^[1-9]\d*$/.test(national)) {
    // A number that cannot start with 0 is never a country we could dial — say
    // what is missing instead of blaming the country.
    return {
      national: '',
      e164: '',
      warning: '',
      dial: c.dial,
      error: typedFull ? tr('phone.addSubscriber', { dial: c.dial }) : tr('phone.enterNumber'),
    };
  }
  const e164 = `+${c.dial}${national}`;
  const len = national.length;
  // A length that does not exist here is a WARNING, never a block: the buyer
  // may still be typing, and the supplier lookup is what decides existence.
  const warning =
    len < c.national[0] || len > c.national[1]
      ? tr('phone.digitWarning', {
          country: countryLabel(c.code),
          len: c.national[0] === c.national[1] ? String(c.national[0]) : `${c.national[0]}–${c.national[1]}`,
          dial: c.dial,
          actual: String(len),
        })
      : '';
  return { national, e164, warning, dial: c.dial, error: '' };
}

/**
 * Old contract, kept for callers that have a FREE-FORM number (an API payload,
 * a stored order): normalize into strict E.164 and refuse anything ambiguous.
 * The country is a hint for a leading 0, never a lock.
 */
export function normalizeE164(raw: string, country?: string): { phone: string; error: string } {
  let s = String(raw || '').trim();
  if (!s) return { phone: '', error: tr('phone.enterPhone') };
  const hasPlus = s.startsWith('+');
  s = s.replace(/[^\d+]/g, '').replace(/\+/g, '');
  if (!s) return { phone: '', error: tr('phone.enterPhone') };
  // "00" is the international access prefix: a number that starts with it has
  // already declared its country, so it needs no hint from the cart.
  const international = hasPlus || s.startsWith('00');
  if (s.startsWith('00')) s = s.slice(2);
  const c = phoneCountry(country);
  if (!international) {
    if (!c) return { phone: '', error: tr('phone.intlGeneric') };
    if (s.startsWith('0') || s.startsWith(c.dial)) {
      s = c.dial + s.replace(/^0+/, '').slice(s.startsWith('0') ? 0 : c.dial.length);
    } else {
      return { phone: '', error: tr('phone.intlWithDial', { dial: c.dial, example: `+${c.dial}${nationalExample(c)}` }) };
    }
  }
  if (!/^[1-9]\d{6,14}$/.test(s)) {
    return { phone: '', error: tr('phone.invalidMsisdn') };
  }
  return { phone: `+${s}`, error: '' };
}
