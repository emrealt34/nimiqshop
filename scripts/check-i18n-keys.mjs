#!/usr/bin/env node
/**
 * check-i18n-keys.mjs — every `t('ns.key')` / `tr('ns.key')` / `i18nT('ns.key')`
 * reference in src/ must resolve to a real key in src/i18n/locales/en.ts.
 *
 * WHY THIS EXISTS: TypeScript types the KEY parameter as DictKey, which catches
 * most typos — but a *computed* or newly added reference string is only checked
 * at runtime, and `t()` returns the key itself when it is missing, so a wrong
 * key ships as the literal text "orderPage.summaryTitle" on screen. Parity
 * (scripts/check-i18n.mjs) only proves the six languages agree with each other.
 *
 * The reference scan is text-based (it tolerates comments/props), the key
 * RESOLUTION is not: the locale module is bundled with esbuild and the path is
 * looked up on the real object, so nested namespaces (ns.obj.leaf) are accurate.
 *
 * Usage: node scripts/check-i18n-keys.mjs [--all]     (--all includes admin/*)
 */
import { build } from 'esbuild';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, relative } from 'node:path';
import { readdirSync, statSync } from 'node:fs';

const ROOT = process.cwd();
const SKIP = /(node_modules|\.test\.|i18n\/locales|\/devtools\/|__fixtures__|\.d\.ts$)/;
const INCLUDE_ADMIN = process.argv.includes('--all');

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) walk(p, out);
    else if (/\.(ts|tsx|astro|mjs|js)$/.test(name)) out.push(p);
  }
  return out;
}

// Strip comments so JSDoc usage examples are not mistaken for real refs.
const strip = (s) =>
  s
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((line) => (line.trim().startsWith('//') ? '' : line))
    .join('\n');

const refs = new Map();
for (const file of walk(join(ROOT, 'src'))) {
  const rel = relative(ROOT, file);
  if (SKIP.test(rel) || (!INCLUDE_ADMIN && rel.includes('admin/'))) continue;
  const src = strip(readFileSync(file, 'utf8'));
  src.split('\n').forEach((line, i) => {
    for (const m of line.matchAll(/\b(?:t|tr|i18nT)\(\s*['"]([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)['"]/g)) {
      if (!refs.has(m[1])) refs.set(m[1], `${rel}:${i + 1}`);
    }
  });
}

const dir = mkdtempSync(join(tmpdir(), 'i18n-keys-'));
try {
  const bundle = await build({
    entryPoints: [join(ROOT, 'src/i18n/locales/en.ts')],
    bundle: true, format: 'esm', platform: 'node', write: false,
  });
  const outFile = join(dir, 'en.mjs');
  writeFileSync(outFile, bundle.outputFiles[0].text);
  const mod = await import(outFile);
  const en = mod.default ?? Object.values(mod).find((v) => v && typeof v === 'object');

  // A ref resolves when it lands on a string, or on a PLURAL bucket — an
  // object whose values are all strings (t('ns.key', { count }) picks one).
  const isPluralBucket = (v) =>
    !!v && typeof v === 'object' && !Array.isArray(v) && Object.values(v).every((x) => typeof x === 'string');

  const missing = [];
  for (const [key, where] of refs) {
    const [ns, ...rest] = key.split('.');
    let node = en?.[ns];
    for (const part of rest) node = node?.[part];
    if (typeof node !== 'string' && !isPluralBucket(node)) missing.push([key, where]);
  }
  console.log(`i18n key refs: ${refs.size} checked, ${missing.length} unresolved${INCLUDE_ADMIN ? '' : ' (admin excluded)'}`);
  for (const [key, where] of missing) console.log(`  UNRESOLVED ${key}  (${where})`);
  process.exit(missing.length ? 1 : 0);
} finally {
  rmSync(dir, { recursive: true, force: true });
}
