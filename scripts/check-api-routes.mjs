#!/usr/bin/env node
/**
 * Frontend ↔ backend API contract. Fails (exit 1) when:
 *   • the frontend calls a path/method the Go backend does not register,
 *   • the frontend uses a bare same-origin fetch('/api/…') (404s on GitHub
 *     Pages — must go through api() so API_BASE is used),
 *   • no backend routes could be found at all (extractor out of date).
 * Also lists backend routes the frontend never calls (info only).
 *
 *   node scripts/check-api-routes.mjs [--verbose]
 */
import { backendRoutes, frontendCalls, matchRoute } from './api-routes.mjs';

const verbose = process.argv.includes('--verbose');
const routes = backendRoutes().filter((r) => r.path.startsWith('/api'));
const calls = frontendCalls();
let errors = 0;

if (!routes.length) { console.error('✗ no /api routes found in backend/ — update scripts/api-routes.mjs'); process.exit(1); }

for (const c of calls) {
  if (c.bareFetch) { errors++; console.error(`✗ ${c.where}: bare fetch('${c.raw}') — use api() so API_BASE is respected`); continue; }
  if (!matchRoute(routes, c.method, c.path)) { errors++; console.error(`✗ ${c.where}: ${c.method} ${c.path} — no such backend route`); }
}

const unused = routes.filter((r) => !calls.some((c) => matchRoute([r], c.method, c.path)));
if (verbose) {
  console.log('\nBackend routes:'); for (const r of routes) console.log(`  ${r.method.padEnd(6)} ${r.path}   (${r.where})`);
  console.log('\nFrontend calls:'); for (const c of calls) console.log(`  ${c.method.padEnd(6)} ${c.path}   (${c.where})`);
}
console.log(`\n${routes.length} backend routes, ${calls.length} frontend call sites, ${unused.length} backend routes not called by the frontend (admin/webhooks/etc.)`);
if (errors) { console.error(`\n${errors} API contract problem(s).`); process.exit(1); }
console.log('✓ every frontend API call maps to a backend route');
