/**
 * public-url.mjs — ONE public URL, wired into every layer.
 *
 * THE BUG THIS FIXES: the launcher started a Cloudflare tunnel and printed its
 * URL, then threw it away. Nothing was ever told about it, so
 *   • the browser kept calling public/config.js's hard-coded
 *     API_BASE = https://shopapi.nimiqbase.com/api  → the PRODUCTION backend,
 *     never the Go process running behind the tunnel;
 *   • SITE_HOST / FRONTEND_URL / APP_NAME kept saying shop.nimiqbase.com in
 *     titles, the footer, the Hub appName and share links;
 *   • the Go process started with ALLOWED_ORIGINS from .env (production), so a
 *     cross-origin call from the tunnel host was rejected by CORS, and
 *     PUBLIC_WEBHOOK_BASE_URL pointed nowhere the supplier could reach.
 *
 * Now the launcher resolves ONE public URL (the quick tunnel's, or PUBLIC_URL
 * for a named tunnel, or the local origin as a last resort) and derives every
 * layer from it:
 *
 *   browser  →  API_BASE '/api'            (same origin — no CORS at all)
 *   Node hop →  /api/* → Go (private hop)  (see proxy.mjs)
 *   Go       →  SITE_HOST, ALLOWED_ORIGINS, PUBLIC_WEBHOOK_BASE_URL
 *
 * A named tunnel with no PUBLIC_URL is the one case with no known host: then
 * only API_BASE is rewritten (it is request-relative, so correct anywhere) and
 * the backend env is left alone, rather than inventing a SITE_HOST.
 *
 * public/config.js is NEVER edited: it stays the deployment default. The
 * generated file lives in devtools/.runtime/ and is served over /config.js by
 * static-server.mjs (preview/tunnel) and the astro dev middleware (dev). It
 * pre-seeds window.APP_CONFIG and then includes the real config.js verbatim, so
 * the file's own `Object.assign(defaults, window.APP_CONFIG)` keeps our values
 * and any key added to config.js later still works.
 */
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const root = fileURLToPath(new URL('..', import.meta.url));
export const RUNTIME_DIR = join(root, 'devtools', '.runtime');
const STATE_FILE = join(RUNTIME_DIR, 'public-url.json');
const CONFIG_FILE = join(RUNTIME_DIR, 'config.js');

/** Kill switch: NIMSHOP_NO_URL_OVERRIDE=1 keeps public/config.js untouched. */
export function overrideDisabled(env = process.env) {
  return ['1', 'true', 'yes', 'on'].includes(String(env.NIMSHOP_NO_URL_OVERRIDE || '').trim().toLowerCase());
}

/**
 * 'https://abc-123.trycloudflare.com/' → {url, host, hostname}
 *
 * `host` keeps the port (display), `hostname` never does — the backend
 * validates SITE_HOST with strings.ContainsAny(host, " :/?#") and REFUSES TO
 * START on a colon, so SITE_HOST must always be the bare hostname.
 */
export function normalizePublicUrl(raw) {
  const text = String(raw || '').trim().replace(/\/+$/, '');
  if (!text) throw new Error('public URL is empty');
  let u;
  try {
    u = new URL(text);
  } catch {
    throw new Error(`public URL is not absolute: ${text}`);
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') throw new Error(`public URL must be http(s): ${text}`);
  if (!u.hostname) throw new Error(`public URL has no hostname: ${text}`);
  return { url: u.origin, origin: u.origin, host: u.host, hostname: u.hostname };
}

/**
 * cloudflared prints the quick tunnel URL inside a banner on stderr. Take the
 * first match; later lines repeat it.
 *
 * NEVER accept api.trycloudflare.com: that host is the quick-tunnel
 * REGISTRATION API and appears inside cloudflared's own FAILURE line
 * (`failed to request quick Tunnel: Post "https://api.trycloudflare.com/tunnel": …`).
 * Matching it once wired the whole stack — /config.js, SITE_HOST,
 * ALLOWED_ORIGINS, PUBLIC_WEBHOOK_BASE_URL — to a URL that is not a tunnel,
 * and the banner told you to open a page that can never load.
 */
export function parseTunnelUrl(text) {
  const re = /https:\/\/([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)\.trycloudflare\.com/ig;
  for (const m of String(text || '').matchAll(re)) {
    if (m[1].toLowerCase() === 'api') continue; // the registration API from error lines, never a tunnel
    return m[0];
  }
  return '';
}

/* ------------------------------------------------------------- fetch errors */

const FETCH_ERROR_HINTS = {
  ENOTFOUND: 'DNS could not resolve the hostname (ISP resolver, DNS filter, or the record has not propagated yet)',
  EAI_AGAIN: 'temporary DNS failure — the resolver did not answer',
  ECONNREFUSED: 'something answered and refused the connection — the origin is not listening',
  ECONNRESET: 'the connection was reset mid-flight (firewall/DPI or an unstable path)',
  ETIMEDOUT: 'the connection timed out (blocked outbound 443, or a very slow path)',
  UND_ERR_CONNECT_TIMEOUT: 'could not establish the connection in time (blocked outbound 443, or a very slow path)',
  UND_ERR_HEADERS_TIMEOUT: 'connected, but no response headers came back in time',
  UND_ERR_BODY_TIMEOUT: 'connected, but the body never finished',
  EPROTO: 'TLS handshake failed',
  CERT_HAS_EXPIRED: 'the TLS certificate is expired (check the system clock)',
  UNABLE_TO_VERIFY_LEAF_SIGNATURE: 'TLS chain could not be verified (corporate TLS inspection?)',
  EHOSTUNREACH: 'no route to the host',
  ENETUNREACH: 'no network route (is the network up?)',
};

/** Proxy env vars Node's fetch ignores but a browser would use. */
export function proxyEnv(env = process.env) {
  const keys = ['HTTPS_PROXY', 'https_proxy', 'HTTP_PROXY', 'http_proxy', 'ALL_PROXY', 'all_proxy'];
  return keys.filter((k) => String(env[k] || '').trim()).map((k) => `${k}=${env[k]}`);
}

/**
 * undici (Node's fetch) reports almost everything as a bare "fetch failed" and
 * hides the actual reason in `err.cause` — which made the launcher's [verify]
 * line useless exactly when it mattered. Walk the cause chain and surface the
 * error code plus what it means.
 */
export function describeFetchError(err) {
  const codes = [];
  let e = err;
  for (let i = 0; e && i < 5; i++) {
    const code = e.code || e.errno;
    if (code && !codes.includes(code)) codes.push(String(code));
    e = e.cause;
  }
  const detail = String(err?.cause?.message || err?.message || err || '').replace(/^fetch failed\s*/i, '').trim();
  const head = codes[0] || 'FETCH_FAILED';
  const hint = FETCH_ERROR_HINTS[head] || '';
  const bits = [hint || detail || 'no detail reported'];
  if (codes.length > 1) bits.push(`chain: ${codes.join(' → ')}`);
  return `${head}: ${bits.join(' | ')}`;
}

/* ------------------------------------------------------------ runtime state */

export function writePublicUrl(state) {
  mkdirSync(RUNTIME_DIR, { recursive: true });
  writeFileSync(STATE_FILE, JSON.stringify({ ...state, at: new Date().toISOString() }, null, 2) + '\n');
  return STATE_FILE;
}

export function readPublicUrl() {
  try {
    const j = JSON.parse(readFileSync(STATE_FILE, 'utf8'));
    const n = normalizePublicUrl(j.url);
    return { ...n, source: String(j.source || ''), at: String(j.at || ''), runId: String(j.runId || '') };
  } catch {
    return null;
  }
}

/**
 * THE STALE-URL BUG THIS RUN-ID FIXES.
 *
 * devtools/stop.bat (and closing the console window) kills the stack with
 * taskkill /F, so run-stack's cleanup() never runs and public-url.json survives
 * with the PREVIOUS run's tunnel URL. On the next start, run-stack spawned
 * tunnel-run.js — which deletes that file — and then immediately began polling,
 * winning the race by the ~200 ms Node needs to boot the child. The stack was
 * then wired to a tunnel that had died minutes ago: /config.js, SITE_HOST,
 * ALLOWED_ORIGINS and PUBLIC_WEBHOOK_BASE_URL all named a dead host, the console
 * printed that dead URL, and the REAL tunnel URL scrolled past 9 s later
 * ("[verify] … /config.js not reachable after retries (0 fetch failed)").
 *
 * So every record now carries the runId of the launcher that created it, and a
 * launcher only ever accepts its own.
 */
function fromThisRun(found, runId) {
  if (!found) return false;
  if (!runId) return true; // no id required: accept whatever is there (tests, manual use)
  return found.runId === runId;
}

/** Removes the generated state + config so a stale URL never leaks into a later run. */
export function clearPublicUrl() {
  for (const f of [STATE_FILE, CONFIG_FILE]) {
    try {
      rmSync(f, { force: true });
    } catch {
      /* already gone */
    }
  }
}

/**
 * Waits for tunnel-run.js to report THIS run's URL. Resolves null on timeout.
 *
 * `runId` makes a leftover public-url.json from an earlier launcher invisible:
 * without it, the first poll used to return the previous run's dead tunnel.
 */
export async function waitForPublicUrl({ timeoutMs = 45_000, intervalMs = 250, signal, runId = '' } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (signal?.aborted) return null;
    const found = readPublicUrl();
    if (fromThisRun(found, runId)) return found;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  const found = readPublicUrl();
  return fromThisRun(found, runId) ? found : null;
}

/**
 * Polls for a DIFFERENT URL from this run — the case where the quick tunnel
 * arrives after the launcher already wired the local fallback (or where
 * cloudflared re-registered and printed a new hostname). Returns the new record
 * or null when nothing new shows up before the timeout / the signal aborts.
 */
export async function waitForNewPublicUrl({ currentUrl, runId = '', timeoutMs = 30_000, intervalMs = 500, signal } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (signal?.aborted) return null;
    const found = readPublicUrl();
    if (fromThisRun(found, runId) && found.url !== currentUrl) return found;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  return null;
}

/* ------------------------------------------------------- generated config.js */

/** The deployment config.js, verbatim — the tail of the generated file. */
export function baseConfigSource() {
  for (const p of [join(root, 'public', 'config.js'), join(root, 'dist', 'config.js')]) {
    if (existsSync(p)) return readFileSync(p, 'utf8');
  }
  return '';
}

/**
 * The browser values derived from ONE public URL.
 *
 * With no known URL (named tunnel and no PUBLIC_URL) only API_BASE is derived:
 * '/api' is request-relative, so it is correct on any host, while inventing a
 * SITE_HOST we never observed would put a wrong name in titles and emails.
 */
export function frontendConfigValues(publicUrl) {
  if (!publicUrl) return { API_BASE: '/api' };
  return {
    // Same origin: /api goes to the Node hop, which proxies to the Go backend
    // over the authenticated private hop. No absolute URL, so no CORS and no
    // way to accidentally reach another environment's API.
    API_BASE: '/api',
    SITE_HOST: publicUrl.hostname,
    FRONTEND_URL: publicUrl.origin,
    APP_NAME: publicUrl.hostname,
  };
}

/**
 * Pre-seed + the real config.js. Order matters: config.js ends with
 * `Object.assign({…defaults}, window.APP_CONFIG || {})`, so values already on
 * window.APP_CONFIG WIN over its defaults — without duplicating a single key.
 */
export function generatedConfigSource(publicUrl, values = frontendConfigValues(publicUrl)) {
  const head = publicUrl
    ? `Public URL: ${publicUrl.url}  (source: ${publicUrl.source || 'launcher'})`
    : 'No public URL known — only API_BASE is rewritten (request-relative).';
  const banner = [
    '/* GENERATED by devtools — do not edit, do not commit.',
    ` * ${head}`,
    ' * Everything the browser needs is derived from that one URL:',
    " *   API_BASE '/api' (same origin → Node hop → Go), SITE_HOST, FRONTEND_URL.",
    ' * The deployment defaults in public/config.js follow below, verbatim.',
    ' */',
  ].join('\n');
  const seed = `window.APP_CONFIG = Object.assign(window.APP_CONFIG || {}, ${JSON.stringify(values, null, 2)});`;
  return `${banner}\n${seed}\n\n${baseConfigSource()}`;
}

export function writeGeneratedConfig(publicUrl, values) {
  mkdirSync(RUNTIME_DIR, { recursive: true });
  writeFileSync(CONFIG_FILE, generatedConfigSource(publicUrl, values));
  return CONFIG_FILE;
}

/** The generated file, or null when there is none (serve dist/ as usual). */
export function readGeneratedConfig() {
  try {
    const s = readFileSync(CONFIG_FILE, 'utf8');
    return s.length ? s : null;
  } catch {
    return null;
  }
}

/** Standalone dev/preview also owns a local API proxy. Never silently send
 * those requests to the production default when no launcher was used. */
export function servedConfigSource() {
  if (overrideDisabled()) return baseConfigSource();
  return readGeneratedConfig() || generatedConfigSource(null);
}

/* --------------------------------------------------------- backend/frontend */

/**
 * Go env derived from the same URL. SITE_HOST is the bare hostname (a colon
 * makes Validate() refuse to start); the webhook base is only set for https,
 * because the backend requires an absolute https URL there.
 */
export function backendUrlEnv(publicUrl, frontendPort) {
  const origins = [publicUrl.origin];
  if (publicUrl.hostname === 'localhost' || publicUrl.hostname === '127.0.0.1') {
    origins.push(`http://localhost:${frontendPort}`, `http://127.0.0.1:${frontendPort}`);
  } else {
    // Local dev origins stay allowed so you can open the site on both at once.
    origins.push(`http://localhost:${frontendPort}`, `http://127.0.0.1:${frontendPort}`);
  }
  const env = {
    SITE_HOST: publicUrl.hostname,
    ALLOWED_ORIGINS: [...new Set(origins)].join(','),
  };
  if (publicUrl.origin.startsWith('https://')) env.PUBLIC_WEBHOOK_BASE_URL = publicUrl.origin;
  return env;
}

/** Human-readable summary of what the URL was wired into. */
export function describeWiring(publicUrl, { frontendPort, apiPort }) {
  const hop = `  Node hop     /api/*  →  http://127.0.0.1:${apiPort}  (private authenticated hop)`;
  if (!publicUrl) {
    return [
      '  public URL   unknown (named tunnel — pass PUBLIC_URL to wire the rest)',
      `  browser      API_BASE '/api'  →  http://127.0.0.1:${frontendPort}/api/*  (same origin, no CORS)`,
      hop,
      '  Go           SITE_HOST / ALLOWED_ORIGINS left as configured in backend/.env',
    ].join('\n');
  }
  const b = backendUrlEnv(publicUrl, frontendPort);
  return [
    `  public URL   ${publicUrl.url}   (${publicUrl.source || 'launcher'})`,
    `  browser      API_BASE '/api'  →  http://127.0.0.1:${frontendPort}/api/*  (same origin, no CORS)`,
    hop,
    `  Go           SITE_HOST=${b.SITE_HOST}`,
    `               ALLOWED_ORIGINS=${b.ALLOWED_ORIGINS}`,
    `               PUBLIC_WEBHOOK_BASE_URL=${b.PUBLIC_WEBHOOK_BASE_URL || '(not set — http origin)'}`,
  ].join('\n');
}
