/**
 * config.ts — deployment configuration for the static frontend.
 *
 * This mirrors the original `config.js` (loaded via <script src="/config.js">
 * which assigns `window.APP_CONFIG`). It is the ONLY place where frontend /
 * backend domains are configured. We read the browser-injected value first
 * (so /public/config.js still wins) and fall back to safe defaults.
 *
 * Server vs browser: every page island is `client:load`, so the first render
 * happens TWICE — once here at build time (static HTML) and once in the
 * browser on hydration. This module's job is to keep those two renders
 * identical in everything that ends up on screen:
 *
 *   • The build has no `window`, so `readAppConfig()` returns DEFAULTS;
 *     in the browser /config.js has already assigned `window.APP_CONFIG`.
 *     The two agree for every value the UI renders (hostname, Hub URL, repo),
 *     so a config value may NOT be read while rendering something whose
 *     output depends on it — read it in an effect or an event handler, which
 *     is what consumers of CFG do. (`API_BASE` and `TEST_MODE` do differ
 *     between the two sources; both are only consumed after user interaction,
 *     where there is no server markup left to mismatch.)
 */

export type AppConfig = {
  /** '/api' for same-origin nginx proxy, or an absolute backend URL. */
  API_BASE: string;
  SITE_HOST: string;
  FRONTEND_URL: string;
  HUB_URL: string;
  APP_NAME: string;
  NETWORK: 'mainnet' | 'testnet';
  TEST_MODE: boolean;
  GITHUB_URL: string;
  RELEASE_REPO: string;
  [key: string]: unknown;
};

const DEFAULTS: AppConfig = {
  API_BASE: '/api',
  SITE_HOST: 'shop.nimiqbase.com',
  FRONTEND_URL: 'https://shop.nimiqbase.com',
  HUB_URL: 'https://hub.nimiq.com',
  APP_NAME: 'shop.nimiqbase.com',
  NETWORK: 'mainnet',
  TEST_MODE: false,
  GITHUB_URL: 'https://github.com/emrealt34/nimiqshop',
  RELEASE_REPO: 'emrealt34/nimiqshop',
};

/** Read the runtime config. Safe on a server too (defaults only, never used for rendering). */
export function readAppConfig(): AppConfig {
  if (typeof window === 'undefined') return { ...DEFAULTS };
  const injected =
    typeof window !== 'undefined' && (window as unknown as { APP_CONFIG?: Partial<AppConfig> }).APP_CONFIG;
  return Object.assign({ ...DEFAULTS }, injected || {});
}

export const CFG: AppConfig = new Proxy({ ...DEFAULTS }, {
  get(_target, prop: string) {
    return readAppConfig()[prop];
  },
});


/**
 * The product wordmark shown in the navbar (owner, 2026-10-05: "navbar'da site
 * adı yerine nimiqshop.io yaz"). Deliberately NOT siteName(): that is the live
 * hostname and it stays in titles, share links, delivery notes and the API
 * copy. The 404 page already printed this same wordmark by hand; this is the
 * one place it is defined for the app chrome.
 */
export const BRAND_NAME = 'nimiqshop.io';

/** Live shop hostname. One value for titles, footer, Hub, copy. */
export function siteName(): string {
  const n = String(CFG.SITE_HOST || '').trim();
  return n || 'shop.nimiqbase.com';
}

export function siteURL(): string {
  const u = String(CFG.FRONTEND_URL || '').trim();
  if (/^https?:\/\//i.test(u)) return u.replace(/\/$/, '');
  return 'https://' + siteName();
}
