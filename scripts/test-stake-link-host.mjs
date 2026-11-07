/**
 * Unit tests for the stake-link signer choice — which wallet app signs, and
 * which of its accounts.
 *
 *   node --test scripts/test-stake-link-host.mjs
 *
 * The rule under test is the one a browser cannot be trusted to discover on its
 * own: Nimiq Hub shows a wallet picker, Nimiq Pay does not. Pay's provider
 * signs with the account it holds and `sign(message)` takes no address, so a
 * `?link_stake=` URL naming a wallet Pay does not hold cannot be honoured there.
 * Guessing would produce a perfectly valid signature for the WRONG wallet, the
 * backend would verify it, record the link — and the buyer would only find out
 * when his stake never moved his rate. So the choice is refused instead, which
 * is what lets the cashback card name the wallet that was asked for.
 *
 * Both halves of the flow are pinned here: which host is used is `inNimiqPay()`
 * in lib/hub.ts (the same branch login already makes in SiteShell and
 * CheckoutFlow), and which account signs is the pure function below.
 *
 * Bundled with esbuild rather than imported as .ts, so this runs on the Node
 * the repo pins as well as the Node CI installs.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

const dir = await mkdtemp(join(tmpdir(), 'stake-link-host-'));
const file = join(dir, 'intent.cjs');
let intent;
try {
  buildSync({
    entryPoints: [new URL('../src/lib/stakeLinkIntent.ts', import.meta.url).pathname],
    bundle: true, platform: 'node', format: 'cjs', outfile: file, logLevel: 'silent',
  });
  intent = createRequire(import.meta.url)(file);
} finally {
  await rm(dir, { recursive: true, force: true });
}
const {
  pickHostSigner,
  sameStakeAddress,
  normalizeStakeAddress,
  canonicalStakeAddress,
  sanitizeStakeInviteToken,
  buildStakeInviteUrl,
} = intent;

/** Two wallets, in the spaced display form a host may well report. */
const A = 'NQ860123456789ABCDEFGHJKLMNPQRSTUVXY';
const B = `NQ39${'1'.repeat(32)}`;
const spaced = (address) => address.replace(/(.{4})/g, '$1 ').trim();
const HELD = [spaced(A), B];

test('without an expectation the host signs with its first account', () => {
  assert.equal(pickHostSigner(HELD), HELD[0], 'the same choice login makes');
  assert.equal(pickHostSigner(HELD, ''), HELD[0]);
  assert.equal(pickHostSigner(HELD, null), HELD[0]);
  assert.equal(pickHostSigner(HELD, '   '), HELD[0], 'blank is no expectation');
});

test('an expectation is matched across spacing and case, and returned in the host spelling', () => {
  assert.equal(pickHostSigner(HELD, A), HELD[0], 'the host reported it spaced; send back what it said');
  assert.equal(pickHostSigner(HELD, spaced(A)), HELD[0]);
  assert.equal(pickHostSigner(HELD, A.toLowerCase()), HELD[0]);
  assert.equal(pickHostSigner(HELD, B), B);
  assert.equal(pickHostSigner([A], spaced(A)), A, 'the other order works too');
});

test('a wallet the host does not hold is refused rather than substituted', () => {
  assert.equal(pickHostSigner(HELD, `NQ11${'2'.repeat(32)}`), '', 'not in Pay: the caller must not sign');
  assert.equal(pickHostSigner([A], B), '');
  assert.equal(pickHostSigner([A], 'NQ86'), '', 'a prefix is not a match');
  assert.equal(pickHostSigner([A], `${A}9`), '', 'neither is a longer one');
});

test('a host with no usable account cannot sign at all', () => {
  assert.equal(pickHostSigner([]), '');
  assert.equal(pickHostSigner([], A), '');
  assert.equal(pickHostSigner(null), '');
  assert.equal(pickHostSigner(undefined, A), '');
  assert.equal(pickHostSigner(['', '   ', null]), '', 'blank entries are dropped, not returned');
  assert.equal(pickHostSigner(['  ', B]), B, 'and a real one still survives the filter');
});

test('comparison ignores spacing and case, and never treats empty as a wallet', () => {
  assert.equal(sameStakeAddress(A, spaced(A)), true);
  assert.equal(sameStakeAddress(A, A.toLowerCase()), true);
  assert.equal(sameStakeAddress(A, B), false);
  assert.equal(sameStakeAddress('', ''), false, 'two empty strings are not the same wallet');
  assert.equal(sameStakeAddress(A, ''), false);
  assert.equal(sameStakeAddress('', A), false);
  assert.equal(sameStakeAddress(null, undefined), false);
});

test('canonicalisation compares without validating, normalisation still validates', () => {
  // The split is deliberate: a host that reports an unfamiliar form must still
  // be comparable, or the shop refuses a wallet it can read perfectly well.
  assert.equal(canonicalStakeAddress(spaced(A)), A);
  assert.equal(canonicalStakeAddress('not-an-address'), 'NOT-AN-ADDRESS');
  assert.equal(canonicalStakeAddress(''), '');
  // …while the form the backend stores is still checked.
  assert.equal(normalizeStakeAddress(spaced(A)), A);
  assert.equal(normalizeStakeAddress('not-an-address'), '');
  assert.equal(normalizeStakeAddress(`NQ86${'1'.repeat(31)}`), '', 'one character short');
  assert.equal(normalizeStakeAddress(`NQ86${'1'.repeat(32)}9`), '', 'one too many');
  assert.equal(normalizeStakeAddress(`NQ86${'I'.repeat(32)}`), '', 'I is not in the Nimiq alphabet');
});

test('cross-device pairing invite tokens validate JWT shape and build clean URLs', () => {
  const validJwt = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1aWQiOiJ1MSIsImFjY291bnQiOiJOUTA3MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAifQ.abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE';
  assert.equal(sanitizeStakeInviteToken(validJwt), validJwt);
  assert.equal(sanitizeStakeInviteToken(`  ${validJwt}  `), validJwt);
  assert.equal(sanitizeStakeInviteToken('not-a-jwt'), '');
  assert.equal(sanitizeStakeInviteToken('a.b.c'), '', 'segments too short');
  assert.equal(sanitizeStakeInviteToken(''), '');

  const url = buildStakeInviteUrl('https://shop.nimiqbase.com/cashback?link_stake=NQ0700000000000000000000000000000000#top', validJwt);
  assert.equal(url, `https://shop.nimiqbase.com/cashback?stake_link=${validJwt}`);
  assert.equal(buildStakeInviteUrl('https://shop.nimiqbase.com/cashback', 'bad'), '');
});
