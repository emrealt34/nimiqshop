/* api.js — typed fetch client for the nimiqshop Go backend.
 * All money values travel as strings/ints exactly as the backend sends them.
 * FIXED: supports separate frontend/backend domains via config.js API_BASE
 * and handles real CryptoRefills response shapes.
 */
import { CFG, uuid } from './util.js';

export class ApiError extends Error {
  constructor(status, message, code) {
    super(message || `Request failed (${status})`);
    this.status = status;
    this.code = code;
  }
}

let sessionGetter = () => null;
export function _setSessionGetter(fn) { sessionGetter = fn; }

/* ---------------- client-side GET micro-cache (anti-hammer) ----------------
 * The SAME user (or fifty tabs of them) re-requesting the same catalog data
 * in a burst must not turn into N network calls. Every cacheable GET is:
 *   1. served from this in-page cache while fresh (TTL per endpoint class),
 *   2. deduplicated while an identical request is in flight (one fetch, N
 *      awaiting callers),
 *   3. served stale (up to 10 minutes old) when the network itself fails —
 *      a flaky connection can no longer blank the storefront.
 * Only public, read-only endpoints are cached; anything auth'd, mutating,
 * or user-specific (quotes, orders, admin) always goes to the network.
 */
const _getCache = new Map();   // path -> { data, at, ttl }
const _inflight = new Map();   // path -> Promise
const STALE_MAX = 10 * 60 * 1000;

function cacheTtlFor(path) {
  if (path.startsWith('/catalog/brands') || path.startsWith('/catalog/giftcards') ||
      path.startsWith('/catalog/topups') || path.startsWith('/catalog/esims')) return 5 * 60 * 1000;
  if (path.startsWith('/catalog/products')) return 5 * 60 * 1000; // SPEED: products rarely change; in-page cache 2→5 min
  if (path.startsWith('/catalog/price')) return 60 * 1000;
  if (path.startsWith('/catalog/payment-vias')) return 10 * 60 * 1000;
  if (path.startsWith('/catalog/search')) return 60 * 1000;
  if (path.startsWith('/catalog/check-phone')) return 30 * 1000;
  if (path.startsWith('/market/')) return 30 * 1000;
  if (path.startsWith('/activity')) return 10 * 1000;
  if (path.startsWith('/ratings/')) return 60 * 1000;
  if (path.startsWith('/track/')) return 5 * 1000;
  if (path.startsWith('/geo')) return 60 * 1000;
  return 0; // not cacheable
}

async function _fetchGet(path, headers, timeoutMs) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  let res;
  try {
    const base = (CFG.API_BASE || '/api').replace(/\/$/, '');
    res = await fetch(base + path, {
      method: 'GET',
      headers,
      signal: ctrl.signal,
      credentials: base.startsWith('/') ? 'same-origin' : 'include',
      cache: 'no-store',
    });
  } catch (e) {
    clearTimeout(timer);
    if (e.name === 'AbortError') throw new ApiError(0, 'The server took too long to respond. Check your connection and try again.');
    throw new ApiError(0, 'Cannot reach the nim.shop API at ' + (CFG.API_BASE || '/api') + '. Is the backend running and ALLOWED_ORIGINS set?');
  }
  clearTimeout(timer);

  let data = {};
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch { /* non-JSON error body */ }
  }
  if (!res.ok) {
    throw new ApiError(res.status, data.error || data.message || data.detail || `Request failed (${res.status})`);
  }
  return data;
}

async function cachedGet(path, { headers = {}, timeoutMs = 15000 } = {}) {
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
    .catch((e) => {
      // Network unreachable / timeout: serve stale instead of a blank page.
      if ((e instanceof ApiError && e.status === 0) && hit && now - hit.at < ttl + STALE_MAX) {
        return hit.data;
      }
      throw e;
    })
    .finally(() => _inflight.delete(path));
  _inflight.set(path, p);
  return p;
}

export async function api(path, { method = 'GET', body, auth = false, headers = {}, timeoutMs = 15000 } = {}) {
  const h = { ...headers };
  let payload;
  if (body !== undefined) {
    h['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  if (auth) {
    const token = sessionGetter();
    if (!token) throw new ApiError(401, 'Not signed in — connect your wallet first.');
    h['Authorization'] = `Bearer ${token}`;
  }

  if (method === 'GET' && !auth) {
    return cachedGet(path, { headers: h, timeoutMs });
  }

  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  let res;
  try {
    // CFG.API_BASE can be '/api' (same-origin) or 'https://api.nim.shop/api' (separate domains)
    const base = (CFG.API_BASE || '/api').replace(/\/$/, '');
    res = await fetch(base + path, {
      method,
      headers: h,
      body: payload,
      signal: ctrl.signal,
      credentials: base.startsWith('/') ? 'same-origin' : 'include',
      cache: 'no-store',
    });
  } catch (e) {
    clearTimeout(timer);
    if (e.name === 'AbortError') throw new ApiError(0, 'The server took too long to respond. Check your connection and try again.');
    throw new ApiError(0, 'Cannot reach the nim.shop API at ' + (CFG.API_BASE || '/api') + '. Is the backend running and ALLOWED_ORIGINS set?');
  }
  clearTimeout(timer);

  let data = {};
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch { /* non-JSON error body */ }
  }

  if (!res.ok) {
    if (res.status === 401 && auth) {
      window.dispatchEvent(new CustomEvent('nimshop:unauthorized'));
    }
    throw new ApiError(res.status, data.error || data.message || data.detail || `Request failed (${res.status})`);
  }
  return data;
}

/* ---------------- Auth ---------------- */

export const authChallenge = () => api('/auth/challenge', { method: 'POST' });
export const hubLogin = (req) => api('/auth/hub-login', { method: 'POST', body: req });

/* ---------------- Catalog — FIXED FOR REAL SHAPES ---------------- */

const catQS = (country, test) => { const p = new URLSearchParams(); if (country) p.set('country', country); if (test) p.set('test', '1'); const s = p.toString(); return s ? '?' + s : ''; };
export const listGiftCards = (country, test) => api('/catalog/giftcards' + catQS(country, test));
export const listTopups = (country, test) => api('/catalog/topups' + catQS(country, test));
export const listEsims = (country, test) => api('/catalog/esims' + catQS(country, test));
export const searchProducts = (q) => api(`/catalog/search?q=${encodeURIComponent(q)}`);

// getProduct — country is always required.
// SPEED: same session-cache mechanism as getNimRate/getFXRates — fresh for
// 5 minutes, stale-served when the backend is slow/unreachable, so revisits,
// back-navigation and reloads render instantly instead of re-hitting the
// (tunneled) supplier chain. The hover-prefetch on shop cards fills this
// cache BEFORE the visitor clicks.
// coin: REMOVED from the client — the backend forces the payment coin
// (BTC / Lightning) server-side on every public catalog call.
const PROD_CACHE_PREFIX = 'nim_prod:';
const PROD_CACHE_TTL_MS = 5 * 60 * 1000;
export const getProduct = async (id, country, opts = {}) => {
  let path = `/catalog/products/${encodeURIComponent(id)}`;
  const qs = new URLSearchParams();
  if (country) qs.set('country', country);
  const s = qs.toString();
  const fullPath = path + (s ? '?' + s : '');
  const key = PROD_CACHE_PREFIX + fullPath;
  const now = Date.now();
  if (!opts.force) {
    try {
      const raw = sessionStorage.getItem(key);
      if (raw) {
        const j = JSON.parse(raw);
        if (j && j.fetched_at && (now - j.fetched_at) < PROD_CACHE_TTL_MS) return j.data;
      }
    } catch { /* corrupt cache, ignore */ }
  }
  try {
    const data = await api(fullPath);
    try {
      // keep only this entry — product payloads are big, don't blow the quota
      for (let i = sessionStorage.length - 1; i >= 0; i--) {
        const k = sessionStorage.key(i);
        if (k && k.startsWith(PROD_CACHE_PREFIX) && k !== key) sessionStorage.removeItem(k);
      }
      sessionStorage.setItem(key, JSON.stringify({ data, fetched_at: now }));
    } catch { /* quota full — cache is best-effort */ }
    return data;
  } catch (err) {
    // Backend slow/unreachable — serve the last known product instead of a blank page.
    try {
      const raw = sessionStorage.getItem(key);
      if (raw) return JSON.parse(raw).data;
    } catch {}
    throw err;
  }
};

// check-phone normalizes the number server-side (separators, 00 access
// prefix, national 0-prefix format using the product's country) and
// returns the strict E.164 value in `phone_number` — always send THAT.
export const checkPhone = (phoneNumber, country) => api(`/catalog/check-phone?phone_number=${encodeURIComponent(phoneNumber)}` + (country ? `&country=${encodeURIComponent(country)}` : ''));

/* ---------------- Quotes ---------------- */

// Purchase-side anti-double-order guard. Each click used to carry a FRESH
// idempotency key, so a double-click (or a retry storm) created one
// supplier order PER CLICK — a buyer reported "1 order became 3". Two
// client layers now prevent that:
//   1. identical in-flight createQuote calls share ONE promise (no second
//      request while the first is still going),
//   2. a quote created <90s ago for the EXACT same cart is returned from
//      cache instead of creating another supplier order.
// Deliberate re-purchases stay possible: the guard is short-window and
// payload-exact, and the backend runs the same guard as the last line of
// defense.
const _quoteInflight = new Map();
const _quoteRecent = new Map();
const QUOTE_DEDUPE_MS = 90 * 1000;

function cartFingerprint(req) {
  const s = JSON.stringify(req);
  let h = 5381;
  for (let i = 0; i < s.length; i++) { h = ((h * 33) ^ s.charCodeAt(i)) >>> 0; }
  return h.toString(36);
}

export const createQuote = (req, idempotencyKey) => {
  const fp = cartFingerprint(req);
  const now = Date.now();
  const recent = _quoteRecent.get(fp);
  if (recent && now - recent.at < QUOTE_DEDUPE_MS) return Promise.resolve(recent.quote);
  if (_quoteInflight.has(fp)) return _quoteInflight.get(fp);

  const p = api('/quotes', {
    method: 'POST',
    body: req,
    auth: true,
    headers: { 'Idempotency-Key': idempotencyKey || uuid() },
  }).then((quote) => {
    _quoteRecent.set(fp, { quote, at: Date.now() });
    return quote;
  }).finally(() => _quoteInflight.delete(fp));
  _quoteInflight.set(fp, p);
  return p;
};
export const listQuotes = () => api('/quotes', { auth: true });
export const getQuote = (id) => api(`/quotes/${encodeURIComponent(id)}`, { auth: true });

/* ---------------- Orders ---------------- */

export const listOrders = () => api('/orders', { auth: true });

// Curated USD-per-unit fiat table served by the backend — the SAME rates
// its price-cap filter uses, so frontend NIM estimates match admin rules.
export const getFXRates = async (opts = {}) => {
  // FX CACHE: same mechanism as getNimRate below — sessionStorage with a
  // 5-minute TTL, stale-serve on backend failure. Every product/buy/orders
  // page reads usd_per_unit; without this each mount re-hit /market/fx.
  const now = Date.now();
  if (!opts.force) {
    try {
      const raw = sessionStorage.getItem(FX_CACHE_KEY);
      if (raw) {
        const j = JSON.parse(raw);
        if (j && j.usd_per_unit && j.fetched_at && (now - j.fetched_at) < FX_CACHE_TTL_MS) {
          return j;
        }
      }
    } catch { /* corrupt cache, ignore */ }
  }
  try {
    const fresh = await api('/market/fx');
    if (fresh && fresh.usd_per_unit) {
      try { sessionStorage.setItem(FX_CACHE_KEY, JSON.stringify({ ...fresh, fetched_at: now })); } catch {}
    }
    return fresh;
  } catch (err) {
    // Backend unreachable — return the stale cache if we have one.
    try {
      const raw = sessionStorage.getItem(FX_CACHE_KEY);
      if (raw) return JSON.parse(raw);
    } catch {}
    throw err;
  }
};
export const getOrder = (id) => api(`/orders/${encodeURIComponent(id)}`, { auth: true });
export const refreshOrder = (id) => api(`/orders/${encodeURIComponent(id)}/refresh`, { method: 'POST', auth: true });
export const getOrderSupport = (id) => api(`/orders/${encodeURIComponent(id)}/support`, { auth: true });

/* ---------------- Support ---------------- */

export const createTicket = (req) => api('/support/tickets', { method: 'POST', body: req, auth: true });
export const listTickets = () => api('/support/tickets', { auth: true });
export const getTicket = (id) => api(`/support/tickets/${encodeURIComponent(id)}`, { auth: true });
export const replyTicket = (ticketId, message) => api(`/support/tickets/${encodeURIComponent(ticketId)}/messages`, {
  method: 'POST',
  body: { message },
  auth: true,
});

/* ---------------- Health ---------------- */
export const getGeo = () => api('/geo', { timeoutMs: 10000 });

// getNimRate returns NIM/BTC/USD from the oracle. The value is also written
// to sessionStorage (TTL 5 minutes) so every product/buy/orders page can
// read it synchronously without re-hitting the backend. Cached reads are
// free — no network, no spinner, no timeout. A stale cache (older than 5
// minutes) still serves the last value so the UI never goes blank; in that
// case a background refresh is triggered so the next call is fresh.
const FX_CACHE_KEY = 'nim_fx';
const FX_CACHE_TTL_MS = 5 * 60 * 1000;
const NIM_CACHE_KEY = 'nim_market';
const NIM_CACHE_TTL_MS = 5 * 60 * 1000;
export const getNimRate = async (opts = {}) => {
  const now = Date.now();
  if (!opts.force) {
    try {
      const raw = sessionStorage.getItem(NIM_CACHE_KEY);
      if (raw) {
        const j = JSON.parse(raw);
        if (j && j.usd_per_nim && j.fetched_at && (now - j.fetched_at) < NIM_CACHE_TTL_MS) {
          return j;
        }
      }
    } catch { /* corrupt cache, ignore */ }
  }
  try {
    const fresh = await api('/market/nim-rate', { timeoutMs: opts.timeoutMs || 8000 });
    if (fresh && fresh.usd_per_nim) {
      try { sessionStorage.setItem(NIM_CACHE_KEY, JSON.stringify({ ...fresh, fetched_at: now })); } catch {}
    }
    return fresh;
  } catch (err) {
    // Backend unreachable — return the stale cache if we have one.
    try {
      const raw = sessionStorage.getItem(NIM_CACHE_KEY);
      if (raw) return JSON.parse(raw);
    } catch {}
    throw err;
  }
};
// Sync access to the cached NIM/BTC rate for inline rendering.
export const cachedNimRate = () => {
  try {
    const raw = sessionStorage.getItem(NIM_CACHE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch { return null; }
};
// Sync access to the cached FX table ({usd_per_unit: {USD:1, TRY:0.03…}}).
// cart.js used to read the same sessionStorage key with its own copy of the
// key name — now the key lives here and nowhere else.
export const cachedFX = () => {
  try {
    const raw = sessionStorage.getItem(FX_CACHE_KEY);
    if (!raw) return null;
    const j = JSON.parse(raw);
    return j && j.usd_per_unit ? j.usd_per_unit : null;
  } catch { return null; }
};

/* ---------------- Public activity ---------------- */
export const getActivity = (limit = 50) => api(`/activity?limit=${limit}`);
export const trackOrder = (id) => api(`/track/${encodeURIComponent(id)}`);
export const rateOrder = (id, rating) => api(`/orders/${encodeURIComponent(id)}/rate`, { method: 'POST', body: { rating }, auth: true });
export const rateQuote = (id, rating) => api(`/quotes/${encodeURIComponent(id)}/rate`, { method: 'POST', body: { rating }, auth: true });
export const getAccountLimits = () => api('/account/limits', { auth: true });
export const startEmailVerification = (email) => api('/account/email/start', { method: 'POST', body: { email }, auth: true });
export const verifyEmail = (email, code) => api('/account/email/verify', { method: 'POST', body: { email, code }, auth: true });
/* ---------------- admin (operator) endpoints ---------------- */
export const adminLogin = (req) => api('/admin/auth/login', { method: 'POST', body: req });
export const adminLogout = () => api('/admin/auth/logout', { method: 'POST' });
export const adminStatus = () => api('/admin/notification/status');
export const adminMe = () => api('/admin/auth/me');
export const adminSend = (req) => api('/admin/notification/send', { method: 'POST', body: req });
export const adminCatalogRules = (opts = {}) => api('/admin/catalog-rules' + (opts.path || ''), opts.method ? { method: opts.method, body: opts.body } : {});
