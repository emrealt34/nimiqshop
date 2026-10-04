/**
 * localeRoutes.ts — the static per-locale route prefixes.
 *
 * Every content page is generated once per language under `/de/… /fr/… /es/…
 * /pt/… /tr/…` (English stays at the unprefixed root, so every existing link,
 * email and QR keeps working). The build renders each variant in its own
 * language — shell AND islands — so the first paint a visitor sees is already
 * their language: there is no visible language flip on load any more.
 *
 * The unprefixed root pages keep a pre-paint redirect: the boot script in
 * Base.astro resolves the visitor's language and, when it is not English,
 * location.replace()s to the prefixed twin BEFORE any paint happens.
 */

/** Non-English languages that get a prefixed static twin of every page. */
export const LOCALE_PREFIXES = ['de', 'fr', 'es', 'pt', 'tr'] as const;
export type LocalePrefix = (typeof LOCALE_PREFIXES)[number];

export function isLocaleSeg(seg: string | undefined | null): boolean {
  return !!seg && (LOCALE_PREFIXES as readonly string[]).includes(seg);
}

/** getStaticPaths() for every `[...lang]` page: root (English) + 5 prefixes. */
export function localeStaticPaths() {
  return [
    { params: { lang: undefined as unknown as string } },
    ...LOCALE_PREFIXES.map((lang) => ({ params: { lang } })),
  ];
}

/** Same-origin URL for `code` at the current path (switcher URL sync). */
export function localeUrlFor(code: string): string | null {
  if (typeof window === 'undefined') return null;
  const base = String(import.meta.env.BASE_URL || '/').replace(/\/$/, '');
  let p = window.location.pathname;
  if (base && p.startsWith(base)) p = p.slice(base.length) || '/';
  const seg = p.split('/')[1];
  const rest = isLocaleSeg(seg) ? p.slice(('/' + seg).length) || '/' : p;
  return base + (code === 'en' ? '' : '/' + code) + rest + window.location.search + window.location.hash;
}
