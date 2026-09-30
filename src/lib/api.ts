import { supplierProblemMessage } from './supplierProblems';
/**
 * api.ts — typed fetch client for the nimiqshop Go backend. Ported verbatim
 * from the original api.js (caching, dedup, stale-serve, idempotency guards
 * all preserved). All money values travel as strings/ints exactly as the
 * backend sends them.
 */
import { CFG, siteName } from './config';
import { getLang, t as tr } from '../i18n';
import { uuid as uuid2 } from './format';
import { canonicalIntent, purchaseIntentPayload, loadOrCreateIntent, saveIntentQuote, releaseIntentForQuote } from './checkoutIntent';
import { dailyLimitMessage, isDailyLimitError } from './dailyLimit';

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
  if (status === 0 || /cannot reach|too long|network|failed to fetch/i.test(msg)) {
    return tr('api.cannotReach', { site: siteName() });
  }
  if (msg && msg.length < 180 && !/request failed \(\d+\)/i.test(msg) && !/ALLOWED_ORIGINS|\/api/i.test(msg)) {
    return msg;
  }
  return fallback;
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
  if (path.startsWith('/geo')) return 60 * 1000;
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
  }: { method?: string; body?: unknown; auth?: boolean; headers?: Record<string, string>; timeoutMs?: number } = {}
): Promise<Record<string, any>> {
  const h: Record<string, string> = { ...headers };
  if (!h['Accept-Language']) h['Accept-Language'] = langTag();
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

  if (res.status === 401 && auth && typeof window !== 'undefined') {
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
// One canonical endpoint: /catalog/brands?kind=giftcard|mobile_recharge|esim
export const listGiftCards = (country?: string, test?: boolean) => api(catQS('giftcard', country, test));
export const listTopups = (country?: string, test?: boolean) => api(catQS('mobile_recharge', country, test));
export const listEsims = (country?: string, test?: boolean) => api(catQS('esim', country, test));
export const searchProducts = (q: string, country?: string) => api(`/catalog/search?q=${encodeURIComponent(q)}${country ? '&country=' + encodeURIComponent(country) : ''}`);

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
export const createQuoteBatch = (items: unknown[], email?: string, key?: string, cashbackCode?: string, paymentMethod?: string, cashbackDest?: string, anonymous?: boolean) =>
  createCheckout('/quotes/batch', {
    items,
    email,
    cashback_code: cashbackCode || '',
    payment_method: paymentMethod || 'nimiq_pay',
    cashback_destination: cashbackDest || 'cashback',
    anonymous: !!anonymous,
  }, key);

/* ---------------- Site config (feature flags) ---------------- */
export const getSiteConfig = () => api('/site-config');

/* ---------------- Tree planting ---------------- */
export const getTrees = (bucket: 'week'|'month'|'all'|string = 'all') => api('/trees?bucket=' + bucket);
export const getMyTrees = () => api('/trees/me', { auth: true });
export const setTreePrefs = (destination: 'cashback'|'trees') =>
  api('/trees/me/prefs', { method: 'POST', auth: true, body: { destination } });
export const adminListTreeSettlements = () => api('/admin/trees/settlements');
export const adminRecordTreeSettlement = (body: {
  month_bucket?: string; amount_usdt?: number; amount_label?: string; amount_value?: string; trees_planted?: number; tx_hash?: string; transaction_url?: string;
  from_address?: string; to_address?: string; note?: string;
  status?: 'paid'|'skipped'; amount_nim?: number; wallet_nim?: number; proof_image?: string; proof_images?: { data: string; caption?: string }[];
}) => api('/admin/trees/settlements', { method: 'POST', body });
export const adminUpdateTreeSettlement = (id: string, body: Parameters<typeof adminRecordTreeSettlement>[0]) => api('/admin/trees/settlements/' + encodeURIComponent(id), { method: 'PUT', body });
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

export const getProductPrice = (brand: string, country: string, value: number) =>
  api('/catalog/price?' + new URLSearchParams({ brand_name: brand, country_code: country, face_value: String(value) }));

/* ---------------- Wallet notification prefs ---------------- */
export const getNotificationPrefs = () => api('/account/notifications', { auth: true });
export const setNotificationPrefs = (enabled: boolean) =>
  api('/account/notifications', { method: 'PUT', body: { enabled: !!enabled }, auth: true });

/* ---------------- Orders ---------------- */
export const listOrders = () => api('/orders', { auth: true });
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
export const getGeo = () => api('/geo', { timeoutMs: 10000 });
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
export const refreshPoolStake = () => api('/poolstake/refresh', { method: 'POST', auth: true });

/* ---------------- FX / NIM rate (sessionStorage cached) ---------------- */
const FX_CACHE_KEY = 'nim_fx';
const FX_CACHE_TTL_MS = 5 * 60 * 1000;
const NIM_CACHE_KEY = 'nim_market';
const NIM_CACHE_TTL_MS = 5 * 60 * 1000;

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
// TEST MODE: the customer's simulated "pay" button — drives the quote through
// the real state machine without any on-chain payment.
export const payQuoteSimulated = (id: string, action = 'auto') =>
  api(`/quotes/${encodeURIComponent(id)}/test-pay`, { method: 'POST', body: { action }, auth: true, timeoutMs: 30000 });
// Operator sandbox: buy a real product on the simulated supplier, fake-pay it
// through the real state machine, and poll the result.
export const adminTestPurchase = (req: Record<string, unknown>) =>
  api('/admin/test-purchase', { method: 'POST', body: req, timeoutMs: 30000 });
export const adminTestPay = (req: Record<string, unknown>) =>
  api('/admin/test-pay', { method: 'POST', body: req, timeoutMs: 30000 });
export const adminTestQuote = (id: string) => api(`/admin/test-quote/${encodeURIComponent(id)}`, { timeoutMs: 15000 });
export const adminCatalogRules = (opts: { path?: string; method?: string; body?: unknown } = {}) =>
  api('/admin/catalog-rules' + (opts.path || ''), opts.method ? { method: opts.method, body: opts.body } : {});
export const adminGetCashback = () => api('/admin/settings/cashback');
/** Headline counters: Players + the cashback payment queue. */
export const adminDashboard = () => api('/admin/dashboard');
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
