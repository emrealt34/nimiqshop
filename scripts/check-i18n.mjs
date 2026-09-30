#!/usr/bin/env node
/**
 * check-i18n.mjs — CI guard for translations.
 *
 * Run as:  node scripts/check-i18n.mjs
 *
 * Verifies that:
 *   1. Every frontend locale file (src/i18n/locales/<code>.ts) has every key
 *      that en.ts has (no missing translations silently fall through to the
 *      bare key on screen).
 *   2. Every Go locale map (backend/internal/i18n/locales/<code>.go) has every
 *      key en.go has.
 *   3. The two "en" bundles agree on their shared "email.*" keys, so the
 *      frontend and backend never render a different sentence for the same
 *      semantic key.
 *
 * Exit code 0 = clean. Non-zero prints a readable report of missing keys.
 *
 * This script reads the TypeScript locale files as plain text and walks the
 * nested object literal structure — it does NOT import TypeScript at runtime.
 * That keeps the check zero-dependency and fast enough to run on every
 * `npm test`.
 */
import { readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');

const FRONTEND_DIR = join(ROOT, 'src', 'i18n', 'locales');
const BACKEND_DIR  = join(ROOT, 'backend', 'internal', 'i18n', 'locales');
const LANGS = ['en', 'es', 'de', 'fr', 'pt', 'tr'];

/* ---------- 1. Parse a TS object literal into nested plain objects ------- */
// Very small recursive-descent parser that handles:
//   - string literals: '...', "..." (with '' and "" escapes)
//   - template literals for as-const suffix (we treat them as plain strings
//     because none of our locale values use ${} interpolation)
//   - nested objects: { ... }
//   - plural objects: { one: '...', other: '...' }
//   - trailing commas and comments (line // and block /* */)
function parseObject(src) {
  let i = 0;
  // Skip whitespace and comments
  function skip() {
    while (i < src.length) {
      const ch = src[i];
      if (ch === ' ' || ch === '\n' || ch === '\r' || ch === '\t') { i++; continue; }
      if (ch === '/' && src[i + 1] === '/') {
        while (i < src.length && src[i] !== '\n') i++;
        continue;
      }
      if (ch === '/' && src[i + 1] === '*') {
        i += 2;
        while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++;
        i += 2;
        continue;
      }
      break;
    }
  }
  function parseKey() {
    skip();
    const ch = src[i];
    if (ch === '"' || ch === "'") return parseStringRaw();
    // identifier key
    let out = '';
    while (i < src.length && /[A-Za-z0-9_$]/.test(src[i])) out += src[i++];
    return out;
  }
  function parseStringRaw() {
    const quote = src[i++];
    let out = '';
    while (i < src.length && src[i] !== quote) {
      if (src[i] === '\\') {
        i++;
        const esc = src[i++];
        if (esc === 'n') out += '\n';
        else if (esc === 't') out += '\t';
        else if (esc === 'r') out += '\r';
        else out += esc;
      } else {
        out += src[i++];
      }
    }
    i++; // closing quote
    return out;
  }
  function parseValue() {
    skip();
    const ch = src[i];
    if (ch === '{') {
      const saved = i;
      i++; // consume {
      const obj = {};
      let ok = true;
      // Parse the object tentatively: if every value is a string AND we find
      // 'one' and 'other' keys, it is a plural leaf; otherwise restore and
      // re-parse normally so nested sub-objects become namespaces.
      let j = saved + 1;
      function subSkip() {
        while (j < src.length && ' \n\r\t'.includes(src[j])) j++;
        if (src[j] === '/' && src[j+1] === '/') { while (j < src.length && src[j] !== '\n') j++; return subSkip(); }
        if (src[j] === '/' && src[j+1] === '*') { j += 2; while (j < src.length && !(src[j]==='*' && src[j+1]==='/')) j++; j += 2; return subSkip(); }
      }
      function subStr() {
        const q = src[j++]; let o='';
        while (j < src.length && src[j] !== q) { if (src[j]==='\\'){j++;o+=src[j++]} else o+=src[j++]; }
        j++; return o;
      }
      function subKey() {
        subSkip();
        const c = src[j];
        if (c === '"' || c === "'") return subStr();
        let o=''; while (j < src.length && /[A-Za-z0-9_$]/.test(src[j])) o += src[j++];
        return o;
      }
      subSkip();
      while (j < src.length && src[j] !== '}') {
        const k = subKey();
        subSkip();
        if (src[j] !== ':') { ok = false; break; }
        j++;
        subSkip();
        const c = src[j];
        if (c === '"' || c === "'") { obj[k] = subStr(); }
        else { ok = false; break; }
        subSkip();
        if (src[j] === ',') { j++; subSkip(); }
      }
      const keys = Object.keys(obj);
      const isPlural = ok && keys.includes('one') && keys.includes('other') &&
        keys.every((kk) => typeof obj[kk] === 'string');
      if (isPlural) { i = j + 1; return obj; }
      // Not plural — fall through to normal parseObj (i still at opening brace).
      i = saved;
      return parseObj();
    }
    if (ch === '"' || ch === "'") return parseStringRaw();
    if (ch === '`') {
      i++;
      let out = '';
      while (i < src.length && src[i] !== '`') {
        if (src[i] === '\\') { i++; out += src[i++]; continue; }
        out += src[i++];
      }
      i++;
      return out;
    }
    throw new Error('Unexpected value char ' + JSON.stringify(ch) + ' at pos ' + i);
  }
  function parseObj() {
    const out = {};
    i++; // {
    skip();
    while (i < src.length && src[i] !== '}') {
      const key = parseKey();
      skip();
      if (src[i] !== ':') throw new Error('Expected : at pos ' + i + ' got ' + src[i]);
      i++;
      const val = parseValue();
      out[key] = val;
      skip();
      if (src[i] === ',') { i++; skip(); }
    }
    i++; // }
    return out;
  }
  skip();
  // Find the OUTERMOST object literal that starts the `const en = { … }`
  // export. Using indexOf('{') lands on the first backticked inline code
  // in the JSDoc comment; walk forward counting braces to find the matching
  // pair that ends at the last '}' in the file.
  const lastBrace = src.lastIndexOf('}');
  let depth = 0, firstBrace = -1;
  for (let k = 0; k <= lastBrace; k++) {
    if (src[k] === '{') { if (depth === 0) firstBrace = k; depth++; }
    else if (src[k] === '}') depth--;
  }
  if (firstBrace < 0) throw new Error('Could not find locale object literal');
  const body = src.slice(firstBrace, lastBrace + 1);
  i = 0;
  src = body;
  const obj = parseObj();
  return obj;
}

function flatten(obj, prefix = '', out = new Map()) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? prefix + '.' + k : k;
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      // Plural leaves are { one, other } — treat as leaf.
      const keys = Object.keys(v);
      const isPlural = keys.includes('one') && keys.includes('other') &&
        keys.every((kk) => typeof v[kk] === 'string');
      if (isPlural) { out.set(key, v); continue; }
      flatten(v, key, out);
    } else {
      out.set(key, v);
    }
  }
  return out;
}

function readTsLocale(code) {
  const p = join(FRONTEND_DIR, code + '.ts');
  const src = readFileSync(p, 'utf8');
  // Strip the `export default` / `export`/`as const` — parseObject slices the
  // outermost braces itself.
  return parseObject(src);
}

/* ---------- 2. Parse a Go locale (var En = map[string]string{...}) ------- */
function parseGoMap(src) {
  const out = {};
  // Find the map literal: 	map[string]string{
  const start = src.indexOf('{');
  const end = src.lastIndexOf('}');
  const body = src.slice(start + 1, end);
  // Match "key": "value", pairs (values can contain \" escapes).
  const re = /"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)"/g;
  let m;
  while ((m = re.exec(body)) !== null) {
    const key = m[1].replace(/\\"/g, '"').replace(/\\n/g, '\n').replace(/\\\\/g, '\\');
    const val = m[2].replace(/\\"/g, '"').replace(/\\n/g, '\n').replace(/\\\\/g, '\\');
    out[key] = val;
  }
  return out;
}

function readGoLocale(code) {
  const p = join(BACKEND_DIR, code + '.go');
  return parseGoMap(readFileSync(p, 'utf8'));
}

/* ---------- 3. Compare and report --------------------------------------- */
const problems = [];

const enFe = flatten(readTsLocale('en'));
const enBe = new Map(Object.entries(readGoLocale('en')));

for (const code of LANGS) {
  // Frontend
  if (code !== 'en') {
    const d = flatten(readTsLocale(code));
    for (const k of enFe.keys()) {
      if (!d.has(k)) problems.push(`[frontend:${code}] missing key "${k}"`);
    }
  }
  // Backend
  if (code !== 'en') {
    const d = new Map(Object.entries(readGoLocale(code)));
    for (const k of enBe.keys()) {
      if (!d.has(k)) problems.push(`[backend:${code}]  missing key "${k}"`);
    }
  }
}

// Cross-check: every "email.*" key in frontend en.ts must exist in backend en.go
for (const k of enFe.keys()) {
  if (k.startsWith('email.') && !enBe.has(k)) {
    problems.push(`[parity]    frontend has "${k}" but backend en.go does not`);
  }
}
for (const k of enBe.keys()) {
  if (k.startsWith('email.') && !enFe.has(k)) {
    problems.push(`[parity]    backend en.go has "${k}" but frontend en.ts does not`);
  }
}

if (problems.length) {
  console.error('i18n check failed:\n');
  for (const p of problems) console.error('  • ' + p);
  console.error(`\n${problems.length} problem(s). Add the missing keys to the listed locale files.`);
  process.exit(1);
}
console.log(`✓ i18n parity OK — ${LANGS.length} languages, ${enFe.size} frontend keys, ${enBe.size} backend keys.`);
