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
    return unavailable();
  }
  if (upstream.status >= 500) {
    await upstream.body?.cancel();
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
