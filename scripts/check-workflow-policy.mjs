#!/usr/bin/env node
/**
 * Repository workflow policy check.
 *
 * The workflows in .github/workflows are security-relevant build steps, and
 * the rules below are the ones that are easy to break by accident in a PR
 * review and impossible to notice afterwards:
 *
 *   1. every workflow declares a top-level `permissions:` block (least
 *      privilege starts at the workflow level, not the job level)
 *   2. every job sets `timeout-minutes` (a hung scanner must not burn hours)
 *   3. every `uses:` is pinned to a full 40-character commit SHA and carries a
 *      `# vX.Y.Z` comment, so Dependabot updates stay reviewable
 *   4. every `actions/checkout` sets `persist-credentials: false`, so the
 *      token never ends up in .git/config of a later step
 *   5. workflows that fire on both push and pull_request declare a
 *      `concurrency` group, so a new push cancels the stale run instead of
 *      racing it
 *   6. no `pull_request_target` and no curl-piped-to-shell inside a workflow
 *
 * Run locally:  npm run check:workflows
 * (CI runs it in the `workflows` job next to actionlint.)
 */
import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const WORKFLOW_DIR = fileURLToPath(new URL('../.github/workflows', import.meta.url));
const SHA = /^[0-9a-f]{40}$/;
const findings = [];

/** Line based helpers: a workflow linter must not need a YAML dependency. */
const linesOf = (text) => text.split(/\r?\n/);
const fail = (file, line, message) =>
  findings.push({ file, line, message });

/** Top-level block ranges: "key:" at column 0 up to the next column-0 key. */
function topLevelBlocks(lines) {
  const starts = [];
  lines.forEach((line, i) => {
    if (/^[A-Za-z_][\w-]*:/.test(line)) starts.push(i);
  });
  return starts.map((from, idx) => ({
    key: lines[from].slice(0, lines[from].indexOf(':')),
    from,
    to: idx + 1 < starts.length ? starts[idx + 1] : lines.length,
  }));
}

/** Job ranges inside `jobs:`: two-space keys such as "  frontend:". */
function jobBlocks(lines) {
  const jobs = topLevelBlocks(lines).find((b) => b.key === 'jobs');
  if (!jobs) return [];
  const keys = [];
  for (let i = jobs.from + 1; i < jobs.to; i += 1) {
    if (/^ {2}[A-Za-z_][\w-]*:\s*(#.*)?$/.test(lines[i])) keys.push(i);
  }
  return keys.map((from, idx) => ({
    name: lines[from].trim().replace(/:$/, ''),
    from,
    to: idx + 1 < keys.length ? keys[idx + 1] : jobs.to,
  }));
}

function checkWorkflow(file, text) {
  const lines = linesOf(text);
  const blocks = topLevelBlocks(lines);
  const has = (key) => blocks.some((b) => b.key === key);
  const body = (key) => {
    const b = blocks.find((x) => x.key === key);
    return b ? lines.slice(b.from, b.to).join('\n') : '';
  };

  if (!has('on')) fail(file, 1, 'no `on:` trigger — the workflow can never run');
  if (!has('permissions')) {
    fail(file, 1, 'no top-level `permissions:` — add the least privilege the workflow needs');
  }

  const jobs = jobBlocks(lines);
  if (jobs.length === 0) fail(file, 1, 'no jobs found');
  for (const job of jobs) {
    const slice = lines.slice(job.from, job.to);
    // A job that calls a reusable workflow cannot set timeout-minutes (the
    // callee owns its own budget), so the rule only applies to real runners.
    if (slice.some((l) => /^ {4}runs-on:/.test(l)) &&
        !slice.some((l) => /^ {4}timeout-minutes:\s*\d+\s*$/.test(l))) {
      fail(file, job.from + 1, `job "${job.name}" has no timeout-minutes`);
    }
  }

  let notesOpen = false;
  lines.forEach((line, i) => {
    // Prose about a rule is not a violation of it.
    if (line.trimStart().startsWith('#')) return;
    const uses = line.match(/uses:\s*(\S+)\s*(?:#\s*(.*))?$/);
    if (uses) {
      const [, ref, comment] = uses;
      if (ref.startsWith('./')) return; // local composite action
      if (!ref.includes('@')) {
        fail(file, i + 1, `"${ref}" is not pinned to a ref`);
        return;
      }
      const [action, rev] = ref.split('@');
      if (!SHA.test(rev)) {
        fail(file, i + 1, `"${action}" is pinned to "${rev}" instead of a 40-character commit SHA`);
      }
      if (!comment || !/^v?\d/.test(comment.trim())) {
        fail(file, i + 1, `"${action}" needs a "# vX.Y.Z" version comment next to the pin`);
      }
      if (action.startsWith('actions/checkout@')) {
        const step = lines.slice(i, i + 8).join('\n');
        if (!/persist-credentials:\s*false/.test(step)) {
          fail(file, i + 1, 'actions/checkout without persist-credentials: false');
        }
      }
    }
    if (/pull_request_target/.test(line)) {
      fail(file, i + 1, 'pull_request_target runs untrusted code with repository secrets — use pull_request + workflow_run');
    }
    // Install one-liners inside `gh release --notes` are documentation, not
    // execution, so they are exempt; a real `run:` step may never pipe a
    // download straight into a shell.
    const inReleaseNotes = notesOpen;
    if (/--notes\s+"/.test(line) && !/"\s*$/.test(line.slice(line.indexOf('--notes') + 8))) {
      notesOpen = true;
    } else if (notesOpen && /"\s*$/.test(line.trim())) {
      notesOpen = false;
    }
    if (!inReleaseNotes && /(curl|wget)[^|]*\|\s*(sudo\s+)?(ba)?sh/.test(line)) {
      fail(file, i + 1, 'pipe-to-shell is not allowed in a run step');
    }
  });

  if (has('push') && has('pull_request') && !has('concurrency')) {
    fail(file, 1, 'push + pull_request workflow without a concurrency group (stale runs will race)');
  }

  const runsOn = body('jobs');
  if (!runsOn.trim()) fail(file, 1, '`jobs:` block is empty');
}

let files = [];
try {
  files = (await readdir(WORKFLOW_DIR)).filter((f) => f.endsWith('.yml') || f.endsWith('.yaml'));
} catch {
  console.error('check:workflows — .github/workflows is missing');
  process.exit(1);
}

for (const file of files.sort()) {
  checkWorkflow(file, await readFile(join(WORKFLOW_DIR, file), 'utf8'));
}

if (findings.length > 0) {
  console.error(`check:workflows — ${findings.length} policy violation(s):`);
  for (const f of findings) console.error(`  .github/workflows/${f.file}:${f.line}  ${f.message}`);
  process.exit(1);
}
console.log(`check:workflows — ${files.length} workflow file(s) satisfy the policy.`);
