#!/usr/bin/env node
/**
 * sync-catalog.mjs — pull the shop catalog straight from CryptoRefills and
 * bake it into `public/data/catalog/` as static JSON, so the storefront can
 * browse brands in every country WITHOUT the Go backend being alive.
 *
 * WHY THIS EXISTS
 * The backend (shopapi.nimiqbase.com) is the merchant-side server: sessions,
 * orders, payments, FX guards. It also proxied the supplier catalog — which
 * meant a down backend showed an EMPTY shop even though the catalog is
 * public, cacheable data that changes a few times a day. The Cloudflare
 * Pages deploy workflow now runs this script hourly (schedule in
 * .github/workflows/cloudflare-pages.yml) before building, and the committed
 * copy of its output is the seed/fallback when a sync run cannot reach the
 * supplier. The frontend reads the static files first (src/lib/api.ts) and
 * only falls back to /api for what genuinely needs the server.
 *
 * AUTH
 * CryptoRefills' Business API authenticates partners with two public
 * headers — X-Cr-Application (the partner id from the account page) and
 * X-Cr-Version — plus a User-Agent. There is no secret key involved, which
 * is why this can run in CI from values already present in
 * backend/.env.example. Override via CRYPTOREFILLS_* env vars if the
 * partner account ever changes.
 *
 * SHAPE
 * One file per country: the /v2/brands listing pruned to the fields the
 * frontend and the Go Brand/BrandCategory structs use, keeping ALL kinds in
 * one payload (the kind filter the backend applied in filterBrandCategories
 * is mirrored client-side in src/lib/api.ts, so one fetch serves the three
 * tabs). `_global.json` is the no-country listing (the supplier answers it
 * for this partner; if it ever 400s we fall back to TR exactly like the
 * backend's Brands() does).
 *
 * FAILURE MODE
 * A country that fails keeps its previous file (the committed seed or the
 * last hourly sync), so a supplier wobble can never blank the shop. The run
 * exits 0 as long as at least one country refreshed; the workflow step is
 * continue-on-error anyway — a fully-down supplier must not stop a deploy.
 */
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const OUT = join(ROOT, 'public', 'data', 'catalog', 'brands');

const BASE = String(process.env.CRYPTOREFILLS_BASE_URL || 'https://api.cryptorefills.com').replace(/\/+$/, '');
const HEADERS = {
  'X-Cr-Application': process.env.CRYPTOREFILLS_PARTNER_ID || 'YQyw0dJ0FM',
  'X-Cr-Version': process.env.CRYPTOREFILLS_APP_VERSION || 'nimshop/1.0',
  'User-Agent': process.env.CRYPTOREFILLS_USER_AGENT || 'nimshop/1.0 +https://shop.nimiqbase.com',
  Accept: 'application/json',
};

/** The Brand struct's wire fields, minus nothing the frontend reads. */
const BRAND_FIELDS = [
  'family', 'brand_id', 'logo_url', 'logo_base_url', 'bg_color',
  'min', 'max', 'category', 'kind', 'is_out_of_stock', 'country_code', 'product_type',
];
const pruneBrand = (b) => {
  const o = {};
  for (const k of BRAND_FIELDS) if (b && b[k] !== undefined && b[k] !== null && b[k] !== '') o[k] = b[k];
  if (b && b.is_out_of_stock === true) o.is_out_of_stock = true;
  return o;
};

async function countryList() {
  const src = await readFile(join(ROOT, 'src', 'lib', 'countries.ts'), 'utf8');
  const seen = new Set();
  for (const m of src.matchAll(/\['([A-Z]{2})',\s*'/g)) seen.add(m[1]);
  return [...seen];
}

async function fetchBrands(cc) {
  const url = cc === '_global' ? `${BASE}/v2/brands` : `${BASE}/v2/brands?country_code=${encodeURIComponent(cc)}`;
  let lastErr;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      const res = await fetch(url, { headers: HEADERS, signal: AbortSignal.timeout(25_000) });
      if (res.status === 400 && cc === '_global') {
        // Partner accounts that reject an empty country_code: same default
        // the backend uses (TR), so the no-country home view matches it.
        return fetchBrands('TR');
      }
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      const cats = Array.isArray(j?.categories)
        ? j.categories.map((c) => ({
            kind: c?.kind ?? '',
            category: c?.category ?? '',
            brands: (Array.isArray(c?.brands) ? c.brands : []).map(pruneBrand),
          }))
        : [];
      return { country_code: j?.country_code || (cc === '_global' ? '' : cc), categories: cats };
    } catch (e) {
      lastErr = e;
      await new Promise((r) => setTimeout(r, 700 * (attempt + 1)));
    }
  }
  throw lastErr;
}

async function pool(items, worker, concurrency) {
  const q = [...items];
  await Promise.all(Array.from({ length: concurrency }, async () => {
    for (;;) {
      const it = q.shift();
      if (!it) return;
      await worker(it);
    }
  }));
}

const started = Date.now();
const countries = await countryList();
const targets = ['_global', ...countries];
await mkdir(OUT, { recursive: true });

const manifest = { generated_at: new Date().toISOString(), source: `${BASE}/v2/brands`, countries: {}, failed: [] };
let ok = 0;

await pool(targets, async (cc) => {
  try {
    const data = await fetchBrands(cc);
    const brands = data.categories.reduce((n, c) => n + c.brands.length, 0);
    if (!brands && cc !== '_global') {
      // Empty listing: keep whatever seed/snapshot is already there rather
      // than publishing a blank country (the supplier glitches occasionally).
      manifest.failed.push({ country: cc, reason: 'empty-listing-kept-previous' });
      return;
    }
    await writeFile(join(OUT, `${cc}.json`), JSON.stringify({ ...data, generated_at: manifest.generated_at }));
    manifest.countries[cc] = brands;
    ok += 1;
  } catch (e) {
    manifest.failed.push({ country: cc, reason: String(e && e.message || e).slice(0, 120) });
  }
}, 6);

await writeFile(join(OUT, '..', 'manifest.json'), JSON.stringify(manifest, null, 1));
const secs = ((Date.now() - started) / 1000).toFixed(1);
console.log(`[sync-catalog] ${ok}/${targets.length} countries refreshed in ${secs}s; failed: ${manifest.failed.length}`);
if (manifest.failed.length) console.log('[sync-catalog] failed:', manifest.failed.slice(0, 12).map((f) => f.country).join(', '));
process.exit(ok > 0 ? 0 : 1);
