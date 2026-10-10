import { readFile, appendFile } from 'node:fs/promises';

const manifest = JSON.parse(await readFile(new URL('../public/data/catalog/manifest.json', import.meta.url), 'utf8'));
const v = manifest.verified || {};
const summary = [
  '### Catalog refresh',
  '',
  `Snapshot timestamp: ${manifest.generated_at}`,
  `Countries refreshed: ${Object.keys(manifest.countries || {}).length}`,
  `Countries kept from the previous snapshot: ${(manifest.failed || []).length}`,
  `Stock: ${v.checked || 0} checked, ${v.fresh || 0} recently checked, ${v.empty || 0} unavailable, ${v.unresolved || 0} unresolved, ${v.skipped || 0} waiting for a later run.`,
  '',
  'Brand listings refresh each run. Product stock checks rotate oldest first within the supplier request budget. GitHub may delay scheduled runs; this timestamp records when data actually refreshed.',
  '',
].join('\n');
console.log(summary);
if (process.env.GITHUB_STEP_SUMMARY) await appendFile(process.env.GITHUB_STEP_SUMMARY, summary);
if (manifest.failed?.length) console.log(`::warning::Kept previous listings: ${manifest.failed.map((f) => f.country).join(', ')}`);
if (v.unresolved || v.skipped) console.log('::warning::Some product stock checks remain pending; see the refresh summary.');
