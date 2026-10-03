/**
 * hostLang.ts — where the shop's STARTING language and market come from when
 * the visitor has not chosen one.
 *
 * Inside Nimiq Pay the host injects the user's chosen app language BEFORE any
 * script runs, at `window.nimiqPay.language` (ISO 639-1, e.g. "de"). The
 * official SDK's `getHostLanguage()` is a one-line reader of exactly that field
 * (@nimiq/mini-app-sdk 0.2.4). Docs:
 * https://nimiq.dev/mini-apps/features/localization
 *
 * There is deliberately NO IP/geo lookup anywhere in this file. The buyer's
 * IP reaches the backend through the Cloudflare tunnel, so a geo lookup would
 * geolocate the tunnel egress (or a VPN), not the buyer; the device's and the
 * host app's own language settings are the honest signals.
 *
 * This module imports nothing — not even the SDK — because `src/i18n` (entry
 * bundle) and the pre-paint boot script in Base.astro both use it and the
 * budget is ~220 KB JS. Everything here is SSR-safe: without a DOM every
 * reader returns null/[] so the server render never depends on the visitor.
 */

const TWO_LETTER = /^[a-z]{2}$/;

function lower2(raw: unknown): string | null {
  if (typeof raw !== 'string') return null;
  const code = raw.trim().toLowerCase().slice(0, 2);
  return TWO_LETTER.test(code) ? code : null;
}

function navigatorTags(): string[] {
  if (typeof navigator === 'undefined') return [];
  const raw = [...(navigator.languages || []), navigator.language].filter(Boolean) as string[];
  return raw.map((t) => String(t).trim()).filter(Boolean);
}

/** Nimiq Pay's own UI language ("de"), or null outside the mini app. */
export function hostLanguage(): string | null {
  if (typeof window === 'undefined') return null;
  const w = window as unknown as { nimiqPay?: { language?: unknown } };
  return lower2(w.nimiqPay?.language);
}

/** The device's preferred languages, most preferred first, 2-letter codes. */
export function deviceLanguages(): string[] {
  const out: string[] = [];
  for (const tag of navigatorTags()) {
    const code = lower2(tag);
    if (code && !out.includes(code)) out.push(code);
  }
  return out;
}

/** First region subtag the device asks for, uppercase ("tr-TR" → "TR"). */
export function deviceRegion(): string | null {
  for (const tag of navigatorTags()) {
    const m = /^[a-z]{2,3}[-_]([a-z]{2})\b/i.exec(tag);
    if (m) return m[1].toUpperCase();
  }
  return null;
}

/** The country a bare language usually means (when the locale has no region). */
const LANGUAGE_COUNTRY: Record<string, string> = {
  tr: 'TR',
  de: 'DE',
  fr: 'FR',
  es: 'ES',
  pt: 'PT',
};

/**
 * Market candidates in priority order — the device's own region first, then
 * whatever the Nimiq Pay language (and failing that the device language)
 * usually means. The caller picks the first candidate the shop actually
 * serves, so an unsupported region (de-AT when only DE exists) falls through
 * instead of leaving the visitor on the SSR default.
 */
export function countryCandidates(): string[] {
  const out: string[] = [];
  const push = (c: string | null) => {
    if (c && !out.includes(c)) out.push(c);
  };
  push(deviceRegion());
  push(LANGUAGE_COUNTRY[hostLanguage() ?? ''] ?? null);
  for (const lang of deviceLanguages()) push(LANGUAGE_COUNTRY[lang] ?? null);
  return out;
}
