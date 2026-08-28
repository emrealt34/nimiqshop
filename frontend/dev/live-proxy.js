/* dev/live-proxy.js — serves the static frontend and proxies /api/* to the REAL
 * Go backend (default http://127.0.0.1:8090) so the preview shows LIVE CryptoRefills
 * data instead of the mock. DEVELOPMENT/preview only.
 *   BACKEND=http://127.0.0.1:8090 PORT=8080 node dev/live-proxy.js
 */
const http = require('http');
const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const PORT = process.env.PORT || 4321;
const BACKEND = process.env.BACKEND || 'http://127.0.0.1:8080';

const MIME = { '.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.json':'application/json', '.woff2':'font/woff2', '.txt':'text/plain', '.svg':'image/svg+xml', '.png':'image/png', '.jpg':'image/jpeg', '.ico':'image/x-icon' };

const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://x');
  // Proxy /api/* to the real backend (same-origin to the browser → no CORS).
  if (url.pathname.startsWith('/api/')) {
    const proxyReq = http.request(BACKEND + req.url, { method: req.method, headers: { ...req.headers, host: new URL(BACKEND).host } }, (up) => {
      res.writeHead(up.statusCode || 502, up.headers);
      up.pipe(res);
    });
    proxyReq.on('error', () => { res.writeHead(502); res.end('{"error":"backend unreachable — is the Go server running on ' + BACKEND + '?"}'); });
    req.pipe(proxyReq);
    return;
  }
  // Static files. Extensionless paths are treated as the matching .html page
  // (clean URLs): / -> index.html, /orders -> orders.html, /product -> product.html.
  // Paths that already have an extension (assets, css, js, fonts) are untouched.
  let file = url.pathname === '/' ? '/index.html' : url.pathname;
  if (file !== '/' && !path.extname(file)) file += '.html';
  const fp = path.join(ROOT, file);
  if (!fp.startsWith(ROOT)) { res.writeHead(403); return res.end('forbidden'); }
  fs.readFile(fp, (err, buf) => {
    if (err) { res.writeHead(404); return res.end('not found'); }
    const ext = path.extname(fp);
    /* STALE-CACHE FIX: this server used to send NO Cache-Control at all, so
     * browsers heuristic-cached JS/CSS across deploys — visitors kept running
     * OLD util.js (and hitting crashes that were already fixed). Code and
     * documents must always revalidate; only immutable-ish assets cache. */
    const code = ['.html', '.js', '.css', '.json', '.txt', '.xml'];
    const assets = ['.woff2', '.png', '.jpg', '.jpeg', '.svg', '.ico', '.webp'];
    const headers = { 'Content-Type': MIME[ext] || 'application/octet-stream' };
    if (code.includes(ext)) Object.assign(headers, { 'Cache-Control': 'no-store, no-cache, must-revalidate', 'CDN-Cache-Control': 'no-store' });
    else if (assets.includes(ext)) Object.assign(headers, { 'Cache-Control': 'public, max-age=3600' });
    res.writeHead(200, headers);
    res.end(buf);
  });
});

server.listen(PORT, '0.0.0.0', () => console.log(`nim.shop LIVE preview (proxy → ${BACKEND}) on http://0.0.0.0:${PORT}`));
