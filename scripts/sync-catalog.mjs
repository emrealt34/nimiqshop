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

/* ------------------------ stock verification ------------------------ */
/*
 * WHY THIS EXISTS
 * /v2/brands and /v5/products are different supplier endpoints. A brand can
 * be listed with is_out_of_stock=false while its product list is empty, so
 * the storefront showed the card as available and the buyer hit "not
 * available" the moment they clicked it. The backend already learns this on
 * click (tombstones) but the static listing the home page reads never
 * consulted those, so every new visitor was sent into the same dead end.
 *
 * WHAT IT DOES
 * For every family listed as in stock, ask the product endpoint whether any
 * products exist for that country. Only a definitive "200 and no products"
 * (confirmed by a second read) marks the family out of stock; errors, 403/429,
 * timeouts and malformed answers leave the flag exactly as the brand listing
 * had it. The storefront hides out-of-stock cards by default, so the buyer
 * never sees the dead card, and "show out of stock" stays truthful.
 *
 * BUDGET
 * The pass is time-boxed and stops early after a run of unresolved answers,
 * so a supplier rate limit can never stall or fail the deploy. Countries are
 * verified in order (TR first, then by listing size); anything not reached in
 * this run keeps its listing flags.
 */
const VERIFY_BUDGET_MS = Number(process.env.CATALOG_VERIFY_BUDGET_MS) || 10 * 60_000;
const VERIFY_CONCURRENCY = 2;
// The supplier answers a steady ~1 request/second without refusing; bursts
// of parallel product reads from one runner get 403 for minutes at a time.
const VERIFY_INTERVAL_MS = Number(process.env.CATALOG_VERIFY_INTERVAL_MS) || 1000;
let verifyNextSlot = 0;
async function paceVerify() {
  const now = Date.now();
  const at = Math.max(now, verifyNextSlot);
  verifyNextSlot = at + VERIFY_INTERVAL_MS;
  if (at > now) await sleep(at - now);
}
const VERIFY_UNRESOLVED_BEFORE_COOLDOWN = 8;
const VERIFY_COOLDOWN_MS = 45_000;
const VERIFY_MAX_COOLDOWNS = 6;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** One product-presence read: 'ok' | 'empty' | 'unknown' (+ the HTTP status seen). */
async function probeOnce(cc, family) {
  await paceVerify();
  const url = `${BASE}/v5/products/country/${encodeURIComponent(cc)}?family_name=${encodeURIComponent(family)}&lang=en`;
  try {
    const res = await fetch(url, { headers: HEADERS, signal: AbortSignal.timeout(20_000) });
    if (res.status !== 200) return { state: 'unknown', status: res.status };
    let j;
    try { j = JSON.parse(await res.text()); } catch { return { state: 'unknown', status: 'bad-json' }; }
    if (!Array.isArray(j)) return { state: 'unknown', status: 'bad-shape' };
    const has = j.some((f) => Array.isArray(f?.products) && f.products.length > 0);
    return { state: has ? 'ok' : 'empty', status: 200 };
  } catch (e) {
    return { state: 'unknown', status: e && e.name === 'TimeoutError' ? 'timeout' : 'network' };
  }
}

/**
 * Rate limits (403/429) and transient errors are retried with a growing
 * pause; they never count as evidence that a product is missing.
 */
async function productPresence(cc, family, stats) {
  const RETRY_DELAYS = [4_000, 12_000];
  let r = await probeOnce(cc, family);
  for (const delay of RETRY_DELAYS) {
    if (r.state !== 'unknown') break;
    stats.status[r.status] = (stats.status[r.status] || 0) + 1;
    await sleep(delay);
    r = await probeOnce(cc, family);
  }
  if (r.state === 'unknown') {
    stats.status[r.status] = (stats.status[r.status] || 0) + 1;
    return 'unknown';
  }
  if (r.state !== 'empty') return r.state;
  // Hiding a card is the expensive mistake: confirm an empty answer once more.
  await sleep(1500);
  const again = await probeOnce(cc, family);
  return again.state;
}

async function verifyStock(plan) {
  const jobs = [];
  for (const { cc, data } of plan) {
    const families = new Set();
    for (const cat of data.categories) {
      for (const b of cat.brands) if (!b.is_out_of_stock) families.add(b.family);
    }
    for (const family of families) jobs.push({ cc, family });
  }
  const stats = { families: jobs.length, checked: 0, ok: 0, empty: 0, unresolved: 0, skipped: 0, cooldowns: 0, status: {}, hidden: {} };
  const deadline = Date.now() + VERIFY_BUDGET_MS;
  const queue = [...jobs];
  let unresolvedRun = 0;
  let stop = false;
  let pausedUntil = 0;
  await Promise.all(Array.from({ length: VERIFY_CONCURRENCY }, async () => {
    while (queue.length) {
      if (stop || Date.now() > deadline) {
        stats.skipped += queue.length;
        queue.length = 0;
        return;
      }
      // The supplier is pushing back: every worker waits out the same cool-down.
      if (Date.now() < pausedUntil) await sleep(pausedUntil - Date.now());
      const job = queue.shift();
      const result = await productPresence(job.cc, job.family, stats);
      stats.checked += 1;
      if (result === 'unknown') {
        stats.unresolved += 1;
        unresolvedRun += 1;
        if (unresolvedRun >= VERIFY_UNRESOLVED_BEFORE_COOLDOWN) {
          unresolvedRun = 0;
          stats.cooldowns += 1;
          if (stats.cooldowns > VERIFY_MAX_COOLDOWNS) stop = true;
          pausedUntil = Date.now() + VERIFY_COOLDOWN_MS;
        }
        continue;
      }
      unresolvedRun = 0;
      if (result === 'ok') {
        stats.ok += 1;
      } else {
        stats.empty += 1;
        (stats.hidden[job.cc] ||= new Set()).add(job.family);
      }
    }
  }));
  // Apply: flag every brand of an empty family in that country as out of stock.
  for (const { cc, data } of plan) {
    const emptyFamilies = stats.hidden[cc];
    if (!emptyFamilies) continue;
    for (const cat of data.categories) {
      for (const b of cat.brands) if (emptyFamilies.has(b.family)) b.is_out_of_stock = true;
    }
  }
  stats.hidden = Object.fromEntries(Object.entries(stats.hidden).map(([cc, set]) => [cc, [...set].sort()]));
  stats.budget_ms = VERIFY_BUDGET_MS;
  stats.aborted_on_errors = stop;
  return stats;
}

const fetched = [];
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
    // Written after the verification pass below, so a half-finished run
    // never publishes a country whose stock flags were not yet checked.
    fetched.push({ cc, data, brands });
    manifest.countries[cc] = brands;
    ok += 1;
  } catch (e) {
    manifest.failed.push({ country: cc, reason: String(e && e.message || e).slice(0, 120) });
  }
}, 6);

// `_global` has no country to probe, so it is published as listed.
const plan = fetched
  .filter((f) => f.cc !== '_global')
  .sort((a, b) => (a.cc === 'TR' ? -1 : b.cc === 'TR' ? 1 : b.brands - a.brands));
manifest.verified = await verifyStock(plan);
const v = manifest.verified;
console.log(`[sync-catalog] stock check: ${v.checked}/${v.families} families (ok ${v.ok}, hidden-empty ${v.empty}, unresolved ${v.unresolved}, skipped ${v.skipped}, cooldowns ${v.cooldowns}, http ${JSON.stringify(v.status)})${v.aborted_on_errors ? ' — stopped early: supplier kept refusing' : ''}`);
for (const [cc, names] of Object.entries(v.hidden)) console.log(`[sync-catalog] hidden (no products) ${cc}: ${names.join(' | ')}`);
for (const f of fetched) {
  await writeFile(join(OUT, `${f.cc}.json`), JSON.stringify({ ...f.data, generated_at: manifest.generated_at }));
}
await writeFile(join(OUT, '..', 'manifest.json'), JSON.stringify(manifest, null, 1));
const secs = ((Date.now() - started) / 1000).toFixed(1);
console.log(`[sync-catalog] ${ok}/${targets.length} countries refreshed in ${secs}s; failed: ${manifest.failed.length}`);
if (manifest.failed.length) console.log('[sync-catalog] failed:', manifest.failed.slice(0, 12).map((f) => f.country).join(', '));
process.exit(ok > 0 ? 0 : 1);
