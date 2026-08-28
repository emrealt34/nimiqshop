#!/usr/bin/env node
/**
 * verify.mjs — prove a DEPLOYED nim.shop frontend equals a published build.
 *
 * Usage:
 *   node tools/integrity/verify.mjs <base-url> [expected-root-hash]
 *
 *   node tools/integrity/verify.mjs https://shop.example.com
 *   node tools/integrity/verify.mjs https://shop.example.com 9f1a...   # CI: fail if mismatch
 *
 * What it checks (the trust chain):
 *   1. Fetches <base>/integrity.json from the live server.
 *   2. Recomputes the ROOT hash from the manifest's own file list and checks
 *      it equals manifest.rootHash  → proves the manifest is internally sound
 *      (not truncated/corrupt/tampered-by-hand).
 *   3. If an expected-root-hash is given, checks the live root equals it
 *      → proves the manifest is the AUTHENTIC published one (out-of-band anchor).
 *      Without this, steps 1-2 only prove the manifest is self-consistent.
 *   4. Fetches every file listed in the manifest from the live server, hashes
 *      it, and compares to the recorded hash → proves each file is unmodified.
 *
 * Exit code 0 = everything verified, 1 = any mismatch.
 */
import { createHash } from 'node:crypto';

const base = process.argv[2]?.replace(/\/+$/, '');
const expectedRoot = process.argv[3];
if (!base) {
  console.error('usage: verify.mjs <base-url> [expected-root-hash]');
  process.exit(2);
}

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');
const fetchBytes = async (url) => {
  const res = await fetch(url, { cache: 'no-store', redirect: 'follow' });
  if (!res.ok) throw new Error(`HTTP ${res.status} ${res.statusText} — ${url}`);
  return Buffer.from(await res.arrayBuffer());
};

const green = (s) => `\x1b[32m${s}\x1b[0m`;
const red = (s) => `\x1b[31m${s}\x1b[0m`;
const dim = (s) => `\x1b[2m${s}\x1b[0m`;

let failures = 0;

console.log(`verifying ${base}\n`);

// --- fetch manifest ---
let manifest;
try {
  manifest = JSON.parse((await fetchBytes(`${base}/integrity.json`)).toString('utf8'));
} catch (e) {
  console.error(red('✗ could not fetch /integrity.json: ') + e.message);
  process.exit(1);
}
console.log(`manifest schema    : ${manifest.schema}`);
console.log(`manifest fileCount : ${manifest.fileCount}`);

// --- 1+2: internal consistency of the manifest ---
const canon = manifest.files.map((e) => `${e.path}\n${e.sha256}\n`).join('');
const recomputedRoot = sha256(Buffer.from(canon, 'utf8'));
const manifestSelfOk = recomputedRoot === manifest.rootHash;
console.log(`live rootHash      : ${manifest.rootHash}`);
console.log(`recomputed from list: ${recomputedRoot}`);
if (manifestSelfOk) {
  console.log(green('  ✓ manifest is internally consistent'));
} else {
  console.log(red('  ✗ manifest rootHash does NOT match its own file list'));
  failures++;
}

// --- 3: out-of-band authenticity anchor (if provided) ---
let rootAnchorOk = null;
if (expectedRoot) {
  rootAnchorOk = manifest.rootHash === expectedRoot.toLowerCase();
  if (rootAnchorOk) {
    console.log(green('  ✓ live rootHash matches the PUBLISHED root hash'));
  } else {
    console.log(red('  ✗ live rootHash does NOT match the published root hash'));
    console.log(`    published : ${expectedRoot}`);
    failures++;
  }
} else {
  console.log(dim('  (no published root given — manifest authenticity not anchored;'));
  console.log(dim('   pass the root hash as the 2nd argument or publish it manually)'));
}
console.log('');

// --- 4: verify every file ---
let checked = 0, ok = 0;
const width = Math.max(...manifest.files.map((f) => f.path.length), 8);
for (const { path, sha256: expected } of manifest.files) {
  process.stdout.write(`  ${path.padEnd(width)}  `);
  let actual;
  try {
    const buf = await fetchBytes(`${base}/${path}`);
    actual = sha256(buf);
  } catch (e) {
    console.log(red('FETCH FAIL') + dim(`  ${e.message}`));
    failures++;
    checked++;
    continue;
  }
  if (actual === expected) {
    console.log(green('OK') + dim(`  ${actual.slice(0, 16)}…`));
    ok++;
  } else {
    console.log(red('MISMATCH'));
    console.log(dim(`      expected ${expected}`));
    console.log(dim(`      actual   ${actual}`));
    failures++;
  }
  checked++;
}

console.log('');
console.log(`files verified: ${ok}/${checked} OK`);
const proven = failures === 0 && rootAnchorOk !== false;
if (proven) {
  const strength =
    rootAnchorOk === true
      ? green('FULLY VERIFIED — matches the published build (root anchored out-of-band).')
      : green('SELF-CONSISTENT — manifest matches all files (no out-of-band root provided).');
  console.log(strength);
  process.exit(0);
} else {
  console.log(red(`${failures} problem(s) found — the live deployment does NOT match.`));
  process.exit(1);
}
