/**
 * GitHub-Pages-like static server for the e2e suite.
 *
 * Serves dist/ under BASE (default /nimiqshop/) the way GitHub Pages serves
 * the project site, because that is where the real bugs showed up:
 *   • /nimiqshop/orders    → 301 → /nimiqshop/orders/   (directory index)
 *   • unknown path          → 404 status + dist/404.html (custom 404 page)
 *   • anything outside BASE → plain 404 (like emrealt34.github.io/orders)
 */
import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

const PORT = Number(process.env.PORT || 4455);
const HOST = process.env.HOST || '127.0.0.1';
const BASE = '/' + String(process.env.BASE || '/nimiqshop/').replace(/^\/+|\/+$/g, '') + '/';
const ROOT = fileURLToPath(new URL('../../../dist', import.meta.url));

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png',
  '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp', '.ico': 'image/x-icon', '.woff2': 'font/woff2',
  '.woff': 'font/woff', '.txt': 'text/plain; charset=utf-8', '.webmanifest': 'application/manifest+json', '.xml': 'application/xml',
};
// no in-memory cache: a rebuilt dist/ must be served immediately
const read = (p) => readFile(p);
// dist/ is byte-identical to the Pages build, whose CSP ends with
// `upgrade-insecure-requests`. Chromium and Firefox exempt loopback hosts from
// that directive, but WebKit upgrades http://127.0.0.1 subresources to https,
// so every module import fails ("Importing a module script failed" / TLS
// handshake error). Pages is https-only, where the directive is a no-op, so
// this local http server drops it from the HTML it serves — nothing else.
const readHtml = async (p) => Buffer.from(String(await readFile(p)).replace(/;\s*upgrade-insecure-requests/g, ''));
async function kind(p) { try { const s = await stat(p); return s.isFile() ? 'file' : s.isDirectory() ? 'dir' : null; } catch { return null; } }

async function send404(res) {
  const page = join(ROOT, '404.html');
  const body = (await kind(page)) === 'file' ? await readHtml(page) : Buffer.from('404');
  res.writeHead(404, { 'Content-Type': MIME['.html'] });
  res.end(body);
}

createServer(async (req, res) => {
  try {
    const url = new URL(req.url || '/', 'http://localhost');
    const path = decodeURIComponent(url.pathname);
    if (path === '/_health') { res.writeHead(200); return res.end('ok'); }
    if (path + '/' === BASE) { res.writeHead(301, { Location: BASE + url.search }); return res.end(); }
    if (!path.startsWith(BASE)) { res.writeHead(404, { 'Content-Type': 'text/plain' }); return res.end('Not under ' + BASE); }
    const file = join(ROOT, normalize(path.slice(BASE.length)).replace(/^(\.\.[/\\])+/, ''));
    if (!file.startsWith(ROOT)) return send404(res);
    const k = await kind(file);
    if (k === 'dir') {
      if (!path.endsWith('/')) { res.writeHead(301, { Location: path + '/' + url.search }); return res.end(); }
      const index = join(file, 'index.html');
      if ((await kind(index)) === 'file') { res.writeHead(200, { 'Content-Type': MIME['.html'] }); return res.end(await readHtml(index)); }
      return send404(res);
    }
    if (k === 'file') {
      const ext = extname(file).toLowerCase();
      res.writeHead(200, { 'Content-Type': MIME[ext] || 'application/octet-stream' });
      return res.end(ext === '.html' ? await readHtml(file) : await read(file));
    }
    return send404(res);
  } catch (e) {
    console.error('[e2e] request failed:', e);
    res.writeHead(500, { 'Content-Type': 'text/plain' });
    res.end('500 Internal Server Error');
  }
}).listen(PORT, HOST, () => console.log(`[e2e] dist/ at http://${HOST}:${PORT}${BASE}`));
