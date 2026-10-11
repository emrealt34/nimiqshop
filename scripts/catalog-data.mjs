/** Persist hourly data as Actions artifacts; main is the only Git branch. */
import { mkdir, readdir, readFile, writeFile, lstat, appendFile } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { tmpdir } from 'node:os';

const ROOT = process.cwd();
const mode = process.argv[2];
if (!['find', 'stage', 'restore'].includes(mode)) throw new Error('Use catalog-data.mjs find|stage|restore');
const SNAPSHOT = process.env.SNAPSHOT_DIR || join(process.env.RUNNER_TEMP || tmpdir(), 'catalog-snapshot');

async function findSnapshot() {
  const repo = process.env.GITHUB_REPOSITORY;
  const token = process.env.GITHUB_TOKEN;
  if (!token || !/^[\w.-]+\/[\w.-]+$/.test(repo || '')) throw new Error('Snapshot lookup requires repository and token');
  const get = async (path) => {
    const response = await fetch(`https://api.github.com/repos/${repo}${path}`, {
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json' },
      signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) throw new Error(`Snapshot API HTTP ${response.status}`);
    return response.json();
  };
  const runs = await get('/actions/workflows/catalog-refresh.yml/runs?branch=main&per_page=100');
  for (const run of runs.workflow_runs || []) {
    if (run.head_branch !== 'main' || !['schedule', 'workflow_dispatch'].includes(run.event)) continue;
    const result = await get(`/actions/runs/${run.id}/artifacts?per_page=100`);
    if (!(result.artifacts || []).some(a => a.name === 'catalog-snapshots' && !a.expired)) continue;
    // A code push can replace a queued deployment after data was saved.
    // Judge the refresh job itself, so that complete data is not discarded
    // while its deployment waits, fails, or is superseded by newer code.
    const jobs = await get(`/actions/runs/${run.id}/jobs?per_page=100`);
    if ((jobs.jobs || []).some(job => job.name === 'Refresh and persist catalog snapshots'
      && job.status === 'completed' && job.conclusion === 'success')) return run.id;
  }
  return 0;
}

async function copyJSON(src, dst, relative) {
  const from = join(src, relative);
  const to = join(dst, relative);
  let entries;
  try { entries = await readdir(from, { withFileTypes: true }); }
  catch (err) { if (err.code === 'ENOENT') return; throw err; }
  for (const entry of entries) {
    const path = join(relative, entry.name);
    if (entry.isDirectory()) await copyJSON(src, dst, path);
    else if (entry.isFile() && entry.name.endsWith('.json')) {
      const data = await readFile(join(src, path), 'utf8');
      JSON.parse(data); // data only; never restore executable code or symlinks
      await mkdir(dirname(join(dst, path)), { recursive: true });
      await writeFile(join(dst, path), data);
    }
  }
  await mkdir(to, { recursive: true });
}

async function copySnapshots(src, dst) {
  await copyJSON(src, dst, 'public/data/catalog');
  await copyJSON(src, dst, 'public/data/market');
  const path = 'scripts/data/stock-state.json';
  if (!(await lstat(join(src, path))).isFile()) throw new Error('Stock state must be a regular JSON file');
  const data = await readFile(join(src, path), 'utf8');
  JSON.parse(data);
  await mkdir(dirname(join(dst, path)), { recursive: true });
  await writeFile(join(dst, path), data);
}

if (mode === 'find') {
  const run = await findSnapshot();
  if (process.env.GITHUB_OUTPUT) await appendFile(process.env.GITHUB_OUTPUT, `run_id=${run}\n`);
  console.log(run ? `Restore snapshot from main workflow run ${run}.` : 'No saved snapshot yet; using the main seed.');
} else if (mode === 'stage') {
  await copySnapshots(ROOT, SNAPSHOT);
  console.log('Staged catalog, market and stock progress in one snapshot.');
} else {
  await copySnapshots(SNAPSHOT, ROOT);
  console.log('Restored JSON snapshot; application code stays on main.');
}
