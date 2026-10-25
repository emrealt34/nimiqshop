/**
 * Unit tests for the diagnostics gate (2026-10-25).
 *
 *   node --test scripts/test-debug-flags.mjs
 *
 * Why this exists: the wallet strip printed its whole reading — wallet
 * address, host accounts, luna figures, raw error strings — in a "Balance
 * debug" block for EVERY buyer inside Nimiq Pay (owner: "Balance debug satırı
 * kullanıcıya açık görünüyor"). The diagnostics are still needed for support,
 * so they became opt-in instead of being deleted. These tests pin that the
 * default is OFF for every shape of visitor, and that both opt-ins work —
 * including from a phone inside Nimiq Pay, where `?nimshopDebug=1` is the only
 * reachable switch.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dir = mkdtempSync(path.join(tmpdir(), 'debug-flags-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.cjs');

writeFileSync(entry, `
export { debugEnabled, DEBUG_PARAM, DEBUG_STORAGE_KEY } from ${JSON.stringify(path.join(root, 'src/lib/debugFlags'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=cjs', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, `esbuild failed: ${built.stderr || built.stdout}`);

const require = createRequire(import.meta.url);
const { debugEnabled, DEBUG_PARAM, DEBUG_STORAGE_KEY } = require(bundle);
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

/* A tiny fake browser surface, rebuilt for every case. */
function fakeBrowser({ search = '', stored = null, storageThrows = false } = {}) {
  const g = globalThis;
  g.window = { location: { search } };
  g.localStorage = {
    getItem(key) {
      if (storageThrows) throw new Error('SecurityError: storage disabled');
      return key === DEBUG_STORAGE_KEY ? stored : null;
    },
  };
}

const cleanUp = () => {
  const g = globalThis;
  delete g.window;
  delete g.localStorage;
};

test('no window (SSR / first paint) means no diagnostics', () => {
  cleanUp();
  assert.equal(debugEnabled(), false);
});

test('a plain visitor — no parameter, no stored flag — sees nothing', () => {
  fakeBrowser();
  assert.equal(debugEnabled(), false);
  cleanUp();
});

test('?nimshopDebug=1 turns them on (the phone-in-Nimiq-Pay route)', () => {
  fakeBrowser({ search: `?${DEBUG_PARAM}=1` });
  assert.equal(debugEnabled(), true);
  cleanUp();
});

test('?nimshopDebug=true works too, and other values do not', () => {
  for (const [value, want] of [['true', true], ['TRUE', true], ['0', false], ['', false], ['yes', false], ['2', false]]) {
    fakeBrowser({ search: `?${DEBUG_PARAM}=${value}` });
    assert.equal(debugEnabled(), want, `${DEBUG_PARAM}=${value}`);
    cleanUp();
  }
});

test('the stored flag is a sticky opt-in, and only "1" counts', () => {
  fakeBrowser({ stored: '1' });
  assert.equal(debugEnabled(), true);
  cleanUp();
  for (const stored of ['0', 'true', '', 'yes']) {
    fakeBrowser({ stored });
    assert.equal(debugEnabled(), false, `stored ${JSON.stringify(stored)}`);
    cleanUp();
  }
});

test('the URL wins even when storage is disabled (private mode)', () => {
  fakeBrowser({ search: `?${DEBUG_PARAM}=1`, storageThrows: true });
  assert.equal(debugEnabled(), true);
  cleanUp();
  fakeBrowser({ storageThrows: true });
  assert.equal(debugEnabled(), false, 'a storage error must not enable diagnostics');
  cleanUp();
});

test('a broken location does not throw', () => {
  const g = globalThis;
  g.window = { get location() { throw new Error('nope'); } };
  g.localStorage = { getItem: () => null };
  assert.equal(debugEnabled(), false);
  cleanUp();
});
