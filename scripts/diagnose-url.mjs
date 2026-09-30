#!/usr/bin/env node
// diagnose-url.mjs — "the URL does not work" → say exactly WHERE the chain breaks.
//
// Walks the same path a visitor's request takes and reports each hop:
//   DNS  →  TCP/TLS  →  static server  →  /config.js  →  /api (Node hop → Go)
// plus the local origin, the launcher's wired URL, and any proxy env that Node's
// fetch ignores but a browser would use. Paste the output back and the break is
// obvious.
//
//   node scripts/diagnose-url.mjs [https://your-tunnel.trycloudflare.com]
//   npm run diagnose -- [url]
//
// No dependencies, makes no changes.
import { promises as dns } from 'node:dns';
import { connect } from 'node:net';
import { connect as tlsConnect } from 'node:tls';
import { readPublicUrl, describeFetchError, proxyEnv } from './public-url.mjs';
import { resolveViaDoH, httpsGetViaIp } from './tunnel-health.mjs';

const argUrl = process.argv[2] || '';
const wired = readPublicUrl();
const origin = (argUrl || wired?.url || '').replace(/\/+$/, '');
const PORT = Number(process.env.PORT || 8085);

const line = (ok, name, detail) =>
  console.log(`${ok ? '  [ ok ]' : '  [FAIL]'} ${name.padEnd(28)} ${detail}`);

console.log('nimshop URL diagnosis');
console.log('=====================');
console.log(`wired by launcher : ${wired ? `${wired.url}  (source ${wired.source || '?'}, run ${wired.runId || 'none'})` : '(none — devtools/.runtime/public-url.json is absent)'}`);
console.log(`testing           : ${origin || '(no URL — pass one as the first argument)'}`);
const proxies = proxyEnv();
console.log(`proxy env         : ${proxies.length ? proxies.join(', ') + '   ← Node fetch IGNORES these; a browser uses them' : '(none)'}`);
console.log('');

if (!origin) process.exit(0);

let host = '';
try { host = new URL(origin).hostname; } catch { console.log(`not an absolute URL: ${origin}`); process.exit(1); }

/* 1 — DNS */
console.log(`1) DNS for ${host}`);
try { const a = await dns.resolve4(host); line(true, 'A  (IPv4)', a.join(', ')); } catch (e) { line(false, 'A  (IPv4)', describeFetchError(e)); }
try { const a = await dns.resolve6(host); line(true, 'AAAA (IPv6)', a.join(', ')); } catch { line(false, 'AAAA (IPv6)', 'no IPv6 record (usually fine)'); }

/* 1b — DoH + direct IP: is the hostname live at the edge even when THIS
   machine's resolver cannot see it? This is the hop that tells "propagating /
   broken local DNS" apart from "the tunnel is really down". */
console.log('1b) DNS-over-HTTPS (bypasses your resolver) + direct-IP probe');
let dohIps = [];
try {
  const doh = await resolveViaDoH(host);
  if (doh.length) {
    dohIps = [...new Set(doh.flatMap((d) => d.ips))];
    line(true, 'DoH A', doh.map((d) => `${new URL(d.server).hostname}: ${d.ips.join(', ')}`).join(' | '));
    const pr = await httpsGetViaIp(dohIps[0], host, '/_health');
    line(pr.status === 200, `HTTPS via IP ${dohIps[0]}`, pr.status === 200 ? '200 — the tunnel IS live; only your resolver is blind' : `HTTP ${pr.status || pr.text}`);
  } else {
    line(false, 'DoH A', 'no public resolver knows this hostname — still propagating, or the registration is gone');
  }
} catch (e) { line(false, 'DoH A', e.message); }

/* 2 — TCP + TLS to 443 */
console.log('2) TCP / TLS to 443');
const tcpOk = await new Promise((resolve) => {
  const s = connect({ host, port: 443, family: 0 }, () => { s.destroy(); resolve(true); });
  s.on('error', (e) => { line(false, 'TCP 443', e.code || e.message); resolve(false); });
  setTimeout(() => { s.destroy(); resolve(false); }, 8000);
});
if (tcpOk) {
  line(true, 'TCP 443', 'connected');
  await new Promise((resolve) => {
    const t = tlsConnect({ host, port: 443, servername: host, rejectUnauthorized: true }, () => {
      const c = t.getPeerCertificate();
      line(true, 'TLS', `${c?.subject?.CN || '?'} (issuer ${c?.issuer?.CN || '?'}, exp ${c?.valid_to || '?'})`);
      t.destroy(); resolve();
    });
    t.on('error', (e) => { line(false, 'TLS', describeFetchError(e)); resolve(); });
    setTimeout(() => { t.destroy(); resolve(); }, 8000);
  });
}

/* 3 — HTTP over the public URL */
console.log('3) HTTP over the public URL');
const get = async (u) => {
  try {
    const r = await fetch(u, { signal: AbortSignal.timeout(10_000), headers: { 'User-Agent': 'nimshop-diagnose' } });
    return { status: r.status, text: await r.text() };
  } catch (e) { return { status: 0, text: describeFetchError(e) }; }
};
const cfg = await get(origin + '/config.js');
if (cfg.status === 200) line(true, '/config.js', /API_BASE[^,]*/.exec(cfg.text)?.[0]?.trim() || '200');
else line(false, '/config.js', cfg.text);
const hh = await get(origin + '/_health');
line(hh.status === 200, '/_health', hh.status === 200 ? '200' : hh.text);
const api = await get(origin + '/api/health');
line(api.status > 0 && api.status < 500, '/api/health', api.status > 0 ? `HTTP ${api.status}` : api.text);

/* 4 — local origin, the source of truth for "is the shop itself up?" */
console.log(`4) local origin http://127.0.0.1:${PORT}`);
const l = await get(`http://127.0.0.1:${PORT}/_health`);
line(l.status === 200, '/_health', l.status === 200 ? '200 — the shop itself is up' : l.text);

console.log('');
console.log('Interpretation');
console.log('--------------');
if (l.status === 200 && cfg.status !== 200 && dohIps.length) {
  console.log('The hostname IS live at Cloudflare (1b answered) but your resolver/edge path');
  console.log('cannot reach it. The launcher verifies and serves such a tunnel via direct IP');
  console.log('automatically; for YOUR browser, switch DNS to 1.1.1.1 or use another network.');
} else if (l.status === 200 && cfg.status !== 200) {
  console.log('The shop is healthy; the break is between this machine and Cloudflare.');
  console.log('Test the same URL from a phone on mobile data. If it opens there, the tunnel is');
  console.log('fine and the problem is THIS network (DNS filter, firewall on 443, or a required proxy).');
} else if (l.status !== 200) {
  console.log('The local stack itself is not answering — start it first (devtools/start.bat).');
} else {
  console.log('Everything answered locally and publicly — if a browser still fails, look at the');
  console.log('exact browser error (F12 → Console) and check for a stale cache or extension.');
}
