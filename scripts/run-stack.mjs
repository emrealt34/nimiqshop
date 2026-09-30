#!/usr/bin/env node
// Shared Linux/macOS/Windows launcher. One private-hop secret, both processes,
// readiness before tunnel startup, and whole-process-tree cleanup.
//
// AUTO-BUILD: if the backend binary or frontend dist/ is missing/stale, we
// invoke scripts/build-all.mjs to compile whatever is needed before booting.
// This means a fresh `node scripts/run-stack.mjs preview` on a clean checkout
// builds Go + Astro itself (Go 1.26+ and Node 22+ must be on PATH). Pass
// --no-build to skip auto-building (useful if you prebuilt elsewhere and
// just want to launch).
import { spawn, spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { existsSync, copyFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import {
  backendUrlEnv,
  clearPublicUrl,
  describeFetchError,
  describeWiring,
  frontendConfigValues,
  normalizePublicUrl,
  overrideDisabled,
  proxyEnv,
  waitForNewPublicUrl,
  waitForPublicUrl,
  writeGeneratedConfig,
  writePublicUrl,
} from './public-url.mjs';
import { probePublicUrl, httpsGetViaIp, verdictLine, pinHostsEntry, hostsPath } from './tunnel-health.mjs';

const root=fileURLToPath(new URL('..',import.meta.url));
const args=process.argv.slice(2);
const skipBuild=args.includes('--no-build') || args.includes('--skip-build');
const forceBuild=args.includes('--build') || args.includes('--rebuild');
const mode=args.find(a=>!a.startsWith('-')) || 'preview';
if (!['dev','preview','tunnel','tunnel-origin'].includes(mode)) {
  console.error('Usage: node scripts/run-stack.mjs {dev|preview|tunnel|tunnel-origin} [--no-build|--build]'); process.exit(1);
}
function runNode(script, extraArgs=[], cwd=root, stdio='inherit') {
  const r=spawnSync(process.execPath,[script,...extraArgs],{cwd,stdio,windowsHide:true});
  if (r.error) throw new Error(`node ${script} could not start: ${r.error.message}`);
  return r.status;
}
const frontendPort=Number(process.env.PORT || 8085);
const apiPort=Number(process.env.API_PORT || 8084);
if (![frontendPort,apiPort].every(p=>Number.isInteger(p)&&p>0&&p<65536) || frontendPort===apiPort) throw new Error('PORT and API_PORT must be different valid ports');
const tunnelMode=mode.startsWith('tunnel');
const key=process.env.FORWARDED_HEADER_SECRET || randomBytes(48).toString('base64url');
if (key.length<32 || key.length>512 || /\s/.test(key)) throw new Error('Invalid FORWARDED_HEADER_SECRET');
// Identifies THIS launcher in devtools/.runtime/public-url.json. A launcher that
// was hard-killed (stop.bat → taskkill /F, or closing the console window) leaves
// that file behind with a tunnel URL that is already dead, so a record is only
// trusted when it carries this run's id.
const runId=randomBytes(8).toString('hex');
const children=new Set(); const windows=process.platform==='win32';
let cleaning=false;
function killTree(child) {
  if (!child?.pid || child.exitCode!==null) return;
  try {
    if (windows) spawn('taskkill',['/PID',String(child.pid),'/T','/F'],{stdio:'ignore',windowsHide:true});
    else process.kill(-child.pid,'SIGTERM');
  } catch { try { child.kill(); } catch {} }
}
async function cleanup() {
  if (cleaning) return; cleaning=true;
  for (const child of children) killTree(child);
  await delay(250);
  if (!windows) for (const child of children) { try { process.kill(-child.pid,'SIGKILL'); } catch {} }
  // A dead tunnel URL must never be served by the next run.
  try { clearPublicUrl(); } catch {}
}
function child(command,args,cwd,env=process.env) {
  const p=spawn(command,args,{cwd,env,stdio:'inherit',windowsHide:true,detached:!windows});
  children.add(p);
  p.once('exit',()=> {
    // Clean descendants immediately; do not keep old build PIDs around for
    // hours and risk signalling a recycled PID during a later shutdown.
    if (!windows && p.pid) { try { process.kill(-p.pid,'SIGTERM'); } catch {} }
    children.delete(p);
  });
  p.once('error',e=>console.error('Process could not start:',e.message));
  return p;
}
function exits(p) { return new Promise((resolve,reject)=>{p.once('error',reject);p.once('exit',(code,signal)=>resolve({code,signal}));}); }
// Waits for the CURRENT backend: a supervised rewire marks the old process
// superseded and chains .next, so a planned restart never ends the stack.
async function exitsBox(box) {
  let p=box.proc;
  for(;;){
    const r=await exits(p);
    if(p.next){p=p.next;continue;}
    return r;
  }
}
async function ready(url,headers={}) {
  const deadline=Date.now()+60_000;
  while(Date.now()<deadline && !cleaning) {
    try { const r=await fetch(url,{headers,signal:AbortSignal.timeout(2000)}); if(r.ok) return; } catch {}
    await delay(250);
  }
  throw new Error('Origin did not become ready; check the process errors above');
}
for (const signal of ['SIGINT','SIGTERM']) process.once(signal,()=>{cleanup().finally(()=>process.exit(0));});
process.once('exit',()=>{for(const p of children) killTree(p);});

try {
  const backendDir=resolve(root,process.env.BACKEND_DIR || 'backend');
  const executable=resolve(root,process.env.BACKEND_BIN || join(backendDir,'bin',windows?'nimshop-server.exe':'nimshop-server'));

  // Always hand the build decision to build-all.mjs when the caller didn't
  // pass --no-build. It does quick mtime checks and no-ops when everything
  // is up to date (in dev mode it only builds the backend, since astro dev
  // serves src/ directly).
  if (!skipBuild) {
    const buildArgs=[];
    if (forceBuild) buildArgs.push('--force');
    if (mode==='dev') buildArgs.push('--backend-only');
    console.log('[start] ensuring binaries are built…');
    const code=runNode(join(root,'scripts/build-all.mjs'),buildArgs);
    if (code!==0) throw new Error('Auto-build failed. Fix compile errors or re-run with --no-build if you already built elsewhere.');
  }

  if (!existsSync(executable)) throw new Error(`Backend binary missing at ${executable} and auto-build did not produce it. Install Go or set BACKEND_BIN.`);
  if (mode!=='dev' && !existsSync(join(root,'dist/index.html'))) throw new Error('Frontend dist/index.html missing and auto-build did not produce it. Install Node.js and retry.');

  // First-run convenience: generate backend/.env from backend/.env.example if
  // .env does not exist yet. The Go process loads .env from its working
  // directory (backend/). An existing .env is NEVER overwritten, so keep your
  // real secrets in .env once it has been created.
  const envExample = join(backendDir, '.env.example');
  const envFile = join(backendDir, '.env');
  if (!existsSync(envFile)) {
    if (existsSync(envExample)) {
      copyFileSync(envExample, envFile);
      console.log(`[start] created ${envFile} from ${envExample} (first run). Edit .env to override defaults.`);
    } else {
      console.warn(`[start] warning: no ${envFile} and no ${envExample} — backend will run with empty process env only.`);
    }
  }

  const backendURL=`http://127.0.0.1:${apiPort}`;

  /* --------------------------------------------------------- public URL */
  // EVERYTHING user-facing hangs off this one value: the browser's API_BASE,
  // the shop name, CORS origins, the supplier webhook base. A quick tunnel
  // only reveals its URL after cloudflared connects, so in tunnel mode the
  // tunnel is opened FIRST and the backend is then configured with the URL it
  // printed — previously the URL was printed and thrown away.
  const useOverride=!overrideDisabled();
  let publicUrl=null;
  const explicitURL=String(process.env.PUBLIC_URL || '').trim();

  if (useOverride && explicitURL) {
    publicUrl={...normalizePublicUrl(explicitURL),source:'PUBLIC_URL',runId};
    writePublicUrl(publicUrl);
  } else if (useOverride && mode==='tunnel') {
    // Auto-fetch the official cloudflared binary once (into devtools/.bin) so a
    // one-click tunnel works without a manual install. If the download fails
    // (e.g. github.com unreachable), DON'T abort the whole stack — warn and let
    // tunnel-run fall back to a cloudflared already on PATH / CLOUDFLARED_BIN.
    const ensure=spawnSync(process.execPath,[join(root,'devtools/ensure-cloudflared.mjs')],{cwd:root,encoding:'utf8'});
    let cfBin='';
    if (ensure.status===0) cfBin=(ensure.stdout||'').trim();
    else {
      console.error(ensure.stderr || 'cloudflared auto-download failed.');
      console.error('Continuing — will use a cloudflared already installed on PATH/CLOUDFLARED_BIN if present.');
    }
    // An explicitly chosen CLOUDFLARED_BIN always wins. Overriding it with the
    // downloaded copy meant a pinned/test binary was silently ignored.
    if (process.env.CLOUDFLARED_BIN) cfBin='';
    console.log('Opening a development Quick Tunnel. Use a named tunnel + tunnel-origin for production.');
    // Drop any leftover record BEFORE spawning: tunnel-run.js deletes it too, but
    // only once its own Node process has booted — by then we were already
    // polling, and a previous run's dead URL answered instead of this tunnel's.
    try { clearPublicUrl(); } catch {}
    const tunnelProc=child(process.execPath,[join(root,'devtools/tunnel-run.js'),'--port',String(frontendPort)],root,
      {...process.env,PORT:String(frontendPort),NIMSHOP_TUNNEL_RUN_ID:runId,...(cfBin?{CLOUDFLARED_BIN:cfBin}:{})});
    // A Quick Tunnel is best-effort: if Cloudflare is unreachable it must NOT
    // take the local backend+frontend down with it.
    const tunnelDead = new AbortController(); // a dead tunnel never reports a URL — stop waiting for one
    tunnelProc.once('exit',(code)=>{
      console.warn(`\n[tunnel] Quick Tunnel stopped (exit ${code}). The local site is still running at http://localhost:${frontendPort}.`);
      console.warn('[tunnel] To share publicly, re-run "npm run start:tunnel" once your network can reach Cloudflare, or use a named tunnel (run-stack tunnel-origin). For local-only use "npm run start".');
      tunnelDead.abort();
    });
    process.stdout.write('[tunnel] waiting for THIS run’s tunnel URL…');
    // runId: ignore a public-url.json left behind by an earlier launcher. Without
    // it the very first poll answered with a tunnel that had already died, and
    // the whole stack — /config.js, SITE_HOST, ALLOWED_ORIGINS,
    // PUBLIC_WEBHOOK_BASE_URL — was wired to a host nobody could reach.
    publicUrl=await waitForPublicUrl({timeoutMs:Number(process.env.TUNNEL_URL_TIMEOUT_MS || 60_000),runId,signal:tunnelDead.signal});
    if (publicUrl) console.log(` ok  →  ${publicUrl.url}`);
    else if (tunnelDead.signal.aborted) {
      console.log(' failed');
      console.warn(`[tunnel] Cloudflare could not be reached, so there is NO public URL. Everything below is wired to the LOCAL origin — open http://localhost:${frontendPort}`);
    }
    else {
      console.log(' timed out');
      console.warn('[tunnel] No URL within the timeout — falling back to the local origin, so SITE_HOST/CORS stay local while the tunnel may still come up. Re-run with a longer TUNNEL_URL_TIMEOUT_MS, or pass PUBLIC_URL=<your named tunnel URL>.');
      console.warn('[tunnel] If the tunnel arrives late it is picked up automatically: /config.js is regenerated and the URL is printed here. The Go process keeps the local SITE_HOST until you re-run.');
    }
  }
  if (useOverride && !publicUrl && mode!=='tunnel-origin') {
    // No tunnel (or the tunnel never came up): wire the local origin instead.
    // Relative API_BASE still beats a hard-coded production API domain.
    publicUrl={...normalizePublicUrl(`http://localhost:${frontendPort}`),source:'local',runId};
    writePublicUrl(publicUrl);
  }

  const urlEnv=useOverride && publicUrl ? backendUrlEnv(publicUrl,frontendPort) : {};
  const backendEnv={...process.env,LISTEN_ADDR:`0.0.0.0:${apiPort}`,TRUST_PROXY:'true',
    TRUSTED_PROXY_CIDRS:'127.0.0.1/32,::1/128',PROXY_HEADER_MODE:'forwarded',FORWARDED_HEADER_SECRET:key,...urlEnv};
  // The backend lives in a box: when the tunnel re-registers under a new
  // hostname, PUBLIC_WEBHOOK_BASE_URL / SITE_HOST must follow, so the watcher
  // can respawn it (see rewireBackend) while the main race keeps waiting on
  // whichever process is current.
  const backendBox={proc:null};
  function spawnBackend(env){
    const proc=child(executable,[],backendDir,env);
    backendBox.proc=proc;
    return proc;
  }
  const backend=spawnBackend(backendEnv);
  await Promise.race([
    ready(backendURL+'/api/health',{'X-Nimshop-Proxy-Secret':key,'X-Forwarded-For':'127.0.0.1','User-Agent':'nimshop-startup-probe'}),
    exits(backend).then(()=>{throw new Error('Backend stopped during startup');}),
  ]);
  const REWIRE_COOLDOWN_MS=Number(process.env.TUNNEL_REWIRE_COOLDOWN_MS||60_000);
  const REWIRE_MAX=Number(process.env.TUNNEL_REWIRE_MAX||3);
  let rewires=0, lastRewireAt=0;
  async function rewireBackend(nextUrl){
    if(rewires>=REWIRE_MAX){console.warn(`[tunnel] backend rewire budget (${REWIRE_MAX}) spent — PUBLIC_WEBHOOK_BASE_URL keeps the previous host until you re-run the launcher.`);return false;}
    if(Date.now()-lastRewireAt<REWIRE_COOLDOWN_MS){console.warn('[tunnel] skipping a backend rewire inside the cooldown window.');return false;}
    rewires+=1; lastRewireAt=Date.now();
    const old=backendBox.proc;
    old.superseded=true;
    const env={...backendEnv,...backendUrlEnv(nextUrl,frontendPort)};
    console.warn(`[tunnel] restarting the Go backend so PUBLIC_WEBHOOK_BASE_URL / SITE_HOST follow the new tunnel host (rewire ${rewires}/${REWIRE_MAX})…`);
    killTree(old);
    const proc=spawnBackend(env);
    old.next=proc;
    await Promise.race([
      ready(backendURL+'/api/health',{'X-Nimshop-Proxy-Secret':key,'X-Forwarded-For':'127.0.0.1','User-Agent':'nimshop-startup-probe'}),
      exits(proc).then(()=>{throw new Error('Backend stopped during rewire');}),
    ]);
    console.log('[tunnel] backend is back with the new public URL wired in.');
    return true;
  }
  // The browser's config is generated from the SAME URL — public/config.js is
  // never touched, so the deployment defaults stay intact in the repo.
  if (useOverride) {
    const cfg=writeGeneratedConfig(publicUrl,frontendConfigValues(publicUrl));
    console.log(`[start] generated ${cfg.replace(root,'.')} — served at /config.js`);
  }
  const frontendEnv={...process.env,BACKEND:backendURL,PORT:String(frontendPort),FORWARDED_HEADER_SECRET:key,
    ...(publicUrl?{PUBLIC_URL:publicUrl.url}:{}),
    EDGE_MODE:tunnelMode?'cloudflare-tunnel':'direct',TUNNEL_PROXY_CIDRS:process.env.TUNNEL_PROXY_CIDRS || '127.0.0.1/32,::1/128'};
  const frontend=mode==='dev'
    ?child(process.execPath,[join(root,'node_modules/astro/astro.js'),'dev','--host','0.0.0.0','--port',String(frontendPort)],root,frontendEnv)
    :child(process.execPath,[join(root,'scripts/static-server.mjs')],root,frontendEnv);
  await Promise.race([ready(`http://127.0.0.1:${frontendPort}/_health`),exits(frontend).then(()=>{throw new Error('Frontend stopped during startup');})]);
  console.log(`Ready: ${mode}; API uses an authenticated private proxy hop. The secret is not printed.`);
  // NOTE: tunnelProc is deliberately NOT in `running` — a Quick Tunnel dying
  // must warn and keep the local stack alive, not take it down.
  if (useOverride) console.log('[url wiring]\n' + describeWiring(publicUrl,{frontendPort,apiPort}));
  if(mode==='tunnel-origin') {
    console.log(`Point the named cloudflared connector to http://127.0.0.1:${frontendPort}. All /api traffic must pass through this origin.`);
    if (!explicitURL) console.log('[url wiring] tip: pass PUBLIC_URL=https://shop.example.com to wire SITE_HOST, ALLOWED_ORIGINS and PUBLIC_WEBHOOK_BASE_URL to your named tunnel host.');
  }
  // The address to open is printed BEFORE verification: waiting for up to 30 s
  // of edge retries first is exactly how the console ended up burying the live
  // URL under other people's output.
  if (useOverride && publicUrl) console.log(openBanner(publicUrl));
  // Prove the wiring end-to-end through the URL a visitor would actually use,
  // instead of trusting that the pieces agree.
  if (useOverride) await verifyWiring(publicUrl,{frontendPort});
  else console.log(`Open http://localhost:${frontendPort} (NIMSHOP_NO_URL_OVERRIDE — public/config.js served as-is)`);
  // A quick tunnel can still arrive (or be re-registered under a new hostname)
  // after this point. The console must never end up showing two different
  // trycloudflare URLs with no way to tell which one is live.
  if (useOverride && mode==='tunnel' && !explicitURL) watchPublicUrl(publicUrl,{frontendPort, rewireBackend}).catch(()=>{});
  const result=await Promise.race([exitsBox(backendBox),exits(frontend)]);
  if(result.code && result.code!==0) process.exitCode=1;
} catch(err) { console.error(err.message); process.exitCode=1; }
finally { await cleanup(); }

/**
 * Fetches the public URL the way a browser would and checks the two things
 * that used to silently disagree: /config.js must send API_BASE '/api' (never
 * a production API domain) and /api/* must be answered by THIS backend.
 * Best-effort — a warning, never a reason to kill a running stack.
 */
async function verifyWiring(publicUrl, {frontendPort}) {
  const origin=publicUrl ? publicUrl.url : `http://localhost:${frontendPort}`;
  const local=!publicUrl || publicUrl.source==='local';
  const label=`${local ? 'local' : 'public URL'} ${origin}`;
  // undici reports nearly everything as a bare "fetch failed" and hides the real
  // reason in err.cause — useless exactly when it matters, so surface the code.
  const fetchJsonish=async(u)=>{
    try {
      const r=await fetch(u,{signal:AbortSignal.timeout(15_000),headers:{'User-Agent':'nimshop-launcher-verify'}});
      return {status:r.status,text:await r.text()};
    } catch(e) { return {status:0,text:describeFetchError(e)}; }
  };
  const get=(path)=>fetchJsonish(origin+path);
  // A fresh quick tunnel hostname settles in TWO phases: registration at the
  // edge, then DNS caches filling (sometimes minutes — sometimes never on a
  // broken resolver). The old fixed 20×1.5 s fetch loop could not tell
  // "propagating" from "dead" and cried wolf on both. The ladder in
  // tunnel-health.mjs waits propagation out silently and verifies through
  // DoH + a direct-IP probe when this machine's own DNS is blind.
  const budget=Number(process.env.TUNNEL_VERIFY_MS||120_000);
  let cfg=null, via='system', ips=[], verdict='propagating';
  {
    const started=Date.now();
    while (Date.now()-started<budget && !cleaning) {
      const probe=await probePublicUrl(origin,'/config.js',{timeoutMs:15_000});
      verdict=probe.verdict; ips=probe.ips||[];
      if (probe.ok) { cfg={status:probe.status,text:probe.text}; via=probe.via; break; }
      if (Date.now()-started>15_000) {
        console.log(`[verify] waiting for the edge (${Math.round((Date.now()-started)/1000)}s): ${verdictLine(verdict,{url:origin,ips})}`);
        await delay(30_000);
      } else await delay(3000);
    }
  }
  const getSmart=(path)=> via==='ip' && ips.length
    ? httpsGetViaIp(ips[0], new URL(origin).hostname, path).then(r=>({status:r.status,text:r.text}))
    : get(path);
  if (cfg?.status===200 && /\"API_BASE\":\s*\"\/api\"|API_BASE:\s*\'\/api\'/.test(cfg.text)) {
    console.log(`[verify] ${label} /config.js → API_BASE '/api'${via==='ip'?` — verified via direct IP ${ips[0]}: ${verdictLine('local-dns-broken',{url:origin,ips})}`:''}`);
    if (via==='ip' && ips.length && !['1','true','yes','on'].includes(String(process.env.NIMSHOP_NO_HOSTS_PIN||'').trim().toLowerCase())) {
      // The browser uses the SYSTEM resolver, so a resolver-blind machine still
      // shows DNS_PROBE_FINISHED_NXDOMAIN for a tunnel we just proved live.
      // Pin hostname→verified edge IP in the hosts file (marker-managed, one
      // line, replaced on every re-registration) so the shop opens at home too.
      let hostName=''; try { hostName=new URL(origin).hostname; } catch {}
      const pin=pinHostsEntry(hostName, ips[0]);
      if (pin.ok) {
        console.log(`[verify] hosts pin installed (${pin.how}): ${ips[0]} ${hostName} — your browser can open the URL now. If it showed NXDOMAIN before, flush once: ${process.platform==='win32'?'ipconfig /flushdns':'sudo systemd-resolve --flush-caches'} (Chrome: chrome://net-internals/#dns).`);
      } else {
        console.warn(`[verify] could not pin automatically (${pin.detail}). Add this line to ${hostsPath()} once (admin rights), then the URL opens in your browser:`);
        console.warn(`           ${ips[0]} ${hostName}   # nimshop-tunnel-pin`);
      }
    }
  } else if (cfg?.status===200) {
    console.warn(`[verify] ${label} /config.js did not report API_BASE '/api' — the browser may call another environment's API.`);
  } else if (cfg?.status===0) {
    // Nothing answered. Say WHY, and prove which half of the chain is broken
    // instead of guessing: the shop itself, or the path to Cloudflare's edge.
    console.warn(`[verify] ${label} never answered — ${cfg?.text} (${verdictLine(verdict,{url:origin,ips})})`);
    const proxies=proxyEnv();
    if (proxies.length) console.warn(`[verify] Proxy env is set (${proxies.join(', ')}). Node's fetch IGNORES it while your browser uses it — so this check failing does not mean the site is down for you; open the URL and see.`);
    const lh=await fetchJsonish(`http://127.0.0.1:${frontendPort}/_health`);
    if (lh.status===200) {
      console.log(`[verify] The shop itself IS up — http://127.0.0.1:${frontendPort}/_health → 200. So the break is between this machine and Cloudflare's edge, not in the site.`);
      console.warn('[verify] Usual suspects: DNS that cannot resolve *.trycloudflare.com, a firewall/DPI box on outbound 443, or a network that needs a proxy. Test the same URL from your phone on mobile data — if it opens there, the tunnel is fine and the problem is this machine’s network.');
    } else {
      console.warn(`[verify] The local stack is not answering either (${lh.text}) — fix that first: http://localhost:${frontendPort}`);
    }
    console.warn('[verify] Full chain report: npm run diagnose');
    console.warn(`[verify] The shop is fully usable locally right now: http://localhost:${frontendPort}`);
    return;
  } else {
    console.warn(`[verify] ${label} /config.js returned ${cfg?.status} after retries (${String(cfg?.text || '').slice(0,80)}). The local stack is still running.`);
    return;
  }
  const health=await getSmart('/_health');
  const api=await getSmart('/api/health');
  if (health.status===200) console.log(`[verify] ${label} /_health → 200`);
  if (api.status>0) console.log(`[verify] ${label} /api/health → ${api.status} (answered by this Go backend through the Node hop)`);
  else console.warn(`[verify] ${label} /api/health unreachable — the tunnel origin is up but the API hop is not.`);
}

/**
 * The one line that says which address to open. The console used to end with
 * two different trycloudflare.com URLs and no hint which one was live (one dead
 * host from an earlier launch, one real), so this is printed last and restated
 * whenever the tunnel URL changes.
 */
function openBanner(p) {
  const rule = '='.repeat(66);
  return [
    '',
    rule,
    `  OPEN THIS URL    ${p.url}`,
    `  local copy       http://localhost:${frontendPort}`,
    `  source           ${p.source || 'launcher'}`,
    rule,
    "  Any OTHER trycloudflare.com address printed above is not this run's tunnel.",
    '',
  ].join('\n');
}

/**
 * Keeps the console honest after startup. A quick tunnel that arrives late (or
 * one that cloudflared re-registers under a new hostname) rewrites
 * public-url.json; when that record belongs to THIS run we regenerate
 * /config.js — static-server.mjs re-reads it on every request, so the browser
 * picks the new SITE_HOST/FRONTEND_URL up with no restart — and re-verify.
 *
 * The Go process is deliberately NOT restarted: the browser talks to /api on the
 * same origin (no CORS involved) and the backend never validates the request
 * Host, so SITE_HOST there is branding only. Re-running the launcher rewires it.
 */
async function watchPublicUrl(current, { frontendPort: fp, rewireBackend }) {
  while (!cleaning) {
    const next = await waitForNewPublicUrl({
      currentUrl: current?.url || '', runId,
      timeoutMs: Number(process.env.TUNNEL_URL_WATCH_MS || 15 * 60_000), intervalMs: 500,
    });
    if (!next || cleaning) return;
    current = next;
    console.warn(`\n[tunnel] the tunnel URL is now ${next.url}`);
    try {
      writeGeneratedConfig(next, frontendConfigValues(next));
      console.log("[tunnel] /config.js regenerated (SITE_HOST/FRONTEND_URL). API_BASE stays '/api', so nothing needs restarting.");
      console.warn('[tunnel] The Go process keeps the SITE_HOST it started with — branding in titles/emails only. Re-run the launcher for a full rewire.');
    } catch (e) {
      console.warn(`[tunnel] could not regenerate /config.js: ${e.message}`);
    }
    console.log(openBanner(next));
    // The Go process caches PUBLIC_WEBHOOK_BASE_URL / SITE_HOST from its env at
    // boot. A re-registered tunnel host makes the OLD webhook URL unreachable,
    // so the backend is respawned with the new host (cooldown + budget guard
    // against flap loops). The browser needs nothing: API_BASE is '/api'.
    try { if (rewireBackend) await rewireBackend(next); } catch (e) { console.warn(`[tunnel] backend rewire failed: ${e.message}`); }
    await verifyWiring(next, { frontendPort: fp });
  }
}
