/**
 * Shared fixtures for the e2e suite.
 *
 *  • Fully mocked backend: every call to API_BASE (and any stray same-origin
 *    /api/ call) is answered here — tests never touch the real server.
 *    `api` picks the mode (ok / down / error500 / garbage / empty / slow)
 *    and the data (signed in? how many trees? …).
 *  • Route contract: every API path the page requests must exist in the Go
 *    backend router (read from backend/ at start-up). A typo'd or removed
 *    endpoint fails the test that triggered it.
 *  • Signed-in wallet session + language pinned in localStorage before any
 *    page script runs.
 *  • Crash guard (auto): uncaught exceptions, unhandled rejections, React
 *    error-boundary logs and console.error FAIL the test.
 */
import { test as base, expect, type Page, type Route, type BrowserContext } from '@playwright/test';
import type { Lang } from './data';
// @ts-ignore — plain ESM helper shared with scripts/check-api-routes.mjs
import { backendRoutes, matchRoute } from '../../../scripts/api-routes.mjs';

export const ADDRESS = 'NQ07 0000 0000 0000 0000 0000 0000 0000 0000';
export const OTHER_ADDRESS = 'NQ12 3456 7890 ABCD EFGH JKLM NPQR STUV';
export const API_ORIGIN = 'https://shopapi.nimiqbase.com';

export type ApiMode = 'ok' | 'down' | 'error500' | 'garbage' | 'empty' | 'slow';
export type ApiOptions = {
  mode: ApiMode;
  authed: boolean;
  orders: number;
  preference: 'burn' | 'cashback';
};
export const DEFAULT_API: ApiOptions = { mode: 'ok', authed: true, orders: 5, preference: 'cashback' };

const ROUTES: { method: string; path: string }[] = backendRoutes();

type Fixtures = {
  lang: Lang;
  api: Partial<ApiOptions>;
  allowedErrors: RegExp[];
  requests: string[];
  unknownApi: string[];
  pageErrors: string[];
};

const now = () => Math.floor(Date.now() / 1000);
const iso = (d = 0) => new Date(Date.now() - d * 86400_000).toISOString();

function cors(route: Route) {
  return {
    'Access-Control-Allow-Origin': route.request().headers()['origin'] || '*',
    'Access-Control-Allow-Credentials': 'true',
    'Access-Control-Allow-Headers': 'content-type, x-csrf-token, accept-language, authorization',
    'Access-Control-Allow-Methods': 'GET, POST, PUT, PATCH, DELETE, OPTIONS',
  };
}

/** Order row as served by GET /api/orders (see OrdersPage normalizeOrders). */
const order = (id: string, status: string, i: number) => {
  const brand = ['Amazon', 'Steam', 'Netflix', 'Spotify'][i % 4];
  const usd = 25 + i * 25;
  return {
    id, status, kind: 'giftcard', product_id: brand, quantity: 1, price_usd: usd, nim_usd_rate: 0.0012,
    estimated_nim: usd / 0.0012, created_at: iso(i), updated_at: iso(i), rating: 0, has_ticket: i === 2, ticket_status: i === 2 ? 'open' : undefined,
    payload: { product_name: `${brand} Gift Card`, country: 'TR', value: usd, currency: 'USD', logo_url: `https://logos.example.test/${brand}.svg`, bg_color: '#1f2348' },
    nimiq_address: ADDRESS, tx_hash: 'a'.repeat(64),
  };
};

/** Realistic payloads (shapes follow backend/internal/handlers). */
function payload(p: string, method: string, o: ApiOptions): unknown {
  if (p === '/auth/session') return o.authed
    ? { authed: true, user: { id: 'u1', nimiq_address: ADDRESS }, expires_at: now() + 86400 }
    : { authed: false };
  if (p === '/auth/challenge') return { challenge: 'test-challenge', expires_at: now() + 300 };
  if (p === '/auth/logout' || p === '/presence') return { ok: true };
  if (p === '/site-config') return { burn_nim_address: ADDRESS, cashback_pct: 2 };
  if (p === '/market/nim-rate') return { usd: 0.0012, nim_usd: 0.0012, updated_at: iso() };
  if (p === '/market/fx') return { base: 'USD', rates: { EUR: 0.92, TRY: 41.2, GBP: 0.78 } };
  if (p === '/geo') return { country: 'TR' };
  if (p === '/cashback/rate') return {
    cashback_bps: 0, cashback_percent: 0, staker_base_bps: 100, staker_base_percent: 1, staker_base_rule: 'any_positive_stake',
    cashback_code_enabled: false, staker_program_enabled: true, pool_validator_address: OTHER_ADDRESS, staker_tiers: [], staker_loyalty: [],
    stake_cashback: { max_boost_bps: 1000, max_boost_percent: 10, ledger_max_usd: 50, min_stake_nim: 1, daily_cap_usd: 5, monthly_cap_usd: 50, loyalty_start: 0.5, loyalty_ramp_days: 365, loyalty_ramp_years: 1, profit_credit_share: 0.5 },
  };
  if (p === '/cashback/burn-balance') return { available: true, balance_nim: 450000, cached_at: iso(), stale: false };
  if (p.startsWith('/cashback/leaderboard')) return {
    bucket: 'all',
    burn_address: ADDRESS,
    totals: { total_nim: 1250000, paid_nim: 1200000, pending_nim: 50000, burned_nim: 450000, wallet_nim: 800000, orders: 9120 },
    leaderboard: [
      { rank: 1, user: OTHER_ADDRESS, total_nim: 51200, burned_nim: 15000, wallet_nim: 36200, orders: 81 },
      { rank: 2, user: ADDRESS, total_nim: 1640, burned_nim: 500, wallet_nim: 1140, orders: o.orders },
    ],
  };
  if (p === '/cashback/me') return {
    totals: {
      paid_nim: 1520,
      paid_count: 4,
      pending_nim: 120,
      pending_count: 1,
      earned_nim: 1640,
      burned_nim: 500,
      burned_count: 2,
      wallet_nim: 1140,
      wallet_count: 3,
      orders: o.orders,
      preference: o.preference,
    },
    cashbacks: [
      { order_id: 'o1', status: 'paid', amount_nim: 380, paid_at: iso(2), created_at: iso(3) },
      { order_id: 'o2', status: 'pending', amount_nim: 120, paid_at: null, created_at: iso(1) },
    ],
    staker_program_enabled: false,
  };
  if (p === '/account/limits') return { daily_usd: 500, used_usd: 20 };
  if (p === '/orders') return o.authed ? [order('o1', 'delivered', 0), order('o2', 'supplier_processing', 1), order('o3', 'failed', 2)] : [];
  if (p.startsWith('/orders/')) return order(p.split('/')[2], 'delivered', 0);
  if (p === '/quotes') return [];
  if (p === '/support/tickets') return o.authed ? [{ id: 't1', subject: 'Code not received', status: 'open', created_at: iso(1), updated_at: iso(0), messages: [] }] : [];
  if (p.startsWith('/support/tickets/')) return { id: 't1', subject: 'Code not received', status: 'open', created_at: iso(1), messages: [{ id: 'm1', from: 'user', body: 'Hello', created_at: iso(1) }] };
  if (p === '/wallet/balance') {
    // The buyer's own NIM balance. Signed-out visitors never ask (the client
    // gates on session state), but answer 401 faithfully if they do.
    return o.authed
      ? { address: 'NQ07 0000 0000 0000 0000 0000 0000 0000 0000', available: true, balance_luna: 12400000, balance_nim: 124, network: 'mainnet', observed_at: iso(0) }
      : { __status: 401, error: 'unauthorized' };
  }
  if (p === '/activity') return [];
  if (p.startsWith('/track/')) return { ...order(p.split('/')[2], 'delivered', 0), stage: 'delivered' };
  if (p.startsWith('/catalog/brands')) {
    const b = (family: string, kind: string, bg: string, extra: Record<string, unknown> = {}) => ({
      family, brand_id: family.toLowerCase().replace(/\W+/g, '-'), logo_url: `https://logos.example.test/${encodeURIComponent(family)}.svg`,
      bg_color: bg, min: '5', max: '500', category: kind === 'giftcard' ? 'Shopping' : kind === 'esim' ? 'Travel' : 'Mobile',
      kind, is_out_of_stock: false, country_code: 'TR', product_type: kind, ...extra,
    });
    return { country_code: 'TR', categories: [
      { kind: 'giftcard', category: 'Shopping', brands: [b('Amazon', 'giftcard', '#ff9900'), b('Steam', 'giftcard', '#1b2838'), b('Netflix', 'giftcard', '#e50914'), b('Spotify', 'giftcard', '#1db954'), b('Google Play', 'giftcard', '#34a853'), b('PlayStation Store Türkiye Extra Long Brand Name', 'giftcard', '#003791'), b('Apple', 'giftcard', '#111111', { is_out_of_stock: true })] },
      { kind: 'mobile_recharge', category: 'Mobile', brands: [b('Turkcell', 'mobile_recharge', '#ffc900'), b('Vodafone', 'mobile_recharge', '#e60000')] },
      { kind: 'esim', category: 'Travel', brands: [b('Airalo Europe', 'esim', '#0a84ff')] },
    ] };
  }
  if (p.startsWith('/catalog/products/')) return { id: 'p1', name: 'Amazon Gift Card', brand: 'Amazon', kind: 'giftcard', country: 'TR', denominations: [10, 25, 50, 100], currency: 'USD' };
  if (p.startsWith('/admin/')) return { __status: 401, error: 'unauthorized' };
  return {};
}

export async function mockApi(ctx: BrowserContext, opts: Partial<ApiOptions>, requests: string[] = [], unknownApi: string[] = []) {
  const o: ApiOptions = { ...DEFAULT_API, ...opts };
  const handler = async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const method = req.method();
    requests.push(`${method} ${url.origin}${url.pathname}`);
    if (method === 'OPTIONS') return route.fulfill({ status: 204, headers: cors(route) });
    const apiPath = url.pathname.replace(/^\/api/, '');
    if (ROUTES.length && !matchRoute(ROUTES, method, '/api' + apiPath)) unknownApi.push(`${method} /api${apiPath}`);
    if (o.mode === 'down') return route.abort('connectionrefused');
    if (o.mode === 'error500') return route.fulfill({ status: 500, contentType: 'application/json', headers: cors(route), body: '{"error":"internal"}' });
    if (o.mode === 'garbage') return route.fulfill({ status: 200, contentType: 'application/json', headers: cors(route), body: '<html>502 Bad Gateway</html>' });
    if (o.mode === 'empty') return route.fulfill({ status: 200, contentType: 'application/json', headers: cors(route), body: 'null' });
    if (o.mode === 'slow') await new Promise((r) => setTimeout(r, 2500));
    const body = payload(apiPath, method, o) as any;
    const status = body && typeof body === 'object' && '__status' in body ? body.__status : 200;
    return route.fulfill({ status, contentType: 'application/json', headers: cors(route), body: JSON.stringify(body) });
  };
  await ctx.route(`${API_ORIGIN}/**`, handler);
  await ctx.route(/^http:\/\/127\.0\.0\.1:\d+\/api\//, handler);
  // Other third parties (social share targets, analytics…) stay offline.
  await ctx.route(/^https?:\/\/(?!127\.0\.0\.1|shopapi\.nimiqbase\.com)/, (route) => {
    requests.push('EXTERNAL ' + route.request().url());
    if (route.request().resourceType() === 'image') {
      // brand logos etc.: a real, decodable image so layout is realistic
      const name = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop() || 'Logo').replace(/\.\w+$/, '').slice(0, 12);
      return route.fulfill({ status: 200, contentType: 'image/svg+xml', body: `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="160" viewBox="0 0 240 160"><rect width="240" height="160" rx="16" fill="#1f2348"/><text x="120" y="92" font-family="sans-serif" font-size="26" font-weight="700" fill="#e9b213" text-anchor="middle">${name.replace(/[<&>]/g, '')}</text></svg>` });
    }
    return route.fulfill({ status: 204, body: '' });
  });
}

export const test = base.extend<Fixtures>({
  lang: ['en', { option: true }],
  api: [{}, { option: true }],
  allowedErrors: [[], { option: true }],
  requests: async ({}, use) => { await use([]); },
  unknownApi: async ({}, use) => { await use([]); },
  pageErrors: async ({}, use) => { await use([]); },

  context: async ({ context, lang, api, requests, unknownApi }, use) => {
    const o = { ...DEFAULT_API, ...api };
    await context.addInitScript(({ lang, authed, address }) => {
      try {
        // i18n-ui.spec switches language mid-test via sessionStorage.
        const l = sessionStorage.getItem('e2e.lang') || lang;
        localStorage.setItem('nimshop.lang', l);
        // The cookie outranks localStorage in Base.astro's language bootstrap
        // (?lang → cookie → storage → navigator), and the app writes it on first load.
        document.cookie = `nimshop-lang=${l}; path=/; SameSite=Lax`;
        // same theme for every screenshot (the scanner toggles it while exploring)
        localStorage.setItem('nimshop.theme', sessionStorage.getItem('e2e.theme') || 'light');
        // The home market is now the visitor's SAVED country, else their own
        // locale — the IP-based /api/geo suggestion is gone (owner's call,
        // 2026-10-03). Pin it to what the mocked catalog serves so the suite
        // does not silently run against whatever locale the runner machine has.
        localStorage.setItem('nimshop_country', 'TR');
        if (authed) {
          localStorage.setItem('nimshop.sess', JSON.stringify({ uid: 'u1', address, expiresAt: Math.floor(Date.now() / 1000) + 86400 }));
          localStorage.setItem('nimshop.addr', address);
        } else {
          localStorage.removeItem('nimshop.sess');
          localStorage.removeItem('nimshop.addr');
        }
      } catch { /* storage disabled */ }
      // Wallet popups (Nimiq Hub) are not ours to test.
      (window as any).open = () => null;
    }, { lang, authed: o.authed, address: ADDRESS });
    await mockApi(context, o, requests, unknownApi);
    await use(context);
  },

  page: async ({ page, allowedErrors, pageErrors, unknownApi }, use, testInfo) => {
    const allowed = (m: string) => allowedErrors.some((re) => re.test(m));
    page.on('pageerror', (err) => { const m = 'pageerror: ' + (err.stack || err.message); if (!allowed(m)) pageErrors.push(m); });
    page.on('console', (msg) => {
      if (msg.type() !== 'error') return;
      // Browsers log every non-2xx response; API errors we return on purpose
      // are not app errors. Broken same-origin assets: see links.spec.ts.
      // Browser-level network noise, not app errors (Chromium / Firefox wording):
      if (/Failed to load resource|status of \d{3}|net::ERR_|Cross-Origin Request Blocked|CORS request did not succeed|NS_BINDING_ABORTED|NS_ERROR_/i.test(msg.text())) return;
      const m = 'console.error: ' + msg.text();
      if (!allowed(m)) pageErrors.push(m);
    });
    page.on('crash', () => pageErrors.push('page crashed'));
    page.on('dialog', (d) => d.dismiss().catch(() => {}));
    await use(page);
    if (pageErrors.length) await testInfo.attach('page-errors.txt', { body: pageErrors.join('\n\n'), contentType: 'text/plain' });
    expect(pageErrors, 'uncaught errors / console.error in the page').toEqual([]);
    expect([...new Set(unknownApi)], 'API paths the frontend called that the Go backend does not serve').toEqual([]);
  },
});

export { expect };

/** Go to a page and wait until React rendered its content. (Not
 *  'networkidle': the app prefetches every route chunk for ~3 s.) */
export async function open(page: Page, url: string) {
  let res = await page.goto(url, { waitUntil: 'load' });
  // A starved CPU (many parallel browsers) can stall the first hydration. One
  // reload is allowed; a page that genuinely never renders still fails below.
  const attached = await page.locator('#page-content').first().waitFor({ state: 'attached', timeout: 15_000 }).then(() => true, () => false);
  if (!attached) {
    res = await page.reload({ waitUntil: 'load' });
    await page.locator('#page-content').first().waitFor({ state: 'attached', timeout: 15_000 });
  }
  await expect
    .poll(async () => (await page.locator('#page-content').first().innerText().catch(() => '')).trim().length, { timeout: 15_000 })
    .toBeGreaterThan(10);
  await page.evaluate(() => document.fonts?.ready);
  // The island hydrates eagerly (client:load), but "content is visible" still
  // does not imply "clicks do something" — the server-rendered HTML paints
  // before React attaches. Wait for AppRoot's beacon so a click can't race
  // hydration. Pages with no island at all (the static 404) never set it —
  // they are not hydrating, they are done.
  await page.waitForFunction(
    () => document.documentElement.getAttribute('data-app-ready') === '1' || !document.querySelector('astro-island'),
    null,
    { timeout: 20_000 },
  );
  // Non-English visitors are held behind the boot loader until their own
  // strings are committed (Base.astro → src/i18n). Never measure or click a
  // page that is still held — and let a stuck hold fail loudly here instead of
  // quietly emptying the text-based assertions in i18n-ui.spec.
  await page.waitForFunction(() => !document.documentElement.hasAttribute('data-i18n-hold'), null, { timeout: 20_000 });
  await page.waitForTimeout(300);
  return res;
}

export async function contentText(page: Page) {
  return (await page.locator('#page-content').first().innerText()).trim();
}

export async function hasHorizontalScroll(page: Page) {
  return page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);
}

/** Elements inside `root` whose box sticks out of root's box horizontally. */
export async function overflowingChildren(page: Page, rootSelector: string, tolerance = 1) {
  return page.evaluate(({ sel, tol }) => {
    const root = document.querySelector(sel);
    if (!root) return ['<root not found: ' + sel + '>'];
    const r = root.getBoundingClientRect();
    const out: string[] = [];
    root.querySelectorAll('*').forEach((el) => {
      const b = el.getBoundingClientRect();
      if (!b.width || !b.height || getComputedStyle(el).position === 'fixed') return;
      if (b.right > r.right + tol || b.left < r.left - tol) out.push(`${el.tagName.toLowerCase()}.${String((el as HTMLElement).className || '').split(' ').join('.')} [${Math.round(b.left)}..${Math.round(b.right)}] outside [${Math.round(r.left)}..${Math.round(r.right)}]`);
    });
    return out.slice(0, 10);
  }, { sel: rootSelector, tol: tolerance });
}

export async function clippedText(page: Page, rootSelector: string) {
  return page.evaluate((sel) => {
    const root = document.querySelector(sel);
    if (!root) return ['<root not found>'];
    const out: string[] = [];
    root.querySelectorAll('*').forEach((el) => {
      const h = el as HTMLElement;
      if (!h.textContent?.trim() || h.children.length) return;
      const cs = getComputedStyle(h);
      if (cs.overflow === 'visible' && cs.overflowX === 'visible') return;
      if (h.scrollWidth > h.clientWidth + 1) out.push(h.textContent.trim().slice(0, 40));
    });
    return out.slice(0, 10);
  }, rootSelector);
}
