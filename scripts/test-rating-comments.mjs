/**
 * test-rating-comments.mjs — the public buyer comments on the Activity page.
 *
 * Owner (2026-10-19): a comment was entered and the block "looked wrong" in
 * Activity. Two real defects are pinned here:
 *
 *  1. the list was rendered INSIDE the rating summary's two-column grid, so
 *     it was dropped into the left column and squeezed into a third of the
 *     card. It belongs under the grid, full width (see the .rs-comments
 *     rule and ActivityPage).
 *  2. a comment had no face, no date and no product — just stars glued to a
 *     run-on line of text with `align-items: baseline`, which is what made
 *     long comments stretch the whole block sideways.
 *
 * And the privacy rule the whole thing rests on: an anonymous buyer's
 * comment shows NO wallet, ever — not even the identicon — because the feed
 * does not publish that buyer's wallet beside their purchase either.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

const dir = mkdtempSync(join(tmpdir(), 'rating-comments-'));
const bundle = join(dir, 'bundle.cjs');

buildSync({
  stdin: {
    contents: `
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
globalThis.window = { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
globalThis.document = { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { renderToStaticMarkup } from 'react-dom/server';
export { createElement } from 'react';
export { RecentRatingComments } from ${JSON.stringify(new URL('../src/components/rating/RatingFlow.tsx', import.meta.url).pathname)};
`,
    resolveDir: new URL('..', import.meta.url).pathname,
    sourcefile: 'rating-comments-entry.tsx',
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
const render = (items) => client.renderToStaticMarkup(client.createElement(client.RecentRatingComments, { seed: items }));
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const WALLET = 'NQ242XCDPK1C7DR525U53PERU64VUSR2LPRK';

test('a public comment shows its buyer, their stars and what they bought', () => {
  const out = render([
    { stars: 5, comment: 'Fast delivery, the code arrived in seconds.', at: '2026-10-19T16:54:44Z', address: WALLET, product: 'Amazon' },
  ]);
  assert.ok(out.includes('NQ24 2XCD…'), `expected the wallet chip, got: ${out}`);
  assert.ok(!out.includes('2XCD P…'), 'the short address must end on a whole group of four');
  assert.ok(out.includes('<img'), 'the buyer gets their identicon face');
  assert.ok(out.includes('Fast delivery'), 'the comment itself is shown');
  assert.ok(out.includes('Amazon'), 'what they bought is shown with it');
  // The wallet is copied short, never printed whole: this list is public.
  assert.ok(!out.includes(WALLET), 'the activity page never prints a full wallet');
});

test('an anonymous buyer stays private — no face, no wallet, no guessing', () => {
  const out = render([
    { stars: 4, comment: 'Good.', at: '2026-10-19T16:54:44Z', anonymous: true, product: 'Amazon' },
  ]);
  assert.ok(!out.includes('<img'), 'an anonymous comment must not show an identicon');
  assert.ok(!out.includes('NQ'), 'and it must not leak any part of a wallet');
  assert.ok(out.includes('feed-wallet-private'), 'it shows the private-buyer chip instead');
});

test('a comment the shop cannot match to a buyer simply has no face', () => {
  const out = render([{ stars: 3, comment: 'Written before faces were captured.', at: '2026-10-12T10:00:00Z' }]);
  assert.ok(!out.includes('<img'), 'no invented avatar');
  assert.ok(out.includes('Written before faces were captured.'));
});

test('a long comment wraps in its own block instead of stretching the row', () => {
  const long = 'x'.repeat(400);
  const out = render([{ stars: 5, comment: long, at: '2026-10-19T16:54:44Z', address: WALLET }]);
  assert.ok(out.includes(long), 'the whole comment is shown, not truncated mid-word');
  assert.ok(out.includes('rating-comment-body'), 'it lives in a text block of its own');
  assert.ok(!out.includes('align-items:baseline'), 'the old baseline row layout is gone');
});

test('the list is one card per comment and never a cell of the summary grid', () => {
  const out = render([
    { stars: 5, comment: 'One.', at: '2026-10-19T16:54:44Z', address: WALLET },
    { stars: 2, comment: 'Two.', at: '2026-10-18T16:54:44Z', address: 'NQ38AU4HYEPNXCHLPBYDPF4CEYMCC92HRHEJ' },
  ]);
  assert.equal((out.match(/class="rating-comment"/g) || []).length, 2, 'one card per comment');
  assert.ok(out.includes('rs-comments'), 'the block spans the card, under the two-column summary');
  assert.ok(!out.includes('rs-body'), 'and is not rendered inside that grid');
});

test('the layout rule that broke it is still in the stylesheet', () => {
  const css = readFileSync(new URL('../src/styles/fixes.css', import.meta.url), 'utf8');
  assert.ok(/\.rating-summary \.rs-comments \{ width: 100%/.test(css), 'the comments block must span the summary card');
  assert.ok(/\.rating-comment-body \{[\s\S]*?overflow-wrap: anywhere/.test(css), 'long words must wrap, not stretch the card');
});
