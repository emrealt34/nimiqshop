#!/usr/bin/env node
// build-all.mjs — one-command build for both the Go backend and the Astro
// frontend. devtools/entrypoints invoke this before launching so a fresh
// checkout "just works" without remembering `go build` and `npm run build`.
//
// Behavior:
//   * Backend: if `go` is on PATH and backend/ has go.mod, build
//     backend/bin/nimshop-server(.exe) from ./cmd/server. If `go` is missing
//     but the binary already exists, we keep going (prebuilt binary). If
//     neither exists, exit non-zero with a friendly message.
//   * Frontend: if node_modules/ is missing or package-lock.json is newer,
//     run `npm ci` with a lockfile (or `npm install` without one). If dist/ is missing or
//     any src/ public/ integrations/ scripts/ astro.config.mjs package.json
//     is newer than dist/, run `npm run build` (astro build).
//
//     dist/ is ALSO considered stale — whatever its timestamp says — when any
//     built page or _headers still contains the raw `__CSP_SCRIPT_HASHES__`
//     token, because that means the CSP integration never ran over it. See
//     distHasUnsubstitutedCsp() below.
//
//   * After the build, dist/ is re-read and the build FAILS if that token is
//     still present, instead of shipping pages that block their own scripts.
//
// Safe to run repeatedly — only rebuilds what looks stale.

import { spawnSync } from 'node:child_process';
import { existsSync, statSync, readdirSync, utimesSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { CSP_HASH_PLACEHOLDER, hasPlaceholder } from './csp-inline.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));
const windows = process.platform === 'win32';
const backendDir = resolve(root, process.env.BACKEND_DIR || 'backend');
const binName = windows ? 'nimshop-server.exe' : 'nimshop-server';
const binPath = resolve(root, process.env.BACKEND_BIN || join(backendDir, 'bin', binName));

function log(...a) { console.log('[build]', ...a); }
function err(...a) { console.error('[build]', ...a); }

function has(cmd) {
  const res = spawnSync(windows ? 'where' : 'which', [cmd], { stdio: 'ignore' });
  return res.status === 0;
}

function run(cmd, args, cwd = root, env = process.env) {
  log('$', cmd, args.join(' '), `(in ${cwd})`);
  const r = spawnSync(cmd, args, { cwd, stdio: 'inherit', shell: windows, env });
  if (r.error) throw new Error(`Failed to spawn ${cmd}: ${r.error.message}`);
  if (r.status !== 0) throw new Error(`${cmd} exited with status ${r.status}`);
}

// readEnvValue pulls one KEY= value out of a .env file (last assignment wins,
// comments ignored). Used so backend/.env's PUBLIC_API_URL can drive the
// frontend build without duplicating the URL anywhere else.
function readEnvValue(file, key) {
  try {
    const text = readFileSync(file, 'utf8');
    let value = '';
    for (const line of text.split('\n')) {
      const m = /^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)\s*$/.exec(line);
      if (m && m[1] === key) value = m[2].replace(/^["']|["']$/g, '');
    }
    return value.trim();
  } catch {
    return '';
  }
}

function newerThan(src, target) {
  try { return statSync(src).mtimeMs > statSync(target).mtimeMs; } catch { return !existsSync(target); }
}

function anyNewerThan(sources, target) {
  if (!existsSync(target)) return true;
  const t = statSync(target).mtimeMs;
  for (const s of sources) {
    if (!existsSync(s)) continue;
    const st = statSync(s);
    if (st.isDirectory()) {
      const stack = [s];
      while (stack.length) {
        const dir = stack.pop();
        for (const entry of readdirSync(dir, { withFileTypes: true })) {
          const p = join(dir, entry.name);
          if (entry.isDirectory()) stack.push(p);
          else if (statSync(p).mtimeMs > t) return true;
        }
      }
    } else if (st.mtimeMs > t) {
      return true;
    }
  }
  return false;
}

function buildBackend({ force = false } = {}) {
  const goMod = join(backendDir, 'go.mod');
  const needs = force || !existsSync(binPath) || anyNewerThan(
    [join(backendDir, 'cmd'), join(backendDir, 'internal'), goMod],
    binPath,
  );
  if (!needs) { log('backend binary up to date:', binPath); return; }
  const goAvail = has('go');
  if (!existsSync(goMod)) {
    if (existsSync(binPath)) { log('no go.mod, keeping existing binary'); return; }
    throw new Error(`No backend source at ${backendDir} and no prebuilt binary at ${binPath}`);
  }
  if (!goAvail) {
    throw new Error(
      'Go is not installed and no prebuilt backend binary exists. Install Go 1.26+ (https://go.dev/dl/) and retry, or drop a prebuilt binary at ' + binPath
    );
  }
  // Build the committed module graph without rewriting go.mod/go.sum.
  // Archives need no VCS metadata from an unrelated parent checkout.
  run('go', ['build', '-mod=readonly', '-buildvcs=false', '-o', binPath, './cmd/server'], backendDir);
  if (!windows) { try { utimesSync(binPath, new Date(), new Date()); } catch {} }
  log('backend built →', binPath);
}

function npmInstallIfNeeded({ force = false } = {}) {
  const nm = join(root, 'node_modules');
  const lock = join(root, 'package-lock.json');
  const pkg = join(root, 'package.json');
  const needsInstall = force || !existsSync(nm) || newerThan(pkg, nm) || newerThan(lock, nm);
  if (!needsInstall) { log('node_modules up to date'); return; }
  if (has('npm')) {
    if (existsSync(lock)) {
      run('npm', ['ci'], root); return;
    }
    run('npm', ['install'], root);
  } else {
    throw new Error('npm is not installed. Install Node.js 22.13+ or 24+ (https://nodejs.org/) and retry.');
  }
}

// Self-heal a stale/corrupt root package.json. On some machines the local
// package.json drifts from the repo (e.g. an older copy without the "build"
// script), which makes `npm run build` die with the cryptic
// `npm error Missing script: "build"` — the exact failure that took the
// devtools launcher down. Ensure the scripts we need exist before invoking
// them; never touch anything that is already there.
function ensurePackageScripts() {
  const pkg = join(root, 'package.json');
  let data;
  try { data = JSON.parse(readFileSync(pkg, 'utf8')); }
  catch (e) { throw new Error(`Cannot read/parse ${pkg}: ${e.message}`, { cause: e }); }
  const wanted = {
    build: 'astro build',
    dev: 'astro dev --host 0.0.0.0',
  };
  let changed = false;
  for (const [name, cmd] of Object.entries(wanted)) {
    const scripts = (data.scripts ??= {});
    if (typeof scripts[name] !== 'string' || !scripts[name].trim()) {
      scripts[name] = cmd;
      changed = true;
    }
  }
  if (changed) {
    writeFileSync(pkg, JSON.stringify(data, null, 2) + '\n', 'utf8');
    log('restored missing npm scripts in package.json (build/dev) and continuing');
  }
}

/**
 * True when dist/ is a build whose CSP placeholder was never substituted.
 *
 * This is the check that was missing, and its absence is exactly how a broken
 * policy reached a browser. The old staleness test only compared mtimes, so a
 * dist/ built before the CSP integration existed — or extracted from an
 * archive whose timestamps make it look newer than src/ — was declared "up to
 * date" and served as-is. Every page then shipped
 * `script-src 'self' __CSP_SCRIPT_HASHES__`, which the browser reports as an
 * invalid source, ignores, and then blocks all four inline scripts: the theme
 * never applies, the Nimiq-Pay viewport fix never runs, scroll position is
 * lost. mtime cannot see that; reading the output can.
 */
function distHasUnsubstitutedCsp(dist) {
  if (!existsSync(dist)) return false;
  const stack = [dist];
  while (stack.length) {
    const dir = stack.pop();
    let entries;
    try { entries = readdirSync(dir, { withFileTypes: true }); } catch { continue; }
    for (const entry of entries) {
      const p = join(dir, entry.name);
      if (entry.isDirectory()) { stack.push(p); continue; }
      if (!entry.name.endsWith('.html') && entry.name !== '_headers') continue;
      try {
        if (hasPlaceholder(readFileSync(p, 'utf8'))) return true;
      } catch { /* unreadable — the mtime check will handle it */ }
    }
  }
  return false;
}

/** Fail the build rather than ship a dist/ whose CSP blocks every script. */
function assertDistCspSubstituted(dist) {
  if (!distHasUnsubstitutedCsp(dist)) return;
  throw new Error(
    'dist/ still contains the raw ' + CSP_HASH_PLACEHOLDER + ' token after the build.\n' +
    '  That means the CSP integration (integrations/csp-inline-hashes.mjs, registered in\n' +
    '  astro.config.mjs) did not run — the shipped pages would block their own inline scripts.\n' +
    '  Fix: run "npm run rebuild" (or devtools\\build.bat). If it still happens, check that\n' +
    '  astro.config.mjs lists cspInlineHashes() in its integrations array.'
  );
}

function buildFrontend({ force = false } = {}) {
  const dist = join(root, 'dist');
  const marker = join(dist, 'index.html');
  const sources = [
    join(root, 'src'),
    join(root, 'public'),
    join(root, 'astro.config.mjs'),
    // The CSP integration is build-affecting code that lives outside src/:
    // without it here, editing how hashes are computed would never trigger a
    // rebuild and dist/ would keep the old (or unsubstituted) policy.
    join(root, 'integrations'),
    join(root, 'scripts'),
    join(root, 'vite.config.js'),
    join(root, 'tsconfig.json'),
    join(root, 'package.json'),
    // backend/.env participates through PUBLIC_API_URL: when it changes, the
    // baked API_BASE/CSP must follow, so it counts as a build input.
    join(root, 'backend', '.env'),
  ];
  // Reading dist/ beats trusting its mtime: a dist/ that still carries the raw
  // CSP placeholder is stale no matter how recent its timestamp is.
  const staleCsp = distHasUnsubstitutedCsp(dist);
  const needs = force || staleCsp || !existsSync(marker) || anyNewerThan(sources, marker);
  if (staleCsp) log('dist/ carries an unsubstituted CSP placeholder — rebuilding');
  if (!needs) { log('frontend dist/ up to date'); return; }
  npmInstallIfNeeded();
  ensurePackageScripts();
  // AUTO API_URL: one place — backend/.env — drives the frontend build. An
  // explicit API_URL/PUBLIC_API_URL env var still wins (CI overrides), but by
  // default PUBLIC_API_URL from backend/.env is baked into every page
  // (API_BASE seed + CSP connect-src + preconnect) so `npm run build` alone
  // produces a dist/ pointed at the right API for cross-domain deployments.
  // SAME-ORIGIN BY DEFAULT. backend/.env's PUBLIC_API_URL is the BACKEND's
  // own setting (its public address). It used to be copied here, which baked
  // the API origin into every page as API_BASE. The browser then called
  // shopapi.* from shop.*: a cross-site session cookie that Safari and Nimiq
  // Pay refuse, so sign-in looked fine and vanished on the next page load. An
  // absolute API origin is used only when the build environment sets API_URL
  // or PUBLIC_API_URL explicitly (for example the Pages workflow sets /api).
  const apiUrl = String(process.env.API_URL || process.env.PUBLIC_API_URL || '').trim();
  const buildEnv = process.env;
  if (apiUrl) log(`API_URL (from environment): ${apiUrl}`);
  else log("API_URL not set: frontend uses the same-origin '/api' proxy (session cookies stay first-party)");
  try {
    run('npm', ['run', 'build'], root, buildEnv);
  } catch (e) {
    throw new Error(
      `${e.message} — the Astro build failed (see compiler output above). ` +
      'If you see "Missing script: build", delete the local package.json and re-extract the project, or run "npm run build" by hand to see the full error.'
    , { cause: e });
  }
  // Verify the OUTPUT, not just the exit code. `astro build` can succeed while
  // the CSP integration is absent from the config (or while a stale dist/ is
  // reused), and the only symptom is a browser console full of "invalid source
  // __CSP_SCRIPT_HASHES__". Catch it here, where it is still actionable.
  assertDistCspSubstituted(dist);
  // API_URL build knob: when the frontend is built to call an absolute API
  // (e.g. hosted on Cloudflare Pages with the backend elsewhere), the origin
  // must also be allowed by the static _headers CSP copy used by static hosts.
  if (apiUrl) {
    const headersFile = join(dist, '_headers');
    if (existsSync(headersFile)) {
      let t = readFileSync(headersFile, 'utf8');
      if (!t.includes(apiUrl)) {
        t = t.replace(/connect-src 'self'/, `connect-src 'self' ${apiUrl}`);
        writeFileSync(headersFile, t);
        log(`API_URL: added ${apiUrl} to dist/_headers connect-src`);
      }
    }
  }
  log('frontend built →', dist);
}

const args = new Set(process.argv.slice(2));
const force = args.has('--force') || args.has('-f');
const only = args.has('--backend-only') ? 'backend' : args.has('--frontend-only') ? 'frontend' : 'all';

try {
  if (only === 'all' || only === 'backend') buildBackend({ force });
  if (only === 'all' || only === 'frontend') buildFrontend({ force });
  log('build done');
} catch (e) {
  err(e.message);
  process.exit(1);
}
