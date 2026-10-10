import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, copyFile, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { promisify } from 'node:util';
import { execFile } from 'node:child_process';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';
import { stockState, validStockRecord, applyStockState, RECHECK_AFTER_MS, STOCK_VERSION } from './catalog-stock.mjs';

const live = { products: [{ product_id: 'sku', delivery_type: 'by_email' }] };
test('prefetch stock survives navigation, isolates countries, expires and recovers', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'catalog-stock-client-'));
  const clock = Date.now;
  try {
    const bundle = join(dir, 'stock.cjs');
    buildSync({ entryPoints: [new URL('../src/lib/catalogStock.ts', import.meta.url).pathname], bundle: true,
      platform: 'node', format: 'cjs', outfile: bundle,
      define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, logLevel: 'silent' });
    const items = new Map();
    globalThis.sessionStorage = { getItem: (k) => items.get(k) || null, setItem: (k, v) => items.set(k, v) };
    const require = createRequire(import.meta.url);
    let stock = require(bundle);
    let changes = 0;
    const off = stock.onStockChange(() => changes++);
    const now = clock();
    Date.now = () => now;
    stock.recordAvailability('Sold Card', 'tr', [{ ...live, is_out_of_stock: true }]);
    stock.recordAvailability('Sold Card', 'tr', [{ ...live, is_out_of_stock: true }]);
    assert.equal(changes, 1, 'unchanged warm results do not rerender the entire grid');
    assert.equal(stock.observedAvailability('SOLD CARD', 'TR'), 'unavailable');
    assert.equal(stock.observedAvailability('Sold Card', 'DE'), 'unknown');
    const p = { id: 'Sold Card', family: 'Sold Card', country: 'TR', in_stock: true, is_out_of_stock: false };
    assert.equal(stock.applyObservedStock([p])[0].in_stock, false);
    stock.recordAvailability('Sold Card', 'TR', { error: 'timeout' });
    assert.equal(stock.observedAvailability('Sold Card', 'TR'), 'unavailable', 'transport errors cannot change stock');
    off();
    delete require.cache[require.resolve(bundle)];
    stock = require(bundle);
    assert.equal(stock.observedAvailability('Sold Card', 'TR'), 'unavailable', 'reload restores observations');
    stock.recordAvailability('Sold Card', 'TR', [live]);
    assert.equal(stock.applyObservedStock([p])[0].in_stock, true);
    assert.equal(stock.applyObservedStock([{ ...p, in_stock: false, reported_oos: true }])[0].in_stock, false);
    Date.now = () => now + 5 * 60_000 + 1;
    assert.equal(stock.observedAvailability('Sold Card', 'TR'), 'unknown');
  } finally { Date.now = clock; delete globalThis.sessionStorage; await rm(dir, { recursive: true, force: true }); }
});

test('stock uses purchasable inventory, including merged supplier variants', () => {
  for (const [payload, want] of [
    [[live], 'ok'],
    [[{ ...live, is_out_of_stock: true }], 'empty'],
    [[{ products: [] }], 'empty'],
    [[], 'empty'],
    [[{ products: [{ product_id: 'bill', delivery_type: 'by_account' }] }], 'empty'],
    [[{ products: [{ product_id: 'sold-sku', is_out_of_stock: true }] }], 'empty'],
    [[{ products: [{ range: { min: 5, max: 20 } }] }], 'ok'],
    [[{ products: [{ range: { min: 20, max: 5 } }] }], 'empty'],
    [[{ ...live, is_out_of_stock: true }, live], 'ok'],
    [[{ products: [] }, live], 'ok'],
    [[{}], 'unknown'],
    [[{ products: [] }, {}], 'unknown'],
    [null, 'unknown'],
    [{ error: 'upstream failure' }, 'unknown'],
  ]) assert.equal(stockState(payload), want, JSON.stringify(payload));
});

test('presence-only state is invalidated; stock expires hourly and recovers', () => {
  const now = Date.now();
  assert.equal(validStockRecord({ state: 'ok', at: now }), false);
  assert.equal(validStockRecord({ state: 'ok', at: now + 120_000, version: STOCK_VERSION }), false);
  const data = { categories: [{ brands: [
    { family: 'sold', is_out_of_stock: false },
    { family: 'recovered', is_out_of_stock: true },
    { family: 'expired', is_out_of_stock: false },
  ] }] };
  applyStockState(data, 'TR', {
    'TR|sold': { state: 'empty', at: now, version: STOCK_VERSION },
    'TR|recovered': { state: 'ok', at: now, version: STOCK_VERSION },
    'TR|expired': { state: 'empty', at: now - RECHECK_AFTER_MS, version: STOCK_VERSION },
  }, now);
  assert.deepEqual(data.categories[0].brands.map((b) => b.is_out_of_stock), [true, false, false]);
  assert.equal(data.categories[0].brands[0].stock_checked_at, new Date(now).toISOString());
});

test('the real sync script persists stock and preserves a failed country without freshening it', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'catalog-sync-'));
  try {
    for (const d of ['scripts/data', 'src/lib', 'public/data/catalog/brands']) await mkdir(join(dir, d), { recursive: true });
    for (const f of ['scripts/sync-catalog.mjs', 'scripts/catalog-stock.mjs', 'src/lib/catalogAvailability.ts']) {
      await copyFile(new URL('../' + f, import.meta.url), join(dir, f));
    }
    await writeFile(join(dir, 'package.json'), '{"type":"module"}');
    await writeFile(join(dir, 'src/lib/countries.ts'), "[['TR', 'Turkey'], ['DE', 'Germany']]");
    const old = { generated_at: '2020-01-01T00:00:00Z', categories: [{ brands: [{ family: 'previous' }] }] };
    await writeFile(join(dir, 'public/data/catalog/brands/DE.json'), JSON.stringify(old));
    await writeFile(join(dir, 'scripts/data/stock-state.json'), JSON.stringify({
      'TR|sold': { state: 'ok', at: Date.now() }, // old presence-only result
    }));
    const preload = join(dir, 'mock-fetch.mjs');
    await writeFile(preload, `
      import { appendFile } from 'node:fs/promises';
      const families = ['live', 'sold', 'empty', 'account', 'mixed'];
      globalThis.fetch = async (input) => {
        const u = new URL(input);
        if (u.pathname === '/v2/brands') {
          if (u.searchParams.get('country_code') === 'DE') return Response.json({error:'bad-shape'});
          return Response.json({country_code:'TR',categories:[{kind:'giftcard',brands:families.map(family=>({family,is_out_of_stock:false}))}]});
        }
        await appendFile(${JSON.stringify(join(dir, 'calls'))}, u.href+'\\n');
        const f = u.searchParams.get('family_name');
        const live = {family:f,products:[{product_id:'sku',delivery_type:'by_email'}]};
        return Response.json(f === 'empty' ? [] : f === 'sold' ? [{...live,is_out_of_stock:true}] : f === 'account' ? [{family:f,products:[{product_id:'bill',delivery_type:'by_account'}]}] : f === 'mixed' ? [{...live,is_out_of_stock:true},live] : [live]);
      };
    `);
    const exec = promisify(execFile);
    const env = { ...process.env, CRYPTOREFILLS_BASE_URL: 'https://supplier.example.test', CATALOG_VERIFY_INTERVAL_MS: '1', CATALOG_VERIFY_BUDGET_MS: '15000', CATALOG_STOCK_STATE: join(dir, 'state.json') };
    await exec(process.execPath, ['--import', preload, join(dir, 'scripts/sync-catalog.mjs')], { env, timeout: 30_000 });
    const tr = JSON.parse(await readFile(join(dir, 'public/data/catalog/brands/TR.json')));
    assert.deepEqual(tr.categories[0].brands.map((b) => b.is_out_of_stock), [false, true, true, true, false]);
    const global = JSON.parse(await readFile(join(dir, 'public/data/catalog/brands/_global.json')));
    assert.deepEqual(global.categories[0].brands, tr.categories[0].brands);
    assert.deepEqual(JSON.parse(await readFile(join(dir, 'public/data/catalog/brands/DE.json'))), old);
    const calls = await readFile(join(dir, 'calls'), 'utf8');
    assert.match(calls, /family_name=sold/);
    assert.match(calls, /coin=BTC/);
    const manifest = JSON.parse(await readFile(join(dir, 'public/data/catalog/manifest.json')));
    assert.equal(manifest.verified.empty, 3);
    assert.equal(manifest.failed[0].country, 'DE');
    await exec(process.execPath, ['--import', preload, join(dir, 'scripts/sync-catalog.mjs')], { env, timeout: 30_000 });
    assert.equal(await readFile(join(dir, 'calls'), 'utf8'), calls, 'fresh verified records avoid duplicate stock calls');
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('hourly snapshots persist and restore without changing protected main', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'catalog-data-git-'));
  const exec = promisify(execFile);
  const script = new URL('./catalog-data.mjs', import.meta.url).pathname;
  try {
    const root = join(dir, 'checkout');
    const remote = join(dir, 'remote.git');
    await mkdir(root);
    const git = async (args, cwd = root) => (await exec('git', args, { cwd })).stdout.trim();
    await git(['init', '--bare', remote], dir);
    await git(['init', '--initial-branch=main']);
    await git(['config', 'user.name', 'Test']);
    await git(['config', 'user.email', 'test@example.test']);
    await git(['remote', 'add', 'origin', remote]);
    for (const [path, value] of [
      ['public/data/catalog/brands/TR.json', { categories: [] }],
      ['public/data/catalog/manifest.json', { generated_at: 'original' }],
      ['public/data/market/fx.json', { base: 'USD' }],
      ['scripts/data/stock-state.json', {}],
    ]) {
      await mkdir(join(root, dirname(path)), { recursive: true });
      await writeFile(join(root, path), JSON.stringify(value));
    }
    await writeFile(join(root, 'application.txt'), 'application code');
    await git(['add', '.']);
    await git(['commit', '-m', 'seed']);
    await git(['push', 'origin', 'main']);
    const head = await git(['rev-parse', 'HEAD']);
    await writeFile(join(remote, 'hooks/pre-receive'), '#!/bin/sh\nwhile read old new ref; do\n  if [ "$ref" = refs/heads/main ]; then exit 1; fi\ndone\n', { mode: 0o755 });
    await writeFile(join(root, 'public/data/catalog/manifest.json'), '{"generated_at":"fresh"}');
    const env = { ...process.env, GITHUB_TOKEN: 'test-token', RUNNER_TEMP: dir };
    await exec(process.execPath, [script, 'persist'], { cwd: root, env });
    assert.equal(await git(['rev-parse', 'refs/heads/main'], remote), head);
    const files = await git(['ls-tree', '-r', '--name-only', 'catalog-data'], remote);
    assert.ok(!files.includes('application.txt'), 'snapshot branch contains no application code');
    assert.match(files, /stock-state.json/);
    await writeFile(join(root, 'public/data/catalog/manifest.json'), '{}');
    await exec(process.execPath, [script, 'restore'], { cwd: root, env });
    assert.equal(JSON.parse(await readFile(join(root, 'public/data/catalog/manifest.json'))).generated_at, 'fresh');
    await writeFile(join(root, 'public/data/catalog/manifest.json'), '{"generated_at":"newer"}');
    await exec(process.execPath, [script, 'persist'], { cwd: root, env });
    assert.equal(await git(['rev-list', '--count', 'catalog-data'], remote), '2', 'hourly history is retained');
    assert.equal(await git(['rev-parse', 'refs/heads/main'], remote), head);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
