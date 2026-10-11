import { readFile, appendFile } from 'node:fs/promises';

const manifest = JSON.parse(await readFile(new URL('../public/data/catalog/manifest.json', import.meta.url), 'utf8'));
const v = manifest.verified || {};
let market;
try { market = JSON.parse(await readFile(new URL('../public/data/market/manifest.json', import.meta.url), 'utf8')); }
catch { market = null; }
const marketLines = market ? [
  '### Market refresh', '',
  `Checked at: ${market.generated_at}`,
  `NIM: ${typeof market.nim === 'object' && market.nim ? `${market.nim.usd_per_nim} USD; observed ${market.nim.observed_at}; ${market.nim.sources} sources` : market.nim}`,
  `BTC: ${typeof market.btc === 'object' && market.btc ? `${market.btc.usd_per_btc} USD; observed ${market.btc.observed_at}; ${market.btc.sources} sources` : market.btc}`,
  `FX: ${typeof market.fx === 'object' && market.fx ? `${market.fx.currencies} currencies from ${market.fx.source}; observed ${market.fx.observed_at}; ${market.fx.fallback_currencies.length} baseline-only currencies` : market.fx}`,
  '',
  'The catalog and all rate files are saved in the same main workflow snapshot and deployed together. The free fiat feed publishes daily; hourly polling does not create hourly market observations.',
  ...(market.fallbacks || []).map(note => `- ${note}`), '',
] : ['### Market refresh', '', 'No market refresh report is available.', ''];
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
  ...marketLines,
].join('\n');
console.log(summary);
if (process.env.GITHUB_STEP_SUMMARY) await appendFile(process.env.GITHUB_STEP_SUMMARY, summary);
if (manifest.failed?.length) console.log(`::warning::Kept previous listings: ${manifest.failed.map((f) => f.country).join(', ')}`);
if (v.unresolved || v.skipped) console.log('::warning::Some product stock checks remain pending; see the refresh summary.');
if (!market || market.fallbacks?.length || market.nim === 'kept-previous-file' || market.btc === 'kept-previous-file' || market.fx === 'kept-previous-file') console.log('::warning::Some market sources failed or kept prior rates; see the provider timestamps in the refresh summary.');
