/** Persist JSON snapshots separately from protected application code. */
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, mkdir, readdir, readFile, writeFile, rm, lstat } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { tmpdir } from 'node:os';

const exec = promisify(execFile);
const ROOT = process.cwd();
const BRANCH = 'catalog-data';
const mode = process.argv[2];
if (!['restore', 'persist'].includes(mode)) throw new Error('Use catalog-data.mjs restore|persist');
const token = process.env.GITHUB_TOKEN;
if (!token) throw new Error('GITHUB_TOKEN is required for snapshot access');
const env = { ...process.env, GIT_CONFIG_COUNT: '1',
  GIT_CONFIG_KEY_0: 'http.https://github.com/.extraheader',
  GIT_CONFIG_VALUE_0: 'AUTHORIZATION: basic ' + Buffer.from('x-access-token:' + token).toString('base64') };
const git = async (args, cwd = ROOT) => (await exec('git', args, { cwd, env, timeout: 60_000 })).stdout.trim();

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

let exists = true;
try { await git(['ls-remote', '--exit-code', '--heads', 'origin', BRANCH]); }
catch (err) { if (err.code === 2) exists = false; else throw err; }
if (!exists && mode === 'restore') {
  console.log('No persisted catalog yet; using the committed seed.');
} else {
  const temp = await mkdtemp(join(process.env.RUNNER_TEMP || tmpdir(), 'catalog-data-'));
  const work = join(temp, 'snapshots');
  try {
    if (exists) await git(['fetch', '--depth=1', 'origin', `refs/heads/${BRANCH}`]);
    await git(['worktree', 'add', '--detach', work, exists ? 'FETCH_HEAD' : 'HEAD']);
    if (mode === 'restore') {
      await copySnapshots(work, ROOT);
      console.log(`Restored catalog-data ${await git(['rev-parse', 'HEAD'], work)}.`);
    } else {
      if (!exists) {
        await git(['checkout', '--orphan', 'catalog-snapshot'], work);
        await git(['rm', '-rf', '.'], work);
      }
      await copySnapshots(ROOT, work);
      await git(['add', 'public/data/catalog', 'public/data/market', 'scripts/data/stock-state.json'], work);
      const changes = await git(['diff', '--cached', '--name-only'], work);
      if (changes) {
        await git(['-c', 'user.name=github-actions[bot]', '-c', 'user.email=41898282+github-actions[bot]@users.noreply.github.com',
          'commit', '-m', 'Refresh hourly catalog and market snapshots'], work);
        // No force: a concurrent refresh fails rather than overwriting data.
        await git(['push', 'origin', `HEAD:${BRANCH}`], work);
        console.log(`Persisted ${changes.split('\n').length} snapshot files to catalog-data.`);
      } else console.log('Snapshot data unchanged.');
    }
  } finally {
    await git(['worktree', 'remove', '--force', work]).catch(() => {});
    await rm(temp, { recursive: true, force: true });
  }
}
