/** Read-only Railway monitor. Runs once every six hours, then exits.
 * GitHub owns refreshes; this script never dispatches work or calls suppliers. */
import { pathToFileURL } from 'node:url';

export async function monitorCatalog({ readReport, readRuns, now = Date.now() }) {
  let report;
  try { report = await readReport(); } catch { report = null; }
  const recent = value => {
    const time = Date.parse(value || '');
    return Number.isFinite(time) && now >= time && now - time < 9 * 3600000;
  };
  const complete = report?.complete === true && report?.rates_complete === true
    && report?.catalog?.complete === true && report.catalog.stock?.completed === true
    && report.catalog.stock.mode === 'full' && !(report.catalog.retained_countries?.length)
    && !report.catalog.stock.unresolved;
  const fresh = report && recent(report.catalog?.generated_at) && recent(report.market?.generated_at)
    && recent(report.market?.nim?.observed_at) && recent(report.market?.btc?.observed_at);
  if (complete && fresh) return { state: 'published', run_id: report.run_id || null, catalog_at: report.catalog.generated_at, market_at: report.market.generated_at };
  const { workflow_runs: runs = [] } = await readRuns();
  const active = runs.find(r => r.head_branch === 'main' && String(r.name).includes('Catalog and rates')
    && ['schedule', 'workflow_dispatch'].includes(r.event) && ['queued', 'in_progress', 'waiting', 'pending', 'requested'].includes(r.status)
    && recent(r.created_at) && now - Date.parse(r.created_at) < 220 * 60000);
  if (active) return { state: 'refresh_in_progress', run_id: active.id, published_complete: !!complete, published_fresh: !!fresh };
  return { state: report ? complete ? 'stale' : 'incomplete' : 'unavailable', run_id: report?.run_id || null };
}

async function main() {
  const read = async url => {
    const r = await fetch(url, { headers: { Accept: 'application/json', 'User-Agent': 'nimiqshop-read-only-catalog-monitor' }, signal: AbortSignal.timeout(15000) });
    if (!r.ok) throw new Error(`Catalog monitor read HTTP ${r.status}`);
    return r.json();
  };
  const result = await monitorCatalog({
    readReport: () => read('https://shop.nimiqbase.com/data/market/refresh.json'),
    readRuns: () => read('https://api.github.com/repos/emrealt34/nimiqshop/actions/runs?branch=main&per_page=100'),
  });
  console.log(JSON.stringify({ checked_at: new Date().toISOString(), mode: 'read_only', ...result }));
  if (!['published', 'refresh_in_progress'].includes(result.state)) process.exitCode = 1;
}
if (import.meta.main || (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)) {
  try { await main(); } catch (err) { console.error(err.message); process.exitCode = 1; }
}
