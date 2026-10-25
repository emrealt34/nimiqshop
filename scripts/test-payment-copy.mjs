/**
 * Unit tests for "copy the Nimiq Pay payment request" — the 2026-10-25
 * regression guard.
 *
 *   node --test scripts/test-payment-copy.mjs
 *
 * Why this exists: the copy button used to share the pay button's handler and
 * awaited `POST /quotes/:id/payment-launch` BEFORE calling the clipboard.
 * `document.execCommand('copy')` is only permitted while the click's transient
 * user activation is alive (≈5 s in Chromium, and WebKit drops it at the first
 * task boundary), so on a slow supplier — and always on iPhone — the copy came
 * back false and the buyer saw "Kopyalama tamamlanmadı" instead of the
 * invoice. A 4xx from that POST was even worse: the handler threw before the
 * clipboard was touched, so the shop refused to hand over the payment request
 * exactly when it had to be copied by hand.
 *
 * These tests pin the three rules that fix it:
 *   1. the clipboard is written FIRST and SYNCHRONOUSLY (inside the click);
 *   2. the audit stamp runs AFTER it, and can never break or delay the copy;
 *   3. "copied" is only ever reported after a real copy.
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
const dir = mkdtempSync(path.join(tmpdir(), 'payment-copy-'));
const entry = path.join(dir, 'entry.ts');
const bundle = path.join(dir, 'bundle.cjs');

writeFileSync(entry, `
(globalThis as any).localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };
(globalThis as any).window ??= { addEventListener() {}, location: { href: '' }, navigator: { language: 'en' } };
(globalThis as any).document ??= { addEventListener() {}, documentElement: { lang: 'en' }, createElement: () => ({ style: {} }), cookie: '' };
export { copyPaymentRequest } from ${JSON.stringify(path.join(root, 'src/lib/paymentHandoff'))};
`);

const esbuild = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild');
const built = spawnSync(esbuild, [
  entry, '--bundle', '--platform=node', '--format=cjs', `--outfile=${bundle}`,
  '--define:import.meta.env.BASE_URL="/"', '--define:import.meta.env={}',
  '--loader:.css=empty', '--log-level=error',
], { encoding: 'utf8' });
assert.equal(built.status, 0, `esbuild failed: ${built.stderr || built.stdout}`);

const require = createRequire(import.meta.url);
const { copyPaymentRequest } = require(bundle);
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

const INVOICE = 'lnbc2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4js';

/* ------------------------- the order that matters ------------------------ */

test('copy runs synchronously, inside the click, before the survey stamp', () => {
  const seen = [];
  const out = copyPaymentRequest(INVOICE, {
    copy: (text) => { seen.push('copy:' + text.slice(0, 8)); return true; },
    stamp: () => { seen.push('stamp'); },
  });
  // A synchronous return value is the whole point: an async helper would be
  // awaited by the caller, and the browser would refuse the clipboard write.
  assert.equal(typeof out, 'string');
  assert.equal(out, 'copied');
  assert.deepEqual(seen, ['copy:lnbc2500', 'stamp']);
});

test('a slow/never-resolving stamp does not hold the copy back', () => {
  let copyAlreadyDone = false;
  const out = copyPaymentRequest(INVOICE, {
    copy: () => { copyAlreadyDone = true; return true; },
    stamp: () => {
      // By the time the audit is started, the invoice is already on the
      // clipboard — and the stamp is still pending when this returns.
      assert.equal(copyAlreadyDone, true);
      return new Promise(() => { /* never settles: a hung server */ });
    },
  });
  assert.equal(out, 'copied');
});

test('a throwing stamp can never turn a real copy into a failure', () => {
  const out = copyPaymentRequest(INVOICE, {
    copy: () => true,
    stamp: () => { throw new Error('server unreachable'); },
  });
  assert.equal(out, 'copied');
});

test('a rejected stamp promise is swallowed, not left unhandled', async () => {
  let unhandled = false;
  const onUnhandled = () => { unhandled = true; };
  process.on('unhandledRejection', onUnhandled);
  const out = copyPaymentRequest(INVOICE, {
    copy: () => true,
    stamp: () => Promise.reject(new Error('409 payment not payable')),
  });
  await new Promise((r) => setTimeout(r, 10));
  process.off('unhandledRejection', onUnhandled);
  assert.equal(out, 'copied');
  assert.equal(unhandled, false);
});

/* ------------------------------ honest answers --------------------------- */

test('a refused clipboard is reported as failed, and nothing is stamped', () => {
  let stamped = 0;
  const out = copyPaymentRequest(INVOICE, {
    copy: () => false,
    stamp: () => { stamped += 1; },
  });
  assert.equal(out, 'failed');
  // The buyer never received the invoice, so no "wallet was handed this
  // invoice" audit stamp may be claimed on their behalf.
  assert.equal(stamped, 0);
});

test('no invoice means no copy and no success message', () => {
  let copied = 0;
  let stamped = 0;
  for (const empty of ['', '   ', null, undefined]) {
    const out = copyPaymentRequest(empty, {
      copy: () => { copied += 1; return true; },
      stamp: () => { stamped += 1; },
    });
    assert.equal(out, 'empty');
  }
  assert.equal(copied, 0);
  assert.equal(stamped, 0);
});

test('the clipboard gets the exact invoice the buyer was shown, trimmed', () => {
  let got = '';
  const out = copyPaymentRequest(`  ${INVOICE}\n`, { copy: (text) => { got = text; return true; } });
  assert.equal(out, 'copied');
  assert.equal(got, INVOICE);
});

test('the stamp is optional — a copy-only caller still works', () => {
  assert.equal(copyPaymentRequest(INVOICE, { copy: () => true }), 'copied');
});
