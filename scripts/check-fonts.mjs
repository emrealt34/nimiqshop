#!/usr/bin/env node
/**
 * check-fonts.mjs — every character this shop can print must exist in the
 * vendored fonts.
 *
 * Why this exists: `unicode-range` in fonts.css promises which characters a
 * face covers, but a *promise* is all it is — the file behind it can be a
 * subset that lacks the glyph (the Turkish İ ı Ş ş Ğ ğ were declared, and
 * missing, for months: they silently fell back to a system font mid-word).
 * This reads the actual cmap out of each .woff2 and fails if any character the
 * six locales use is not in the files.
 *
 * WOFF2 stores `cmap` untransformed, so this is a dependency-free parser: read
 * the table directory, brotli-decompress the cmap table, walk its subtables.
 * Emoji and pictographs are skipped on purpose — those are meant to come from
 * the system emoji font, not from a text face.
 */
import { readFileSync, readdirSync } from 'node:fs';
import { brotliDecompressSync } from 'node:zlib';
import { join } from 'node:path';

const FONT_DIR = 'src/assets/fonts';

/** codepoints covered by one .woff2 */
const KNOWN_TAGS = ['cmap', 'head', 'hhea', 'hmtx', 'maxp', 'name', 'OS/2', 'post', 'cvt ', 'fpgm', 'glyf', 'loca', 'prep', 'CFF ', 'VORG', 'EBDT', 'EBLC', 'gasp', 'hdmx', 'kern', 'LTSH', 'PCLT', 'VDMX', 'vhea', 'vmtx', 'BASE', 'GDEF', 'GPOS', 'GSUB', 'EBSC', 'JSTF', 'MATH', 'CBDT', 'CBLC', 'COLR', 'CPAL', 'SVG ', 'sbix', 'acnt', 'avar', 'bdat', 'bloc', 'bsln', 'cvar', 'fdsc', 'feat', 'fmtx', 'fvar', 'gvar', 'hsty', 'just', 'lcar', 'mort', 'morx', 'opbd', 'prop', 'trak', 'Zapf', 'Silf', 'Glat', 'Gloc', 'Feat', 'Sill'];

function cmapOf(file) {
  const buf = readFileSync(file);
  if (buf.toString('ascii', 0, 4) !== 'wOF2') throw new Error(`${file}: not WOFF2`);
  const numTables = buf.readUInt16BE(12);
  const totalCompressedSize = buf.readUInt32BE(20);
  // UIntBase128, per the WOFF2 spec
  let at = 48;
  const varint = () => {
    let result = 0;
    for (let i = 0; i < 5; i++) {
      const b = buf[at++];
      if (i === 0 && b === 0x80) throw new Error(`${file}: bad UIntBase128`);
      result = (result << 7) | (b & 0x7f);
      if ((b & 0x80) === 0) return result >>> 0;
    }
    throw new Error(`${file}: UIntBase128 too long`);
  };
  const tables = [];
  for (let i = 0; i < numTables; i++) {
    const flags = buf[at++];
    const tagIndex = flags & 0x3f;
    const transform = flags >> 6;
    const tag = tagIndex === 63 ? buf.toString('ascii', at, (at += 4)) : KNOWN_TAGS[tagIndex];
    const origLength = varint();
    // Only glyf/loca carry a real transform (version 0 = transformed, 3 =
    // null); every other table is stored as-is. Mis-reading this shifts the
    // whole directory and the brotli stream then fails to decompress.
    const transformed = (tag === 'glyf' || tag === 'loca') ? transform === 0 : transform === 1;
    const stored = transformed ? varint() : origLength;
    tables.push({ tag, origLength, stored });
  }
  // the whole table data is ONE brotli stream
  const dataStart = at;
  const data = brotliDecompressSync(buf.subarray(dataStart, dataStart + totalCompressedSize));
  let off = 0;
  const found = [];
  for (const t of tables) {
    if (t.tag === 'cmap') found.push(data.subarray(off, off + t.origLength));
    off += t.stored;
  }
  if (!found.length) throw new Error(`${file}: no cmap table`);
  const cmap = found[0];
  const covered = new Set();
  const numSubtables = cmap.readUInt16BE(2); // header: version(2) + numTables(2)
  for (let i = 0; i < numSubtables; i++) {
    const sub = cmap.readUInt32BE(8 + i * 8);
    const format = cmap.readUInt16BE(sub);
    if (format === 4) {
      const segCountX2 = cmap.readUInt16BE(sub + 6);
      const endBase = sub + 14;
      const startBase = endBase + segCountX2 + 2;
      const deltaBase = startBase + segCountX2;
      const rangeBase = deltaBase + segCountX2;
      for (let s = 0; s < segCountX2 / 2; s++) {
        const end = cmap.readUInt16BE(endBase + s * 2);
        const start = cmap.readUInt16BE(startBase + s * 2);
        if (start > end || start === 0xffff) continue;
        const delta = cmap.readInt16BE(deltaBase + s * 2);
        const rangeOffset = cmap.readUInt16BE(rangeBase + s * 2);
        for (let cp = start; cp <= end && cp !== 0x10000; cp++) {
          if (rangeOffset === 0) { if (((cp + delta) & 0xffff) !== 0) covered.add(cp); }
          else {
            const gi = cmap.readUInt16BE(rangeBase + s * 2 + rangeOffset + (cp - start) * 2);
            if (gi !== 0) covered.add(cp);
          }
        }
      }
    } else if (format === 12) {
      const nGroups = cmap.readUInt32BE(sub + 12);
      for (let g = 0; g < nGroups; g++) {
        const b = sub + 16 + g * 12;
        const start = cmap.readUInt32BE(b), end = cmap.readUInt32BE(b + 4);
        for (let cp = start; cp <= end; cp++) covered.add(cp);
      }
    }
  }
  return covered;
}

/** text the app can actually print */
function usedChars() {
  const chars = new Set();
  const files = [];
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (/\.(tsx?|astro)$/.test(p)) files.push(p);
    }
  };
  walk('src'); // every locale file, component and page, so new copy is covered too
  for (const f of files) for (const c of readFileSync(f, 'utf8')) chars.add(c);
  return chars;
}

const fonts = readdirSync(FONT_DIR).filter((f) => f.endsWith('.woff2'));
const coverage = new Map(fonts.map((f) => [f, cmapOf(join(FONT_DIR, f))]));
const union = new Set();
for (const set of coverage.values()) for (const cp of set) union.add(cp);

/**
 * Two buckets, because they mean different things:
 *
 *  - letters/digits that are missing are a BUG: the browser swaps in a system
 *    font for that character only, so a word renders in two typefaces (the
 *    Turkish "İ" used to do exactly this) — exit 1.
 *  - symbols the upstream webfonts simply do not contain (arrows, math, ₮,
 *    blackboard 𝕏) can only come from the system font; they are listed so a
 *    future copy change that leans on them is a conscious choice — warn only.
 */
const bug = [];
const fallback = [];
for (const ch of usedChars()) {
  const cp = ch.codePointAt(0);
  if (cp < 0x80) continue;
  if (cp >= 0x1f000 || /[\p{So}\p{Sk}\p{Cf}\p{Mn}]/u.test(ch)) continue; // emoji / variation selectors
  if (union.has(cp)) continue;
  const entry = `${ch} (U+${cp.toString(16).toUpperCase().padStart(4, '0')})`;
  // U+2139 ℹ and U+1D54F 𝕏 sit in the letter category but are used as icons;
  // the upstream webfonts have no glyph for either.
  const symbolLike = cp === 0x2139 || cp === 0x1d54f;
  if (!symbolLike && /[\p{L}\p{Nd}]/u.test(ch)) bug.push(entry);
  else fallback.push(entry);
}

for (const [f, set] of coverage) console.log(`  ${f.padEnd(22)} ${set.size} codepoints`);

if (fallback.length) {
  console.log(`\n… ${fallback.length} symbol(s) with no glyph in any vendored face — they render from the`);
  console.log('  system font when the copy uses them (decorative only, upstream has no glyph):');
  console.log('  ' + [...new Set(fallback)].join(' '));
}

if (bug.length) {
  console.error(`\n✘ ${bug.length} letter/digit(s) in NO vendored font — text would fall back`);
  console.error('  mid-word to a system font. Add them to the subset (recipe in src/styles/fonts.css):');
  console.error('  ' + [...new Set(bug)].join(' '));
  process.exit(1);
}
console.log(`\n✔ every letter and digit the six locales use is in the vendored faces (${fonts.length} files)`);
