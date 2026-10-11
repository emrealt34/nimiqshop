// A partial snapshot is useful, but it is not a completed refresh.
import { readFile, appendFile } from 'node:fs/promises';
import { refreshCompletion } from './catalog-refresh-completion.mjs';
const catalog = JSON.parse(await readFile(new URL('../public/data/catalog/manifest.json', import.meta.url)));
const market = JSON.parse(await readFile(new URL('../public/data/market/manifest.json', import.meta.url)));
const { complete } = refreshCompletion(catalog, market, Date.now(), {
  catalog: process.env.CATALOG_REFRESH_OUTCOME, market: process.env.MARKET_REFRESH_OUTCOME,
});
if (process.env.GITHUB_OUTPUT) await appendFile(process.env.GITHUB_OUTPUT, `complete=${complete}\n`);
if (!complete) {
  console.error('Full catalog or rate refresh incomplete. See the saved snapshot and refresh summary.');
} else {
  console.log(`Full stock scan completed: ${catalog.verified.checked} families, no pending or unresolved checks.`);
}
