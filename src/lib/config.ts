/**
 * config.ts — deployment configuration for the static frontend.
 *
 * This mirrors the original `config.js` (loaded via <script src="/config.js">
 * which assigns `window.APP_CONFIG`). It is the ONLY place where frontend /
 * backend domains are configured. We read the browser-injected value first
 * (so /public/config.js still wins) and fall back to safe defaults.
 *
 * NO SSR: every page island is client:only, so components only ever render
 * in the browser — after /config.js has run. There is no server HTML to keep
 * in sync and hydration mismatches are structurally impossible.
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
  API_BASE: 'https://shopapi.nimiqbase.com/api',
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

export const CFG: AppConfig = readAppConfig();

/**
 * isTestMode resolves whether the CURRENT ORDER is a simulated one.
 *
 * The backend stamps an explicit boolean `test_mode` (and `simulated_payment`)
 * on every quote and checkout response, derived from its own TEST_MODE env
 * flag. That is the authority: it is the process that will actually refuse to
 * broadcast a payment or call the supplier.
 *
 * `public/config.js` ALSO carries a `TEST_MODE` flag, and it ships `true` in
 * this repository. The two used to be OR-ed together, which meant the static
 * file could not be overridden by the server: a production deployment whose
 * operator flipped the backend to real payments but forgot the static file
 * would render the "Pay now — simulated" screen and the gold test banner on
 * top of REAL invoices, and expose a simulated-pay button that the backend
 * would then 404. That is a confusing, trust-damaging failure mode for a
 * paying customer, caused purely by two sources of truth being merged with
 * `||`.
 *
 * Resolution order below: an explicit boolean from the server wins; the static
 * flag is only a fallback for responses that predate the field or for the
 * brief window before the first API reply arrives.
 *
 * @param sources response objects to inspect, in priority order.
 */
export function isTestMode(...sources: Array<Record<string, unknown> | null | undefined>): boolean {
  for (const src of sources) {
    if (!src || typeof src !== 'object') continue;
    for (const key of ['test_mode', 'simulated_payment']) {
      const v = src[key];
      // Only a real boolean is treated as an answer. `undefined` (field not
      // present) falls through to the next source; `true`/`false` settles it.
      if (typeof v === 'boolean') {
        if (v) return true;
        // The server explicitly said this order is NOT simulated. That is a
        // definitive answer and must not be re-overridden by the static
        // config flag further down the list.
        return false;
      }
    }
  }
  return CFG.TEST_MODE === true;
}

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
