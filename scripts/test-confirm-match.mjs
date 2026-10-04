/**
 * Unit tests for the double-entry checkout fields (batch 6).
 *
 *   node --test scripts/test-confirm-match.mjs
 *
 * Why this exists: the delivery email and the top-up number cannot be undone
 * once the order is created — a typo mails the code to a stranger or credits
 * someone else's line. Both are therefore typed TWICE and the second entry
 * must agree with the first. The comparison rules (case-insensitive email,
 * digits-only phone) are pinned here so a future "helpful" change cannot
 * silently start rejecting "0555 123 45 67" against "05551234567" — or, worse,
 * start accepting two different addresses.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dir = mkdtempSync(path.join(tmpdir(), 'confirm-match-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.mjs');

writeFileSync(entry, `
(globalThis as any).localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };
(globalThis as any).window ??= { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
(globalThis as any).document ??= { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { confirmKey, confirmMatches } from ${JSON.stringify(path.join(root, 'src/lib/validate'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=esm', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, `esbuild failed: ${built.stderr || built.stdout}`);

const { confirmKey, confirmMatches } = await import(pathToFileURL(bundle).href);
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

/* ------------------------------- email ---------------------------------- */
test('email: the case of an address never blocks the buyer', () => {
  assert.equal(confirmMatches('Buyer@Gmail.com', 'buyer@gmail.com', 'email'), true);
  assert.equal(confirmMatches('  buyer@gmail.com  ', 'buyer@gmail.com', 'email'), true);
});

test('email: one different character is a mismatch', () => {
  assert.equal(confirmMatches('buyer@gmail.com', 'buyer@gmail.co', 'email'), false);
  assert.equal(confirmMatches('buyer@gmail.com', 'buyer2@gmail.com', 'email'), false);
  assert.equal(confirmMatches('buyer@gmail.com', 'buyer@gnail.com', 'email'), false);
});

/* ------------------------------- phone ---------------------------------- */
test('phone: formatting differences are the same number', () => {
  assert.equal(confirmMatches('0555 123 45 67', '05551234567', 'phone'), true);
  assert.equal(confirmMatches('0555-123-45-67', '0555 123 4567', 'phone'), true);
  assert.equal(confirmMatches('+90 555 123 45 67', '905551234567', 'phone'), true);
});

test('phone: a wrong digit is a mismatch', () => {
  assert.equal(confirmMatches('0555 123 45 67', '0555 123 45 68', 'phone'), false);
  assert.equal(confirmMatches('0555 123 45 67', '0555 123 45 6', 'phone'), false);
});

test('phone: letters in the confirmation cannot pass as digits', () => {
  assert.equal(confirmMatches('05551234567', 'o5551234567', 'phone'), false);
});

/* -------------------------------- text ---------------------------------- */
test('text: compared verbatim after trimming', () => {
  assert.equal(confirmMatches('hello', ' hello ', 'text'), true);
  assert.equal(confirmMatches('Hello', 'hello', 'text'), false);
});

/* ------------------------------- keys ----------------------------------- */
test('confirmKey normalises each kind as documented', () => {
  assert.equal(confirmKey(' A@B.C ', 'email'), 'a@b.c');
  assert.equal(confirmKey('(0555) 123 45 67', 'phone'), '05551234567');
  assert.equal(confirmKey(' x ', 'text'), 'x');
});

test('empty entries compare equal here — "required" is the callers\u2019 rule', () => {
  // The checkout validates emptiness with its own copy (`validate.emailRequired`
  // / `delivery.enterPhone`); this helper must not invent a second, conflicting
  // error message for the same field.
  assert.equal(confirmMatches('', '   ', 'email'), true);
  assert.equal(confirmMatches('', '', 'phone'), true);
});
