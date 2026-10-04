/**
 * Unit tests for the pay-path error copy — the batch-5 regression guard.
 *
 *   node --test scripts/test-friendly-api-message.mjs
 *
 * Why this exists: on 2026-10-04 the owner pressed "Nimiq Pay ile öde" on the
 * live shop and got "shop.nimiqbase.com şu anda erişilemiyor" while the site
 * was perfectly healthy (payment-launch answered 200 in 374 ms). The wallet
 * had reported a network problem of its own; `friendlyApiMessage` flattened
 * every plain Error (no `.status` ⇒ Number(undefined) ⇒ 0) into the
 * site-unreachable sentence. The rule is now shape-based attribution, and it
 * is pinned here.
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
const dir = mkdtempSync(path.join(tmpdir(), 'friendly-api-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.cjs');

writeFileSync(entry, `
(globalThis as any).localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };
(globalThis as any).window ??= { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
(globalThis as any).document ??= { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { friendlyApiMessage, ApiError } from ${JSON.stringify(path.join(root, 'src/lib/api'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=cjs', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr || 'esbuild failed to bundle src/lib/api.ts');
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const require = createRequire(import.meta.url);
const { friendlyApiMessage, ApiError } = require(bundle);

const FALLBACK = 'SOMETHING WENT WRONG — FALLBACK COPY';
/** The "our site is unreachable" sentence in every language we ship. */
const BLAMES_THE_SITE = /cannot reach|erişilemiyor|nicht erreichbar|no se puede llegar|impossible d'atteindre|não é possível alcançar/i;

const say = (err) => friendlyApiMessage(err, FALLBACK);

test('a wallet refusal is never reworded as "our site is unreachable"', () => {
  const out = say(new Error('User rejected'));
  assert.match(out, /User rejected/);
  assert.doesNotMatch(out, BLAMES_THE_SITE);
});

test('the Nimiq Pay provider timeout keeps its own words', () => {
  const out = say(new Error('Nimiq provider was not injected. Are you running inside a Nimiq app?'));
  assert.match(out, /Nimiq provider was not injected/);
  assert.doesNotMatch(out, BLAMES_THE_SITE);
});

test('a wallet -32000 NETWORK_ERROR is the wallet\'s sentence, not ours', () => {
  // Exactly what the mini-app SDK throws: NimiqProviderError(type, message).
  const out = say(Object.assign(new Error('Network error'), { name: 'NimiqProviderError', type: 'NETWORK_ERROR', code: -32000 }));
  assert.match(out, /Network error/);
  assert.doesNotMatch(out, BLAMES_THE_SITE);
});

test('a raw browser fetch failure IS attributed to the site (honest case)', () => {
  const out = say(new TypeError('Failed to fetch'));
  assert.match(out, BLAMES_THE_SITE);
  assert.notEqual(out, FALLBACK);
});

test('our own transport failure (ApiError status 0) keeps the site copy', () => {
  const out = say(new ApiError(0, 'Cannot reach shop.nimiqbase.com right now. Check your connection and try again.'));
  assert.match(out, BLAMES_THE_SITE);
});

test('5xx still reads as "shop temporarily unavailable", not as unreachable', () => {
  const out = say(new ApiError(500, 'internal server error'));
  assert.notEqual(out, FALLBACK);
  assert.doesNotMatch(out, BLAMES_THE_SITE);
});

test('a business-rule API error passes through untouched', () => {
  assert.equal(say(new ApiError(409, 'This order is no longer payable.')), 'This order is no longer payable.');
});

test('an empty error falls back to the caller\'s copy', () => {
  assert.equal(say(new Error('')), FALLBACK);
});

test('a long unhelpful server message falls back to the caller\'s copy', () => {
  assert.equal(say(new ApiError(400, 'x'.repeat(200))), FALLBACK);
});
