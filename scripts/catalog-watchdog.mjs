/** Railway cron (every five minutes). Dispatches the existing main workflow
 * only when an hourly refresh is due. No supplier or deployment secrets here. */
import { pathToFileURL } from 'node:url';

export async function checkCatalog({ get, dispatch, now = Date.now() }) {
  const { workflow_runs: runs = [] } = await get('/actions/workflows/catalog-refresh.yml/runs?branch=main&per_page=100');
  const candidates = runs.filter(r => r.head_branch === 'main' && ['schedule', 'workflow_dispatch'].includes(r.event));
  for (const r of candidates) {
    const age = now - Date.parse(r.created_at);
    if (!Number.isFinite(age) || age < 0) continue;
    if (['queued', 'in_progress', 'waiting', 'pending', 'requested'].includes(r.status) && age < 40 * 60_000) {
      return { action: 'skip', reason: 'refresh_running', run_id: r.id };
    }
    if (r.status !== 'completed' || r.conclusion !== 'success' || age >= 55 * 60_000) continue;
    const { jobs = [] } = await get(`/actions/runs/${r.id}/jobs?per_page=100`);
    if (!jobs.some(j => j.name === 'Refresh and persist catalog snapshots' && j.conclusion === 'success')) continue;
    const { artifacts = [] } = await get(`/actions/runs/${r.id}/artifacts?per_page=100`);
    if (artifacts.some(a => a.name === 'catalog-snapshots' && !a.expired)) {
      return { action: 'skip', reason: 'published_snapshot_recent', run_id: r.id };
    }
  }
  await dispatch();
  return { action: 'dispatch', reason: 'hourly_refresh_due', branch: 'main' };
}

async function main() {
  const token = process.env.CATALOG_GITHUB_TOKEN;
  if (!token) throw new Error('CATALOG_GITHUB_TOKEN is required');
  const base = 'https://api.github.com/repos/emrealt34/nimiqshop';
  const request = async (path, method = 'GET', body) => {
    const response = await fetch(base + path, {
      method, headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json',
        'Content-Type': 'application/json', 'X-GitHub-Api-Version': '2022-11-28' },
      ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(15_000),
    });
    // Never print response bodies, request headers or credentials.
    if (!response.ok) throw new Error(`GitHub ${method} HTTP ${response.status}`);
    return response.status === 204 ? null : response.json();
  };
  const result = await checkCatalog({ get: request,
    dispatch: () => request('/actions/workflows/catalog-refresh.yml/dispatches', 'POST',
      { ref: 'main', inputs: { source: 'railway-watchdog' } }),
  });
  console.log(JSON.stringify({ checked_at: new Date().toISOString(), ...result }));
}

if (import.meta.main || (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)) {
  try { await main(); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
