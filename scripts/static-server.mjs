/* Minimal static file server for the built dist/ — serves on 0.0.0.0 and
 * does NOT check the Host header, so the sandbox preview proxy AND any
 * Cloudflare quick tunnel work (no "Blocked request" 403, ever).
 *
 * Handles clean URLs (fall back to /index.html), and proxies /api/* to the
 * real Go backend (default http://127.0.0.1:8084) so the frontend + backend
 * run together through the same tunnel without CORS.
 *
 *   npm run build
 *   BACKEND=http://127.0.0.1:8084 node scripts/static-server.mjs   # or npm run preview
 */
import { createServer } from 'node:http';
import { proxyApi, proxyOptionsFromEnv } from './proxy.mjs';
import { baseConfigSource, readGeneratedConfig, servedConfigSource } from './public-url.mjs';
import { readFile, stat } from 'node:fs/promises';
import { extname, join, normalize, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  CSP_HASH_PLACEHOLDER,
  collectInlineScriptHashes,
  substituteInlineScriptHashes,
  hasPlaceholder,
} from './csp-inline.mjs';

const PORT = Number((process.env.PORT || '').trim()) || 8085;
const ROOT = fileURLToPath(new URL('../dist', import.meta.url));
const PUBLIC = fileURLToPath(new URL('../public', import.meta.url));
const proxyOptions = proxyOptionsFromEnv();
const BACKEND = proxyOptions.backend.origin;

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.txt': 'text/plain; charset=utf-8',
};
// Code-ish assets: never cache (dev/preview always fresh).
const NO_CACHE = ['.html', '.js', '.mjs', '.css', '.json', '.txt', '.xml'];

/**
 * Join a request path onto a root directory and refuse anything that escapes
 * it. Returns null when the resolved path is outside `root` (traversal,
 * encoded dot segments, NUL bytes …) so callers fall through to the 404.
 */
function safeJoin(root, ...parts) {
  const full = resolve(root, ...parts);
  const rootWithSep = root.endsWith(sep) ? root : root + sep;
  if (full !== root && !full.startsWith(rootWithSep)) return null;
  return full;
}

async function statOrNull(p) {
  if (!p) return null;
  try { return await stat(p); } catch { return null; }
}

async function resolvePath(urlPath) {
  let raw;
  try { raw = decodeURIComponent(urlPath.split('?')[0]); } catch { return null; }
  if (raw.includes('\0')) return null;
  // Split into segments, refuse any ".." segment outright, and rebuild the
  // path relative to ROOT so join()/resolve() can never climb out of it.
  const segments = raw.split(/[/\\]+/).filter((seg) => seg !== '' && seg !== '.');
  if (segments.includes('..')) return null;
  let p = normalize(segments.join('/'));
  if (p === '' || p === '.') p = 'index.html';

  let candidate = safeJoin(ROOT, p);
  let s = await statOrNull(candidate);
  if (s && s.isDirectory()) {
    candidate = safeJoin(candidate, 'index.html');
  } else if (!s) {
    candidate = safeJoin(ROOT, p + '.html');
    s = await statOrNull(candidate);
    if (!s || !s.isFile()) candidate = safeJoin(ROOT, p, 'index.html');
  }
  // Stale-dist safety net: when dist/ was NOT rebuilt (build-all saw it as up to
  // date on a machine where public/ gained files after the last build), assets
  // that exist in public/ must still resolve instead of 404-ing — this is how
  // /img/* icons kept 404-ing behind the tunnel after an asset refresh.
  if (!(await statOrNull(candidate))) {
    const pub = safeJoin(PUBLIC, p);
    const s2 = await statOrNull(pub);
    if (s2 && s2.isFile()) return pub;
  }
  return candidate;
}

/**
 * Last line of defence for a stale dist/.
 *
 * The preview server is the thing people actually open (devtools → start), and
 * it will happily serve a dist/ that was built before the CSP integration
 * existed — the mtime check in build-all.mjs can be fooled by an archive
 * extract whose timestamps are newer than the sources. Serving that page
 * unchanged means the browser logs
 *   "invalid source: '__CSP_SCRIPT_HASHES__' … it will be ignored"
 * and then blocks all four inline scripts, so the site comes up unstyled,
 * without the theme and without the Nimiq-Pay viewport fix.
 *
 * Rather than let that happen, repair it here: hash the inline scripts in the
 * page we are about to send and substitute them, exactly as the build
 * integration would. This is a preview safety net, not a production feature —
 * production serves dist/_headers, which the build substitutes. The warning is
 * printed once per process because the fix is to rebuild, not to rely on this.
 */
let cspRepairWarned = false;
function repairStaleCsp(text) {
  if (!hasPlaceholder(text)) return { text, repaired: false };
  const hashes = collectInlineScriptHashes(text);
  if (hashes.length === 0) return { text, repaired: false };
  if (!cspRepairWarned) {
    cspRepairWarned = true;
    console.warn(
      `\n[static-server] WARNING: dist/ contains the raw ${CSP_HASH_PLACEHOLDER} token — ` +
      'it was built without the CSP integration (or is simply stale).\n' +
      '[static-server] Substituting inline-script hashes on the fly so the preview works, but ' +
      'run "npm run rebuild" (or devtools\\build.bat) — a real deploy would not be repaired here.\n'
    );
  }
  return { text: substituteInlineScriptHashes(text, hashes), repaired: true };
}

createServer(async (req, res) => {
  const pathname = (req.url || '/').split('?')[0];
  try {
    if (pathname === '/_health') { res.writeHead(200, {'Content-Type':'application/json','Cache-Control':'no-store'}); return res.end('{"ok":true}'); }
    // The launcher-generated config (public URL → API_BASE '/api', SITE_HOST,
    // FRONTEND_URL) wins over dist/config.js. Without it the browser would use
    // public/config.js's hard-coded production API domain and never talk to the
    // backend behind this tunnel.
    // ONE-LINE OVERRIDE: API_URL=https://api.example.com makes the browser call
    // that absolute URL instead of the same-origin /api proxy — for serving
    // dist/ locally while the Go backend runs on another host/domain.
    if (pathname === '/config.js') {
      const apiUrl = String(process.env.API_URL || '').trim();
      if (apiUrl) {
        const base = apiUrl.replace(/\/+$/, '').replace(/\/api$/, '');
        const source = `/* API_URL=${base} — set via the API_URL env var (one line, no rebuild). */\n` +
          `window.APP_CONFIG = Object.assign(window.APP_CONFIG || {}, ${JSON.stringify({ API_BASE: base + '/api', SITE_HOST: base.replace(/^https?:\/\//, ''), FRONTEND_URL: base }, null, 2)});\n` +
          baseConfigSource();
        res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8', 'Cache-Control': 'no-store, no-cache, must-revalidate' });
        return res.end(source);
      }
      const generated = servedConfigSource();
      if (generated) {
        res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8', 'Cache-Control': 'no-store, no-cache, must-revalidate' });
        return res.end(generated);
      }
    }
    if (pathname.startsWith('/api/')) {
      // Dev/preview proxy = same trust boundary as the Go binary: drop the
      // browser's Origin so the backend's production CORS allowlist (meant
      // for direct public exposure) never sees a "foreign" origin through us.
      delete req.headers.origin;
      return proxyApi(req, res, proxyOptions);
    }
    const file = await resolvePath(req.url || '/');
    if (!file) throw new Error('forbidden path');
    const ext = extname(file).toLowerCase();
    const type = MIME[ext] || 'application/octet-stream';
    const headers = { 'Content-Type': type, 'Access-Control-Allow-Origin': '*' };
    if (NO_CACHE.includes(ext)) {
      headers['Cache-Control'] = 'no-store, no-cache, must-revalidate';
    }
    let data = await readFile(file);
    if (ext === '.html') {
      const fixed = repairStaleCsp(data.toString('utf8'));
      if (fixed.repaired) {
        data = Buffer.from(fixed.text, 'utf8');
        headers['X-CSP-Repair'] = 'inline-script-hashes-substituted';
      }
    }
    res.writeHead(200, headers);
    res.end(data);
  } catch {
    // Designed 404 page instead of a bare "404 Not Found" text.
    try {
      const data = await readFile(join(ROOT, '404.html'));
      res.writeHead(404, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end(data);
    } catch {
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('404 Not Found');
    }
  }
// Listen on all interfaces, DUAL-STACK (IPv4 + IPv6).
// Binding '0.0.0.0' (IPv4-only) made Windows cloudflared fail because it
// dials localhost as [::1]:PORT (IPv6) and got "connection refused".
// With no host, Node binds '::' dual-stack and accepts BOTH 0.0.0.0 and ::1.
}).listen(PORT, () => {
  const host = String(process.env.PUBLIC_URL || '').trim();
  console.log(`${host || 'shop.nimiqbase.com'} static server (proxy → ${BACKEND}) on http://0.0.0.0:${PORT} serving ${ROOT}`);
  console.log(`/config.js: ${readGeneratedConfig() ? 'launcher-generated (public URL wired)' : 'same-origin proxy config (standalone preview)'}`);
});
