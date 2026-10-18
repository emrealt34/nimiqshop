#!/usr/bin/env node
/**
 * Full-stack browser run: the REAL backend (Go handlers, Badger, idempotency
 * gate, settlement tracker, in-process supplier mock — TEST_MODE off) under
 * the real storefront build in a real browser. No API mocks.
 *
 *   npm run test:e2e:fullstack            # all tests/e2e/fullstack-*.spec.ts
 *   npm run test:e2e:fullstack -- --headed
 *
 * What it does:
 *   1. boots backend/cmd/server TestServeStackForBrowser on a free port; the
 *      harness signs a fresh buyer in and writes {base_url, supplier_url,
 *      bearer, uid, address} to a temp file;
 *   2. runs Playwright (chromium, one worker — the specs are stateful
 *      lifecycles of ONE buyer) with E2E_BACKEND_INFO pointing at that file;
 *   3. stops the stack through its stop file and exits with Playwright's code.
 *
 * Needs Go on PATH (the backend toolchain) and the e2e layout in dist/
 * (`npm run test:e2e:build`, run automatically when dist/ is missing).
 */
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
const backend = path.join(root, 'backend');
const dir = mkdtempSync(path.join(tmpdir(), 'nimshop-fullstack-'));
const info = path.join(dir, 'stack.json');
const stop = info + '.stop';
const extra = process.argv.slice(2);

const freePort = () => new Promise((resolve, reject) => {
  const srv = createServer();
  srv.unref();
  srv.on('error', reject);
  srv.listen(0, '127.0.0.1', () => { const { port } = srv.address(); srv.close(() => resolve(port)); });
});

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

if (!existsSync(path.join(root, 'dist'))) {
  const built = spawnSync(process.execPath, [path.join(root, 'tests/e2e/support/build.mjs')], { stdio: 'inherit', cwd: root });
  if (built.status !== 0) process.exit(built.status ?? 1);
}

const port = await freePort();
const stack = spawn('go', ['test', './cmd/server', '-run', 'TestServeStackForBrowser', '-count=1', '-timeout', '40m', '-v'], {
  cwd: backend,
  env: { ...process.env, BROWSER_STACK_ADDR: `127.0.0.1:${port}`, BROWSER_STACK_OUT: info, BROWSER_STACK_STOP: stop, BROWSER_STACK_TIMEOUT: '30m' },
  stdio: ['ignore', 'pipe', 'inherit'],
});
let stackLog = '';
stack.stdout.on('data', (d) => { stackLog += d; if (process.env.FULLSTACK_VERBOSE) process.stdout.write(d); });
let stackExit = null;
stack.on('exit', (code) => { stackExit = code ?? 1; });

const shutdown = async () => {
  if (stackExit !== null) return;
  try { writeFileSync(stop, ''); } catch { /* the harness may already be gone */ }
  for (let i = 0; i < 80 && stackExit === null; i++) await sleep(250);
  if (stackExit === null) stack.kill('SIGTERM');
};
process.on('SIGINT', async () => { await shutdown(); process.exit(130); });

// Wait for the harness to come up (it compiles first: allow a few minutes).
const deadline = Date.now() + 5 * 60_000;
while (!existsSync(info)) {
  if (stackExit !== null) {
    process.stderr.write(`backend harness exited early (${stackExit}):\n${stackLog}`);
    rmSync(dir, { recursive: true, force: true });
    process.exit(1);
  }
  if (Date.now() > deadline) {
    process.stderr.write(`backend harness did not come up in time:\n${stackLog}`);
    await shutdown();
    rmSync(dir, { recursive: true, force: true });
    process.exit(1);
  }
  await sleep(200);
}
process.stdout.write(`real stack up on 127.0.0.1:${port} — running the full-stack specs\n`);

const pw = spawnSync(process.platform === 'win32' ? 'npx.cmd' : 'npx',
  ['playwright', 'test', '--project=chromium', '--workers=1', 'fullstack-', ...extra],
  { cwd: root, stdio: 'inherit', env: { ...process.env, E2E_BACKEND_INFO: info } });

await shutdown();
if (stackExit !== 0) process.stderr.write(`backend harness exit ${stackExit}:\n${stackLog.split('\n').filter((l) => /FAIL|panic|error/i.test(l)).join('\n')}\n`);
rmSync(dir, { recursive: true, force: true });
process.exit(pw.status ?? 1);
