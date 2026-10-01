// Pages executes only /api/*; Astro HTML and assets remain static.
// Fixed upstream: never accept a destination URL from the browser.
const UPSTREAM = 'https://shopapi.nimiqbase.com';
const HOP = ['connection', 'keep-alive', 'proxy-authenticate',
  'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade'];

function uncachedHeaders(input) {
  const headers = new Headers(input);
  for (const name of HOP) headers.delete(name);
  headers.set('Cache-Control', 'no-store');
  headers.set('CDN-Cache-Control', 'no-store');
  headers.set('Cloudflare-CDN-Cache-Control', 'no-store');
  headers.set('X-Content-Type-Options', 'nosniff');
  return headers;
}
// CryptoRefills-direct fallback for ONE endpoint: the product family
// payload (GET /api/catalog/products/{family}). Browsing brands already
// comes from the static hourly snapshot (public/data/catalog), but the
// family payload is per country+language and too big to pre-bake for the
// whole matrix — so when the Go backend is down (502/5xx/unreachable) this
// proxy asks the supplier itself with the partner headers (the same public
// X-Cr-Application auth backend/.env.example documents) and edge-caches the
// answer for two minutes, matching the backend's own Cache-Control. Money
// paths (price, orders, sessions) deliberately stay backend-only.
const CR_BASE = 'https://api.cryptorefills.com';
const CR_HEADERS = {
  'X-Cr-Application': 'YQyw0dJ0FM',
  'X-Cr-Version': 'nimshop/1.0',
  'User-Agent': 'nimshop/1.0 +https://shop.nimiqbase.com',
  Accept: 'application/json',
};
async function crFamilyReply(incoming) {
  const m = incoming.pathname.match(/^\/api\/catalog\/products\/([^/]+)$/);
  if (!m || !['GET', 'HEAD'].includes(incoming.searchParams ? 'GET' : 'GET')) return null;
  const family = decodeURIComponent(m[1]);
  const cc = (String(incoming.searchParams.get('country') || 'TR').toUpperCase().slice(0, 2)) || 'TR';
  const lang = (String(incoming.searchParams.get('lang') || 'en').toLowerCase().slice(0, 5)) || 'en';
  const url = `${CR_BASE}/v5/products/country/${encodeURIComponent(cc)}?family_name=${encodeURIComponent(family)}&lang=${encodeURIComponent(lang)}`;
  try {
    const r = await fetch(url, { headers: CR_HEADERS, signal: AbortSignal.timeout(20_000), cf: { cacheTtl: 120 } });
    if (!r.ok) { await r.body?.cancel(); return null; }
    const body = await r.text();
    return new Response(body, {
      status: 200,
      headers: {
        'Content-Type': 'application/json',
        'Cache-Control': 'public, max-age=120',
        'CDN-Cache-Control': 'public, max-age=120',
        'Cloudflare-CDN-Cache-Control': 'public, max-age=120',
        Vary: 'Accept-Language',
        'X-Content-Type-Options': 'nosniff',
        'X-Catalog-Source': 'cryptorefills-direct',
      },
    });
  } catch {
    return null;
  }
}

function unavailable(status = 502) {
  return Response.json({
    error: 'The shop API is temporarily unavailable. Check your existing order before another payment.',
    code: 'UPSTREAM_UNAVAILABLE',
  }, { status, headers: uncachedHeaders() });
}

export async function onRequest({ request }) {
  const incoming = new URL(request.url);
  if (!incoming.pathname.startsWith('/api/')) {
    return new Response('Not found', { status: 404 });
  }
  // Do not weaken CSRF protection: preserve Origin for Go's allowlist and
  // reject cross-origin writes here as well. Non-browser clients may omit it.
  const origin = request.headers.get('Origin');
  if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method) && origin && origin !== incoming.origin) {
    return Response.json({ error: 'Forbidden origin' }, {
      status: 403, headers: uncachedHeaders(),
    });
  }
  const target = new URL(UPSTREAM);
  target.pathname = incoming.pathname;
  target.search = incoming.search;
  const headers = new Headers(request.headers);
  const connectionTokens = (headers.get('Connection') || '').split(',');
  for (const name of [...HOP, ...connectionTokens.map(s => s.trim()).filter(Boolean),
    'host', 'forwarded', 'x-forwarded-for', 'x-real-ip', 'true-client-ip',
    'x-nimshop-proxy-secret', 'x-nimshop-client-ip', 'x-nimshop-client-country']) {
    headers.delete(name);
  }
  // CF-managed visitor headers are left to Cloudflare; never manufacture
  // trusted client identity or private-hop credentials from browser input.
  let upstream;
  try {
    upstream = await fetch(target.href, {
      method: request.method,
      headers,
      body: ['GET', 'HEAD'].includes(request.method) ? undefined : request.body,
      redirect: 'manual', // never leak session/auth headers by following redirects
      signal: AbortSignal.timeout(90_000),
      cf: { cacheTtl: 0, cacheEverything: false },
    });
  } catch {
    const fb = await crFamilyReply(incoming);
    if (fb) return fb;
    return unavailable();
  }
  if (upstream.status >= 500) {
    await upstream.body?.cancel();
    const fb = await crFamilyReply(incoming);
    if (fb) return fb;
    return unavailable(upstream.status);
  }
  const reply = uncachedHeaders(upstream.headers);
  // CORS is unnecessary on this same-origin endpoint. Don't expose an
  // upstream wildcard policy on a credentialed API.
  const corsHeaders = Array.from(reply.keys()).filter(name => name.startsWith('access-control-'));
  for (const name of corsHeaders) reply.delete(name);
  // A host-only cookie naturally belongs to the shop. Explicit upstream
  // domains must become host-only too. Preserve HttpOnly/Secure/SameSite,
  // expiry and independent Set-Cookie fields (never split on commas).
  const cookies = upstream.headers.getSetCookie();
  reply.delete('Set-Cookie');
  for (const cookie of cookies) {
    reply.append('Set-Cookie', cookie.replace(/;\s*Domain\s*=\s*[^;]*/gi, ''));
  }
  const location = reply.get('Location');
  if (location) {
    const redirect = new URL(location, target);
    if (redirect.origin === UPSTREAM && redirect.pathname.startsWith('/api/')) {
      reply.set('Location', incoming.origin + redirect.pathname + redirect.search + redirect.hash);
    }
  }
  return new Response(upstream.body, {
    status: upstream.status, statusText: upstream.statusText, headers: reply,
  });
}
