#!/usr/bin/env node
// devtools/devtools.mjs — single entrypoint that builds AND runs the whole
// stack. Usage:
//
//   node devtools/devtools.mjs              # same as: build-all then preview
//   node devtools/devtools.mjs dev          # build backend only + astro dev server
//   node devtools/devtools.mjs preview      # build backend + frontend, then preview
//   node devtools/devtools.mjs tunnel       # build + preview + Cloudflare quick tunnel
//   node devtools/devtools.mjs build        # build everything then exit
//   node devtools/devtools.mjs rebuild      # force rebuild everything then exit
//   node devtools/devtools.mjs --no-build … # skip build step (use what's already there)
//
// Windows: devtools\start.bat calls this with "tunnel" mode.
// Linux/macOS: ./start.sh calls scripts/run-stack.mjs which ALSO auto-builds,
// so this is the unified "devtools owns the build" entrypoint.

import { spawnSync } from 'node:child_process';
import { resolve, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..');

const allArgs = process.argv.slice(2);
const flags = allArgs.filter(a => a.startsWith('-'));
const modeOrCmd = allArgs.find(a => !a.startsWith('-')) || 'preview';

function run(nodeScript, extraArgs = [], stdio = 'inherit') {
  const args = [nodeScript, ...extraArgs];
  const r = spawnSync(process.execPath, args, { cwd: root, stdio, windowsHide: true });
  if (r.error) { console.error('[devtools] failed to start node:', r.error.message); process.exit(1); }
  return r.status ?? 1;
}

const buildAll = join(root, 'scripts/build-all.mjs');
const runStack = join(root, 'scripts/run-stack.mjs');

const skipBuild = flags.includes('--no-build') || flags.includes('--skip-build');
const forceBuild = flags.includes('--build') || flags.includes('--rebuild') || flags.includes('-f');

let buildArgs = [...flags];
if (modeOrCmd === 'dev') {
  // Dev mode only needs the backend binary (astro dev serves src directly).
  buildArgs.push('--backend-only');
}
if (modeOrCmd === 'build' || modeOrCmd === 'rebuild') {
  // Build-only commands: build then exit.
  if (forceBuild || modeOrCmd === 'rebuild') buildArgs.push('--force');
  process.exit(run(buildAll, buildArgs));
}
if (!skipBuild) {
  if (forceBuild) buildArgs.push('--force');
  const code = run(buildAll, buildArgs);
  if (code !== 0) {
    console.error('[devtools] build failed; pass --no-build to skip.');
    process.exit(code);
  }
}

// Launch the stack. Pass --no-build because we already built above; avoids
// re-running the staleness check a second time.
const launchArgs = [modeOrCmd, '--no-build', ...flags.filter(f => f !== '--build' && f !== '--rebuild' && f !== '-f')];
process.exit(run(runStack, launchArgs));
