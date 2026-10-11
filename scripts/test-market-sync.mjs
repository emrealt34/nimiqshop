import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { providerFX, backendFX, fxSnapshot } from './market-fx.mjs';

const now = Date.now();
const at = new Date(now - 3600_000).toISOString();
const rates = Object.fromEntries(Array.from({ length: 55 }, (_, i) => ['A' + String.fromCharCode(65 + Math.floor(i / 26)) + String.fromCharCode(65 + i % 26), 2]));
Object.assign(rates, { USD: 1, TRY: 50, EUR: 0.9 });
const feed = { result: 'success', base_code: 'USD', time_last_update_unix: Math.floor((now - 3600_000) / 1000), rates };

test('fiat inversion retains provider time and identifies baseline-only currencies', () => {
  const live = providerFX(feed, now);
  assert.equal(live.rates.TRY, 0.02);
  assert.equal(live.rates.USD, 1);
  assert.equal(live.at, new Date(feed.time_last_update_unix * 1000).toISOString());
  const snapshot = fxSnapshot(live, { USD: 1, TRY: 0.01, ZWL: 0.04 }, null, new Date(now).toISOString());
  assert.equal(snapshot.usd_per_unit.TRY, 0.02);
  assert.deepEqual(snapshot.fallback_currencies, ['ZWL']);
  assert.equal(snapshot.live_age_seconds, 3600);
});

test('partial, wrongly based, future, stale and older replacement FX responses are rejected', () => {
  for (const bad of [
    { ...feed, rates: { USD: 1, TRY: 50 } },
    { ...feed, base_code: 'EUR' },
    { ...feed, rates: { ...rates, USD: 2 } },
    { ...feed, rates: { ...rates, TRY: 0 } },
    { ...feed, time_last_update_unix: Math.floor(now / 1000) + 86400 },
    { ...feed, time_last_update_unix: Math.floor(now / 1000) - 8 * 86400 },
  ]) assert.throws(() => providerFX(bad, now));
  assert.throws(() => backendFX({ base: 'USD', usd_per_unit: rates, live_source: 'embedded', live_currencies: 58, live_observed_at: at }, now));
  assert.throws(() => fxSnapshot(providerFX(feed, now), {}, { live_observed_at: new Date(now).toISOString() }, new Date(now).toISOString()));
});

async function sync(options) {
  const dir = await mkdtemp(join(tmpdir(), 'nimiqshop-market-test-'));
  try {
    const previous = { usd_per_nim: 0.001, observed_at: at, sources: 3, usd_per_btc: 80000, btc_observed_at: at, btc_sources: 3 };
    const previousFX = { base: 'USD', usd_per_unit: { USD: 1, TRY: 0.019 }, live_observed_at: new Date(now - 86400_000).toISOString(), generated_at: at };
    await writeFile(join(dir, 'nim-rate.json'), JSON.stringify(previous));
    await writeFile(join(dir, 'fx.json'), JSON.stringify(previousFX));
    const loader = join(dir, 'fetch.mjs');
    await writeFile(loader, `const options = ${JSON.stringify(options)};
      const feed = ${JSON.stringify(feed)};
      globalThis.fetch = async (url) => {
        if (String(url).includes('open.er-api.com')) return new Response(JSON.stringify(feed), {status: options.fxFails ? 503 : 200});
        if (String(url).includes('/api/market/')) return new Response('{}', {status: 503});
        if (String(url).includes('nim-nimiq')) return new Response(JSON.stringify({quotes:{USD:{price:0.002}}}),{status: options.cryptoFails ? 503 : 200});
        if (String(url).includes('NIMUSDT')) return new Response(JSON.stringify({price:'0.002'}),{status: options.cryptoFails ? 503 : 200});
        return new Response('{}', {status: 503});
      };`);
    const result = spawnSync(process.execPath, ['--import', loader, 'scripts/sync-market.mjs'], {
      cwd: new URL('..', import.meta.url), encoding: 'utf8', timeout: 20000,
      env: { ...process.env, MARKET_OUT_DIR: dir, FX_RATES_URL: 'https://open.er-api.com/v6/latest/USD', CRYPTOCOMPARE_API_KEY: '', ORACLE_MIN_SOURCES: '2', ORACLE_MAX_SPREAD_BPS: '250' },
    });
    assert.equal(result.error, undefined);
    return { code: result.status, previous, previousFX,
      nim: JSON.parse(await readFile(join(dir, 'nim-rate.json'), 'utf8')),
      fxBytes: await readFile(join(dir, 'fx.json'), 'utf8'),
      report: JSON.parse(await readFile(join(dir, 'manifest.json'), 'utf8')) };
  } finally { await rm(dir, { recursive: true, force: true }); }
}

test('real sync updates TRY and NIM while preserving a failed BTC leg and its timestamp', async () => {
  const result = await sync({});
  assert.equal(result.code, 0);
  assert.equal(result.nim.usd_per_nim, 0.002);
  assert.equal(result.nim.usd_per_btc, result.previous.usd_per_btc);
  assert.equal(result.nim.btc_observed_at, result.previous.btc_observed_at);
  assert.equal(JSON.parse(result.fxBytes).usd_per_unit.TRY, 0.02);
  assert.equal(result.report.fx.source, 'open.er-api.com');
});

test('real sync still saves fresh fiat rates during a complete crypto outage', async () => {
  const result = await sync({ cryptoFails: true });
  assert.equal(result.code, 0);
  assert.deepEqual(result.nim, result.previous);
  assert.equal(JSON.parse(result.fxBytes).usd_per_unit.TRY, 0.02);
  assert.equal(result.report.nim, 'kept-previous-file');
});

test('total FX outage keeps previous file byte-for-byte and is reported', async () => {
  const result = await sync({ fxFails: true, cryptoFails: true });
  assert.equal(result.code, 1);
  assert.equal(result.fxBytes, JSON.stringify(result.previousFX));
  assert.deepEqual(result.nim, result.previous);
  assert.equal(result.report.fx, 'kept-previous-file');
  assert(result.report.fallbacks.some(note => note.startsWith('fx:')));
});
