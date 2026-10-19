/**
 * test-address-chip.mjs — the operator's wallet row: an identicon face next
 * to an address written in GROUPS OF FOUR.
 *
 * Owner (2026-10-19): "herkesi avatarlarıyla ve 4 hane ayrılmış şekilde
 * görmek siterdim" — quiz participants, staker ledgers, admin orders and
 * ratings all render the same chip, so the chip itself is what gets pinned
 * here: a real face for a real address, no invented face for anything else,
 * and never a group of five or a run-on string of 36 characters.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

const dir = mkdtempSync(join(tmpdir(), 'address-chip-'));
const bundle = join(dir, 'bundle.cjs');

// The entry is fed on stdin with resolveDir at the repository root, which is
// how every other unit test here bundles a module: node_modules is then found
// from the project, not from the scratch file's directory.
buildSync({
  stdin: {
    contents: `
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
globalThis.window = { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
globalThis.document = { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { renderToStaticMarkup } from 'react-dom/server';
export { createElement } from 'react';
export { AddressChip } from ${JSON.stringify(new URL('../src/components/ui/AddressChip.tsx', import.meta.url).pathname)};
export { formatWalletAddress } from ${JSON.stringify(new URL('../src/lib/format.ts', import.meta.url).pathname)};
`,
    resolveDir: new URL('..', import.meta.url).pathname,
    sourcefile: 'address-chip-entry.tsx',
    loader: 'tsx',
  },
  bundle: true,
  platform: 'node',
  format: 'cjs',
  outfile: bundle,
  jsx: 'automatic',
  define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' },
  loader: { '.css': 'empty' },
  logLevel: 'silent',
});

const client = createRequire(import.meta.url)(bundle);
const html = (props) => client.renderToStaticMarkup(client.createElement(client.AddressChip, props));
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const WALLET = 'NQ242XCDPK1C7DR525U53PERU64VUSR2LPRK';
const GROUPED = 'NQ24 2XCD PK1C 7DR5 25U5 3PER U64V USR2 LPRK';

/** The text an operator actually reads (the <code> body), never the tooltip. */
function chipText(out) {
  const match = /<code[^>]*>([\s\S]*?)<\/code>/.exec(out);
  return match ? match[1] : '';
}

test('an address is shown with its face and in groups of four', () => {
  const out = html({ address: WALLET });
  assert.equal(chipText(out), GROUPED, `expected the grouped address, got: ${out}`);
  assert.ok(out.includes('<img'), `expected an identicon face, got: ${out}`);
  assert.ok(out.includes('addr-chip-face'), 'the face is the identicon image');
  for (const group of chipText(out).split(' ')) {
    assert.equal(group.length, 4, 'every group is four characters');
  }
});

test('an address that arrives already spaced is normalized, not double-spaced', () => {
  const spaced = 'NQ24 2XCDPK1C 7DR525U5 3PERU64V USR2LPRK';
  assert.equal(client.formatWalletAddress(spaced, 4), GROUPED);
  assert.equal(chipText(html({ address: spaced })), GROUPED);
});

test('a wallet stored in lower case still gets its face', () => {
  const out = html({ address: WALLET.toLowerCase() });
  assert.ok(out.includes('<img'), 'case must not decide whether a buyer has a face');
});

test('something that is not an address gets no invented face', () => {
  const out = html({ address: 'user-a1b2c3', fallback: 'user-a1b2c3' });
  assert.equal(chipText(out), 'user-a1b2c3');
  assert.ok(!out.includes('<img'), 'a user id is not a wallet and must not be given an identicon');
  // A participant with no wallet at all still shows something.
  const empty = html({});
  assert.equal(chipText(empty), '\u2014', 'an empty row shows a dash, never an empty box');
  assert.ok(!empty.includes('<img'));
});

test('copy is offered only when there is an address to copy', () => {
  assert.ok(html({ address: WALLET, copy: true }).includes('copy-btn'), 'the operator can copy the address');
  assert.ok(!html({ address: WALLET }).includes('copy-btn'), 'rows stay quiet unless copy is asked for');
  assert.ok(!html({ fallback: 'user-x', copy: true }).includes('copy-btn'), 'nothing to copy without an address');
});
