/**
 * i18n — ultra-lightweight, fully-typed, runtime translation layer for nim.shop.
 *
 * WHY WE WROTE OUR OWN (and did NOT pull i18next / react-i18next)
 * ----------------------------------------------------------------
 *   • Zero deps. The shop ships on 220 KB JS budgets (miniapp SDK already claims a
 *     fair chunk). i18next + react-i18next is ~22 KB gz for something we use in
 *     80 % of screens — this file is <3 KB gz and does everything the shop needs.
 *   • Keys are typed. Every language file must satisfy the same shape as the
 *     default (en), and TypeScript refuses a build that misses a key. So when a
 *     developer adds "checkout.submit" in en.ts they get a red squiggle in es.ts
 *     / de.ts / fr.ts / pt.ts / tr.ts immediately.
 *   • Strings are loaded STATICALLY. No async fetch of a JSON chunk when the
 *     user switches languages — all six bundles are part of the initial JS
 *     (each is ~10 KB gz total, see scripts/check-i18n.mjs) so switching is
 *     instant and works offline / in the Nimiq Pay miniapp.
 *   • Pluralisation + interpolation built in. `t('cart.items', { count: 3 })`
 *     picks the right plural form for the active language.
 *
 * HOW TO USE
 * ----------
 *   // 1. In a React component:
 *   import { useT } from '../i18n';
 *   const { t, lang, setLang } = useT();
 *   return <button>{t('nav.orders')}</button>;
 *
 *   // 2. Outside React (plain module, API handler, toast from a non-component):
 *   import { t, setLang, detectLang } from '../i18n';
 *   toast(t('checkout.connected'));
 *
 *   // 3. Interpolation:
 *   t('toast.connected', { addr: shortAddr(a) })
 *   // -> uses {{addr}} in the string.
 *
 *   // 4. Plural (pass `count`):
 *   t('cart.n_items', { count: n })
 *   // -> picks one/other (es/fr/pt/ de/en) or one/some/other for Turkish (zero is
 *   //    mapped to other; Turkish plural is count===1 → singular, otherwise plural).
 *
 * ADDING A NEW LANGUAGE
 * ---------------------
 *   1. Copy src/i18n/locales/en.ts to src/i18n/locales/<code>.ts and translate.
 *   2. Add the code to the `LANGS` array below, with its flag + native label.
 *   3. The backend copy lives in backend/internal/i18n/locales/<code>.go and is
 *      a Go map of the SAME keys (verified by scripts/check-i18n.mjs).
 *
 * PERSISTENCE + AUTO-DETECT
 * -------------------------
 *   • Chosen language is saved in localStorage (key STORAGE_KEY below) and sent
 *     back to the server as the `?lang=` query param + a `nimshop-lang` cookie so
 *     the backend sends email in the buyer's language.
 *   • On first visit we pick a language by (in order):
 *       (a) `?lang=` on the URL,
 *       (b) the `nimshop-lang` cookie,
 *       (c) localStorage (`nimshop.lang`),
 *       (d) navigator.language / navigator.languages (first 2 chars),
 *       (e) English default.
 *
 *   The site is fully static: Base.astro ships a tiny inline script that
 *   runs before paint and writes <html lang> using the same chain so the
 *   first paint is correct before React hydrates.
 */
import React, { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

import en from './locales/en';
import { deviceLanguages, hostLanguage } from '../lib/hostLang';

export type LangCode = 'en' | 'es' | 'de' | 'fr' | 'pt' | 'tr';

/**
 * A plural dictionary is a leaf `{one, other}` — the translator picks between
 * them based on `vars.count`. It must never be treated as a nested namespace
 * when computing DictKey.
 */
export interface Plural { one: string; other: string; [k: string]: string }

/**
 * Deep-readonly string dictionary shape. We deliberately do NOT use `typeof en`
 * directly as Dict, because `as const` narrows every leaf to a string literal
 * type (e.g. 'Shop' not string) which then rejects translated values ("Tienda").
 * Instead, recursively relax to `string` leaves while preserving the key-tree
 * shape, so TypeScript still validates key names but accepts any translation.
 */
type Relax<T> = T extends string
  ? string
  : T extends { one: string; other: string }
    ? { one: string; other: string }
    : { [K in keyof T]: Relax<T[K]> };

export type Dict = Relax<typeof en>;
/**
 * A dot-path into the dictionary (e.g. 'nav.orders'). Key autocomplete is
 * derived from the EN dictionary (source of truth) but accepts any string
 * for runtime safety (so a late-added key in JS never red-squiggles).
 */
export type DictKey = NestedKey<typeof en> | (string & {});

/** Descriptor for one supported language. */
export interface LangMeta {
  code: LangCode;
  /** Native label — shown in the switcher (e.g. "Deutsch", not "German"). */
  label: string;
  /** English label for accessibility / alt text. */
  english: string;
  /** Flag country-code (uses existing /img/flags/*.svg assets). */
  flag: string;
}

/** The six languages the shop launches with. Order is the switcher order. */
export const LANGS: LangMeta[] = [
  { code: 'en', label: 'English', english: 'English', flag: 'gb' },
  { code: 'es', label: 'Español', english: 'Spanish', flag: 'es' },
  { code: 'de', label: 'Deutsch', english: 'German',  flag: 'de' },
  { code: 'fr', label: 'Français', english: 'French', flag: 'fr' },
  { code: 'pt', label: 'Português', english: 'Portuguese', flag: 'pt' },
  { code: 'tr', label: 'Türkçe', english: 'Turkish', flag: 'tr' },
];

/*
 * LAZY DICTIONARIES
 * -----------------
 * english ships in the entry bundle (it is the fallback every render needs);
 * the other five travel as their own chunks and load the moment the language
 * is actually selected. Statically importing all six meant ~600 KB of source
 * (≈130 KB gzipped) of translations were downloaded, parsed and executed on
 * every page view in every language, which is what Lighthouse measured as
 * unused JavaScript and blocking main-thread time.
 */
const LOADERS: Record<string, () => Promise<{ default: Dict }>> = {
  es: () => import('./locales/es'),
  de: () => import('./locales/de'),
  fr: () => import('./locales/fr'),
  pt: () => import('./locales/pt'),
  tr: () => import('./locales/tr'),
};

const DICTS: Record<string, Dict> = { en: en as Dict };
const pending = new Map<string, Promise<void>>();

/*
 * DICTIONARY CACHE (localStorage)
 * ------------------------------
 * The five non-English dictionaries are ~100 KB each and used to be fetched on
 * every page view; the page also had to stay hidden until that fetch landed,
 * which is why the pre-paint hold (and its loader) existed.
 *
 * A returning visitor does not need the network for this at all: the module
 * writes the dictionary it just used into localStorage, and the next visit
 * reads it back synchronously at module init. Base.astro checks the same key
 * before setting the hold, so for a cached language there is no hold, no
 * loader and no English frame — the first paint is already translated.
 *
 * Freshness: the cached copy is validated in the background every load (the
 * chunk is content-hashed and modulepreloaded anyway). If the deployed strings
 * changed, the cache is replaced and the language is re-applied — same
 * language, newer copy, so the visitor sees a text fix, never a language
 * switch. A deploy that adds keys can therefore never leave a visitor mixing
 * languages for longer than that background fetch.
 *
 * The key has no build id on purpose: presence is what Base.astro needs to
 * know pre-paint, and the content itself is revalidated on every load.
 */
const DICT_CACHE_PREFIX = 'nimshop.dict.';

function readCachedDict(code: LangCode): Dict | null {
  if (typeof localStorage === 'undefined') return null;
  try {
    const raw = localStorage.getItem(DICT_CACHE_PREFIX + code);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as Dict) : null;
  } catch {
    return null;
  }
}

function writeCachedDict(code: LangCode, dict: Dict) {
  if (typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(DICT_CACHE_PREFIX + code, JSON.stringify(dict));
  } catch {
    /* private mode or quota — the network path stays the fallback */
  }
}

/** True when this language's dictionary is already cached for the next visit. */
export function dictCached(code: LangCode): boolean {
  if (typeof localStorage === 'undefined') return false;
  try {
    return !!localStorage.getItem(DICT_CACHE_PREFIX + code);
  } catch {
    return false;
  }
}

/** Load (once) the dictionary for `code`. English is always available. */
export function loadDict(code: LangCode): Promise<void> {
  if (DICTS[code]) return Promise.resolve();
  const loader = LOADERS[code];
  if (!loader) return Promise.resolve();
  const inflight = pending.get(code);
  if (inflight) return inflight;
  const cached = readCachedDict(code);
  if (cached) DICTS[code] = cached;
  const job = loader()
    .then((mod) => {
      const fresh = mod.default as Dict;
      if (!cached) {
        DICTS[code] = fresh;
        writeCachedDict(code, fresh);
        return;
      }
      // Revalidate: has the deployed copy moved on? Compare cheaply, then swap
      // in the new strings without a reload (language unchanged → no flash).
      if (JSON.stringify(fresh) !== JSON.stringify(cached)) {
        DICTS[code] = fresh;
        writeCachedDict(code, fresh);
        if (currentLang === code) applyLang(code, false);
      }
    })
    .catch(() => { /* offline: the cached copy (or English) keeps the UI readable */ })
    .finally(() => { pending.delete(code); });
  pending.set(code, job);
  return job;
}

/** True when the dictionary is already in memory (used to avoid an EN flash). */
export function dictLoaded(code: LangCode): boolean {
  return !!DICTS[code];
}

const STORAGE_KEY = 'nimshop.lang';
const COOKIE_NAME = 'nimshop-lang';
/**
 * The visitor's OWN pick (the language switcher). Kept apart from the
 * auto-detected value in STORAGE_KEY because the two mean different things:
 * an explicit choice must survive a new device locale and the host app's
 * language, while an auto-detected value must not outrank either of them.
 * Written only by the switcher (setUserLang below); nothing auto-saves here.
 */
const USER_KEY = 'nimshop.lang.user';
const DEFAULT_LANG: LangCode = 'en';

const BY_CODE: Record<LangCode, LangMeta> = LANGS.reduce((acc, l) => {
  acc[l.code] = l;
  return acc;
}, {} as Record<LangCode, LangMeta>);

/* ---------------- Helpers ---------------- */

/** Matches a plural leaf {one, other} so we don't treat it as a namespace. */
type IsPlural<T> = T extends object ? 'one' extends keyof T ? 'other' extends keyof T ? true : false : false : false;

/** Nested-key type helper — turns a tree of objects into "a.b.c" literals. */
type NestedKey<T> = T extends object
  ? IsPlural<T> extends true
    ? never
    : { [K in keyof T & (string | number)]: K extends string ? T[K] extends object ? `${K}` | `${K}.${NestedKey<T[K]>}` : `${K}` : never }[keyof T & (string | number)]
  : never;

function isValidCode(c: unknown): c is LangCode {
  return typeof c === 'string' && c in BY_CODE;
}

function resolve(path: string, dict: unknown): unknown {
  let cur: any = dict;
  for (const seg of path.split('.')) {
    if (cur == null || typeof cur !== 'object') return undefined;
    cur = cur[seg];
  }
  return cur;
}

/**
 * Pick the plural sub-key of a node.
 *
 *  • English / German / Spanish / French / Portuguese: one (n===1) / other
 *  • Turkish: one (n===1) / other  (Turkish has no "zero" — "0 ürün" uses the
 *    plural form)
 *  • We deliberately do NOT implement the full CLDR 18-form table: the shop
 *    only uses "one/other" and that keeps the locale files compact.
 */
function pickPlural(value: unknown, count: number, _lang: LangCode): string {
  if (typeof value === 'string') return value;
  if (value && typeof value === 'object') {
    const v = value as Record<string, unknown>;
    if (count === 1 && typeof v.one === 'string') return v.one;
    if (typeof v.other === 'string') return v.other;
    // fall back to the first string value
    for (const k of Object.keys(v)) if (typeof v[k] === 'string') return v[k] as string;
  }
  return '';
}

const INTERPOLATE_RE = /\{\{\s*(\w+)\s*\}\}/g;

function interpolate(tpl: string, vars: Record<string, string | number> | undefined, lang: LangCode): string {
  if (!vars) return tpl;
  return tpl.replace(INTERPOLATE_RE, (_, name: string) => {
    const v = vars[name];
    if (v === undefined || v === null) return '';
    if (typeof v === 'number') {
      // basic locale-aware number formatting; the caller can pre-format if they want
      try { return new Intl.NumberFormat(lang).format(v); } catch { return String(v); }
    }
    return String(v);
  });
}

/* ---------------- Translator ---------------- */

export interface Translator {
  /** Translate a key. Returns the key itself when missing, so a missing translation is NEVER an empty screen. */
  (key: DictKey, vars?: Record<string, string | number>): string;
  /** Explicit-plural version — rarely needed; passing `count` in vars is enough. */
  plural(key: DictKey, count: number, vars?: Record<string, string | number>): string;
  /** Current language. */
  lang: LangCode;
}

function buildT(lang: LangCode): Translator {
  const dict = DICTS[lang] || DICTS[DEFAULT_LANG];
  const fallback = DICTS[DEFAULT_LANG];
  const fn: Translator = ((key: DictKey, vars?: Record<string, string | number>) => {
    let node = resolve(key, dict);
    if (node === undefined) node = resolve(key, fallback); // graceful EN fallback
    if (node === undefined) return String(key); // last resort: print the key so devs SEE the gap
    let str: string;
    if (typeof node === 'string') {
      str = node;
    } else if (node && typeof node === 'object') {
      // plural-dict — pick by count
      const count = Number(vars?.count);
      str = pickPlural(node, Number.isFinite(count) ? count : 1, lang);
    } else {
      str = String(node);
    }
    return interpolate(str, vars, lang);
  }) as Translator;
  fn.plural = (key: DictKey, count: number, vars?: Record<string, string | number>) =>
    fn(key, { ...vars, count });
  fn.lang = lang;
  return fn;
}

/* ---------------- State ---------------- */

function readCookie(name: string): string | null {
  if (typeof document === 'undefined') return null;
  const m = document.cookie.match(new RegExp('(?:^|;\\s*)' + name + '=([^;]*)'));
  return m ? decodeURIComponent(m[1]) : null;
}

function writeCookie(name: string, value: string, days = 365) {
  if (typeof document === 'undefined') return;
  const expires = new Date(Date.now() + days * 86400_000).toUTCString();
  // Path=/; SameSite=Lax so the cookie rides every GET/POST the frontend makes.
  document.cookie = `${name}=${encodeURIComponent(value)}; expires=${expires}; path=/; samesite=lax`;
}

function readURL(): string | null {
  if (typeof location === 'undefined') return null;
  const u = new URLSearchParams(location.search).get('lang');
  return u;
}

function readStorage(): string | null {
  if (typeof localStorage === 'undefined') return null;
  try { return localStorage.getItem(STORAGE_KEY); } catch { return null; }
}

/** The visitor's explicit switcher pick, when there is one. */
function readUserPick(): string | null {
  if (typeof localStorage === 'undefined') return null;
  try { return localStorage.getItem(USER_KEY); } catch { return null; }
}

/** The device's system language (navigator.languages order), 2-letter. */
function readNavigator(): string | null {
  for (const base of deviceLanguages()) {
    if (isValidCode(base)) return base;
  }
  return null;
}

/**
 * Detect the best language without mutating any state — used on boot.
 *
 * Order (2026-10-03, per the owner's call: follow the SYSTEM language, never a
 * guess from the visitor's IP — nothing here reads geo):
 *   1. ?lang=            a deliberate request (shared link, e2e run)
 *   2. the visitor's own switcher pick: an explicit choice always stands
 *   3. window.nimiqPay.language — inside Nimiq Pay the HOST's language is the
 *      user's choice, so it outranks the device locale (docs:
 *      nimiq.dev/mini-apps/features/localization). One deliberate exception:
 *      the host falls back to its own default "en" for languages Nimiq Pay
 *      does not ship (Turkish is one), so when the device asks for a language
 *      we DO ship, that is the better answer than English.
 *   4. a previously auto-detected value the shop saved (cookie first — it is
 *      also what the backend reads for the email language)
 *   5. the device's system language
 *   6. what the server wrote on <html lang>, else English.
 */
export function detectLang(): LangCode {
  const fromUrl = readURL();
  if (isValidCode(fromUrl)) return fromUrl;

  const picked = readUserPick();
  if (isValidCode(picked)) return picked;

  const host = hostLanguage();
  const device = readNavigator();
  if (isValidCode(host) && !(host === 'en' && device && device !== 'en')) return host;

  for (const source of [readCookie(COOKIE_NAME), readStorage()]) {
    if (isValidCode(source)) return source;
  }
  if (isValidCode(device)) return device;

  // SSR rendered <html lang="…"> reflects the server's Accept-Language/cookie
  // decision. It is already on the DOM; no inline global needed.
  if (typeof document !== 'undefined') {
    const attr = document.documentElement.getAttribute('lang')?.slice(0, 2);
    if (isValidCode(attr)) return attr;
  }
  return DEFAULT_LANG;
}

/* ---------------- React integration ---------------- */

interface I18nCtx {
  lang: LangCode;
  setLang: (code: LangCode) => void;
  t: Translator;
  langs: Readonly<LangMeta[]>;
}

const I18nContext = createContext<I18nCtx>({
  lang: DEFAULT_LANG,
  setLang: () => {},
  t: buildT(DEFAULT_LANG),
  langs: LANGS,
});

let currentLang: LangCode = DEFAULT_LANG;
let currentT: Translator = buildT(DEFAULT_LANG);
const listeners = new Set<(lang: LangCode) => void>();

/** Non-React API — usable from any plain TS module (toasts, api.ts, etc). */
export function getLang(): LangCode { return currentLang; }
export function t(key: DictKey, vars?: Record<string, string | number>): string { return currentT(key, vars); }

function applyLang(code: LangCode, persist: boolean) {
  currentLang = code;
  currentT = buildT(code);
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('lang', code);
    document.documentElement.setAttribute('data-lang', code);
    // The dictionary is fetched as its own chunk for the five non-English
    // languages, so `lang`/`data-lang` (written before paint) no longer prove
    // the strings are in place. This attribute is only ever set here, after
    // the translation memory is applied — test suites and debugging read it.
    // NOT `data-i18n*`: those prefixes belong to the static-shell translator
    // (lib/staticI18n.ts), which would try to translate <html> itself.
    document.documentElement.setAttribute('data-i18n-ready', code);
  }
  if (persist) {
    try { localStorage.setItem(STORAGE_KEY, code); } catch {}
    writeCookie(COOKIE_NAME, code);
  }
  listeners.forEach((l) => l(code));
}

/**
 * Set language programmatically (e.g. from the switcher, or from ?lang=).
 * The dictionary chunk is fetched first: applying a language whose strings are
 * not loaded yet would flash English for one frame.
 */
export function setLang(code: LangCode): Promise<void> {
  if (!isValidCode(code)) code = DEFAULT_LANG;
  setUserLang(code);
  return loadDict(code).then(() => { applyLang(code, true); });
}

/**
 * Remember the visitor's explicit pick. Called by the switcher (setLang below)
 * and by nothing else: the auto-detected value keeps living in STORAGE_KEY so
 * a later device-locale or host-language change can still take over.
 */
export function setUserLang(code: LangCode) {
  try { localStorage.setItem(USER_KEY, code); } catch {}
}

/** Subscribe to language changes outside React. Returns an unsubscribe fn. */
export function onLangChange(fn: (lang: LangCode) => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/* ---------------- Provider ---------------- */

export function I18nProvider({ children, initial }: { children: ReactNode; initial?: LangCode }) {
  // The dictionary for a non-English language travels as its own chunk. Until
  // it lands the strings resolve through the English fallback (buildT), and
  // `dictTick` re-renders every consumer the moment the real strings exist.
  //
  // ORDER MATTERS — FIRST RENDER, THEN DETECT. Every page is server-rendered
  // (the islands are `client:load`, not `client:only`), and the server does not
  // know the visitor: it always emits English, because on the server there is no
  // URL, cookie, localStorage or navigator to read. If this component read
  // `detectLang()` during its FIRST render, a Turkish visitor would render
  // Turkish text against English server HTML, React would fail hydration
  // (minified error #418: "text content does not match"), throw the server
  // markup away and rebuild the whole island on the client — discarding exactly
  // the paint we server-rendered for. So the first render is pinned to
  // DEFAULT_LANG (what the HTML contains) and the visitor's real choice is
  // adopted in the effect below, immediately after hydration. Switching later
  // than that costs nothing: a language swap already re-renders through
  // `buildT` while the dictionary chunk is in flight.
  //
  // `initial` stays an explicit override for callers that know the language at
  // build time (e.g. a future per-locale route); it pins both renders.
  const pinned = isValidCode(initial) ? initial : null;
  const [lang, setLangState] = useState<LangCode>(pinned ?? DEFAULT_LANG);
  const [dictTick, setDictTick] = useState(0);
  // The language this tree is ALLOWED to announce and persist. Until detection
  // has run, the pinned English render is a hydration placeholder, not a
  // choice: announcing it wrote data-lang="en" (and the stored cookie/locale —
  // the backend reads that cookie for email language!) on every load, which is
  // what the visitor saw as the page "changing language" a moment after it
  // appeared. `null` = nothing adopted yet, so nothing to announce.
  //
  // STATE, not a ref (2026-10-03): with a ref, the announce effect below ran in
  // the SAME commit as the adoption effect and compared the render's stale
  // `lang` ("en") against a ref the sibling effect had just set to "tr" — so
  // the guard passed and the pinned English render was announced, persisted AND
  // released the pre-paint hold for one frame (measured: lang="en" from t≈269
  // to t≈333 ms, hold up at 271 — an English flash for every first-time
  // Turkish visitor, the exact thing this whole mechanism exists to prevent).
  // As state, the first commit has lang="en" + adoptedLang=null → both effects
  // return early, and every later value travels WITH its render.
  const [adoptedLang, setAdoptedLang] = useState<LangCode | null>(pinned);

  // Adopt the visitor's requested language once the hydrated tree is committed.
  // Mount-only by design: every later change goes through setLang().
  useEffect(() => {
    if (pinned) return;
    const detected = detectLang();
    setAdoptedLang(detected);
    if (detected !== lang) setLangState(detected);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    let alive = true;
    void loadDict(lang).then(() => { if (alive) setDictTick((n) => n + 1); });
    return () => { alive = false; };
  }, [lang]);

  // Keep the global singleton (and the static Astro shell) in sync. The first
  // run is skipped for a non-pinned provider (`adopted` is still null: nothing
  // has been detected, so the pinned English render must not be announced or
  // persisted — that is what used to flip data-lang, the cookie and
  // localStorage to "en" for a moment on every load). From the adoption
  // onwards every render IS the choice (switcher, other tab, `?lang=`), so the
  // effect follows it — that also keeps the two-tab storage sync working.
  useEffect(() => {
    if (adoptedLang === null || adoptedLang !== lang) return;
    if (dictLoaded(lang)) applyLang(lang, true);
    else void loadDict(lang).then(() => applyLang(lang, true));
  }, [lang, adoptedLang]);

  // Release Base.astro's pre-paint hold for non-English visitors. Timing is the
  // whole point: this effect runs AFTER the commit that carries the translated
  // strings (the dictionary is in memory and `dictTick` re-rendered the tree),
  // so the first frame the visitor can see is already in their language — no
  // English flash, no half-translated hybrid. If hydration never gets this far
  // the boot script's own timeout releases the page.
  useEffect(() => {
    if (typeof document === 'undefined') return;
    if (lang === adoptedLang && dictLoaded(lang)) {
      document.documentElement.removeAttribute('data-i18n-hold');
    }
  }, [lang, adoptedLang, dictTick]);

  // Keep two tabs of the same shop in sync.
  useEffect(() => {
    // A language arriving from another tab (storage event) or from non-React
    // code in this one (setLang, ?lang=) is a real choice: adopt it, or the
    // announce effect above would keep comparing against a stale adoptedLang.
    const adopt = (next: string | null) => {
      if (!isValidCode(next)) return;
      setAdoptedLang((cur) => (cur === next ? cur : next));
      setLangState((cur) => (cur === next ? cur : next));
    };
    const onStorage = (e: StorageEvent) => {
      if (e.key === STORAGE_KEY) adopt(e.newValue);
    };
    window.addEventListener('storage', onStorage);
    const off = onLangChange((l) => adopt(l));
    return () => { window.removeEventListener('storage', onStorage); off(); };
  }, []);

  const ctx = useMemo<I18nCtx>(() => ({
    lang,
    // Switching is asynchronous on purpose: a language never renders with the
    // wrong strings, it renders with English fallback and then swaps.
    setLang: (code: LangCode) => {
      const next = isValidCode(code) ? code : DEFAULT_LANG;
      setUserLang(next);
      setLangState(next);
      void loadDict(next).then(() => applyLang(next, true));
    },
    t: buildT(lang),
    langs: LANGS,
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }), [lang, dictTick]);

  return <I18nContext.Provider value={ctx}>{children}</I18nContext.Provider>;
}

/** React hook — what 99 % of components should use. */
/**
 * Renders a translated string that carries **bold** markers as React nodes, so
 * a sentence can keep its emphasis while staying one translatable unit.
 */
export function rich(s: string): ReactNode[] {
  return s.split('**').map((part, i) => (i % 2 ? <strong key={i}>{part}</strong> : part));
}

export function useT() {
  return useContext(I18nContext);
}

/**
 * Imperative API for non-React code that needs to re-read the translator
 * whenever the language changes (e.g. re-creating static copies on locale
 * switch). Most callers can just import `t` directly — this is for modules
 * that CACHE translations and need to invalidate that cache.
 */
export function useLangEffect(fn: (lang: LangCode, t: Translator) => void) {
  const ctx = useContext(I18nContext);
  useEffect(() => { fn(ctx.lang, ctx.t); }, [ctx.lang, ctx.t, fn]);
}

/* ---------------- Initialise on import (for module-level consumers) ---------------- */
// Do a best-effort initial detect so module-level `t` calls before React
// mounts still return a non-default language. (On the server this is a no-op.)
if (typeof window !== 'undefined') {
  const code = detectLang();
  // Apply immediately so <html lang> and every synchronous `t()` consumer agree
  // with the URL/cookie from the first frame…
  applyLang(code, false);
  // …then re-apply once the dictionary has actually arrived. Non-English
  // dictionaries are separate chunks now, so the first call translates with the
  // English fallback; this second one fires the language listeners again and
  // turns the static shell (skip link, footer, 404 page) into the right
  // language. It is a no-op for English and for an already-loaded dictionary.
  void loadDict(code).then(() => applyLang(code, false));
}

export default { t, setLang, getLang, detectLang, LANGS, I18nProvider, useT };
