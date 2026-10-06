import { supplierProblemMessage } from './supplierProblems';
/**
 * api.ts — typed fetch client for the nimiqshop Go backend. Ported verbatim
 * from the original api.js (caching, dedup, stale-serve, idempotency guards
 * all preserved). All money values travel as strings/ints exactly as the
 * backend sends them.
 */
import { CFG, siteName } from './config';
import { asset } from './asset';
import { getLang, t as tr } from '../i18n';
import { uuid as uuid2 } from './format';
import { canonicalIntent, purchaseIntentPayload, loadOrCreateIntent, saveIntentQuote, releaseIntentForQuote } from './checkoutIntent';
import { dailyLimitMessage, isDailyLimitError } from './dailyLimit';
import { countryHint, COUNTRY_HINT_HEADER } from './edgeGeo';

export class ApiError extends Error {
  status: number;
  code?: string;
  data: Record<string, unknown>;
  constructor(status: number, message?: string, code?: string, data: Record<string, unknown> | null = null) {
    super(message || `Request failed (${status})`);
    this.status = status;
    this.code = code;
    this.data = data || {};
  }
}

/**
 * A 429 body the backend already wrote for a human. Infrastructure throttling
 * (request-rate middleware, the supplier's queue) uses safe copy and preserves
 * any explicit cooldown; it must not promise a few seconds for a long wait. Business rules that refill
 * on a slow clock ("you already spun today", the daily purchase budget) carry
 * the buyer's actual situation and must be shown verbatim, because "wait a few
 * seconds and try again" is wrong advice for them.
 */
function isBuyerFacingThrottleMessage(msg: string): boolean {
  if (!msg || msg.length > 400) return false;
  if (/request failed \(\d+\)/i.test(msg)) return false;
  return !/rate limit exceeded|supplier rate limit|supplier queue|retry after|retry shortly|too many requests/i.test(msg);
}

/** Buyer-facing copy — never leak status codes or backend hostnames. */
export function friendlyApiMessage(err: unknown, fallback = tr('api.generic')): string {
  const e = err as { status?: number; message?: string; code?: string; data?: any } | null;
  if (e?.code === 'ACTIVE_CHECKOUT') {
    return tr('api.activeCheckout');
  }
  if (['ORDER_OUTCOME_UNKNOWN','PAYMENT_NOT_PAYABLE','IDEMPOTENCY_MISMATCH'].includes(e?.code || '')) return String(e?.data?.detail || e?.message || tr('api.checkExisting'));
  // The daily purchase budget is a 429 with a code of its own: it is not a
  // request-rate problem, so it must never be flattened into "too many
  // requests" (and never into "could not lock a live price" downstream).
  if (isDailyLimitError(e)) return dailyLimitMessage(e);
  const supplierMessage = supplierProblemMessage(e);
  if (supplierMessage) return supplierMessage;
  const status = Number(e?.status) || 0;
  const msg = String(e?.message || '');
  if (/\b(?:lightning|bitcoin|BTC|BOLT-?11)\b/i.test(msg)) return tr('api.paymentRequest');
  if (status === 401) return tr('api.connectNimiq');
  if (status === 429) {
    if (['SUPPLIER_RATE_LIMITED', 'CHECKOUT_BUDGET_WAIT', 'CHECKOUT_QUEUE_BUSY'].includes(e?.code || '')) {
      const seconds = Number(e?.data?.retry_after_seconds);
      if (Number.isFinite(seconds) && seconds > 0) {
        const count = Math.ceil(seconds >= 60 ? seconds / 60 : seconds);
        const duration = tr(seconds >= 60 ? 'api.minutes' : 'api.seconds', { count });
        return tr('api.pausedFor', { duration });
      }
    }
    // A 429 is not always "you are sending too many requests". Business rules
    // day) arrive as 429 too, and "wait a few seconds" is wrong advice for
    // those — so pass the backend's own buyer-facing wording through and keep
    // the generic line only for real request-rate throttling.
    if (isBuyerFacingThrottleMessage(msg)) return msg;
    return tr('api.tooMany');
  }
  if (status >= 500) return tr('api.shopDown');
  // "Our API could not be reached" is a claim, and it may only be made when it
  // is true. This branch used to say `status === 0`, and since Number(undefined)
  // is 0 every plain Error took it — a wallet refusal, an SDK timeout, a UI
  // bug — and every one of them came out as "shop.nimiqbase.com is
  // unreachable". A Nimiq Pay -32000 NETWORK_ERROR (the owner's report of
  // 2026-10-04) was rendered as our own site being down.
  //
  // Attribution now follows the shape of the failure: OUR transport failure
  // (ApiError with status 0 — fetch threw) and the raw browser fetch strings
  // are the only things blamed on the site, and they get the same localized
  // sentence in every language. Everything else keeps its own words.
  const transport = (err instanceof ApiError && status === 0) ||
    /cannot reach|too long|failed to fetch|networkerror|load failed/i.test(msg);
  if (transport) {
    return tr('api.cannotReach', { site: siteName() });
  }
  if (msg && msg.length < 180 && !/request failed \(\d+\)/i.test(msg) && !/ALLOWED_ORIGINS|\/api/i.test(msg)) {
    return msg;
  }
  return fallback;
}

/**
 * errorFacts — the EXACT failure, for the line under a friendly message.
 *
 * Owner (2026-10-05): "satın alırken … bir 'haa oluştu' diyo o kırmızı yerde;
 * tam hataları söyleyebilirdi". The red boxes used to show only the mapped
 * sentence, so a buyer (and support) had nothing to act on. `friendlyApiMessage`
 * still owns the human sentence; this owns the facts behind it — the backend's
 * code, the HTTP status and the raw message — and the UI renders it in small
 * print instead of hiding it.
 */
export interface ErrorFacts {
  code: string;
  status: number;
  message: string;
}

export function errorFacts(err: unknown): ErrorFacts {
  const e = err as { status?: number; message?: string; code?: string; data?: any } | null;
  const status = Number(e?.status) || 0;
  const code = String(e?.code || e?.data?.code || '').trim();
  const raw = String(e?.message || e?.data?.detail || '').trim();
  return { code, status, message: raw };
}

/** "PEER_LIMIT · 429 · too many quotes in flight" — '' when there is nothing. */
export function errorDetailLine(err: unknown): string {
  const { code, status, message } = errorFacts(err);
  const parts: string[] = [];
  if (code) parts.push(code);
  if (status) parts.push(String(status));
  const msg = message.replace(/\s+/g, ' ').trim();
  if (msg && msg.length <= 200 && !/ALLOWED_ORIGINS/i.test(msg)) parts.push(msg);
  return parts.join(' · ');
}

let sessionGetter: () => string | null = () => null;
export function _setSessionGetter(fn: () => string | null) {
  sessionGetter = fn;
}

// The CSRF token is injected rather than imported from ./session for the same
// reason the session getter is: session.ts imports this module, so importing
// back would create a cycle whose resolution depends on which entry point
// loaded first. Injection keeps the dependency one-directional.
//
// What the getter returns changed meaning along with the session itself. It
// used to be the JWT, which the caller then decoded to obtain `uid`; it is now
// the uid directly, because the JWT lives in an HttpOnly cookie that no script
// can read. The checkout idempotency scope is built from this value, and since
// it is the same `uid` string as before, every intent key already stored in a
// returning shopper's localStorage still resolves identically.
let csrfGetter: () => string = () => '';
export function _setCSRFGetter(fn: () => string) {
  csrfGetter = fn;
}

/* ---------------- client-side GET micro-cache ---------------- */
const _getCache = new Map<string, { data: unknown; at: number; ttl: number }>();
const _inflight = new Map<string, Promise<unknown>>();
const STALE_MAX = 10 * 60 * 1000;

function cacheTtlFor(path: string): number {
  if (path.startsWith('/catalog/brands')) return 5 * 60 * 1000;
  if (path.startsWith('/catalog/products')) return 5 * 60 * 1000;
  if (path.startsWith('/catalog/price')) return 0; // never stale-serve a live price
  if (path.startsWith('/catalog/payment-vias')) return 10 * 60 * 1000;
  if (path.startsWith('/catalog/search')) return 60 * 1000;
  if (path.startsWith('/catalog/check-phone')) return 30 * 1000;
  if (path.startsWith('/market/')) return 30 * 1000;
  if (path.startsWith('/cashback/')) return 5 * 60 * 1000;
  if (path.startsWith('/activity')) return 10 * 1000;
  if (path.startsWith('/ratings/')) return 60 * 1000;
  if (path.startsWith('/track/')) return 5 * 1000;
  return 0;
}

// Success payloads may be arrays (catalog families, activity, etc.). Only
// ERROR payloads are normalized to a record for structured problem parsing.
async function readApiResponse(res: Response): Promise<any> {
  const text = await res.text();
  let parsed: any = {};
  try {
    if (text) parsed = JSON.parse(text);
  } catch {
    if (res.ok) throw new ApiError(502, tr('api.invalidResponse'));
  }
  if (res.ok) return parsed;
  const data = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
  throw new ApiError(res.status, data.detail || data.error || data.message || `Request failed (${res.status})`, data.code, data);
}

/** An AbortSignal that fires after timeoutMs — the request's own deadline. */
function requestTimeout(timeoutMs: number): AbortSignal {
  return AbortSignal.timeout(timeoutMs);
}

/** True when fetch rejected because requestTimeout() fired (browsers raise a
 *  TimeoutError; older engines surface a plain AbortError). */
function isTimeout(e: unknown): boolean {
  const name = (e as { name?: string } | null)?.name;
  return name === 'TimeoutError' || name === 'AbortError';
}

async function _fetchGet(path: string, headers: Record<string, string>, timeoutMs: number): Promise<unknown> {
  if (!headers['Accept-Language']) headers['Accept-Language'] = langTag();
  // The visitor's country as the edge reported it to the browser — see
  // edgeGeo.ts. A hint: the backend only ever spends it on the operator
  // console's origin note, never on a price, an order or the visitor IP.
  if (!headers[COUNTRY_HINT_HEADER]) {
    const cc = countryHint();
    if (cc) headers[COUNTRY_HINT_HEADER] = cc;
  }
  let res: Response;
  try {
    const base = (CFG.API_BASE || '/api').replace(/\/$/, '');
    res = await fetch(base + path, {
      method: 'GET',
      headers,
      signal: requestTimeout(timeoutMs),
      credentials: base.startsWith('/') ? 'same-origin' : 'include',
      cache: 'no-store',
    });
  } catch (e) {
    if (isTimeout(e)) throw new ApiError(0, tr('api.timeout'));
    throw new ApiError(0, tr('api.cannotReach', { site: siteName() }));
  }

  return readApiResponse(res);
}

async function cachedGet(path: string, { headers = {}, timeoutMs = 15000 }: { headers?: Record<string, string>; timeoutMs?: number } = {}): Promise<unknown> {
  const ttl = cacheTtlFor(path);
  const hit = _getCache.get(path);
  const now = Date.now();
  if (ttl > 0 && hit && now - hit.at < ttl) return hit.data;

  if (_inflight.has(path)) return _inflight.get(path);

  const p = _fetchGet(path, headers, timeoutMs)
    .then((data) => {
      if (ttl > 0) _getCache.set(path, { data, at: Date.now(), ttl });
      return data;
    })
    .catch((e: unknown) => {
      if (!path.startsWith('/catalog/price') && ttl > 0 && e instanceof ApiError && e.status === 0 && hit && now - hit.at < ttl + STALE_MAX) {
        return hit.data;
      }
      throw e;
    })
    .finally(() => _inflight.delete(path));
  _inflight.set(path, p);
  return p;
}

/**
 * The active language, as an `Accept-Language` header.
 *
 * The supplier's catalog payload is language-dependent: CryptoRefills' v5 API
 * returns the brand's own description / how-to-redeem / terms and answers in
 * en/es/de/fr/pt/tr — the same six languages this shop renders. The backend
 * reads ?lang=, then the nimshop-lang cookie, then Accept-Language (see
 * backend/internal/i18n), so sending the header here is enough to make the
 * "How to redeem" block arrive in the shopper's language. `Accept-Language` is
 * CORS-safelisted, so this adds no preflight.
 */
function langTag(): string {
  const code = getLang();
  return code ? `${code}, *;q=0.5` : 'en';
}

export async function api(
  path: string,
  {
    method = 'GET',
    body,
    auth = false,
    headers = {},
    timeoutMs = 15000,
    quiet = false,
  }: { method?: string; body?: unknown; auth?: boolean; headers?: Record<string, string>; timeoutMs?: number; quiet?: boolean } = {}
): Promise<Record<string, any>> {
  const h: Record<string, string> = { ...headers };
  if (!h['Accept-Language']) h['Accept-Language'] = langTag();
  // Same edge-country hint as the GET path above.
  if (!h[COUNTRY_HINT_HEADER]) {
    const cc = countryHint();
    if (cc) h[COUNTRY_HINT_HEADER] = cc;
  }
  let payload: string | undefined;
  if (body !== undefined) {
    h['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  if (auth) {
    // No Authorization header is set any more. The credential is an HttpOnly
    // cookie that the browser attaches by itself — that is the entire point of
    // the change, since a token kept in script-readable storage could be
    // copied out by any XSS and replayed for seven days.
    //
    // Two things are still needed here. The local signed-in check gives the
    // shopper an immediate, understandable message instead of a round trip
    // that ends in a bare 401. And because cookies are attached by the browser
    // rather than chosen by this code, a state-changing request must carry the
    // double-submit CSRF echo, which is what stops a third-party site from
    // driving the shopper's session.
    const owner = sessionGetter();
    if (!owner) throw new ApiError(401, tr('api.notSignedIn'));
    if (method !== 'GET' && method !== 'HEAD') {
      const csrf = csrfGetter();
      // Only set when non-empty: sending an empty X-CSRF-Token would fail the
      // server's check in exactly the same way as omitting it, and an explicit
      // header the caller already set must not be overwritten.
      if (csrf && !h['X-CSRF-Token']) h['X-CSRF-Token'] = csrf;
    }
  }

  if (method === 'GET' && !auth) {
    return cachedGet(path, { headers: h, timeoutMs }) as Promise<Record<string, any>>;
  }

  let res: Response;
  try {
    const base = (CFG.API_BASE || '/api').replace(/\/$/, '');
    res = await fetch(base + path, {
      method,
      headers: h,
      body: payload,
      signal: requestTimeout(timeoutMs),
      credentials: base.startsWith('/') ? 'same-origin' : 'include',
      cache: 'no-store',
    });
  } catch (e) {
    if (isTimeout(e)) throw new ApiError(0, 'The server took too long to respond. Check your connection and try again.');
    throw new ApiError(0, 'Cannot reach ' + siteName() + ' right now. Check your connection and try again.');
  }

  if (res.status === 401 && auth && !quiet && typeof window !== 'undefined') {
    // `quiet` requests are display-only probes (the balance strip, background
    // refreshes). A 401 from one of them is a fact about that probe, never
    // proof that the shopper's session is over — and treating it as proof made
    // the shop sign people out while they were reading the page (owner,
    // 2026-10-06: "sürekli giriş yapıyorum çıkıyor"). They still surface the
    // error to their own caller; they simply cannot end a session.
    window.dispatchEvent(new CustomEvent('nimshop:unauthorized'));
  }
  return readApiResponse(res);
}

/* ---------------- Auth ---------------- */
export const authChallenge = () => api('/auth/challenge', { method: 'POST' });
export const hubLogin = (req: Record<string, unknown>) => api('/auth/hub-login', { method: 'POST', body: req });

/* ---------------- Catalog ---------------- */
/**
 * Builds `/catalog/brands?<query>`.
 *
 * `kind` used to be hard-coded into the path and the country was appended with
 * its own '?', producing `/catalog/brands?kind=giftcard?country=TR` — which is
 * not two parameters but one: `kind` = "giftcard?country=TR". The backend
 * (internal/handlers/catalog_handlers.go, ListBrands) validates `kind` against
 * the three allowed values and answers 400 otherwise, so as soon as a country
 * was known — geo detection on first load, or simply the country the shopper
 * picked last visit — every gift-card, top-up and eSIM request failed and the
 * home page came up empty, with no error the shopper could act on.
 *
 * Building the whole query with URLSearchParams makes the separator the
 * parser's problem instead of ours.
 */
const catQS = (kind: string, country?: string, test?: boolean) => {
  const p = new URLSearchParams({ kind });
  if (country) p.set('country', country);
  if (test) p.set('test', '1');
  return '/catalog/brands?' + p.toString();
};
/* ---- Static catalog snapshot — the storefront's first source -------------
 * scripts/sync-catalog.mjs pulls /v2/brands from CryptoRefills hourly (Pages
 * deploy cron) and commits a seed, so `public/data/catalog/brands/*.json`
 * is plain static edge content: it survives a dead backend, which the old
 * /api-only path did not (shopapi down = empty shop). The kind filter the
 * Go handler applied server-side (filterBrandCategories) is mirrored here
 * byte-for-byte in predicate terms; /api stays as the fallback for a country
 * with no snapshot file and for ?test=1 admin probes.
 * Note: admin catalog rules (hidden families etc.) are server-side, so the
 * static path shows the supplier's live listing unfiltered — same as what
 * the supplier's own storefront shows. */
/** A static snapshot older than this is considered stale: the hourly Pages
 *  cron (or the deploy itself) may have been broken for a while, and a
 *  half-day-old price/catalog is worse than a live round trip to the API.
 *  Owner's rule: static first, but never trust a snapshot older than 12 h —
 *  past that, fall through to /api automatically. */
const STATIC_MAX_AGE_MS = 12 * 60 * 60 * 1000;
function staticFresh(j: any): boolean {
  const at = Date.parse(String(j?.generated_at || ''));
  if (!Number.isFinite(at)) return false; // untimestamped snapshot: not trustworthy
  const age = Date.now() - at;
  if (age < 0) return -age <= 60_000; // tolerate a minute of clock skew
  return age <= STATIC_MAX_AGE_MS;
}

const _staticInflight = new Map<string, Promise<{ country_code?: string; categories: any[] } | null>>();
async function staticBrands(country?: string): Promise<{ country_code?: string; categories: any[] } | null> {
  if (typeof fetch !== 'function') return null;
  const raw = String(country || '').toUpperCase().slice(0, 2);
  const file = raw.length === 2 ? raw : '_global';
  const flying = _staticInflight.get(file);
  if (flying) return flying;
  const p = staticBrandsFetch(file).finally(() => _staticInflight.delete(file));
  _staticInflight.set(file, p);
  return p;
}
async function staticBrandsFetch(file: string): Promise<{ country_code?: string; categories: any[] } | null> {
  try {
    const res = await fetch(asset(`/data/catalog/brands/${file}.json`), {
      cache: 'default',
      headers: { Accept: 'application/json' },
    });
    if (!res.ok) return null;
    const j = await res.json();
    return j && Array.isArray(j.categories) && staticFresh(j) ? j : null;
  } catch {
    return null;
  }
}
/** Static market snapshot (scripts/sync-market.mjs, hourly): the NIM/BTC
 *  price and the FX table as edge files, so a page load never waits on the
 *  Go backend for a number that changes a few percent a day. Shapes are
 *  byte-compatible with /market/nim-rate and /market/fx. */
async function staticMarket(name: 'fx' | 'nim-rate'): Promise<Record<string, any> | null> {
  if (typeof fetch !== 'function') return null;
  try {
    const res = await fetch(asset(`/data/market/${name}.json`), { cache: 'default', headers: { Accept: 'application/json' } });
    if (!res.ok) return null;
    const j = await res.json();
    return j && typeof j === 'object' && staticFresh(j) ? j : null;
  } catch {
    return null;
  }
}

/** Mirror of the backend's filterBrandCategories predicate. */
function filterKind(json: { categories: any[] }, kind: string) {
  const out = (json.categories || []).filter((c) => {
    let match = c?.kind === kind || c?.category === kind || (c?.category === 'e-sim' && kind === 'esim');
    if (kind === 'mobile_recharge' && c?.category === 'e-sim') match = false;
    return match;
  });
  return { categories: out };
}

// One canonical endpoint: /catalog/brands?kind=giftcard|mobile_recharge|esim
export const listGiftCards = async (country?: string, test?: boolean) => {
  if (!test) { const s = await staticBrands(country); if (s) return filterKind(s, 'giftcard'); }
  return api(catQS('giftcard', country, test));
};
export const listTopups = async (country?: string, test?: boolean) => {
  if (!test) { const s = await staticBrands(country); if (s) return filterKind(s, 'mobile_recharge'); }
  return api(catQS('mobile_recharge', country, test));
};
export const listEsims = async (country?: string, test?: boolean) => {
  if (!test) { const s = await staticBrands(country); if (s) return filterKind(s, 'esim'); }
  return api(catQS('esim', country, test));
};
export const searchProducts = async (q: string, country?: string) => {
  const query = String(q || '').toLowerCase().trim();
  if (query && query.length <= 100) {
    const s = await staticBrands(country);
    if (s) {
      // Same row shape and 50-row cap as the backend's /catalog/search.
      const rows: Array<{ family: string; kind: string; category: string; country_code?: string }> = [];
      outer: for (const c of s.categories || []) {
        for (const b of c.brands || []) {
          if (String(b.family || '').toLowerCase().includes(query)) {
            rows.push({ family: b.family, kind: c.kind, category: c.category, country_code: b.country_code || s.country_code });
            if (rows.length >= 50) break outer;
          }
        }
      }
      return rows;
    }
  }
  return api(`/catalog/search?q=${encodeURIComponent(q)}${country ? '&country=' + encodeURIComponent(country) : ''}`);
};

const PROD_CACHE_PREFIX = 'nim_prod:v2:';
const PROD_CACHE_TTL_MS = 5 * 60 * 1000;
export const getProduct = async (
  id: string,
  country?: string,
  opts: { force?: boolean } = {}
): Promise<Record<string, any>> => {
  const path = `/catalog/products/${encodeURIComponent(id)}`;
  const qs = new URLSearchParams();
  if (country) qs.set('country', country);
  // The family payload carries the supplier's translated how-to-redeem text,
  // so the language belongs in the request — and therefore in the cache key
  // below (sessionStorage) and in the backend's own per-language entry.
  qs.set('lang', getLang());
  const s = qs.toString();
  const fullPath = path + (s ? '?' + s : '');
  const key = PROD_CACHE_PREFIX + fullPath;
  if (opts.force) _getCache.delete(fullPath);
  const now = Date.now();
  if (!opts.force && typeof sessionStorage !== 'undefined') {
    try {
      const raw = sessionStorage.getItem(key);
      if (raw) {
        const j = JSON.parse(raw);
        if (j && j.fetched_at && now - j.fetched_at < PROD_CACHE_TTL_MS &&
          (Array.isArray(j.data) || Array.isArray(j.data?.products))) return j.data;
      }
    } catch {
      /* corrupt cache, ignore */
    }
  }
  try {
    const data = await api(fullPath);
    try {
      for (let i = sessionStorage.length - 1; i >= 0; i--) {
        const k = sessionStorage.key(i);
        if (k && k.startsWith(PROD_CACHE_PREFIX) && k !== key) sessionStorage.removeItem(k);
      }
      sessionStorage.setItem(key, JSON.stringify({ data, fetched_at: now }));
    } catch {
      /* quota full — cache is best-effort */
    }
    return data;
  } catch (err) {
    if (!(err instanceof ApiError) || err.status !== 0) throw err;
    try {
      const raw = sessionStorage.getItem(key);
      if (raw) return JSON.parse(raw).data;
    } catch {
      /* ignore */
    }
    throw err;
  }
};

export const checkPhone = (phoneNumber: string, country?: string) =>
  api(
    `/catalog/check-phone?phone_number=${encodeURIComponent(phoneNumber)}` +
      (country ? `&country=${encodeURIComponent(country)}` : '')
  );

/* ---------------- Quotes: durable, retry-safe intent ---------------- */
const _quoteInflight = new Map<string, Promise<Record<string, any>>>();

async function createCheckout(path: string, req: unknown, proposedKey?: string): Promise<Record<string, any>> {
  // sessionGetter returns the user id (the JWT `uid` claim, now delivered as
  // plain data instead of decoded out of a token), so the scope string is
  // byte-for-byte what it was before the cookie change and intents already
  // persisted in localStorage still resolve to the same key.
  const owner = sessionGetter();
  if (!owner) throw new ApiError(401, tr('api.connectFirst'));
  const scope = owner + '|' + path + '|' + canonicalIntent(purchaseIntentPayload(req));
  const pending = _quoteInflight.get(scope);
  if (pending) return pending;
  const promise = (async () => {
    // Store FIRST, then send. Ambiguous failures never delete or rotate this key.
    const saved = await loadOrCreateIntent(localStorage, scope, proposedKey || uuid2());
    const quote = await api(path, { method: 'POST', body: req, auth: true,
      headers: { 'Idempotency-Key': saved.intent.key }, timeoutMs: 90000 });
    const id = String(quote.quote_id || quote.id || '');
    if (!id) throw new ApiError(0, tr('api.orderUnclear'));
    saveIntentQuote(localStorage, saved.storageKey, saved.intent, id);
    return quote;
  })().finally(() => _quoteInflight.delete(scope));
  _quoteInflight.set(scope, promise);
  return promise;
}

// Legacy callers may clear display state, never the durable purchase key.
export const forgetQuote = (_req: unknown) => {};
export const createQuote = (req: unknown, key?: string) => createCheckout('/quotes', req, key);
export const createQuoteBatch = (items: unknown[], email?: string, key?: string, cashbackCode?: string, paymentMethod?: string, cashbackDest?: string, anonymous?: boolean, ackActiveCheckout?: boolean) =>
  createCheckout('/quotes/batch', {
    items,
    email,
    ack_active_checkout: !!ackActiveCheckout,
    cashback_code: cashbackCode || '',
    payment_method: paymentMethod || 'nimiq_pay',
    cashback_destination: cashbackDest || 'cashback',
    anonymous: !!anonymous,
  }, key);

/* ---------------- Site config (feature flags) ---------------- */
export const getSiteConfig = () => api('/site-config');

export const listQuotes = () => api('/quotes', { auth: true });
export const getQuote = async (id: string) => {
  const out = await api(`/quotes/${encodeURIComponent(id)}`, { auth: true });
  if (out.quote) out.quote = { ...out.quote, can_pay: out.can_pay === true };
  return out;
};
export const refreshQuote = async (id: string) => {
  const out = await api(`/quotes/${encodeURIComponent(id)}/refresh`, {method:'POST',auth:true,timeoutMs:30000});
  if (out.quote) out.quote = {...out.quote,can_pay:out.can_pay===true};
  return out;
};
// PaymentLaunch authorizes an explicit wallet/clipboard/QR handoff of a single
// supplier Lightning invoice. There is no "already opened" barrier: the invoice
// is single-use, so handing it off again is always safe and never a duplicate.
export const authorizePaymentLaunch = async (id: string) => {
  const out = await api(`/quotes/${encodeURIComponent(id)}/payment-launch`, {
    method: 'POST', auth: true, body: {}, timeoutMs: 30000,
  });
  if (out.quote) out.quote = { ...out.quote, can_pay: out.can_pay === true };
  return out;
};

// Rebuy is a deliberate action on the completed order, never a timeout retry.
export const allowNewPurchase = async (id: string) => {
  const out = await getQuote(id); const q = out.quote || out;
  const safe = ['fulfilled', 'refunded'].includes(q.status) ||
    (q.status === 'failed' && (!q.supplier_order_id || (q.supplier_status === 'PaymentSetupFailed' && !q.payment_observed)));
  if (!safe) throw new ApiError(409, tr('api.resolveFirst'));
  releaseIntentForQuote(localStorage, id);
};

/* Price quotes are live supplier money-path data with a 15 s lifetime — they
 * are deliberately NOT static-first (see the backend: no-store, no stale
 * fallback, checkout re-quotes). What we CAN remove is repeat round trips:
 * re-renders and denomination re-clicks within the quote's own lifetime reuse
 * the in-flight or ~12 s-old answer instead of refetching. */
const PRICE_TTL_MS = 12_000;
const _priceCache = new Map<string, { at: number; data: Record<string, any> }>();
const _priceInflight = new Map<string, Promise<Record<string, any>>>();
export const getProductPrice = (brand: string, country: string, value: number): Promise<Record<string, any>> => {
  const key = '/catalog/price?' + new URLSearchParams({ brand_name: brand, country_code: country, face_value: String(value) });
  const now = Date.now();
  const hit = _priceCache.get(key);
  if (hit && now - hit.at < PRICE_TTL_MS) return Promise.resolve(hit.data);
  const flying = _priceInflight.get(key);
  if (flying) return flying;
  const p = api(key)
    .then((data: any) => {
      _priceCache.set(key, { at: Date.now(), data });
      if (_priceCache.size > 60) {
        const oldest = _priceCache.keys().next().value;
        if (oldest) _priceCache.delete(oldest);
      }
      return data;
    })
    .finally(() => _priceInflight.delete(key));
  _priceInflight.set(key, p);
  return p;
};

/* ---------------- Wallet notification prefs ---------------- */
export const getNotificationPrefs = () => api('/account/notifications', { auth: true });
export const setNotificationPrefs = (enabled: boolean) =>
  api('/account/notifications', { method: 'PUT', body: { enabled: !!enabled }, auth: true });

/* ---------------- Orders ---------------- */
export const listOrders = () => api('/orders', { auth: true });
/**
 * The signed-in wallet's own NIM balance (session-scoped; no address param).
 *
 * Stake-aware on purpose (owner, 2026-10-05: "your wallet'deki NIM miktarım
 * yanlış"): `getAccountByAddress` reports the LIQUID balance only, so a buying
 * wallet that also stakes looked 50x smaller than it is. `balance_nim` /
 * `balance_luna` are what a payment can spend; `total_nim` is what the wallet
 * app shows its owner.
 */
export interface WalletBalanceResponse {
  /** false when the backend could not answer — see `reason`. */
  available: boolean;
  /** no_wallet | rpc_unavailable | … */
  reason?: string;
  address?: string;
  balance_luna?: number;
  balance_nim?: number;
  staked_nim?: number;
  inactive_nim?: number;
  retired_nim?: number;
  total_nim?: number;
  network?: string;
  cached?: boolean;
  observed_at?: string;
}

export const getWalletBalance = (opts: { fresh?: boolean } = {}) =>
  api(`/wallet/balance${opts.fresh ? '?fresh=1' : ''}`, {
    auth: true,
    timeoutMs: 9000,
    // Display-only: a failed balance probe must never end the session.
    quiet: true,
  }) as Promise<WalletBalanceResponse>;
export const getOrder = (id: string) => api(`/orders/${encodeURIComponent(id)}`, { auth: true });
export const refreshOrder = (id: string) => api(`/orders/${encodeURIComponent(id)}/refresh`, { method: 'POST', auth: true });
export const getOrderSupport = (id: string) => api(`/orders/${encodeURIComponent(id)}/support`, { auth: true });

/* ---------------- Support ---------------- */
export const createTicket = (req: Record<string, unknown>) => api('/support/tickets', { method: 'POST', body: req, auth: true });
export const listTickets = () => api('/support/tickets', { auth: true });
export const getTicket = (id: string) => api(`/support/tickets/${encodeURIComponent(id)}`, { auth: true });
export const replyTicket = (ticketId: string, message: string) =>
  api(`/support/tickets/${encodeURIComponent(ticketId)}/messages`, { method: 'POST', body: { message }, auth: true });
/**
 * Buyer self-service status change on their own ticket. The backend only
 * accepts `resolved` (close it yourself) and `open` (reopen a resolved one)
 * and verifies ownership — arbitrary states stay admin-only.
 */
export const updateTicketStatus = (ticketId: string, status: string) =>
  api(`/support/tickets/${encodeURIComponent(ticketId)}/status`, { method: 'POST', body: { status }, auth: true });
export const adminListSupportTickets = (status?: string) =>
  api('/admin/support/tickets' + (status ? '?status=' + encodeURIComponent(status) : ''));
export const adminGetSupportTicket = (ticketId: string) => api(`/admin/support/tickets/${encodeURIComponent(ticketId)}`);
export const adminReplySupportTicket = (ticketId: string, message: string, status?: string, internal?: boolean) =>
  api(`/admin/support/tickets/${encodeURIComponent(ticketId)}/messages`, {
    method: 'POST',
    body: internal ? { message, internal: true } : status ? { message, status } : { message },
  });
export const adminUpdateSupportTicketStatus = (ticketId: string, status: string) =>
  api(`/admin/support/tickets/${encodeURIComponent(ticketId)}/status`, { method: 'POST', body: { status } });

/* ---------------- Market / misc ---------------- */
export const getActivity = (limit = 50) => api(`/activity?limit=${limit}`);
export const trackOrder = (id: string) => api(`/track/${encodeURIComponent(id)}`);
export const rateOrder = (id: string, rating: number) => api(`/orders/${encodeURIComponent(id)}/rate`, { method: 'POST', body: { rating }, auth: true });
export const rateQuote = (id: string, rating: number) => api(`/quotes/${encodeURIComponent(id)}/rate`, { method: 'POST', body: { rating }, auth: true });
export const getAccountLimits = () => api('/account/limits', { auth: true });
export const getCashbackRate = () => api('/cashback/rate');
export const validateCashbackCode = (code: string, orderUSD = 0) => api(`/cashback/code?code=${encodeURIComponent(code)}&order_usd=${encodeURIComponent(String(orderUSD > 0 ? orderUSD : 0))}`);

// Pool-staker cashback: the signed-in buyer's active delegation to the
// operator's validator and the rate it earns. All three are authed in the Go
// backend (RequireAuth) — without `auth: true` the real server answers
// 401 {"error":"missing bearer token"} for every cashback-page load.
export const getPoolStake = () => api('/poolstake/me', { auth: true });
/** The buyer's own cashback ledger: lifetime totals + individual payouts. */
export const getMyCashback = () => api('/cashback/me', { auth: true });
export const getCashbackLeaderboard = (bucket: 'week' | 'month' | 'all' | string = 'all') =>
  api('/cashback/leaderboard?bucket=' + encodeURIComponent(bucket));
export const refreshPoolStake = () => api('/poolstake/refresh', { method: 'POST', auth: true });

/* ---------------- FX / NIM rate (sessionStorage cached) ---------------- */
const FX_CACHE_KEY = 'nim_fx';
const FX_CACHE_TTL_MS = 5 * 60 * 1000;
const NIM_CACHE_KEY = 'nim_market';
const NIM_CACHE_TTL_MS = 5 * 60 * 1000;

/**
 * Live-rate reconciliation.
 *
 * The static snapshots in /data/market are baked by the hourly Pages deploy
 * and are trusted for up to STATIC_MAX_AGE_MS. That is fine for painting fast,
 * but it is NOT fine for a number the buyer reads: our displayed NIM price is
 * coin_amount(BTC) x btc_usd / nim_usd, and while the BTC amount is pinned by
 * the supplier at checkout, the market leg can be hours old when the deploy
 * cadence slips (GitHub throttles its own schedule — measured 5-hour gaps in
 * this repo). A stale leg means the screen shows a NIM figure that no wallet
 * will charge.
 *
 * So: serve the snapshot immediately (never block on a sleeping backend) and
 * then, at most once per RATE_RECONCILE_THROTTLE_MS per tab, ask the live API
 * and hand any material difference to whoever is showing a price. If the
 * backend is down, nothing changes — the same behaviour as before.
 */
const RATE_RECONCILE_THROTTLE_MS = 10 * 60 * 1000;
const RATE_RECONCILE_EPSILON = 0.001; // 0.1% — below that, not worth a repaint
const NIM_RECONCILE_KEY = 'nim_market_reconcile_at';
const FX_RECONCILE_KEY = 'nim_fx_reconcile_at';

type RatesListener = (rates: Record<string, any>) => void;
const _ratesListeners = new Set<RatesListener>();

/** Subscribe to corrected live rates; returns an unsubscribe function. */
export function onRatesChange(cb: RatesListener): () => void {
  _ratesListeners.add(cb);
  return () => {
    _ratesListeners.delete(cb);
  };
}

function emitRates(rates: Record<string, any>): void {
  for (const cb of Array.from(_ratesListeners)) {
    try {
      cb(rates);
    } catch {
      /* a listener must never break the price pipeline */
    }
  }
}

function relDiff(a: number, b: number): number {
  const m = Math.max(Math.abs(a), Math.abs(b));
  return m > 0 ? Math.abs(a - b) / m : 0;
}

/**
 * Compare one served snapshot with the live API and publish the difference.
 * Single-flight per document; silent on every failure path (offline, backend
 * asleep, malformed body) — the snapshot already on screen stays untouched.
 */
const _reconcileInflight = new Map<string, Promise<void>>();
function reconcileRates(name: 'fx' | 'nim-rate', served: Record<string, any> | null): void {
  const flying = _reconcileInflight.get(name);
  if (flying) return;
  const task = (async () => {
    const throttleKey = name === 'fx' ? FX_RECONCILE_KEY : NIM_RECONCILE_KEY;
    const valueKey = name === 'fx' ? 'usd_per_unit' : 'usd_per_nim';
    try {
      const last = Number(sessionStorage.getItem(throttleKey) || 0);
      if (Date.now() - last < RATE_RECONCILE_THROTTLE_MS) return;
      sessionStorage.setItem(throttleKey, String(Date.now()));
    } catch {
      /* no sessionStorage: reconcile once per page view */
    }
    try {
      const fresh: any = await api(name === 'fx' ? '/market/fx' : '/market/nim-rate', { timeoutMs: 5000 });
      if (!fresh || !fresh[valueKey]) return;
      const before = served && typeof served[valueKey] === 'number' ? served[valueKey] : null;
      const after = typeof fresh[valueKey] === 'number' ? fresh[valueKey] : null;
      if (before !== null && after !== null && relDiff(before, after) < RATE_RECONCILE_EPSILON) {
        // Same number: still refresh the session cache so the next read is live.
        try {
          const cacheKey = name === 'fx' ? FX_CACHE_KEY : NIM_CACHE_KEY;
          sessionStorage.setItem(cacheKey, JSON.stringify({ ...fresh, fetched_at: Date.now() }));
        } catch {}
        return;
      }
      try {
        const cacheKey = name === 'fx' ? FX_CACHE_KEY : NIM_CACHE_KEY;
        sessionStorage.setItem(cacheKey, JSON.stringify({ ...fresh, fetched_at: Date.now() }));
      } catch {}
      emitRates({ ...fresh, reconciled: true });
    } catch {
      /* keep the snapshot that is already on screen */
    }
  })().finally(() => _reconcileInflight.delete(name));
  _reconcileInflight.set(name, task);
}

export const getFXRates = async (opts: { force?: boolean } = {}): Promise<Record<string, any>> => {
  const now = Date.now();
  if (!opts.force && typeof sessionStorage !== 'undefined') {
    try {
      const raw = sessionStorage.getItem(FX_CACHE_KEY);
      if (raw) {
        const j = JSON.parse(raw);
        if (j && j.usd_per_unit && j.fetched_at && now - j.fetched_at < FX_CACHE_TTL_MS) return j;
      }
    } catch {
      /* ignore */
    }
  }
  const stFx = await staticMarket('fx');
  if (stFx && stFx.usd_per_unit) {
    try {
      sessionStorage.setItem(FX_CACHE_KEY, JSON.stringify({ ...stFx, fetched_at: now }));
    } catch {}
    reconcileRates('fx', stFx); // paint now, correct in the background
    return stFx;
  }
  try {
    const fresh = await api('/market/fx');
    if (fresh && fresh.usd_per_unit) {
      try {
        sessionStorage.setItem(FX_CACHE_KEY, JSON.stringify({ ...fresh, fetched_at: now }));
      } catch {}
    }
    return fresh;
  } catch (err) {
    try {
      const raw = sessionStorage.getItem(FX_CACHE_KEY);
      if (raw) return JSON.parse(raw);
    } catch {}
    throw err;
  }
};

export const getNimRate = async (opts: { force?: boolean; timeoutMs?: number } = {}): Promise<Record<string, any>> => {
  const now = Date.now();
  if (!opts.force && typeof sessionStorage !== 'undefined') {
    try {
      const raw = sessionStorage.getItem(NIM_CACHE_KEY);
      if (raw) {
        const j = JSON.parse(raw);
        if (j && j.usd_per_nim && j.fetched_at && now - j.fetched_at < NIM_CACHE_TTL_MS) return j;
      }
    } catch {
      /* ignore */
    }
  }
  const stNim = await staticMarket('nim-rate');
  if (stNim && stNim.usd_per_nim) {
    try {
      sessionStorage.setItem(NIM_CACHE_KEY, JSON.stringify({ ...stNim, fetched_at: now }));
    } catch {}
    reconcileRates('nim-rate', stNim); // paint now, correct in the background
    return stNim;
  }
  try {
    const fresh = await api('/market/nim-rate', { timeoutMs: opts.timeoutMs || 8000 });
    if (fresh && fresh.usd_per_nim) {
      try {
        sessionStorage.setItem(NIM_CACHE_KEY, JSON.stringify({ ...fresh, fetched_at: now }));
      } catch {}
    }
    return fresh;
  } catch (err) {
    try {
      const raw = sessionStorage.getItem(NIM_CACHE_KEY);
      if (raw) return JSON.parse(raw);
    } catch {}
    throw err;
  }
};

export const cachedNimRate = () => {
  try {
    const raw = sessionStorage.getItem(NIM_CACHE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
};

export const cachedFX = () => {
  try {
    const raw = sessionStorage.getItem(FX_CACHE_KEY);
    if (!raw) return null;
    const j = JSON.parse(raw);
    return j && j.usd_per_unit ? j.usd_per_unit : null;
  } catch {
    return null;
  }
};

/* ---------------- admin (operator) endpoints ---------------- */
export const adminLogin = (req: Record<string, unknown>) => api('/admin/auth/login', { method: 'POST', body: req });
export const adminLogout = () => api('/admin/auth/logout', { method: 'POST' });
export const adminStatus = () => api('/admin/notification/status');
export const adminMe = () => api('/admin/auth/me');
export const adminSend = (req: Record<string, unknown>) => api('/admin/notification/send', { method: 'POST', body: req });
export const adminTestEmail = (req: Record<string, unknown>) => api('/admin/test-email', { method: 'POST', body: req });
export const adminCatalogRules = (opts: { path?: string; method?: string; body?: unknown } = {}) =>
  api('/admin/catalog-rules' + (opts.path || ''), opts.method ? { method: opts.method, body: opts.body } : {});
export const adminGetCashback = () => api('/admin/settings/cashback');
/** Headline counters: Players + the cashback payment queue. */
export const adminDashboard = () => api('/admin/dashboard');
/** What a maintenance reset would delete right now (read-only). */
export const adminResetPreview = () => api('/admin/reset/preview');
/** Deletes every customer record. Requires the exact phrase the preview returns. */
export const adminReset = (confirm: string) => api('/admin/reset', { method: 'POST', body: { confirm } });
/** Customer list. sort: registered | orders | spend | cashback | last_seen | address */
export const adminListUsers = (sort = 'registered', dir: 'asc' | 'desc' = 'desc', limit = 50) =>
  api(
    '/admin/users?sort=' +
      encodeURIComponent(sort) +
      '&dir=' +
      encodeURIComponent(dir) +
      '&limit=' +
      encodeURIComponent(limit)
  );
/** One customer: aggregates, pool standing and the full activity timeline. */
export const adminUserDetail = (id: string) => api('/admin/users/' + encodeURIComponent(id));
export const adminSetCashback = (body: unknown) => api('/admin/settings/cashback', { method: 'POST', body });
/** The single-ledger staker table: every wallet's book, boost and caps. */
export const adminGetStakeLedger = () => api('/admin/stake-ledger');
/** Operator's per-wallet "start over": wipes the book (stake + caps survive). */
export const adminResetStakeLedger = (address: string) =>
  api('/admin/stake-ledger/reset', { method: 'POST', body: { address } });
export const adminListQuotes = (limit = 50) => api('/admin/quotes?limit=' + encodeURIComponent(limit));
export const adminGetOrder = (id: string) => api('/admin/orders/' + encodeURIComponent(id));
