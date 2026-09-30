#!/usr/bin/env node
// fetch-flags.mjs — download every flag the shop can display into
// public/img/flags/ so the hexagon flag component renders offline and without
// depending on a third-party CDN at page load.
//
//   node scripts/fetch-flags.mjs            # download what is missing
//   node scripts/fetch-flags.mjs --force    # re-download everything
//
// WHY LOCAL FILES
//
// The old FlagMark pointed <img src> straight at https://flagcdn.com/<cc>.svg.
// That puts a third party in the critical path of every page that shows a
// country: the flag is the first thing the eye lands on in the country picker,
// and when the CDN is slow, blocked (corporate DNS, offline demo, a laptop on a
// plane) every flag silently disappeared. Shipping the ~190 SVGs we actually
// reference costs about as much as one product photo and removes that
// dependency completely.
//
// The flag artwork comes from flagcdn.com (Flagpedia / Wikipedia flag set,
// public domain for the overwhelming majority of these). The component keeps
// the CDN as a second-stage fallback for codes this script has not fetched,
// so nothing breaks if a country is added and this script is not re-run.

import { mkdir, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { readFileSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const OUT = join(root, 'public', 'img', 'flags');
const CDN = 'https://flagcdn.com';

/** Every ISO code the shop can show: the catalogue list + the phone/dial list. */
function collectCodes() {
  const codes = new Set();
  const files = [join(root, 'src', 'lib', 'countries.ts'), join(root, 'src', 'lib', 'phoneCountry.ts')];
  for (const file of files) {
    if (!existsSync(file)) continue;
    const src = readFileSync(file, 'utf8');
    // ['TR', 'Türkiye'] — the catalogue list, and cc: 'TR' style entries.
    for (const m of src.matchAll(/\[\s*'([A-Za-z]{2})'\s*,/g)) codes.add(m[1].toUpperCase());
    for (const m of src.matchAll(/\bcc\s*:\s*'([A-Za-z]{2})'/g)) codes.add(m[1].toUpperCase());
  }
  return [...codes].sort();
}

async function fetchOne(code, { force }) {
  const file = join(OUT, `${code.toLowerCase()}.svg`);
  if (!force && existsSync(file)) return { code, status: 'cached' };
  const res = await fetch(`${CDN}/${code.toLowerCase()}.svg`, { redirect: 'follow' });
  if (!res.ok) return { code, status: `http ${res.status}` };
  const body = await res.text();
  if (!body.trimStart().startsWith('<svg')) return { code, status: 'not svg' };
  await writeFile(file, body, 'utf8');
  return { code, status: 'ok', bytes: body.length };
}

async function main() {
  const force = process.argv.includes('--force');
  const codes = collectCodes();
  await mkdir(OUT, { recursive: true });
  console.log(`[flags] ${codes.length} country code(s); writing to public/img/flags/`);

  const results = [];
  const queue = [...codes];
  const workers = Array.from({ length: 8 }, async () => {
    while (queue.length) {
      const code = queue.shift();
      try {
        results.push(await fetchOne(code, { force }));
      } catch (e) {
        results.push({ code, status: `error: ${e.message}` });
      }
    }
  });
  await Promise.all(workers);

  const ok = results.filter((r) => r.status === 'ok');
  const cached = results.filter((r) => r.status === 'cached');
  const failed = results.filter((r) => r.status !== 'ok' && r.status !== 'cached');
  const bytes = ok.reduce((n, r) => n + (r.bytes || 0), 0);
  console.log(`[flags] downloaded ${ok.length} (${(bytes / 1024).toFixed(0)} KB), cached ${cached.length}, failed ${failed.length}`);
  if (failed.length) {
    for (const f of failed) console.log(`[flags]   ${f.code}: ${f.status}`);
    console.log('[flags] failed codes fall back to the CDN at runtime (and then to the emoji).');
  }
}

main().catch((e) => {
  console.error('[flags] failed:', e.message);
  process.exit(1);
});
