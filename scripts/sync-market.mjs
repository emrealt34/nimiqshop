#!/usr/bin/env node
/**
 * sync-market.mjs — bake the NIM/BTC price and the FX table into static JSON
 * (`public/data/market/`), refreshed by the SAME hourly Pages-deploy cron as
 * the catalog snapshot, so no page load has to ask the (often down) Go
 * backend for the price any more.
 *
 * WHAT IT MIRRORS
 * backend/internal/nimiq/oracle.go, exactly: four keyless sources per asset
 * (CoinGecko, CoinPaprika, MEXC, KuCoin — CryptoCompare only when
 * CRYPTOCOMPARE_API_KEY is set, same as the Go oracle), valid = positive
 * finite number, at least ORACLE_MIN_SOURCES (default 2) of them, median
 * (lower-middle average for even counts), and a spread guard of
 * ORACLE_MAX_SPREAD_BPS (default 250) between the extremes — a disagreeing
 * ticker must not move the shop's NIM price.
 *
 * FALLBACK CHAIN (owner's design: static first, API as the safety net)
 *   1. the four public sources, aggregated as above;
 *   2. if the aggregation fails, the live backend's /api/market/nim-rate
 *      (shopapi.nimiqbase.com) — when IT is the healthy one and the public
 *      aggregators are the ones misbehaving;
 *   3. otherwise the previous file is kept untouched (stale beats empty,
 *      the same rule the Go refresher follows) and the run reports it.
 *
 * The FX table is not fetched anywhere: backend/internal/catalog/fx.go holds
 * a curated code→USD map that changes only with code. This script PARSES
 * that map out of the Go source and re-emits it as fx.json, so the static
 * copy can never drift from the server's own table.
 *
 * Response shapes are byte-compatible with what the two endpoints return
 * (/market/nim-rate and /market/fx), plus generated_at/sync markers; the
 * frontend (src/lib/api.ts) reads these files first and only falls back to
 * /api when a file is missing.
 *
 * WHY THAT IS STILL SAFE: this file is only as fresh as the last deploy, and
 * the hourly Pages cron that runs it is a GitHub `schedule` — which this repo
 * has measured slipping to 5-hour gaps. So the frontend no longer TRUSTS the
 * snapshot for a number the buyer reads: it paints from it immediately and
 * then reconciles against the live API at most once per 10 minutes per tab
 * (src/lib/api.ts, onRatesChange). A late build costs a repaint, not a wrong
 * price.
 */
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const OUT = join(ROOT, 'public', 'data', 'market');

const MIN_SOURCES = Number(process.env.ORACLE_MIN_SOURCES || 2);
const MAX_SPREAD_BPS = Number(process.env.ORACLE_MAX_SPREAD_BPS || 250);
const BACKUP_BACKEND = String(process.env.SHOPAPI_FALLBACK || 'https://shopapi.nimiqbase.com').replace(/\/+$/, '');
const CC_KEY = String(process.env.CRYPTOCOMPARE_API_KEY || '').trim();

const num = (v) => {
  const n = typeof v === 'string' ? Number(v) : v;
  return typeof n === 'number' && Number.isFinite(n) && n > 0 ? n : null;
};

async function getJson(url, timeoutMs = 12_000) {
  const res = await fetch(url, { signal: AbortSignal.timeout(timeoutMs), headers: { Accept: 'application/json', 'User-Agent': 'nimshop/1.0 +https://shop.nimiqbase.com' } });
  if (!res.ok) throw new Error(`${url.split('/')[2]} HTTP ${res.status}`);
  return res.json();
}

/* One fetcher per source, field-for-field with oracle.go. */
const SOURCES = {
  nim: {
    coingecko: async () => num((await getJson('https://api.coingecko.com/api/v3/simple/price?ids=nimiq-2&vs_currencies=usd'))?.['nimiq-2']?.usd),
    coinpaprika: async () => num((await getJson('https://api.coinpaprika.com/v1/tickers/nim-nimiq'))?.quotes?.USD?.price),
    mexc: async () => num((await getJson('https://api.mexc.com/api/v3/ticker/price?symbol=NIMUSDT'))?.price),
    kucoin: async () => num((await getJson('https://api.kucoin.com/api/v1/market/orderbook/level1?symbol=NIM-USDT'))?.data?.price),
    ...(CC_KEY ? { cryptocompare: async () => num((await getJson(`https://min-api.cryptocompare.com/data/price?fsym=NIM&tsyms=USD&api_key=${encodeURIComponent(CC_KEY)}`))?.USD) } : {}),
  },
  btc: {
    coingecko: async () => num((await getJson('https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd'))?.bitcoin?.usd),
    coinpaprika: async () => num((await getJson('https://api.coinpaprika.com/v1/tickers/btc-bitcoin'))?.quotes?.USD?.price),
    mexc: async () => num((await getJson('https://api.mexc.com/api/v3/ticker/price?symbol=BTCUSDT'))?.price),
    kucoin: async () => num((await getJson('https://api.kucoin.com/api/v1/market/orderbook/level1?symbol=BTC-USDT'))?.data?.price),
    ...(CC_KEY ? { cryptocompare: async () => num((await getJson(`https://min-api.cryptocompare.com/data/price?fsym=BTC&tsyms=USD&api_key=${encodeURIComponent(CC_KEY)}`))?.USD) } : {}),
  },
};

/** Median + spread guard, copied from MultiSourceOracle.NIMUSD/BTCUSD. */
async function aggregate(asset) {
  const fns = SOURCES[asset];
  const names = Object.keys(fns);
  const settled = await Promise.all(names.map(async (n) => {
    try { return { n, v: await fns[n]() }; } catch (e) { return { n, v: null, e: String(e.message || e).slice(0, 80) }; }
  }));
  const values = {};
  const list = [];
  for (const r of settled) if (r.v !== null) { values[r.n] = r.v; list.push(r.v); }
  const failed = settled.filter((r) => r.v === null).map((r) => `${r.n}:${r.e || 'bad-value'}`);
  if (list.length < MIN_SOURCES) throw new Error(`only ${list.length} valid sources (${failed.join(', ')})`);
  list.sort((a, b) => a - b);
  const median = list.length % 2 ? list[(list.length - 1) / 2] : (list[list.length / 2 - 1] + list[list.length / 2]) / 2;
  const spread = Math.round(((list[list.length - 1] - list[0]) / median) * 10000);
  if (spread > MAX_SPREAD_BPS) throw new Error(`oracle disagreement: ${spread} bps > ${MAX_SPREAD_BPS}`);
  return { median, sources: list.length, spread, values };
}

/** Parse the curated usdPerUnit map straight out of the Go source. */
async function fxTableFromGo() {
  const src = await readFile(join(ROOT, 'backend', 'internal', 'catalog', 'fx.go'), 'utf8');
  const start = src.indexOf('var usdPerUnit = map[string]float64{');
  if (start < 0) throw new Error('usdPerUnit map not found in fx.go');
  const open = src.indexOf('{', start);
  let depth = 0;
  let end = -1;
  for (let i = open; i < src.length; i += 1) {
    if (src[i] === '{') depth += 1;
    else if (src[i] === '}') { depth -= 1; if (depth === 0) { end = i; break; } }
  }
  if (end < 0) throw new Error('usdPerUnit map brace mismatch');
  const body = src.slice(open + 1, end);
  const table = {};
  for (const m of body.matchAll(/"([A-Z]{3})":\s*([0-9.eE+-]+)/g)) table[m[1]] = Number(m[2]);
  if (Object.keys(table).length < 50) throw new Error(`fx table too small (${Object.keys(table).length})`);
  return table;
}

await mkdir(OUT, { recursive: true });
const generatedAt = new Date().toISOString();
const report = { generated_at: generatedAt, nim: null, btc: null, fx: null, fallbacks: [] };

// ---- NIM + BTC, in parallel ------------------------------------------------
const [nimRes, btcRes] = await Promise.allSettled([aggregate('nim'), aggregate('btc')]);
let nim = nimRes.status === 'fulfilled' ? nimRes.value : null;
let btc = btcRes.status === 'fulfilled' ? btcRes.value : null;
if (nimRes.status === 'rejected') report.fallbacks.push(`nim-oracle: ${nimRes.reason.message}`);
if (btcRes.status === 'rejected') report.fallbacks.push(`btc-oracle: ${btcRes.reason.message}`);

// ---- backend as the safety net when the public aggregators fail -----------
if (!nim || !btc) {
  try {
    const snap = await getJson(`${BACKUP_BACKEND}/api/market/nim-rate`, 15_000);
    if (!nim && num(snap?.usd_per_nim)) {
      nim = { median: snap.usd_per_nim, sources: snap.sources || 0, spread: null, values: {}, via: 'shopapi' };
      report.fallbacks.push('nim from shopapi /market/nim-rate');
    }
    if (!btc && num(snap?.usd_per_btc)) {
      btc = { median: snap.usd_per_btc, sources: snap.btc_sources || 0, spread: null, values: {}, via: 'shopapi' };
      report.fallbacks.push('btc from shopapi /market/nim-rate');
    }
  } catch (e) {
    report.fallbacks.push(`shopapi fallback: ${String(e.message || e).slice(0, 80)}`);
  }
}

// ---- write nim-rate.json (keep previous on total failure) ------------------
let wroteRate = false;
if (nim && nim.median > 0) {
  const body = {
    usd_per_nim: nim.median,
    observed_at: generatedAt,
    sources: nim.sources,
    cached: true,
    age_seconds: 0,
    generated_at: generatedAt,
    sync: 'static-hourly',
  };
  if (btc && btc.median > 0) {
    body.usd_per_btc = btc.median;
    body.btc_observed_at = generatedAt;
    body.btc_sources = btc.sources;
  }
  await writeFile(join(OUT, 'nim-rate.json'), JSON.stringify(body));
  wroteRate = true;
  report.nim = { usd_per_nim: nim.median, sources: nim.sources, spread_bps: nim.spread, via: nim.via || 'oracle' };
  if (btc) report.btc = { usd_per_btc: btc.median, sources: btc.sources, spread_bps: btc.spread, via: btc.via || 'oracle' };
} else {
  report.nim = 'kept-previous-file';
}

// ---- fx.json from the Go table (never fetched, never drifted) --------------
try {
  const table = await fxTableFromGo();
  await writeFile(join(OUT, 'fx.json'), JSON.stringify({ base: 'USD', usd_per_unit: table, generated_at: generatedAt, sync: 'static-hourly' }));
  report.fx = `${Object.keys(table).length} currencies from backend/internal/catalog/fx.go`;
} catch (e) {
  report.fx = `kept-previous (${String(e.message || e).slice(0, 80)})`;
}

await writeFile(join(OUT, 'manifest.json'), JSON.stringify(report, null, 1));
console.log(`[sync-market] nim=${report.nim && report.nim.usd_per_nim ? report.nim.usd_per_nim.toFixed(6) : report.nim} btc=${report.btc && report.btc.usd_per_btc ? report.btc.usd_per_btc.toFixed(2) : report.btc} fx=${report.fx}`);
if (report.fallbacks.length) console.log('[sync-market] notes:', report.fallbacks.join(' | '));
process.exit(wroteRate ? 0 : 1);
