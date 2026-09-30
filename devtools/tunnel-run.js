#!/usr/bin/env node
/**
 * tunnel-run.js — start a Cloudflare Quick Tunnel AND report its URL — AND KEEP
 * IT ALIVE.
 *
 * Previously this only inherited cloudflared's output: the URL scrolled past in
 * the console and nothing else ever learned it, so the browser kept calling the
 * production API_BASE and the Go process kept its production SITE_HOST /
 * ALLOWED_ORIGINS. Now the URL is parsed out of cloudflared's log and written to
 * devtools/.runtime/public-url.json, which scripts/run-stack.mjs waits for and
 * wires into the backend env + the generated /config.js.
 *
 * RELIABILITY (the "sometimes unreachable" root fix): a quick tunnel is a free,
 * unauthenticated edge registration — cloudflared CAN exit on its own, and the
 * edge CAN drop a session. Before this, either case left a dead public URL and
 * a stack that looked broken while localhost was fine. Now:
 *
 *   • SUPERVISOR — if cloudflared exits while we are supposed to be running, it
 *     is respawned after restartBackoff(attempt) seconds (2/5/10/20/30). The new
 *     registration yields a new hostname; `reported` is reset so the new URL is
 *     written to public-url.json, and run-stack's watcher picks it up,
 *     regenerates /config.js and re-wires the backend automatically.
 *   • HEARTBEAT — every TUNNEL_HEARTBEAT_MS (default 20 s) the reported URL is
 *     probed through the tunnel-health ladder (system DNS → DoH → direct IP).
 *     Only a hostname that resolvers KNOW but whose edge stops answering counts
 *     as a dead tunnel; DNS-propagation silence never triggers a restart. Four
 *     consecutive dead probes (≈80 s) respawn cloudflared, at most once per
 *     TUNNEL_RESTART_COOLDOWN_MS (default 90 s).
 *   • Neither mechanism ever kills the local stack: this process only owns the
 *     tunnel child.
 *
 * Output is still forwarded verbatim (stderr), so the console looks the same.
 */
import { spawn } from 'node:child_process';
import { existsSync, rmSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { normalizePublicUrl, parseTunnelUrl, writePublicUrl } from '../scripts/public-url.mjs';
import { probePublicUrl, restartBackoff } from '../scripts/tunnel-health.mjs';

const args = process.argv.slice(2);
const pos = args.indexOf('--port');
const port = Number(pos >= 0 ? args[pos + 1] : process.env.PORT || 8085);
if (!Number.isInteger(port) || port < 1 || port > 65535) {
  console.error('Invalid tunnel port'); process.exit(1);
}
const here = dirname(fileURLToPath(import.meta.url));
const exe = process.platform === 'win32' ? 'cloudflared-windows-amd64.exe' : null;
const localBin = exe && existsSync(join(here, '.bin', exe)) ? join(here, '.bin', exe) : null;
// Resolution order: explicit CLOUDFLARED_BIN → locally downloaded .bin → PATH.
const bin = process.env.CLOUDFLARED_BIN || localBin || 'cloudflared';
// The launcher's run id. It is stamped into public-url.json so run-stack can
// tell THIS tunnel apart from a record left behind by a previous launcher that
// was hard-killed (stop.bat uses taskkill /F, so its cleanup never runs).
const runId = String(process.env.NIMSHOP_TUNNEL_RUN_ID || '').trim();
const HEARTBEAT_MS = Number(process.env.TUNNEL_HEARTBEAT_MS || 20_000);
const COOLDOWN_MS = Number(process.env.TUNNEL_RESTART_COOLDOWN_MS || 90_000);
const MAX_RESTARTS = Number(process.env.TUNNEL_MAX_RESTARTS || 50);
const DEAD_PROBES_BEFORE_RESTART = 4;

// Never let a previous run's URL be mistaken for this one. NOTE: this alone was
// NOT enough — run-stack starts polling before this child has even booted, so a
// leftover file used to win the race. The run id above is the real guard; this
// delete just keeps the directory tidy.
try { rmSync(join(here, '.runtime', 'public-url.json'), { force: true }); } catch {}

let cleaning = false;
let child = null;
let restarts = 0;
let lastRestartAt = 0;
let reported = false;
let currentUrl = '';

function report(raw) {
  if (reported || !raw) return;
  let p;
  try { p = normalizePublicUrl(raw); } catch { return; }
  // Defense in depth: api.trycloudflare.com is the quick-tunnel REGISTRATION
  // API — it can only reach this function through a parsed cloudflared ERROR
  // line. Wiring the stack to it makes the launcher advertise a dead URL.
  if (p.hostname.toLowerCase() === 'api.trycloudflare.com') return;
  reported = true;
  currentUrl = p.url;
  writePublicUrl({ ...p, source: 'quick-tunnel', runId });
  console.log('');
  console.log(`[tunnel] public URL: ${p.url}`);
  console.log('[tunnel] backend + frontend are being wired to it (API_BASE /api, SITE_HOST, ALLOWED_ORIGINS).');
  console.log('');
}

function startChild() {
  reported = false; // the respawn registers a NEW hostname — let it be reported
  child = spawn(bin, ['tunnel', '--url', `http://127.0.0.1:${port}`, '--protocol', process.env.CLOUDFLARED_PROTOCOL || 'auto', '--no-autoupdate'], {
    stdio: ['ignore', 'inherit', 'pipe'], windowsHide: true,
  });
  child.stderr.on('data', (chunk) => {
    process.stderr.write(chunk);
    report(parseTunnelUrl(chunk.toString('utf8')));
  });
  child.once('error', () => {
    console.error('cloudflared is not available. Install it yourself or set CLOUDFLARED_BIN. Nothing was downloaded or built.');
    process.exitCode = 1;
    cleaning = true; // do not supervise a binary that does not exist
  });
  child.once('exit', (code) => {
    if (cleaning) { process.exitCode = code || 0; return; }
    const wait = restartBackoff(restarts);
    restarts += 1;
    if (restarts > MAX_RESTARTS) {
      console.error(`[tunnel] cloudflared exited and the restart budget (${MAX_RESTARTS}) is spent — the public URL is gone; the local stack keeps running.`);
      process.exitCode = code || 1;
      return;
    }
    console.warn(`[tunnel] cloudflared exited (code ${code}) — restarting in ${wait}s (attempt ${restarts}/${MAX_RESTARTS}). The local stack stays up; a NEW public URL will be printed and re-wired when it registers.`);
    lastRestartAt = Date.now();
    const respawn = () => { if (!cleaning) startChild(); };
    setTimeout(respawn, wait * 1000);
  });
}

/**
 * Heartbeat: prove the reported URL still terminates at a live edge. The ladder
 * in tunnel-health.mjs matters here — a hostname no resolver knows yet is
 * PROPAGATING, not dead, and must never trigger a restart; only "resolvers know
 * it, the edge refuses" counts.
 */
let deadStreak = 0;
async function heartbeat() {
  if (cleaning || !currentUrl || !child || child.exitCode !== null) return;
  const probe = await probePublicUrl(currentUrl, '/_health').catch(() => null);
  if (!probe) return;
  if (probe.ok || probe.verdict === 'propagating') { deadStreak = 0; return; }
  deadStreak += 1;
  if (deadStreak >= DEAD_PROBES_BEFORE_RESTART && Date.now() - lastRestartAt > COOLDOWN_MS) {
    deadStreak = 0;
    console.warn(`[tunnel] heartbeat: ${currentUrl} is known to DNS but the edge stopped answering (${probe.verdict}) — respawning cloudflared for a fresh registration.`);
    try { child.kill('SIGTERM'); } catch {}
    // the exit handler respawns with backoff
  }
}

startChild();
const hb = setInterval(() => { heartbeat().catch(() => {}); }, HEARTBEAT_MS);
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, () => {
    cleaning = true;
    clearInterval(hb);
    try { child?.kill(signal); } catch {}
  });
}
