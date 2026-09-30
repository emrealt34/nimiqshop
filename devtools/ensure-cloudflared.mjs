#!/usr/bin/env node
// ensure-cloudflared.mjs — make sure the official cloudflared binary exists in
// devtools/.bin, downloading it if necessary. Cross-platform and retry-safe.
//
//   node devtools/ensure-cloudflared.mjs           # fetch if missing
//   node devtools/ensure-cloudflared.mjs --force   # re-download even if present
//
// Prints the resolved path on stdout (so callers can capture it), logs progress
// on stderr. Exits non-zero ONLY if it tried to download and every attempt
// failed.
//
// Tuning (all optional env vars):
//   CLOUDFLARED_VERSION       pin a version, e.g. "2026.8.2" (default = latest)
//   CLOUDFLARED_DOWNLOAD_URL  full custom URL to the binary (mirror / proxy / CDN)
//   CLOUDFLARED_TIMEOUT_MS    per-attempt timeout (default 60000 — some ISPs are slow)
//   CLOUDFLARED_ATTEMPTS      download attempts (default 4)
import { mkdirSync, existsSync, chmodSync, renameSync, rmSync, statSync } from 'node:fs';
import { createWriteStream } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import https from 'node:https';

const here = dirname(fileURLToPath(import.meta.url));
const force = process.argv.includes('--force');

function artifact() {
  const p = process.platform;
  const a = process.arch;
  if (p === 'win32' && a === 'x64')   return 'cloudflared-windows-amd64.exe';
  if (p === 'linux'  && a === 'x64')  return 'cloudflared-linux-amd64';
  if (p === 'linux'  && a === 'arm64')return 'cloudflared-linux-arm64';
  if (p === 'darwin' && a === 'x64')  return 'cloudflared-darwin-amd64';
  if (p === 'darwin' && a === 'arm64')return 'cloudflared-darwin-arm64';
  throw new Error(`cloudflared auto-download is not mapped for ${p}/${a}. Install it yourself and set CLOUDFLARED_BIN.`);
}

const binName = artifact();
const outDir = join(here, '.bin');
const binPath = join(outDir, binName);

if (existsSync(binPath) && !force) {
  console.log(binPath);
  process.exit(0);
}

const timeoutMs = Number(process.env.CLOUDFLARED_TIMEOUT_MS || 60000);
const attempts = Math.max(1, Number(process.env.CLOUDFLARED_ATTEMPTS || 4));
const customUrl = process.env.CLOUDFLARED_DOWNLOAD_URL;
const tag = process.env.CLOUDFLARED_VERSION ? `download/${process.env.CLOUDFLARED_VERSION}` : 'latest/download';
const url = customUrl || `https://github.com/cloudflare/cloudflared/releases/${tag}/${binName}`;
mkdirSync(outDir, { recursive: true });

// Follow-redirect, timeout-bounded HTTPS download straight to .part.
function download(target, partPath) {
  return new Promise((resolve, reject) => {
    const req = https.get(target, { headers: { 'User-Agent': 'nimshop-cloudflared-fetch' } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        res.resume();
        return resolve({ redirect: res.headers.location });
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`HTTP ${res.statusCode}`));
      }
      const out = createWriteStream(partPath);
      res.pipe(out);
      out.on('finish', () => out.close(() => resolve({ done: true })));
      out.on('error', reject);
      res.on('error', reject);
    });
    req.setTimeout(timeoutMs, () => req.destroy(new Error(`timeout after ${timeoutMs}ms`)));
    req.on('error', reject);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function run() {
  console.error(`[cloudflared] downloading ${url}`);
  const partPath = binPath + '.part';
  let lastErr = null;

  for (let i = 1; i <= attempts; i++) {
    try {
      let current = url;
      let followed = 0;
      for (;;) {
        try {
          const r = await download(current, partPath);
          if (r.redirect) {
            current = r.redirect;
            if (++followed > 5) throw new Error('too many redirects');
            console.error(`[cloudflared] -> ${current}`);
            continue;
          }
          break;
        } catch (e) {
          rmSync(partPath, { force: true });
          throw e;
        }
      }
      rmSync(binPath, { force: true });
      renameSync(partPath, binPath);
      if (process.platform !== 'win32') chmodSync(binPath, 0o755);
      console.error(`[cloudflared] saved ${binPath} (${(statSync(binPath).size / 1024).toFixed(0)} KiB)`);
      console.log(binPath);
      return 0;
    } catch (err) {
      lastErr = err;
      if (i < attempts) {
        const wait = Math.min(3000 * 2 ** (i - 1), 15000);
        console.error(`[cloudflared] attempt ${i}/${attempts} failed (${err.message}); retrying in ${Math.round(wait / 1000)}s…`);
        await sleep(wait);
      }
    }
  }

  console.error('\n[cloudflared] DOWNLOAD FAILED — could not reach the release server.');
  console.error(`  tried: ${url}`);
  console.error('  reason: ' + ((lastErr && lastErr.message) || 'unknown'));
  console.error('\nQuick Tunnel needs the cloudflared binary. Options:');
  console.error('  1. Install it any other way and point at it:');
  console.error('       winget install --id Cloudflare.cloudflared      (Windows)');
  console.error('       scoop install cloudflared                       (Windows)');
  console.error('       choco install cloudflared                       (Windows)');
  console.error('       brew install cloudflared                        (macOS)');
  console.error('     then re-run, or:  set CLOUDFLARED_BIN=cloudflared');
  console.error('  2. Use a mirror / proxy you CAN reach:');
  console.error('       set CLOUDFLARED_DOWNLOAD_URL=https://mirror.example/cloudflared-windows-amd64.exe');
  console.error('       npm run install:cloudflared');
  console.error('  3. Just start preview without a tunnel:  start.bat preview');
  return 1;
}

process.exit(await run());
