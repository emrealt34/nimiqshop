// critical-css.mjs — record which CSS rules actually render at load, so the
// build can inline those and defer the rest.
//
// WHY
//
// The shop ships one stylesheet for every screen: 160 KB minified, of which a
// page matches 20-30 KB at load. Measured on the deployed site (mobile, Slow
// 4G, 4x CPU), that sheet costs ~450 ms of LCP: the bytes are on the critical
// path and the parse happens before the first paint. Splitting it is worth a
// page-speed point; guessing which half is which is not, so this script
// measures instead of guessing.
//
// WHAT IT MEASURES
//
// A coverage tour: every page, several viewports, with live data and with the
// API failing (so the empty/error states render too). For each page it records
// the (at-rule context, selector) pairs that matched at least one element while
// the page was loading. Those keys are the ones the build keeps inline; the
// complement — rules that matched NOTHING anywhere during the tour, i.e.
// modals, sheets, hover/focus states, wallet-connected UI — is written to one
// shared file that loads after first paint.
//
// The list is stored INVERTED on purpose (neverMatched, not everMatched):
// a rule the build does not recognize is then inlined by default. A stale
// capture degrades to "more CSS inlined", never to "a rule that was needed at
// load is missing".
//
// USAGE
//
//   node scripts/critical-css.mjs --capture https://shop.nimiqbase.com
//   node scripts/critical-css.mjs --capture http://127.0.0.1:4321
//
// Writes src/styles/critical.json, which integrations/critical-css.mjs reads
// during `astro build`. Re-run after adding or renaming screens or CSS; the
// build prints a warning when the capture predates the current CSS.

import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const OUT = join(root, 'src/styles/critical.json');

/** Paths the tour crawls. Keep in step with src/pages/*.astro. */
const PAGES = [
  '/',
  '/product/',
  '/cart/',
  '/orders/',
  '/order/',
  '/track/',
  '/cashback/',
  '/profile/',
  '/activity/',
  '/support/',
  '/admin/',
];

/** Viewports: phone, phablet, tablet portrait, laptop, desktop. Chosen so the
 *  widths on either side of every breakpoint in the stylesheet are covered. */
const VIEWPORTS = [
  { width: 360, height: 800 },
  { width: 390, height: 844 },
  { width: 768, height: 1024 },
  { width: 1024, height: 768 },
  { width: 1440, height: 900 },
];

/** Selector text for a rule, without the at-rule context. */
const contextOf = (rule) => {
  const parts = [];
  let parent = rule.parent;
  while (parent && parent.type === 'atrule') {
    parts.unshift(`@${parent.name} ${parent.params}`);
    parent = parent.parent;
  }
  return parts.join(' | ');
};

const norm = (text) => text.replace(/\s+/g, ' ').trim();
const keyOf = (context, selector) => `${context}\u0000${norm(selector)}`;

/** sha256 of the four shipped stylesheets, for the staleness warning. */
function sourceCssHash() {
  const dir = join(root, 'src/styles');
  const files = readdirSync(dir).filter((f) => f.endsWith('.css')).sort();
  const h = createHash('sha256');
  for (const f of files) h.update(readFileSync(join(dir, f)));
  return h.digest('hex').slice(0, 16);
}

async function capture(baseUrl, { withApi = true }) {
  const { chromium } = await import('playwright');
  const postcss = (await import('postcss')).default;
  const browser = await chromium.launch({ args: ['--no-sandbox'] });
  const allKeys = new Set();
  const matched = new Set();
  let loads = 0;
  try {
    for (const viewport of VIEWPORTS) {
      const context = await browser.newContext({ viewport });
      if (!withApi) await context.route('**/api/**', (r) => r.abort());
      const page = await context.newPage();
      for (const path of PAGES) {
        try {
          await page.coverage.startCSSCoverage();
          await page.goto(baseUrl.replace(/\/$/, '') + path, { waitUntil: 'load', timeout: 45_000 });
          // Data-driven content (product grid, order list) arrives after load.
          await page.waitForTimeout(withApi ? 2500 : 1200);
          const sheets = await page.coverage.stopCSSCoverage();
          for (const sheet of sheets) {
            if (!sheet.ranges.length && !sheet.text) continue;
            const root = postcss.parse(sheet.text);
            root.walkRules((rule) => {
              if (!rule.nodes || !rule.nodes.some((n) => n.type === 'decl')) return;
              const key = keyOf(contextOf(rule), rule.selector);
              allKeys.add(key);
              const start = rule.source.start.offset;
              const end = rule.source.end.offset + 1;
              if (sheet.ranges.some((r) => r.start < end && start < r.end)) matched.add(key);
            });
          }
          loads++;
        } catch (e) {
          console.warn(`  ${path} @${viewport.width} (api=${withApi}) skipped: ${e.message.split('\n')[0].slice(0, 70)}`);
        }
      }
      await context.close();
    }
  } finally {
    await browser.close();
  }
  return { allKeys, matched, loads };
}

async function main() {
  const args = process.argv.slice(2);
  const captureIdx = args.indexOf('--capture');
  if (captureIdx === -1) {
    console.error('usage: node scripts/critical-css.mjs --capture <baseUrl>');
    process.exit(2);
  }
  const baseUrl = args[captureIdx + 1];
  if (!baseUrl) {
    console.error('--capture needs a base URL, e.g. https://shop.nimiqbase.com');
    process.exit(2);
  }

  console.log(`critical-css: touring ${baseUrl} — ${PAGES.length} pages x ${VIEWPORTS.length} viewports x 2 data states`);
  const allKeys = new Set();
  const matched = new Set();
  let loads = 0;
  for (const withApi of [true, false]) {
    const r = await capture(baseUrl, { withApi });
    for (const k of r.allKeys) allKeys.add(k);
    for (const k of r.matched) matched.add(k);
    loads += r.loads;
    console.log(`  with data=${withApi}: ${r.loads} loads, ${r.matched.size} matched keys so far`);
  }

  const neverMatched = [...allKeys].filter((k) => !matched.has(k)).sort();
  const payload = {
    version: 1,
    capturedAt: new Date().toISOString(),
    source: baseUrl,
    loads,
    sourceCssHash: sourceCssHash(),
    keyCount: allKeys.size,
    neverMatched,
  };
  writeFileSync(OUT, JSON.stringify(payload, null, 1) + '\n');
  const pct = ((100 * neverMatched.length) / Math.max(1, allKeys.size)).toFixed(0);
  console.log(`critical-css: ${allKeys.size} rules seen, ${matched.size} rendered at load, ${neverMatched.length} deferrable (${pct}%)`);
  console.log(`critical-css: wrote ${OUT}`);
}

main().catch((e) => {
  console.error('critical-css: failed —', e);
  process.exit(1);
});
