#!/usr/bin/env node
/**
 * verify-backend.mjs — prove a DEPLOYED nim.shop backend equals a published build.
 *
 *   node verify-backend.mjs <base-url> [expected-source-root] [expected-binary-hash]
 *
 *   node verify-backend.mjs https://shop.example.com
 *   node verify-backend.mjs https://shop.example.com 84ed6e0a... ff941298...
 *
 * Trust chain (the backend half):
 *   1. GET <base>/api/integrity — the running server reports its own binary's
 *      SHA-256 and the source manifest embedded at build time.
 *   2. Recompute the root hash from the embedded source_manifest.files and
 *      check it equals source_manifest.rootHash → manifest is self-consistent.
 *   3. If an expected-source-root is given, check it matches → the embedded
 *      manifest is the AUTHENTIC published one (out-of-band anchor).
 *   4. If an expected-binary-hash is given, check binary_sha256 matches → the
 *      running binary is the published reproducible build. Combined with
 *      rebuilding the source via build-reproducible.sh (which reproduces that
 *      same hash), this links the running binary to the open-source source.
 *
 * Exit 0 = verified, 1 = any mismatch.
 */
import { createHash } from 'node:crypto';

const base = process.argv[2]?.replace(/\/+$/, '');
const expectedSourceRoot = process.argv[3];
const expectedBinaryHash = process.argv[4];
if (!base) {
  console.error('usage: verify-backend.mjs <base-url> [expected-source-root] [expected-binary-hash]');
  process.exit(2);
}

const sha256 = (s) => createHash('sha256').update(s, 'utf8').digest('hex');
const green = (s) => `\x1b[32m${s}\x1b[0m`;
const red = (s) => `\x1b[31m${s}\x1b[0m`;
const dim = (s) => `\x1b[2m${s}\x1b[0m`;

let failures = 0;
console.log(`verifying backend at ${base}\n`);

let report;
try {
  const res = await fetch(`${base}/api/integrity`, { cache: 'no-store' });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  report = await res.json();
} catch (e) {
  console.error(red('✗ cannot reach /api/integrity: ') + e.message);
  process.exit(1);
}

console.log(`go version     : ${report.go_version}`);
console.log(`platform       : ${report.os}/${report.arch}`);

// --- binary hash ---
const binaryHash = report.binary_sha256;
if (binaryHash) {
  console.log(`binary sha256  : ${binaryHash}`);
} else {
  console.log(red('binary sha256  : (unavailable) ' + (report.binary_error || '')));
  failures++;
}

// --- source manifest ---
const manifest = report.source_manifest;
let rootOk = false;
if (manifest && Array.isArray(manifest.files)) {
  const canon = manifest.files.map((e) => `${e.path}\n${e.sha256}\n`).join('');
  const recomputed = sha256(canon);
  rootOk = recomputed === manifest.rootHash;
  console.log(`source root    : ${manifest.rootHash}`);
  console.log(`source files   : ${manifest.fileCount}`);
  if (rootOk) console.log(green('  ✓ embedded manifest is self-consistent'));
  else { console.log(red('  ✗ manifest rootHash does not match its own file list')); failures++; }
} else {
  console.log(red('  ✗ no source manifest embedded in the binary'));
  failures++;
}
console.log('');

// --- out-of-band anchors ---
let srcAnchorOk = null, binAnchorOk = null;
if (expectedSourceRoot) {
  srcAnchorOk = manifest && manifest.rootHash === expectedSourceRoot.toLowerCase();
  console.log(srcAnchorOk ? green('  ✓ source root matches the PUBLISHED source root')
                          : red('  ✗ source root does NOT match the published source root'));
  if (!srcAnchorOk) failures++;
}
if (expectedBinaryHash) {
  binAnchorOk = binaryHash === expectedBinaryHash.toLowerCase();
  console.log(binAnchorOk ? green('  ✓ binary hash matches the PUBLISHED reproducible binary')
                          : red('  ✗ binary hash does NOT match the published binary'));
  if (!binAnchorOk) failures++;
}
if (!expectedSourceRoot && !expectedBinaryHash) {
  console.log(dim('  (no published anchors given — re-run with the source root and/or binary hash'));
  console.log(dim('   as arguments to confirm authenticity out-of-band)'));
}

console.log('');
if (failures === 0 && binAnchorOk !== false && srcAnchorOk !== false) {
  const verdict = (srcAnchorOk === true && binAnchorOk === true)
    ? green('FULLY VERIFIED — running backend matches the published build.')
    : green('SELF-CONSISTENT — binary hashes itself and manifest is sound.');
  console.log(verdict);
  process.exit(0);
} else {
  console.log(red(`${failures} problem(s) found — the running backend does NOT match.`));
  process.exit(1);
}
