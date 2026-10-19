/**
 * test-rating-comments.mjs — the buyer comments on the Activity page and the
 * rating on the track page.
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
 * A comment the shop cannot match to a buyer simply has no face: it is never
 * given an invented one. The card is shared, so the same rating looks the
 * same on the Activity page and on the track page of the purchase itself.
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
export { RecentRatingComments, RatingCommentCard } from ${JSON.stringify(new URL('../src/components/rating/RatingFlow.tsx', import.meta.url).pathname)};
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
const card = (c, props = {}) => client.renderToStaticMarkup(client.createElement(client.RatingCommentCard, { c, ...props }));
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const WALLET = 'NQ242XCDPK1C7DR525U53PERU64VUSR2LPRK';
// explorerUrl only accepts a real 64-hex hash, and the chip renders nothing
// without one — which is exactly what makes the on-chain test meaningful.
const TX = 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90';

test('a public comment shows its buyer, their stars and what they bought', () => {
  const out = render([
    { stars: 5, comment: 'Fast delivery, the code arrived in seconds.', at: '2026-10-19T16:54:44Z', address: WALLET, product: 'Amazon' },
  ]);
  assert.ok(out.includes('NQ24 2XCD…'), `expected the wallet chip, got: ${out}`);
  assert.ok(!out.includes('2XCD P…'), 'the short address must end on a whole group of four');
  assert.ok(out.includes('<img'), 'the buyer gets their identicon face');
  assert.ok(out.includes('Fast delivery'), 'the comment itself is shown');
  assert.ok(out.includes('Amazon'), 'what they bought is shown with it');
});

test("the buyer's wallet copies on a click, the way the feed row does", () => {
  const out = render([{ stars: 5, comment: 'Fast.', at: '2026-10-19T16:54:44Z', address: WALLET }]);
  const chip = out.match(/<button[^>]*class="feed-wallet chip"[^>]*>/);
  assert.ok(chip, 'the wallet is a button — a click copies it');
  assert.ok(chip[0].includes(WALLET), 'the whole address rides on the button, so the click has something to copy');
  assert.ok(out.includes('<span class="mono">NQ24 2XCD…</span>'), 'what is shown is still the short form');
  assert.ok(!out.includes(`>${WALLET}<`), 'the full address is never printed as text');
  assert.ok(out.includes('copy'), 'and the chip shows that it is copyable');
});

test('a comment with no wallet has nothing to copy', () => {
  const out = card({ stars: 4, comment: 'No face, nothing to copy.', at: '2026-10-19T16:54:44Z' });
  assert.ok(!out.includes('<button'), 'no button to click at all');
  assert.ok(!out.includes(WALLET));
});

test('a comment the shop cannot match to a buyer has no face at all', () => {
  const out = card({ stars: 4, comment: 'Written before faces were captured.', at: '2026-10-19T16:54:44Z' });
  assert.ok(!out.includes('<img'), 'no invented avatar');
  assert.ok(!out.includes('NQ'), 'and no invented wallet either');
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

test('the same card carries the rating of the purchase on the track page', () => {
  const out = card(
    { stars: 5, comment: 'payment made under 10 seconds', at: '2026-10-19T19:49:51Z', tx: TX },
    { plain: true, showProduct: false },
  );
  assert.ok(out.includes('payment made under 10 seconds'), "the buyer's words travel with the order");
  assert.ok(out.includes('onchain-chip'), 'and so does the link to the rating on the chain');
  assert.ok(out.includes('rating-comment-plain'), 'it drops its own frame inside the track card');
  // The track page is about this one product already.
  assert.ok(!out.includes('rating-comment-body">payment made under 10 seconds</p><div class="xs faint"'), 'no duplicate product line');
});

test('a rating with no words is still a rating', () => {
  const out = card({ stars: 4, at: '2026-10-19T19:49:51Z' });
  assert.ok(out.includes('stars'), 'the stars are the rating');
  assert.ok(!out.includes('rating-comment-body'), 'no empty paragraph for a rating nobody wrote words for');
});

test('the on-chain chip says rating, in every language', () => {
  const wanted = {
    en: 'On-chain rating',
    de: 'On-Chain-Bewertung',
    fr: 'Évaluation on-chain',
    es: 'Valoración on-chain',
    pt: 'Avaliação on-chain',
    tr: 'Zincir üstü puan',
  };
  for (const [lang, want] of Object.entries(wanted)) {
    const src = readFileSync(new URL(`../src/i18n/locales/${lang}.ts`, import.meta.url), 'utf8');
    assert.ok(src.includes(want), `${lang} must read "${want}"`);
    assert.ok(!src.includes('onChainProof'), `${lang} still carries the old "proof" key`);
  }
});

test('the layout rule that broke it is still in the stylesheet', () => {
  const css = readFileSync(new URL('../src/styles/fixes.css', import.meta.url), 'utf8');
  assert.ok(/\.rating-summary \.rs-comments \{ width: 100%/.test(css), 'the comments block must span the summary card');
  assert.ok(/\.rating-comment-body \{[\s\S]*?overflow-wrap: anywhere/.test(css), 'long words must wrap, not stretch the card');
});
