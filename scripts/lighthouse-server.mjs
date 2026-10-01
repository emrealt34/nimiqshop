#!/usr/bin/env node
/**
 * lighthouse-server.mjs — the static host Lighthouse CI measures.
 *
 * WHY THIS EXISTS
 * ---------------
 * `staticDistDir` serves the built frontend and nothing else, so every
 * `/api/...` request the shop makes while rendering answered 404. Chrome logs
 * those as console errors, and Lighthouse's `errors-in-console` audit — part of
 * best-practices — scored every page 0 for a backend that simply is not part of
 * a static preview. The store is a client of a real API (`/api` on the same
 * origin in production, see public/config.js), so the honest measurement serves
 * the same shell with a *stub* API that answers 200 with empty payloads.
 *
 * It is a measurement fixture only: it is never shipped, never referenced by
 * the app, and it returns no data that could be mistaken for a real catalogue.
 *
 *   node scripts/lighthouse-server.mjs          # http://127.0.0.1:8791
 *   PORT=9000 node scripts/lighthouse-server.mjs
 */
import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { extname, join, normalize, resolve } from 'node:path';
import { brotliCompressSync, gzipSync } from 'node:zlib';

const ROOT = resolve(process.cwd(), 'dist');
const PORT = Number(process.env.PORT || 8791);
const HOST = process.env.HOST || '127.0.0.1';

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
  '.xml': 'application/xml; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
};

/** Empty-but-valid payload per endpoint family: the pages render their real
 *  empty states instead of their error states. */
function stubPayload(pathname) {
  if (pathname.startsWith('/api/site')) return {};
  if (pathname.startsWith('/api/catalog') || pathname.includes('products')) return { products: [], families: [], brands: [] };
  if (pathname.includes('activity') || pathname.includes('presence')) return { items: [], summary: {} };
  if (pathname.includes('orders')) return [];
  if (pathname.includes('quotes')) return [];
  if (pathname.includes('cashback')) return { cashbacks: [], summary: {} };
  if (pathname.includes('stakers') || pathname.includes('pool')) return { staked: false };
  return {};
}

const COMPRESSIBLE = /^(text\/|application\/(javascript|json|xml)|image\/svg\+xml)/;

async function send(req, res, status, body, type = 'application/json; charset=utf-8') {
  const headers = {
    'content-type': type,
    'cache-control': 'no-store',
    // The production host is behind Cloudflare; the stub must not become a
    // caching variable in the measurement.
    vary: 'accept-encoding',
  };
  let payload = typeof body === 'string' ? Buffer.from(body) : body;
  const ae = String(req.headers['accept-encoding'] || '');
  if (COMPRESSIBLE.test(type) && payload.length > 256) {
    if (/\bbr\b/.test(ae)) {
      payload = brotliCompressSync(payload);
      headers['content-encoding'] = 'br';
    } else if (/\bgzip\b/.test(ae)) {
      payload = gzipSync(payload);
      headers['content-encoding'] = 'gzip';
    }
  }
  res.writeHead(status, headers);
  res.end(payload);
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url || '/', `http://${HOST}:${PORT}`);
  const pathname = decodeURIComponent(url.pathname);

  if (pathname.startsWith('/api/')) {
    return send(req, res, 200, JSON.stringify(stubPayload(pathname)));
  }

  // Path traversal guard: everything resolves inside dist/.
  const rel = normalize(pathname).replace(/^(\.\.[/\\])+/, '').replace(/^\/+/, '');
  let file = join(ROOT, rel);
  if (!file.startsWith(ROOT)) return send(req, res, 403, '{"error":"forbidden"}');

  try {
    const info = await stat(file).catch(() => null);
    if (!info || info.isDirectory()) file = join(file, 'index.html');
    const body = await readFile(file);
    return send(req, res, 200, body, TYPES[extname(file).toLowerCase()] || 'application/octet-stream');
  } catch {
    // GitHub Pages-style 404 page, like the production host.
    try {
      const body = await readFile(join(ROOT, '404.html'));
      return send(req, res, 404, body, TYPES['.html']);
    } catch {
      return send(req, res, 404, 'not found', 'text/plain; charset=utf-8');
    }
  }
});

server.listen(PORT, HOST, () => {
  // lhci waits for this line (startServerReadyPattern).
  console.log(`lighthouse fixture server ready on http://${HOST}:${PORT} (dist: ${ROOT})`);
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
