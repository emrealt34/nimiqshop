#!/usr/bin/env node
/* devtools/tunnel-run.js
 *
 * Cloudflare QUICK tunnel (trycloudflare) for nim.shop. Uses `npm i cloudflared`
 * package; auto-downloads cloudflared binary on first run.
 *
 *   node devtools/tunnel-run.js [--port 4321]
 *
 * What it does:
 *   1) Ensures cloudflared binary is PRESENT (downloads if missing).
 *   2) Opens a trycloudflare tunnel to local URL (default http://localhost:4321),
 *   3) Captures public https://<random>.trycloudflare.com address,
 *      writes it to devtools/tunnel-url.txt and prints nicely,
 *   4) Closes tunnel on Ctrl+C / process kill.
 *
 * Exit: 0 clean, 1 error, 2 URL missing after expected time / binary not found.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawn, spawnSync } = require('child_process');

const args = process.argv.slice(2);
function argValue(name, def) {
  const i = args.indexOf('--' + name);
  return i >= 0 && args[i + 1] ? args[i + 1] : def;
}

const PORT = Number(argValue('port', process.env.PORT || 4321));
const TARGET = `http://localhost:${PORT}`;
const HERE = __dirname;
const URL_FILE = path.join(HERE, 'tunnel-url.txt');
const IS_WIN = process.platform === 'win32';
const BIN_NAME = IS_WIN ? 'cloudflared.exe' : 'cloudflared';

const PKG_DIR = path.join(HERE, 'node_modules', 'cloudflared');
const PKG_BIN = path.join(PKG_DIR, 'bin', BIN_NAME);          // the path the npm package will spawn
const PKG_CLI = path.join(PKG_DIR, 'lib', 'cloudflared.js');   // package CLI: `node cloudflared.js bin install`
const RELEASE_BASE = 'https://github.com/cloudflare/cloudflared/releases/latest/download/';

// Per-OS/arch release asset names (matches cloudflared's own install.js).
function assetForOS() {
  const arch = os.arch();
  if (IS_WIN) return arch === 'ia32' ? 'cloudflared-windows-386.exe' : 'cloudflared-windows-amd64.exe';
  if (process.platform === 'darwin') return arch === 'arm64' ? 'cloudflared-darwin-arm64.tgz' : 'cloudflared-darwin-amd64.tgz';
  if (arch === 'arm64') return 'cloudflared-linux-arm64';
  if (arch === 'arm') return 'cloudflared-linux-arm';
  if (arch === 'ia32') return 'cloudflared-linux-386';
  return 'cloudflared-linux-amd64';
}

/* ---------- binary self-heal ---------- */

function tryPackageInstall() {
  if (!fs.existsSync(PKG_CLI)) return false;
  console.log('[tunnel] cloudflared binary not found; running package own installer...');
  try {
    const r = spawnSync(process.execPath, [PKG_CLI, 'bin', 'install'], { cwd: HERE, encoding: 'utf8', timeout: 120000 });
    if (fs.existsSync(PKG_BIN)) return true;
    if (r && r.stdout) console.log('  · ' + r.stdout.trim().split(/\r?\n/).slice(-2).join('\n  · '));
    if (r && r.stderr) console.error('  · package installer error: ' + r.stderr.trim().split(/\r?\n/).slice(-2).join('\n'));
  } catch (e) { console.error('  · package installer error: ' + e.message); }
  return false;
}

function downloadViaShell(url, dest) {
  // curl (Windows 10+ understands it) otherwise powershell Invoke-WebRequest.
  const tryList = [];
  if (IS_WIN) {
    tryList.push({ cmd: 'powershell', args: ['-NoProfile', '-Command', `Invoke-WebRequest -Uri "${url}" -OutFile "${dest}"`], name: 'powershell' });
  }
  tryList.push({ cmd: 'curl', args: ['-L', '-o', dest, url], name: 'curl' });
  tryList.push({ cmd: 'wget', args: ['-q', '-O', dest, url], name: 'wget' });

  for (const t of tryList) {
    try {
      const r = spawnSync(t.cmd, t.args, { stdio: 'ignore', timeout: 180000 });
      if (r.status === 0 && fs.existsSync(dest) && fs.statSync(dest).size > 2_000_000) return true;
    } catch (_) { /* try next */ }
  }
  return false;
}

function downloadNodeHttps(url, dest) {
  // Pure Node https downloader (follows redirects). Fallback if user needs it.
  return new Promise((resolve) => {
    try {
      const https = require('https');
      const req = https.get(url, (res) => {
        const redir = [301, 302, 303, 307, 308];
        if (redir.includes(res.statusCode) && res.headers.location) {
          res.resume();
          resolve(downloadNodeHttps(res.headers.location, dest));
          return;
        }
        if (res.statusCode >= 200 && res.statusCode < 300) {
          fs.mkdirSync(path.dirname(dest), { recursive: true });
          const f = fs.createWriteStream(dest);
          res.pipe(f);
          f.on('finish', () => f.close(() => resolve(fs.existsSync(dest) && fs.statSync(dest).size > 2_000_000)));
          f.on('error', () => resolve(false));
        } else { res.resume(); resolve(false); }
      });
      req.on('error', () => resolve(false));
    } catch (_) { resolve(false); }
  });
}

async function ensureBinary() {
  if (fs.existsSync(PKG_BIN)) return PKG_BIN;

  // 1) Try package's own `bin install` CLI.
  if (tryPackageInstall() && fs.existsSync(PKG_BIN)) {
    console.log('[tunnel] binary downloaded (package installer): ' + PKG_BIN);
    return PKG_BIN;
  }

  // 2) Download directly to target with our own downloader.
  const asset = assetForOS();
  const url = RELEASE_BASE + asset;
  const isTgz = asset.endsWith('.tgz');

  console.log(`[tunnel] binary still missing; downloading directly: ${url}`);
  fs.mkdirSync(path.dirname(PKG_BIN), { recursive: true });

  const tmpDest = PKG_BIN + '.dl' + (isTgz ? '.tgz' : '');
  let ok = await downloadNodeHttps(url, tmpDest);
  if (!ok && fs.existsSync(tmpDest)) fs.unlinkSync(tmpDest);
  if (!ok) ok = downloadViaShell(url, tmpDest);
  if (!ok) {
    console.error('==========================================================');
    console.error('  cloudflared could not be downloaded.');
    console.error('  Likely this network/security node blocks access to github.com.');
    console.error('  Solution: download from browser and copy to path:');
    console.error('    ' + url);
    console.error('    ->  ' + PKG_BIN);
    console.error('  Then run this script again.');
    console.error('==========================================================');
    process.exit(2);
  }

  if (isTgz) {
    // extract darwin .tgz
    const out = path.dirname(PKG_BIN);
    const tarOk = spawnSync('tar', ['-xzf', tmpDest, '-C', out], { stdio: 'ignore' }).status === 0;
    fs.unlinkSync(tmpDest);
    if (tarOk) {
      fs.copyFileSync(path.join(out, 'cloudflared'), PKG_BIN);
      fs.unlinkSync(path.join(out, 'cloudflared'));
    }
  } else {
    fs.renameSync(tmpDest, PKG_BIN);
  }
  if (!IS_WIN) fs.chmodSync(PKG_BIN, 0o755);
  console.log('[tunnel] binary ready: ' + PKG_BIN);
  return PKG_BIN;
}

/* ---------- main ---------- */

function startTunnel(mainBin) {
  return new Promise((resolve) => {
    console.log('Setting up tunnel... (Cloudflare quick tunnel)');
    const child = spawn(mainBin, ['tunnel', '--url', TARGET, '--protocol', 'quic', '--no-autoupdate'], {
      cwd: HERE, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'],
    });
    let settled = false;
    let output = '';
    const findURL = (text) => {
      output += text;
      const m = output.match(/https:\/\/(?!api\.trycloudflare\.com)[a-z0-9-]+\.trycloudflare\.com/ig);
      if (m && !settled) {
        settled = true;
        const url = m[0].toLowerCase();
        try { fs.writeFileSync(URL_FILE, url + '\n'); } catch {}
        console.log('\n  ✅  Live URL (copy/share):\n        ' + url + '\n');
        console.log('     This link CHANGES on each run. Press Ctrl+C to stop.\n');
        resolve({ child, url });
      }
    };
    child.stdout.on('data', (b) => { process.stdout.write(String(b)); findURL(String(b)); });
    child.stderr.on('data', (b) => { process.stderr.write(String(b)); findURL(String(b)); });
    child.once('error', (err) => { if (!settled) { settled = true; resolve({ child: null, error: err }); } });
    child.once('exit', (code) => {
      if (!settled) { settled = true; resolve({ child: null, error: new Error('cloudflared exited with code ' + code) }); }
      else if (code && code !== 0) console.error('[tunnel] closed (code ' + code + ').');
    });
    setTimeout(() => {
      if (!settled) { settled = true; try { child.kill(); } catch {} resolve({ child: null, error: new Error('URL timeout') }); }
    }, 30000);
  });
}

async function main() {
  const mainBin = await ensureBinary();
  console.log('==========================================================');
  console.log('  nim.shop - Cloudflare quick tunnel (trycloudflare)');
  console.log('  Target: ' + TARGET);
  console.log('  Binary: ' + mainBin);
  console.log('==========================================================');
  try { fs.unlinkSync(URL_FILE); } catch {}

  for (;;) {
    const result = await startTunnel(mainBin);
    if (result.url) {
      await new Promise((resolve) => {
        process.on('SIGINT', () => { try { result.child.kill(); } catch {} resolve(); });
        process.on('SIGTERM', () => { try { result.child.kill(); } catch {} resolve(); });
        result.child.once('exit', resolve);
      });
      return;
    }
    console.error('[tunnel] Could not get URL: ' + result.error.message + ' — retrying in 5 seconds.');
    await new Promise((r) => setTimeout(r, 5000));
  }
}

main().catch((e) => { console.error('[tunnel] unexpected error:', e.message); process.exit(2); });
