#!/usr/bin/env node
// npm audit gate with a small, documented exception list.
//
// Why not plain `npm audit --audit-level=high`: it fails the Security
// workflow for vulnerabilities that have NO patched release yet. That turns
// a real security signal into a permanently red badge, and the usual
// "just ignore the job" reaction is exactly how a NEW, fixable advisory
// slips through unnoticed.
//
// So: every high/critical advisory must either be fixable (job fails, you
// upgrade) or listed here with a reason and a review date (job passes, the
// exception is re-checked when it expires). Anything not listed is fatal.
//
// Run locally:  node scripts/audit-gate.mjs
import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';

// Each entry: advisory URL -> { reason, reviewBy, package }.
// Keep this list SHORT. An entry here is a promise to re-check it.
export const ALLOWED = new Map([
  [
    'https://github.com/advisories/GHSA-ch52-4w7c-c8xp',
    {
      package: 'http-cache-semantics',
      reviewBy: '2027-01-31',
      reason:
        'No patched release exists (npm latest is 4.2.0, the advisory covers <= 4.2.0 and ' +
        'declares first_patched_version: none). Reachable only through astro\u2019s build-time ' +
        'HTTP cache; the shipped artifact is static HTML/CSS/JS and never runs this code. ' +
        'Re-check when 4.2.1+ ships and bump via overrides.',
    },
  ],
]);

/** Extract high/critical advisories from `npm audit --json` output. */
export function collectFindings(report) {
  const found = new Map(); // advisory url -> {severity, package, title}
  const vulns = (report && report.vulnerabilities) || {};
  for (const [name, v] of Object.entries(vulns)) {
    const via = Array.isArray(v.via) ? v.via : [];
    for (const entry of via) {
      // Meta-vulnerabilities ("depends on vulnerable X") are plain strings;
      // only the real advisories carry a url/severity.
      if (!entry || typeof entry !== 'object') continue;
      const severity = entry.severity;
      if (severity !== 'high' && severity !== 'critical') continue;
      const url = entry.url || `npm:${entry.source || name}`;
      if (!found.has(url)) {
        found.set(url, { severity, package: name, title: entry.title || '' });
      }
    }
  }
  return found;
}

function main() {
  let raw;
  try {
    raw = execFileSync('npm', ['audit', '--json'], { encoding: 'utf8', maxBuffer: 128 * 1024 * 1024 });
  } catch (err) {
    // npm audit exits non-zero when it finds anything; the JSON is on stdout.
    raw = (err && err.stdout) || '';
    if (!raw) {
      console.error('npm audit produced no JSON output; cannot evaluate the gate.');
      if (err && err.stderr) console.error(String(err.stderr).slice(0, 2000));
      process.exit(2);
    }
  }

  let report;
  try {
    report = JSON.parse(raw);
  } catch {
    console.error('npm audit did not return JSON; refusing to pass on an unreadable report.');
    console.error(raw.slice(0, 2000));
    process.exit(2);
  }

  const findings = collectFindings(report);
  const today = new Date().toISOString().slice(0, 10);
  const allowed = [];
  const blocking = [];
  for (const [url, info] of findings) {
    const ex = ALLOWED.get(url);
    if (!ex) blocking.push([url, info]);
    else if (ex.reviewBy && ex.reviewBy < today) blocking.push([url, { ...info, expired: ex.reviewBy }]);
    else allowed.push([url, info, ex]);
  }

  for (const [url, info, ex] of allowed) {
    console.log(`ALLOWED  ${info.severity}: ${info.package} — ${url}`);
    console.log(`         reason: ${ex.reason}`);
    console.log(`         re-check by ${ex.reviewBy}`);
  }
  for (const [url, info] of blocking) {
    console.error(`BLOCKING ${info.severity}: ${info.package} — ${url}`);
    if (info.title) console.error(`         ${info.title}`);
    if (info.expired) console.error(`         the documented exception expired on ${info.expired}`);
  }

  const totals = report.metadata && report.metadata.vulnerabilities;
  if (totals) {
    console.log(
      `audit totals: ${Object.entries(totals)
        .map(([k, v]) => `${k}=${v}`)
        .join(' ')}`
    );
  }

  if (blocking.length) {
    console.error(
      `\n${blocking.length} unexcepted high/critical advisor${blocking.length === 1 ? 'y' : 'ies'}: ` +
        'upgrade the dependency, or add a documented entry to ALLOWED in scripts/audit-gate.mjs.'
    );
    process.exit(1);
  }
  console.log(`\nnpm audit gate passed (${allowed.length} documented exception(s), 0 blocking).`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();
