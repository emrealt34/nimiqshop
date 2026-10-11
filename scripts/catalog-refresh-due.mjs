/** Redundant schedule checks never imply redundant supplier requests. */
import { appendFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

export async function refreshDue({ event, source, runId, now = Date.now(), get }) {
  if (event === 'schedule') return { due: true, reason: 'Scheduled six-hour full refresh.' };
  if (event === 'workflow_dispatch' && source !== 'railway-watchdog') return { due: true, reason: 'Manual refresh requested.' };
  const freshnessMs = 6 * 60 * 60_000;
  const runs = await get('/actions/workflows/catalog-refresh.yml/runs?branch=main&per_page=100');
  for (const run of runs.workflow_runs || []) {
    if (String(run.id) === String(runId) || run.head_branch !== 'main'
      || !['schedule', 'workflow_dispatch'].includes(run.event)
      || run.status !== 'completed' || run.conclusion !== 'success') continue;
    const created = Date.parse(run.created_at);
    // Clock anomalies, old data and failed deployments must not suppress a refresh.
    if (!Number.isFinite(created) || created > now || now - created >= freshnessMs) continue;
    const jobs = await get(`/actions/runs/${run.id}/jobs?per_page=100`);
    const refresh = (jobs.jobs || []).find(job => job.name === 'Refresh and persist catalog snapshots'
      && job.status === 'completed' && job.conclusion === 'success');
    if (!refresh) continue; // A successful guard-only run has no refreshed data.
    const artifacts = await get(`/actions/runs/${run.id}/artifacts?per_page=100`);
    if (!(artifacts.artifacts || []).some(a => a.name === 'catalog-snapshots' && !a.expired)) continue;
    return { due: false, reason: `Catalog and market were refreshed and deployed by main run ${run.id} less than six hours ago.` };
  }
  return { due: true, reason: 'No recently published catalog and market snapshot; refresh is due.' };
}

async function main() {
  const repo = process.env.GITHUB_REPOSITORY;
  const token = process.env.GITHUB_TOKEN;
  if (!token || !/^[\w.-]+\/[\w.-]+$/.test(repo || '')) throw new Error('Freshness lookup requires repository and token');
  const get = async path => {
    const response = await fetch(`https://api.github.com/repos/${repo}${path}`, {
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json' },
      signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) throw new Error(`Freshness API HTTP ${response.status}`);
    return response.json();
  };
  let result;
  try { result = await refreshDue({ event: process.env.GITHUB_EVENT_NAME, source: process.env.REFRESH_SOURCE, runId: process.env.GITHUB_RUN_ID, get }); }
  catch { result = { due: true, reason: 'Freshness lookup unavailable; attempt refresh rather than silently keep stale data.' }; }
  console.log(result.reason);
  if (process.env.GITHUB_OUTPUT) await appendFile(process.env.GITHUB_OUTPUT, `due=${result.due}\n`);
  if (process.env.GITHUB_STEP_SUMMARY) await appendFile(process.env.GITHUB_STEP_SUMMARY, `### Six-hour refresh check\n\n${result.reason}\n\nScheduled full scans run at 00:17, 06:17, 12:17 and 18:17 UTC. Manual refresh always runs. GitHub scheduling can still be delayed.\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
