#!/usr/bin/env node
/**
 * generate.mjs — nim.shop build-integrity manifest generator.
 *
 *   node generate.mjs            # frontend (default)
 *   node generate.mjs backend    # backend Go source -> internal/integrity/source.integrity.json
 *   generate.mjs all             # both
 *
 * Hashes every file under a target directory with SHA-256 and derives ONE
 * deterministic ROOT hash over the canonical "path\nsha256\n" listing (sorted
 * by path). That root is the value you publish out-of-band (git tag / release).
 *
 * Reproducibility: the manifest contains NO volatile fields (no timestamp, no
 * host). Two builds from the same source produce byte-identical manifests, so
 * embedding the backend manifest into the binary does not break reproducible
 * builds. The root hash depends only on (path, sha256) pairs.
 */
import { createHash } from 'node:crypto';
import { readFile, writeFile, readdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, join, relative, sep } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));   // .../tools/integrity
const REPO = join(HERE, '..', '..');                     // repo root

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');
const toPosix = (p) => p.split(sep).join('/');

// Per-target configuration.
const TARGETS = {
  frontend: {
    root: join(REPO, 'frontend'),
    out: join(REPO, 'frontend', 'integrity.json'),
    basePath: 'frontend',
    // Only files actually deployed to the web root.
    includeExts: null, // include everything not ignored
    includeFiles: null,
    ignoreFiles: new Set(['integrity.json', 'README.md', 'verify.html', '_headers']),
    ignoreDirs: new Set(['dev', 'node_modules', '.git', 'dist', 'out', 'build', '.cache', 'coverage', '__pycache__']),
  },
  backend: {
    root: join(REPO, 'backend'),
    out: join(REPO, 'backend', 'internal', 'integrity', 'source.integrity.json'),
    basePath: 'backend',
    // Source that defines the build. go.sum pins deps (critical for reproducibility).
    includeExts: ['.go', '.mod', '.sum', '.proto'],
    includeFiles: new Set(['.env.example']),
    ignoreFiles: new Set(['internal/integrity/source.integrity.json']),
    ignoreDirs: new Set(['node_modules', '.git', 'dist', 'out', 'build', '.cache', 'coverage', '__pycache__', 'bin']),
  },
};

async function walk(dir, ignoreDirs) {
  // Hard safety net: these are NEVER hashed, no matter what a target config
  // says. Without this, a local `npm ci` (node_modules) or a stray build dir
  // would change the manifest on a dev machine and the CI drift check would
  // fail for reasons unrelated to the source.
  const ALWAYS_IGNORE = new Set(['node_modules', '.git', 'dist', 'out', 'build', '.cache', 'coverage', '__pycache__']);
  let out = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (ignoreDirs.has(entry.name) || ALWAYS_IGNORE.has(entry.name)) continue;
      out = out.concat(await walk(join(dir, entry.name), ignoreDirs));
    } else {
      out.push(join(dir, entry.name));
    }
  }
  return out;
}

function wanted(abs, cfg) {
  const rel = toPosix(relative(cfg.root, abs));
  if (cfg.ignoreFiles.has(rel)) return false;
  if (rel.startsWith('internal/integrity/source.integrity.json')) return false;
  if (cfg.includeExts || cfg.includeFiles) {
    const ext = abs.slice(abs.lastIndexOf('.'));
    if (cfg.includeFiles && cfg.includeFiles.has(rel)) return true;
    if (cfg.includeExts && cfg.includeExts.includes(ext)) return true;
    return false; // backend: only explicit source types
  }
  return true; // frontend: everything not ignored
}

async function generate(name) {
  const cfg = TARGETS[name];
  if (!cfg) throw new Error(`unknown target: ${name}`);

  const absFiles = (await walk(cfg.root, cfg.ignoreDirs)).filter((f) => wanted(f, cfg));
  const entries = [];
  for (const abs of absFiles) {
    const rel = toPosix(relative(cfg.root, abs));
    const buf = await readFile(abs);
    entries.push({ path: rel, sha256: sha256(buf), size: buf.length });
  }
  entries.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));

  const canon = entries.map((e) => `${e.path}\n${e.sha256}\n`).join('');
  const rootHash = sha256(Buffer.from(canon, 'utf8'));

  const manifest = {
    schema: 'nimiq-shop.integrity/v1',
    target: name,
    algorithm: 'sha256',
    rootHashAlgorithm: 'sha256 over sorted "path\\nsha256\\n" lines',
    rootHash,
    basePath: cfg.basePath,
    fileCount: entries.length,
    files: entries, // NOTE: no timestamp/host — fully reproducible
  };

  await writeFile(cfg.out, JSON.stringify(manifest, null, 2) + '\n', 'utf8');

  console.log(`✓ [${name}] ${toPosix(relative(REPO, cfg.out))}  (${entries.length} files)`);
  console.log(`   root: ${rootHash}\n`);
  return rootHash;
}

const target = process.argv[2] || 'frontend';
const roots = {};
if (target === 'all') {
  roots.frontend = await generate('frontend');
  roots.backend = await generate('backend');
} else {
  roots[target] = await generate(target);
}

console.log('════════════════════════════════════════════════════════════');
for (const [k, v] of Object.entries(roots)) {
  console.log(`  PUBLISH [${k}] root hash:  ${v}`);
}
console.log('════════════════════════════════════════════════════════════');
