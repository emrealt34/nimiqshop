/**
 * Frontend ↔ backend API route extraction (shared by check-api-routes.mjs
 * and the e2e fixtures).
 *
 *   backendRoutes()  — routes registered in backend/ Go code
 *   frontendCalls()  — API paths the frontend requests (src/**)
 *   matchRoute()     — does METHOD /path hit a backend route?
 */
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];

function walk(dir, exts, out = []) {
  if (!existsSync(dir)) return out;
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name.startsWith('.') || name === 'vendor' || name === 'dist') continue;
    const p = join(dir, name);
    const s = statSync(p);
    if (s.isDirectory()) walk(p, exts, out);
    else if (exts.some((e) => name.endsWith(e))) out.push(p);
  }
  return out;
}

/** '/api/orders/{id}' | '/api/orders/:id' | '/api/files/{p:*}' → segments */
export function normalize(path) {
  return '/' + path.split('?')[0].replace(/^\/+|\/+$/g, '').split('/').map((seg) => {
    if (/^\{[^}]*:\*\}$|^\*|^\{[^}]*\.\.\.\}$/.test(seg)) return '**';
    if (/^\{.*\}$|^:/.test(seg) || seg === '${}' || /\$\{[^}]*\}/.test(seg)) return '*';
    return seg;
  }).join('/');
}

let cachedBackend;
export function backendRoutes() {
  if (cachedBackend) return cachedBackend;
  const routes = [];
  const add = (method, p, file, line) => routes.push({ method: method.toUpperCase(), path: normalize(p), raw: p, where: `${relative(ROOT, file)}:${line}` });
  for (const file of walk(join(ROOT, 'backend'), ['.go']).filter((f) => !f.endsWith('_test.go'))) {
    const src = readFileSync(file, 'utf8');
    // group prefixes:  api := r.Group("/api")   v1 := api.Group("/v1")
    const groups = {};
    for (const m of src.matchAll(/(\w+)\s*:?=\s*(\w+)\.Group\(\s*"([^"]*)"/g)) groups[m[1]] = (groups[m[2]] || '') + m[3];
    const lineOf = (i) => src.slice(0, i).split('\n').length;
    // r.GET("/x", h) / api.POST(...) / r.Handle("GET", "/x", h) / mux.HandleFunc("GET /x", h)
    for (const m of src.matchAll(/(\w+)\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|ANY|Any)\(\s*"([^"]+)"/g)) {
      const prefix = groups[m[1]] || '';
      const method = /any/i.test(m[2]) ? 'ANY' : m[2];
      add(method, prefix + m[3], file, lineOf(m.index));
    }
    for (const m of src.matchAll(/(\w+)\.Handle\(\s*"(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)"\s*,\s*"([^"]+)"/g)) add(m[2], (groups[m[1]] || '') + m[3], file, lineOf(m.index));
    for (const m of src.matchAll(/\.Handle(?:Func)?\(\s*"(?:(GET|POST|PUT|PATCH|DELETE) )?(\/[^"]+)"/g)) add(m[1] || 'ANY', m[2], file, lineOf(m.index));
    // table-driven: {"GET", "/api/x", h} or {Method: "GET", Path: "/api/x"}
    for (const m of src.matchAll(/\{\s*(?:Method:\s*)?"(GET|POST|PUT|PATCH|DELETE)"\s*,\s*(?:Path:\s*)?"(\/[^"]+)"/g)) add(m[1], m[2], file, lineOf(m.index));
    // switch-based: case "/api/x": / case "GET /api/x":
    for (const m of src.matchAll(/case\s+("[^"]*"(?:\s*,\s*"[^"]*")*)\s*:/g)) {
      for (const s of m[1].matchAll(/"(?:(GET|POST|PUT|PATCH|DELETE) )?(\/api\/[^"]*)"/g)) add(s[1] || 'ANY', s[2], file, lineOf(m.index));
    }
  }
  // unique
  const seen = new Set();
  cachedBackend = routes.filter((r) => { const k = r.method + ' ' + r.path; if (seen.has(k)) return false; seen.add(k); return true; });
  return cachedBackend;
}

/** Frontend calls: api('/x'), api(`/x/${id}`, { method: 'POST' }), fetch(API + '/x') … */
export function frontendCalls() {
  const calls = [];
  for (const file of walk(join(ROOT, 'src'), ['.ts', '.tsx', '.astro'])) {
    const src = readFileSync(file, 'utf8');
    const lineOf = (i) => src.slice(0, i).split('\n').length;
    const re = /\b(?:api|apiGet|apiPost|cachedGet|apiFetch)\(\s*(['"`])(\/[^'"`]*)\1\s*(?:,\s*\{([^}]*)\})?/g;
    for (const m of src.matchAll(re)) {
      const opts = m[3] || '';
      const method = (opts.match(/method:\s*['"](\w+)['"]/) || [])[1] || 'GET';
      const p = m[2].replace(/\$\{[^}]*\}/g, '${}');
      calls.push({ method: method.toUpperCase(), path: normalize('/api' + p), raw: m[2], where: `${relative(ROOT, file)}:${lineOf(m.index)}` });
    }
    // bare same-origin fetches — always wrong on GitHub Pages
    for (const m of src.matchAll(/fetch\(\s*(['"`])\/api\/([^'"`]*)\1/g)) calls.push({ method: 'GET', path: normalize('/api/' + m[2]), raw: '/api/' + m[2], where: `${relative(ROOT, file)}:${lineOf(m.index)}`, bareFetch: true });
  }
  return calls;
}

export function matchRoute(routes, method, path) {
  const want = normalize(path).split('/');
  return routes.find((r) => {
    if (r.method !== 'ANY' && r.method !== method.toUpperCase() && !(method.toUpperCase() === 'HEAD' && r.method === 'GET')) return false;
    const have = r.path.split('/');
    for (let i = 0; i < Math.max(have.length, want.length); i++) {
      if (have[i] === '**') return true;
      if (have[i] === undefined || want[i] === undefined) return false;
      if (have[i] === '*' || want[i] === '*') continue;
      if (have[i] !== want[i]) return false;
    }
    return true;
  }) || null;
}

export { METHODS };
