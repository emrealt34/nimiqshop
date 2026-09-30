#!/usr/bin/env node
/**
 * Deep translation check — fails (exit 1) on:
 *   • keys missing from / extra in any locale compared to en.ts
 *   • empty values
 *   • {placeholder} mismatches  (e.g. en "{count} trees", de "{anzahl} Bäume")
 *   • rich-text tag mismatches  (<b>, <strong>, <a>, <1> … used by rich())
 *   • plural shape mismatches   ({one, other} in en but a string elsewhere)
 *   • untranslated copies       (value identical to English, ≥3 words)
 * Exceptions for the last rule: scripts/i18n-untranslated-allow.json
 *
 *   node scripts/check-i18n-deep.mjs
 */
import { build } from 'esbuild';
import { readdirSync, readFileSync, existsSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { join } from 'node:path';
import { writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const DIR = join(ROOT, 'src/i18n/locales');
const ALLOW_FILE = join(ROOT, 'scripts/i18n-untranslated-allow.json');
const allow = existsSync(ALLOW_FILE) ? JSON.parse(readFileSync(ALLOW_FILE, 'utf8')) : {};

async function load(file) {
  const out = await build({ entryPoints: [join(DIR, file)], bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent' });
  const tmp = mkdtempSync(join(tmpdir(), 'i18n-'));
  const f = join(tmp, file.replace(/\.ts$/, '.mjs'));
  writeFileSync(f, out.outputFiles[0].text);
  const mod = await import(pathToFileURL(f).href);
  rmSync(tmp, { recursive: true, force: true });
  return mod.default ?? Object.values(mod).find((v) => v && typeof v === 'object');
}

const isPlural = (v) => v && typeof v === 'object' && !Array.isArray(v) && 'other' in v && Object.keys(v).every((k) => ['zero', 'one', 'two', 'few', 'many', 'other'].includes(k));
function flatten(obj, prefix = '', out = {}) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === 'string' || isPlural(v)) out[key] = v;
    else if (v && typeof v === 'object') flatten(v, key, out);
  }
  return out;
}
const vars = (s) => [...String(s).matchAll(/\{\s*(\w+)\s*\}/g)].map((m) => m[1]).sort().join(',');
const tags = (s) => [...String(s).matchAll(/<\/?([a-z0-9]+)[^>]*>/gi)].map((m) => m[0].startsWith('</') ? '/' + m[1] : m[1]).sort().join(',');
const words = (s) => (String(s).match(/\p{L}{3,}/gu) || []).length;

const files = readdirSync(DIR).filter((f) => /^[a-z]{2}(-[A-Z]{2})?\.ts$/.test(f));
const en = flatten(await load('en.ts'));
let errors = 0;
const report = (lang, msg) => { errors++; console.error(`✗ ${lang}: ${msg}`); };

for (const file of files.filter((f) => f !== 'en.ts')) {
  const lang = file.replace('.ts', '');
  const loc = flatten(await load(file));
  const allowed = new Set([...(allow['*'] || []), ...(allow[lang] || [])]);
  for (const k of Object.keys(en)) {
    if (!(k in loc)) { report(lang, `missing key ${k}`); continue; }
    const a = en[k], b = loc[k];
    if (isPlural(a) !== isPlural(b)) { report(lang, `${k}: plural shape differs from en`); continue; }
    const pairs = isPlural(a) ? Object.keys(a).map((f) => [a[f], b[f] ?? b.other, `${k}.${f}`]) : [[a, b, k]];
    for (const [ea, lb, kk] of pairs) {
      if (typeof lb !== 'string' || !lb.trim()) { report(lang, `${kk}: empty`); continue; }
      // the singular form may spell the number out ("l'acheteur") — {count} optional there
      const strip = (v) => (kk.endsWith('.one') ? v.split(',').filter((x) => x !== 'count').join(',') : v);
      if (strip(vars(ea)) !== strip(vars(lb))) report(lang, `${kk}: placeholders {${vars(ea)}} ≠ {${vars(lb)}}`);
      if (tags(ea) !== tags(lb)) report(lang, `${kk}: tags [${tags(ea)}] ≠ [${tags(lb)}]`);
      if (ea === lb && words(ea) >= 3 && !allowed.has(kk)) report(lang, `${kk}: not translated ("${String(ea).slice(0, 60)}")`);
    }
  }
  for (const k of Object.keys(loc)) if (!(k in en)) report(lang, `extra key ${k} (not in en)`);
}

if (errors) { console.error(`\n${errors} translation problem(s).`); process.exit(1); }
console.log(`✓ ${files.length} locales, ${Object.keys(en).length} keys each — placeholders, tags, plurals and translations all consistent.`);
