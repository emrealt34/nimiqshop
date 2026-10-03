import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { onRequest } from '../functions/api/[[path]].js';

const shop = 'https://shop.nimiqbase.com';
function req(path, init) { return new Request(shop + path, init); }

// Test-only host check. Parsed rather than substring-matched so a lookalike
// host (shopapi.nimiqbase.com.evil.test) can never be mistaken for the real
// API — and CodeQL's js/incomplete-url-substring-sanitization stays quiet.
function isShopApiUrl(u) {
  try {
    return new URL(u).hostname === 'shopapi.nimiqbase.com';
  } catch {
    return false;
  }
}

test('browser defaults and Pages routing keep assets static', () => {
  const context = { window: {} };
  vm.runInNewContext(readFileSync('public/config.js', 'utf8'), context);
  assert.equal(context.window.APP_CONFIG.API_BASE, '/api');
  assert.deepEqual(JSON.parse(readFileSync('public/_routes.json', 'utf8')),
    { version: 1, include: ['/api/*'], exclude: [] });
  assert.ok(!readFileSync('src/layouts/Base.astro', 'utf8').includes('as="script" href={`${assetBase}/vendor/HubApi.umd.js`}'));
});

test('fixed upstream, query, auth, cookies and no-cache', async t => {
  t.mock.method(globalThis, 'fetch', async (url, init) => {
    assert.equal(url, 'https://shopapi.nimiqbase.com/api/catalog/brands?kind=giftcard&country=TR&url=https://evil.example');
    assert.equal(init.redirect, 'manual');
    assert.equal(init.headers.get('Cookie'), 'session=abc');
    assert.equal(init.headers.get('Authorization'), 'Bearer example');
    assert.equal(init.headers.get('x-nimshop-proxy-secret'), null);
    assert.equal(init.headers.get('x-forwarded-for'), null);
    const headers = new Headers({ 'Cache-Control': 'public, max-age=999', 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Credentials': 'true', 'Access-Control-Allow-Methods': 'GET, POST' });
    headers.append('Set-Cookie', 'session=xyz; Domain=shopapi.nimiqbase.com; Path=/api; Secure; HttpOnly; SameSite=Lax');
    headers.append('Set-Cookie', 'other=1; Expires=Wed, 21 Oct 2037 07:28:00 GMT; Path=/');
    return new Response('{"ok":true}', { headers });
  });
  const response = await onRequest({ request: req('/api/catalog/brands?kind=giftcard&country=TR&url=https://evil.example', {
    headers: { Cookie: 'session=abc', Authorization: 'Bearer example', 'x-nimshop-proxy-secret': 'spoof', 'x-forwarded-for': 'spoof' },
  }) });
  assert.equal(response.headers.get('Cache-Control'), 'no-store');
  assert.equal(response.headers.get('Access-Control-Allow-Origin'), null);
  assert.equal(response.headers.get('Access-Control-Allow-Credentials'), null);
  assert.equal(response.headers.get('Access-Control-Allow-Methods'), null);
  const cookies = response.headers.getSetCookie();
  assert.equal(cookies.length, 2);
  assert.ok(!cookies[0].includes('Domain='));
  assert.match(cookies[0], /Secure; HttpOnly; SameSite=Lax/);
  assert.match(cookies[1], /Wed, 21 Oct/);
  assert.deepEqual(await response.json(), { ok: true });
});

test('POST body and original Origin preserved, not retried', async t => {
  let calls = 0;
  t.mock.method(globalThis, 'fetch', async (_url, init) => {
    calls++;
    assert.equal(init.method, 'POST');
    assert.equal(init.headers.get('Origin'), shop);
    assert.equal(await new Response(init.body).text(), '{"active":true}');
    return new Response(null, { status: 204 });
  });
  const response = await onRequest({ request: req('/api/presence', {
    method: 'POST', headers: { Origin: shop, 'Content-Type': 'application/json' }, body: '{"active":true}',
  }) });
  assert.equal(response.status, 204);
  assert.equal(calls, 1);
});

test('cross-origin writes rejected before fetch', async t => {
  t.mock.method(globalThis, 'fetch', () => assert.fail('must not fetch'));
  const result = await onRequest({ request: req('/api/presence', { method: 'POST', headers: { Origin: 'https://evil.example' } }) });
  assert.equal(result.status, 403);
});

test('preflight forwarded and auth failures preserved', async t => {
  t.mock.method(globalThis, 'fetch', async (_url, init) => {
    assert.equal(init.method, 'OPTIONS');
    return new Response(null, { status: 204 });
  });
  assert.equal((await onRequest({ request: req('/api/presence', { method: 'OPTIONS' }) })).status, 204);
  globalThis.fetch = async () => Response.json({ error: 'unauthorized' }, { status: 401 });
  assert.equal((await onRequest({ request: req('/api/auth/session') })).status, 401);
});

test('upstream redirects stay same-origin without being followed', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response(null, {
    status: 302, headers: { Location: 'https://shopapi.nimiqbase.com/api/auth/session?x=1' },
  }));
  const result = await onRequest({ request: req('/api/auth/challenge') });
  assert.equal(result.headers.get('Location'), shop + '/api/auth/session?x=1');
});

test('502 and network errors remain honest JSON failures, never success', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response('Cloudflare error', { status: 502 }));
  for (const networkError of [false, true]) {
    if (networkError) globalThis.fetch = async () => { throw new Error('offline'); };
    const result = await onRequest({ request: req('/api/market/fx') });
    assert.equal(result.status, 502);
    assert.equal(result.headers.get('Cache-Control'), 'no-store');
    assert.equal((await result.json()).code, 'UPSTREAM_UNAVAILABLE');
  }
});

test('backend down: catalog price falls back to CryptoRefills directly', async t => {
  let crUrl = '';
  t.mock.method(globalThis, 'fetch', async (url, _init) => {
    const u = String(url);
    if (isShopApiUrl(u)) return new Response('down', { status: 502 });
    crUrl = u;
    return new Response(JSON.stringify({ coin_amount: '0.00012345', coin: 'BTC', product_id: 'p1' }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    });
  });
  const result = await onRequest({ request: req('/api/catalog/price?brand_name=Amazon.com.tr&country_code=TR&face_value=3593') });
  assert.equal(result.status, 200);
  assert.equal(result.headers.get('X-Catalog-Source'), 'cryptorefills-direct');
  assert.ok(crUrl.includes('/v4/products/price'), 'asked the supplier price endpoint');
  assert.ok(crUrl.includes('coin=BTC') && crUrl.includes('country_code=TR'));
  const body = await result.json();
  assert.equal(body.coin_amount, '0.00012345');
  assert.ok(body.price_expires_at > body.price_checked_at, 'fresh 15s expiry stamped');
});

test('backend down: bad supplier price is not served as a quote', async t => {
  t.mock.method(globalThis, 'fetch', async (url) => {
    const u = String(url);
    if (isShopApiUrl(u)) return new Response('down', { status: 502 });
    return new Response(JSON.stringify({ coin_amount: '-1', coin: 'BTC' }), { status: 200 });
  });
  const result = await onRequest({ request: req('/api/catalog/price?brand_name=X&country_code=TR&face_value=10') });
  assert.equal(result.status, 502);
  assert.equal((await result.json()).code, 'UPSTREAM_UNAVAILABLE');
});

test('non API paths cannot be proxied', async () => {
  assert.equal((await onRequest({ request: req('/_assets/client.js') })).status, 404);
});
