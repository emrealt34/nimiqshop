/** Shared test matrix. Keep ROUTES in sync with src/lib/router.tsx. */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const BASE = '/nimiqshop/';

export const ROUTES = [
  '/', '/cart', '/product', '/orders', '/order', '/profile', '/activity',
  '/support', '/track', '/admin', '/cashback', '/plant-trees',
] as const;

/** Every screen the responsive deep scan visits (route + query variants). */
export const SCREENS: { name: string; url: string }[] = [
  ...ROUTES.map((r) => ({ name: r === '/' ? 'home' : r.slice(1), url: r })),
  { name: 'product-detail', url: '/product?id=p1&country=TR' },
  { name: 'order-detail', url: '/order?id=o1' },
  { name: 'quote-detail', url: '/order?type=quote&id=q1' },
  { name: 'track-order', url: '/track?order=o1' },
  { name: 'not-found', url: '/this-page-does-not-exist' },
];

export const NAV_ROUTES = ['/activity', '/orders', '/cashback', '/plant-trees', '/support'] as const;

export const LANGS = ['en', 'tr', 'de', 'fr', 'pt', 'es'] as const;
export type Lang = (typeof LANGS)[number];

/** The three screenshot folders. Each device class is swept at every size. */
export const DEVICES = {
  desktop: [
    { width: 1280, height: 800 },
    { width: 1440, height: 900 },
    { width: 1920, height: 1080 },
  ],
  tablet: [
    { width: 768, height: 1024 },
    { width: 820, height: 1180 },
    { width: 1024, height: 768 },
  ],
  phone: [
    { width: 320, height: 640 },
    { width: 360, height: 780 },
    { width: 390, height: 844 },
    { width: 430, height: 932 },
  ],
} as const;
export type DeviceClass = keyof typeof DEVICES;

export const TREE_COUNTS = [0, 7, 1234.5, 98765.4, 1234567] as const;

export const NOT_FOUND_MARK = /^\s*404\b/;

/** '/orders' → '/nimiqshop/orders/', '/product?id=1' → '/nimiqshop/product/?id=1' */
export function path(route: string) {
  const [p, q] = route.split('?');
  const clean = p.replace(/^\/|\/$/g, '');
  return BASE + (clean ? clean + '/' : '') + (q ? '?' + q : '');
}

/** Top-level i18n namespaces (nav, plantTrees, …) from the English source
 *  locale — used to spot raw keys like "plantTrees.title" on screen. */
export const I18N_NAMESPACES: string[] = (() => {
  const src = readFileSync(fileURLToPath(new URL('../../../src/i18n/locales/en.ts', import.meta.url)), 'utf8');
  return [...src.matchAll(/^ {2}([a-zA-Z]+): \{/gm)].map((m) => m[1]);
})();
/** Every real `namespace.key` in en.ts. A raw-key hit must be an actual key —
 *  brand names/domains that merely look dotted ("nim.shop") never match. */
export const I18N_KEYS: Record<string, string[]> = (() => {
  const src = readFileSync(fileURLToPath(new URL('../../../src/i18n/locales/en.ts', import.meta.url)), 'utf8');
  const out: Record<string, string[]> = {};
  let ns = '';
  for (const line of src.split('\n')) {
    const n = /^ {2}([a-zA-Z]+): \{/.exec(line);
    if (n) { ns = n[1]; out[ns] = []; continue; }
    const k = /^ {4}['"]?([a-zA-Z][a-zA-Z0-9_]*)['"]?\s*:/.exec(line);
    if (k && ns) out[ns].push(k[1]);
  }
  return out;
})();
const esc = (x: string) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
export const RAW_KEY_RE = new RegExp(
  `\\b(?:${Object.entries(I18N_KEYS).filter(([, ks]) => ks.length).map(([ns, ks]) => `${esc(ns)}\\.(?:${ks.map(esc).join('|')})`).join('|')})(?:\\.[a-zA-Z0-9_]+)*\\b`,
  'g',
);
