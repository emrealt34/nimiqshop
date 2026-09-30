/**
 * tunnel-health.mjs — WHY is the public URL unreachable, and is it REALLY down?
 *
 * THE BUG THIS FIXES: the launcher verified the quick-tunnel URL with a plain
 * `fetch`, i.e. through THIS machine's DNS resolver, for ~30 s, and then printed
 * a scary "never answered" banner. But a fresh *.trycloudflare.com hostname has
 * two independent settling phases, and a third failure mode exists on top:
 *
 *   1. propagation  — the record is registered at Cloudflare but recursive
 *      resolvers (including this machine's) have not cached it yet: ENOTFOUND
 *      for anything from a few seconds to a couple of minutes. The tunnel is
 *      already live for everyone else;
 *   2. broken local DNS — an ISP resolver / DNS filter / DPI box that can never
 *      resolve *.trycloudflare.com. Also ENOTFOUND, forever, while the tunnel
 *      is perfectly reachable from any other network;
 *   3. a dead tunnel — cloudflared exited or the edge dropped it. The only case
 *      where "unreachable" is actually true.
 *
 * A bare fetch cannot tell these apart, so the launcher reported cases 1 and 2
 * as failures and did nothing about case 3. This module adds the missing eyes:
 *
 *   • resolveViaDoH()  — ask Cloudflare/Google/Quad9 over HTTPS (dns-json),
 *     bypassing the local resolver entirely;
 *   • httpsGetViaIp()  — talk to the edge by IP with the right SNI + Host, so a
 *     live tunnel can be verified (and used) even when local DNS is blind;
 *   • probePublicUrl() — the ladder: system fetch → DoH → IP probe, returning
 *     exactly WHICH rung answered, plus a verdict for the humans;
 *   • waitUntilPublic() — retry the ladder over a generous budget instead of a
 *     fixed 30 s guess, so propagation is waited out silently;
 *   • restartBackoff() — the one backoff schedule both the tunnel supervisor
 *     (devtools/tunnel-run.js) and its heartbeat use.
 *
 * Pure node, no dependencies. Pure helpers are unit-tested in
 * scripts/tunnel-health.node-spec.mjs.
 */
import https from 'node:https';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

/** Public dns-json endpoints, in trust order. Any ONE answering is enough. */
export const DOH_SERVERS = [
  'https://cloudflare-dns.com/dns-query',
  'https://dns.google/resolve',
  'https://dns.quad9.net/dns-query',
];

/** Backoff for tunnel restarts (seconds): fast first, capped, never spammy. */
const BACKOFF_S = [2, 5, 10, 20, 30];
export function restartBackoff(attempt) {
  const n = Math.max(0, Math.floor(Number(attempt) || 0));
  return (n < BACKOFF_S.length ? BACKOFF_S[n] : BACKOFF_S[BACKOFF_S.length - 1]);
}

/** dns-json payload → IPv4 list. Pure: parses, never touches the network. */
export function parseDohAnswer(payload) {
  let j;
  try { j = typeof payload === 'string' ? JSON.parse(payload) : payload; } catch { return []; }
  const answers = Array.isArray(j?.Answer) ? j.Answer : [];
  return answers
    .filter((a) => (a.type === 1 || a.type === undefined) && typeof a.data === 'string' && /^\d+\.\d+\.\d+\.\d+$/.test(a.data))
    .map((a) => a.data);
}

/**
 * Resolve a hostname through DNS-over-HTTPS, ignoring the local resolver.
 * Returns [{server, ips}] for every server that returned at least one A record.
 * A hostname that is live at the edge shows up here long before (or without
 * ever) reaching a broken local resolver.
 */
export async function resolveViaDoH(hostname, { servers = DOH_SERVERS, timeoutMs = 6000 } = {}) {
  const out = [];
  await Promise.all(servers.map(async (server) => {
    try {
      const u = new URL(server);
      u.searchParams.set('name', hostname);
      u.searchParams.set('type', 'A');
      const r = await fetch(u, { signal: AbortSignal.timeout(timeoutMs), headers: { accept: 'application/dns-json' } });
      if (!r.ok) return;
      const ips = parseDohAnswer(await r.text());
      if (ips.length) out.push({ server, ips });
    } catch { /* that DoH endpoint is unreachable from here — others may answer */ }
  }));
  return out;
}

/**
 * GET https://<hostname><path> by connecting to `ip` directly, with SNI and the
 * Host header set to the hostname — exactly what a browser would send, minus
 * the resolver. This is how a live tunnel is proven live (and reachable) on a
 * machine whose DNS cannot see it.
 */
export function httpsGetViaIp(ip, hostname, path, { timeoutMs = 12000 } = {}) {
  return new Promise((resolve) => {
    const req = https.request({
      host: ip, port: 443, path, method: 'GET', servername: hostname,
      headers: { Host: hostname, 'User-Agent': 'nimshop-tunnel-health' },
      timeout: timeoutMs,
    }, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (c) => { body += c; if (body.length > 65536) body = body.slice(0, 65536); });
      res.on('end', () => resolve({ status: res.statusCode || 0, text: body }));
    });
    req.on('timeout', () => { req.destroy(new Error('timed out')); });
    req.on('error', (e) => resolve({ status: 0, text: e.code || e.message }));
    req.end();
  });
}

/** Plain fetch with the real reason surfaced (undici hides it in err.cause). */
export async function fetchStatus(url, { timeoutMs = 12000, ua = 'nimshop-tunnel-health' } = {}) {
  try {
    const r = await fetch(url, { signal: AbortSignal.timeout(timeoutMs), headers: { 'User-Agent': ua } });
    return { status: r.status, text: await r.text() };
  } catch (e) {
    const cause = e?.cause;
    const code = cause?.code || e?.code || e?.name || 'fetch failed';
    return { status: 0, text: `${code}${cause?.message && cause.message !== code ? `: ${cause.message}` : ''}` };
  }
}

export const isDnsFailure = (textOrErr) => /ENOTFOUND|EAI_AGAIN|getaddrinfo|DNS/i.test(String(textOrErr || ''));

/**
 * The verification ladder for one URL+path:
 *   rung 1  system resolver fetch            → via 'system'
 *   rung 2  DoH resolve + IP probe (SNI/Host) → via 'ip'      (local DNS blind)
 * Returns { ok, via, ips, status, text, verdict } — verdict is the human line:
 *   'live'             answered on some rung
 *   'propagating'      nothing knows the hostname yet (DoH included)
 *   'local-dns-broken' DoH sees it but this machine's resolver/edge path fails
 *   'down'             hostname resolves everywhere but the edge refuses/fails
 */
export async function probePublicUrl(url, path = '/_health', opts = {}) {
  const origin = String(url || '').replace(/\/+$/, '');
  const sys = await fetchStatus(origin + path, opts);
  if (sys.status > 0) return { ok: true, via: 'system', ips: [], status: sys.status, text: sys.text, verdict: 'live' };
  let hostname;
  try { hostname = new URL(origin).hostname; } catch { return { ok: false, via: 'none', ips: [], status: 0, text: sys.text, verdict: 'bad-url' }; }
  const doh = await resolveViaDoH(hostname, opts);
  const ips = [...new Set(doh.flatMap((d) => d.ips))];
  if (!ips.length) {
    return { ok: false, via: 'none', ips: [], status: 0, text: sys.text, verdict: isDnsFailure(sys.text) ? 'propagating' : 'down' };
  }
  for (const ip of ips.slice(0, 3)) {
    const r = await httpsGetViaIp(ip, hostname, path, opts);
    if (r.status > 0) return { ok: true, via: 'ip', ips, status: r.status, text: r.text, verdict: 'live' };
  }
  return { ok: false, via: 'none', ips, status: 0, text: sys.text, verdict: isDnsFailure(sys.text) ? 'local-dns-broken' : 'down' };
}

/**
 * Retry the ladder until the URL answers or the budget runs out. Propagation is
 * waited out SILENTLY (onTick only fires every quietMs for a progress line);
 * a verified-via-IP success returns early with via:'ip' so the caller can say
 * "live, but your resolver cannot see it yet".
 */
export async function waitUntilPublic(url, { path = '/_health', budgetMs = 120000, intervalMs = 3000, quietMs = 15000, onTick, shouldStop } = {}) {
  const started = Date.now();
  let last = null;
  let nextQuiet = started + quietMs;
  while (Date.now() - started < budgetMs) {
    if (typeof shouldStop === 'function' && shouldStop()) break;
    last = await probePublicUrl(url, path);
    if (last.ok) return { ...last, waitedMs: Date.now() - started };
    if (Date.now() >= nextQuiet) {
      nextQuiet = Date.now() + quietMs;
      if (typeof onTick === 'function') onTick({ waitedMs: Date.now() - started, verdict: last.verdict });
    }
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  return { ...(last || { ok: false, via: 'none', ips: [], status: 0, text: 'no attempt', verdict: 'propagating' }), ok: false, waitedMs: Date.now() - started };
}

/** One human line per verdict, so every caller phrases it identically. */
export function verdictLine(verdict, { url, ips = [] } = {}) {
  switch (verdict) {
    case 'propagating':
      return `the hostname is not known to ANY resolver yet — propagation in progress (the tunnel registers at Cloudflare first, caches fill later)`;
    case 'local-dns-broken':
      return `Cloudflare's own resolvers SEE ${new URL(url).hostname} (${ips.slice(0, 2).join(', ')}) but this machine's DNS/edge path does not — the tunnel is live for everyone else; verified here via direct IP`;
    case 'down':
      return `the hostname resolves but the edge refused the request — the tunnel itself is down`;
    default:
      return `verified live`;
  }
}

/* ---------------------------------------------------------------- hosts pin */
/**
 * The last mile the ladder could not fix on its own: the BROWSER uses the
 * system resolver, so on a machine whose resolver (or its negative cache) says
 * NXDOMAIN, a tunnel that is provably live at the edge is still unopenable at
 * home — exactly the DNS_PROBE_FINISHED_NXDOMAIN report. The scoped, reversible
 * fix is a single hosts-file pin: hostname → the verified edge IP.
 *
 * The pin is marker-managed ('nimshop-tunnel-pin'): every write REMOVES the
 * previous pin first, so at most one line ever exists and a re-registered
 * tunnel host replaces the old one instead of stacking stale entries.
 */
export const HOSTS_MARKER = 'nimshop-tunnel-pin';

export function hostsPath(env = process.env) {
  if (env.NIMSHOP_HOSTS_FILE) return env.NIMSHOP_HOSTS_FILE;
  return env.SystemRoot
    ? join(env.SystemRoot, 'System32', 'drivers', 'etc', 'hosts')
    : '/etc/hosts';
}

/** Pure: the two-line block a pin occupies in the hosts file. */
export function hostsPinBlock(hostname, ip) {
  return `# ${HOSTS_MARKER}\n${ip} ${hostname}`;
}

/** Pure: a hosts file without any previous pin block. */
export function stripHostsPin(text) {
  return String(text || '')
    .split(/\r?\n/)
    .filter((line, i, arr) => !(
      line.includes(HOSTS_MARKER) ||
      (i > 0 && arr[i - 1].includes(HOSTS_MARKER))
    ))
    .join('\n');
}

/** True when the hosts file already pins this exact hostname→ip. */
export function hostsPinActive(hostname, ip, env = process.env) {
  try {
    const t = readFileSync(hostsPath(env), 'utf8');
    const esc = (v) => String(v).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return new RegExp(`^${esc(ip)}\\s+${esc(hostname)}\\s*(#.*)?$`, 'm').test(t);
  } catch { return false; }
}

/**
 * Install the pin. Plain users cannot edit /etc/hosts or Windows' hosts, so:
 * direct write when writable (some containers/CI), otherwise ONE elevated
 * helper run (UAC on Windows, sudo -n elsewhere). Returns
 * {ok, how: 'direct'|'elevated'|'manual', detail}.
 * Never throws: a refused UAC prompt is a 'manual' result, not a crash.
 */
export function pinHostsEntry(hostname, ip, env = process.env) {
  if (!hostname || !ip) return { ok: false, how: 'manual', detail: 'nothing to pin' };
  if (hostsPinActive(hostname, ip, env)) return { ok: true, how: 'direct', detail: 'already pinned' };
  const path = hostsPath(env);
  const block = hostsPinBlock(hostname, ip);
  const apply = (current) => `${stripHostsPin(current).replace(/\n+$/, '')}\n\n${block}\n`;
  try {
    const current = readFileSync(path, 'utf8');
    writeFileSync(path, apply(current));
    return { ok: hostsPinActive(hostname, ip, env), how: 'direct', detail: path };
  } catch { /* not writable — fall through to the elevated helper */ }
  const desired = (() => { try { return apply(readFileSync(path, 'utf8')); } catch { return `${block}\n`; } })();
  if (env.SystemRoot) {
    const dir = mkdtempSync(join(tmpdir(), 'nimshop-pin-'));
    const ps1 = join(dir, 'pin.ps1');
    const esc = (x) => String(x).replace(/'/g, "''");
    writeFileSync(ps1, `Set-Content -LiteralPath '${esc(path)}' -Value @'\n${desired.replace(/\r?\n/g, '\n')}\n'@ -Encoding ascii\n`, 'utf8');
    const r = spawnSync('powershell.exe', [
      '-NoProfile', '-Command',
      `Start-Process powershell.exe -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File','${esc(ps1)}'`,
    ], { stdio: 'ignore', windowsHide: true });
    if (r.error) return { ok: false, how: 'manual', detail: r.error.message };
    return hostsPinActive(hostname, ip, env)
      ? { ok: true, how: 'elevated', detail: path }
      : { ok: false, how: 'manual', detail: 'UAC declined or the elevated write failed' };
  }
  const dir = mkdtempSync(join(tmpdir(), 'nimshop-pin-'));
  const sh = join(dir, 'pin.sh');
  writeFileSync(sh, `#!/bin/sh\ncat > '${path}' <<'HOSTS'\n${desired}\nHOSTS\n`, 'utf8');
  const r = spawnSync('sudo', ['-n', 'sh', sh], { stdio: 'ignore' });
  if (r.error || r.status !== 0) return { ok: false, how: 'manual', detail: 'sudo unavailable or declined' };
  return hostsPinActive(hostname, ip, env)
    ? { ok: true, how: 'elevated', detail: path }
    : { ok: false, how: 'manual', detail: 'the elevated write did not stick' };
}

/** Best-effort removal (same elevation story); never throws. */
export function unpinHostsEntry(env = process.env) {
  const path = hostsPath(env);
  try {
    const current = readFileSync(path, 'utf8');
    if (!current.includes(HOSTS_MARKER)) return { ok: true, how: 'direct' };
    const stripped = stripHostsPin(current);
    try {
      writeFileSync(path, stripped);
      return { ok: !readFileSync(path, 'utf8').includes(HOSTS_MARKER), how: 'direct' };
    } catch {
      if (env.SystemRoot) {
        const dir = mkdtempSync(join(tmpdir(), 'nimshop-pin-'));
        const ps1 = join(dir, 'unpin.ps1');
        const esc = (x) => String(x).replace(/'/g, "''");
        writeFileSync(ps1, `Set-Content -LiteralPath '${esc(path)}' -Value @'\n${stripped.replace(/\r?\n/g, '\n')}\n'@ -Encoding ascii\n`, 'utf8');
        spawnSync('powershell.exe', ['-NoProfile', '-Command',
          `Start-Process powershell.exe -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File','${esc(ps1)}'`],
          { stdio: 'ignore', windowsHide: true });
      } else {
        const dir = mkdtempSync(join(tmpdir(), 'nimshop-pin-'));
        const sh = join(dir, 'unpin.sh');
        writeFileSync(sh, `#!/bin/sh\ncat > '${path}' <<'HOSTS'\n${stripped}\nHOSTS\n`, 'utf8');
        spawnSync('sudo', ['-n', 'sh', sh], { stdio: 'ignore' });
      }
      return { ok: !readFileSync(path, 'utf8').includes(HOSTS_MARKER), how: 'elevated' };
    }
  } catch { return { ok: false, how: 'manual' }; }
}
