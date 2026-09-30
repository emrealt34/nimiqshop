#!/usr/bin/env node
// run-backend-only.mjs — start ONLY the Go backend (no frontend, no proxy).
// Use this when the frontend is hosted elsewhere (e.g. Cloudflare Pages):
//
//   npm run start:backend                 # build backend if needed, then run
//   npm run start:backend -- --no-build   # skip the build check
//
// CORS reminder: the browser on your Cloudflare frontend calls this backend
// directly, so its origin must be allowed — ONE line in backend/.env:
//   FRONTEND_URL=https://your-frontend.pages.dev
// (that line feeds the whole CORS allowlist; see README_SETUP.md).
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, copyFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const args = process.argv.slice(2);
const skipBuild = args.includes('--no-build') || args.includes('--skip-build');
const forceBuild = args.includes('--build') || args.includes('--rebuild');
const windows = process.platform === 'win32';
const backendDir = resolve(root, process.env.BACKEND_DIR || 'backend');
const executable = resolve(root, process.env.BACKEND_BIN || join(backendDir, 'bin', windows ? 'nimshop-server.exe' : 'nimshop-server'));

// 1. Build if the binary is missing (or --rebuild was passed).
if (!existsSync(executable) || forceBuild) {
  if (skipBuild && !forceBuild) {
    console.error(`[backend] binary missing at ${executable} — remove --no-build or pass --build`);
    process.exit(1);
  }
  console.log('[backend] building…');
  const buildArgs = ['scripts/build-all.mjs', '--backend-only'];
  if (forceBuild) buildArgs.push('--force');
  const r = spawnSync(process.execPath, buildArgs, { cwd: root, stdio: 'inherit', windowsHide: true });
  if (r.status !== 0) process.exit(r.status ?? 1);
}

// 2. First-run convenience: create backend/.env from the template (never overwrite).
const envExample = join(backendDir, '.env.example');
const envFile = join(backendDir, '.env');
if (!existsSync(envFile) && existsSync(envExample)) {
  copyFileSync(envExample, envFile);
  console.log(`[backend] created ${envFile} from the template — edit JWT_SECRET / ADMIN_PASSWORD / FRONTEND_URL before going live`);
}

// 3. Run the server. .env is loaded by the binary itself from its cwd.
const port = Number(process.env.API_PORT || 8084);
console.log(`[backend] starting ${executable} (cwd ${backendDir})`);
console.log(`[backend] health check:  curl http://localhost:${port}/api/health`);
console.log('[backend] admin panel:   served by the frontend at /admin (this process is the API only)');
const child = spawn(executable, [], {
  cwd: backendDir,
  env: process.env,
  stdio: 'inherit',
  windowsHide: true,
  detached: !windows,
});
const bye = () => {
  try { if (windows) spawn('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' }); else process.kill(-child.pid, 'SIGTERM'); } catch { try { child.kill('SIGTERM'); } catch {} }
};
for (const s of ['SIGINT', 'SIGTERM']) process.once(s, () => bye());
child.once('exit', (code, signal) => {
  console.log(`[backend] exited (${signal || 'code ' + code})`);
  process.exit(code ?? 0);
});
