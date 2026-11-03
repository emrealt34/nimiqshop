/**
 * THE DEFERRED STYLESHEET MAY NOT MOVE ANYTHING.
 *
 * The build splits the shop's stylesheet in two: the rules a page needs before
 * its first paint stay inline, everything else (modals, sheets, hover states,
 * other breakpoints) goes into one shared `full.<hash>.css` that is applied a
 * moment later (see integrations/critical-css.mjs). The split is driven by
 * src/styles/critical.json — a capture of which rules matched at load — so a
 * capture that has gone stale silently turns first-paint layout into a late
 * rule, and the page visibly jumps the instant the sheet lands.
 *
 * That is not hypothetical. Before 2026-11-03 the capture predated the product
 * grid: `.product-card`, `.p-name`, `.p-meta .chip` and the whole 719px
 * `@media` block were "never matched" and therefore deferred, so on a real
 * load the hero's aside moved 13 px, every card changed height by 3 px and the
 * phone filter row jumped 21 px — the owner's "sayfa ilk açıldığında bazı
 * öğeler aşağı yukarı oynuyor". The build-time guards added with that fix
 * (selector-level cascade keep, shared-sheet pruning) are what this spec
 * protects: if either is weakened, or the capture goes stale again, this test
 * fails with the exact selectors that moved.
 *
 * NAVIGATION: always via BASE (`/nimiqshop/`), never `'/'`. The e2e server
 * (tests/e2e/support/serve.mjs) mimics GitHub Pages, so every path outside
 * BASE is a plain 404 — a spec that navigates to `'/'` measures the 404 page,
 * finds no elements at all, and passes no matter what the stylesheet does.
 * That is how the first version of this file looked green while the live site
 * was jumping 28 px.
 *
 * HOW IT MEASURES
 *   Load the page twice — once with the deferred sheet allowed to apply, once
 *   with the request aborted — and diff the boxes of the elements that sit in
 *   the first viewport. Anything that differs by more than a pixel across the
 *   two runs moved because of that sheet, by definition.
 */
import { test, expect } from './support/fixtures';
import { BASE } from './support/data';
import type { Page } from '@playwright/test';

/** Elements that are on screen at first paint on the pages under test. */
const WATCHED = [
  'header .topbar',
  '.hero h1',
  '.hero .lede',
  '.hero-actions',
  '.kf-pkg',
  '.how-strip',
  '#browse',
  '.toolbar',
  '.toolbar.filters',
  '.toolbar.filters .field',
  '.toolbar.filters .btn-country',
  '.seg',
  '.searchbox',
  '#gridWrap',
  '.grid.products',
  '.product-card',
  '.product-card .thumb',
  '.product-card .p-name',
  '.product-card .p-meta',
  '.come-back',
  '.wallet-balance',
  '.footer',
];

/** First-viewport boxes of everything in WATCHED. */
async function boxes(page: Page): Promise<Record<string, [number, number, number, number] | null>> {
  return boxesOf(page, WATCHED);
}

/** Boxes for an explicit selector list. */
async function boxesOf(
  page: Page,
  sels: string[],
): Promise<Record<string, [number, number, number, number] | null>> {
  return page.evaluate((sels) => {
    const out: Record<string, [number, number, number, number] | null> = {};
    for (const s of sels) {
      const el = document.querySelector(s);
      if (!el) {
        out[s] = null;
        continue;
      }
      const r = el.getBoundingClientRect();
      out[s] = [r.x, r.y, r.width, r.height].map((n) => Math.round(n * 10) / 10) as [number, number, number, number];
    }
    return out;
  }, sels);
}

/**
 * Load `url` with the deferred stylesheet either applied or blocked, and return
 * the settled boxes. Fonts are pinned to the document's own load event plus a
 * fixed settle window so both runs are compared at the same point of the
 * loading sequence.
 */
async function settledBoxes(page: Page, url: string, blockLateCss: boolean) {
  if (blockLateCss) {
    await page.route('**/full.*.css', (route) => route.abort());
  }
  await page.goto(url, { waitUntil: 'load' });
  // The sheet is fetched with `media="print"` and flipped by a tiny script, so
  // give both paths the same, generous window to reach their final state.
  await page.waitForTimeout(2500);
  const result = await boxes(page);
  await page.unroute('**/full.*.css');
  return result;
}

/** Hero-region boxes for the fallback-vs-webfont comparison. Deliberately
 * narrow: these are the elements the owner saw move, they are laid out by text
 * alone (no geo/country state, no fetched data), and they sit in the first
 * viewport at both sizes. A heading that takes a different number of lines
 * moves every one of them. */
const FONT_WATCHED = ['header .topbar', '.hero h1', '.hero .lede', '.hero-actions', '.kf-pkg', '.how-strip', '.come-back'];

const VIEWPORTS = [
  { name: 'desktop', width: 1280, height: 800 },
  { name: 'phone', width: 390, height: 844 },
];

for (const vp of VIEWPORTS) {
  test(`home: the deferred stylesheet does not move anything (${vp.name}) @smoke`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const withLate = await settledBoxes(page, BASE, false);
    const withoutLate = await settledBoxes(page, BASE, true);

    const moved: string[] = [];
    for (const sel of WATCHED) {
      const a = withLate[sel];
      const b = withoutLate[sel];
      if (!a && !b) continue;
      // An element that only exists in one of the two runs is a bigger
      // difference than a moved box, and worth naming.
      if (!a || !b) {
        moved.push(`${sel}: ${a ? 'only WITH the deferred sheet' : 'only WITHOUT it'}`);
        continue;
      }
      const d = a.map((n, i) => Math.abs(n - b[i]));
      if (Math.max(...d) > 1) {
        moved.push(`${sel}: with=[${a.join(',')}] without=[${b.join(',')}]`);
      }
    }
    expect(
      moved,
      'elements moved when the deferred stylesheet applied — the critical-CSS capture is stale, or a cascade guard in integrations/critical-css.mjs was weakened. Re-run: node scripts/critical-css.mjs --capture <url> --country TR',
    ).toEqual([]);
  });
}

/**
 * THE FALLBACK FACE MAY NOT LAY OUT DIFFERENTLY FROM THE WEBFONT.
 *
 * Both webfonts are `font-display: swap` (the owner rejected `optional`: it
 * locks a whole pageview into the fallback face whenever the ~100 ms window is
 * missed). With `swap` the fallback paints first and the real face replaces it
 * as soon as it lands, so the only thing standing between a cold visitor and a
 * visible jump is the metric-matched `@font-face` blocks in src/styles/fonts.css
 * — ascent/descent overrides for the line box and a measured `size-adjust` for
 * the advance widths.
 *
 * Those blocks are per FONT, not per family: one width ratio cannot describe
 * several fallbacks, and `local()` silently skips the ones that are not
 * installed on the machine doing the rendering. On 2026-11-03 the serif block
 * listed `local("DejaVu Serif")` as its last source with Liberation Serif's
 * 116.7% ratio, so every device without Liberation/Times — a plain Linux box,
 * and Android, where neither the Arial nor the Liberation sans names resolve
 * either — swapped the hero heading from four lines to three: 28 px of
 * movement and CLS 0.0920 on the live site.
 *
 * This test is font-agnostic: it loads the page once with the woff2 files
 * blocked (fallback only) and once normally, and requires the same boxes. It
 * fails on whatever font the runner happens to have, which is the point — the
 * ratios must hold for DejaVu and Liberation (what CI and most Linux desktops
 * have) as well as for the Arial-class faces.
 */
for (const vp of VIEWPORTS) {
  test(`home: the fallback font lays out like the webfont (${vp.name}) @smoke`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const withWebfont = await settledBoxes(page, BASE, false);

    await page.route('**/*.woff2', (route) => route.abort());
    await page.goto(BASE, { waitUntil: 'load' });
    await page.waitForTimeout(2500);
    const fallbackOnly = await boxesOf(page, FONT_WATCHED);
    await page.unroute('**/*.woff2');

    // Only the FLOW is compared: `y` (where the element sits) and, for
    // elements that are not page-tall accumulators, `height`. Intrinsic
    // widths are deliberately ignored — a segmented control measures 670 px
    // in Nunito and 652 px in the fallback on some hosts (a fraction of a
    // percent per glyph) without moving anything, and failing on that would
    // make the guard noise instead of signal. What must not happen is text
    // taking a different number of LINES, and that shows up as `y`/`height`.
    const moved: string[] = [];
    for (const sel of FONT_WATCHED) {
      const a = withWebfont[sel];
      const b = fallbackOnly[sel];
      if (!a && !b) continue;
      if (!a || !b) {
        moved.push(`${sel}: ${a ? 'only with the webfont' : 'only in the fallback run'}`);
        continue;
      }
      const dy = Math.abs(a[1] - b[1]);
      const dh = a[3] <= 1000 ? Math.abs(a[3] - b[3]) : 0;
      if (dy > 1 || dh > 1) {
        moved.push(`${sel}: y/height webfont=[${a[1]},${a[3]}] fallback=[${b[1]},${b[3]}]`);
      }
    }
    expect(
      moved,
      'the fallback face wraps or sizes text differently from the webfont, so the swap moves the page — give the fallback font its OWN @font-face with a measured size-adjust (src/styles/fonts.css has the current ratios)',
    ).toEqual([]);
  });
}

/**
 * THE DEFERRED STYLESHEET MUST SURVIVE A DEAD ISLAND LOADER.
 *
 * `full.<hash>.css` is inert (`media="print"`) until something flips it to
 * "all". Until 2026-11-03 that flip existed only inside `css-late.js` — a
 * 1.3 KB external file — so blocking, delaying or 404ing that one request left
 * the page with nothing but its inline critical rules: half the design simply
 * missing. The owner hit it while clicking through the nav ("yeni sayfada CSS
 * yüklenmiyor"), and it reproduced in one line: abort the css-late script, then
 * count the external sheets — zero, and `#late-css` still said media="print".
 *
 * The flip is now an inline bootstrap (part of the document, hashed into the
 * CSP) that runs on the first animation frames and keeps `load` plus a timer
 * as fallbacks, and the link carries its `href` from the first byte so a
 * script failure can no longer leave it pointing at nothing.
 *
 * This test blocks the loader on purpose and requires the sheet to apply.
 */
test('the deferred stylesheet applies even when the island loader never runs @smoke', async ({ page }) => {
  await page.route('**/css-late*.js', (route) => route.abort());
  await page.goto(BASE, { waitUntil: 'load' });
  await page.waitForFunction(
    () => (document.getElementById('late-css') as HTMLLinkElement | null)?.media === 'all',
    null,
    { timeout: 5000 },
  );

  const state = await page.evaluate(() => {
    const link = document.getElementById('late-css') as HTMLLinkElement | null;
    const href = link?.getAttribute('href') || '';
    const sheet = [...document.styleSheets].find((s) => s.href && /\/full\.[0-9a-f]+\.css$/.test(s.href));
    let rules = 0;
    try {
      rules = sheet?.cssRules.length ?? 0;
    } catch {
      rules = 0;
    }
    return { href, rules, media: link?.media ?? null };
  });

  // The href must exist in the markup itself: an empty href + a dead script was
  // exactly the "no CSS on the new page" state.
  expect(state.href, 'the <link id="late-css"> must carry its href in the HTML, not only in data-href').toMatch(/\/full\.[0-9a-f]+\.css$/);
  expect(state.media, 'the deferred stylesheet never flipped to media="all"').toBe('all');
  expect(state.rules, 'the deferred stylesheet did not parse — every rule behind it is missing').toBeGreaterThan(100);
});

/**
 * The capture itself must have been taken with a market whose catalog renders.
 * A capture that never sees a product grid defers every card rule — the exact
 * regression the spec above then catches, but it is worth failing on the cause
 * rather than only on the symptom.
 */
test('capture was taken with product cards on screen @smoke', async () => {
  const fs = await import('node:fs/promises');
  const raw = await fs.readFile('src/styles/critical.json', 'utf8');
  const capture = JSON.parse(raw) as { neverMatched?: string[]; capturedAt?: string };
  const never = capture.neverMatched || [];
  // `.product-card` (the base rule, no pseudo-class) must have MATCHED.
  const deferredCard = never.filter((k) => k.endsWith('\u0000.product-card'));
  expect(
    deferredCard,
    `the capture (${capture.capturedAt}) deferred the base .product-card rule — it was taken without a filled grid. Re-run: node scripts/critical-css.mjs --capture <url> --country TR`,
  ).toEqual([]);
});

/**
 * THE REFRESH REPORT IS IN THE FIRST BYTE.
 *
 * /status used to paint eight 14 px placeholder bars and then swap in the real
 * table once `/data/market/refresh.json` resolved: the card grew 202 px when
 * the deferred sheet turned the <dl> into a grid, then another 180 px when the
 * data landed — 0.1131 CLS on a throttled phone load, the second-largest shift
 * in the app after the fonts.
 *
 * The report is a file the catalog workflow commits before the build runs, so
 * there is no reason for the page to invent a shape first. status.astro reads
 * it and hands it to the island as `initial`. This test is not about CLS
 * numbers — it is the cheap, deterministic version: with scripting OFF, the
 * table must already show served values. If someone puts the placeholder back
 * (or the build stops finding the report), the first paint is a lie again and
 * this fails.
 */
test('status report is server-rendered, not fetched into an empty table', async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 900 } });
  const page = await context.newPage();
  await page.goto(`${BASE}/status/`, { waitUntil: 'load' });

  const values = await page.locator('.refresh-status-list dd').allInnerTexts();
  expect(values.length, 'the report table must be in the HTML').toBeGreaterThanOrEqual(8);
  // One em dash is the placeholder that used to grow into a real row.
  expect(values.filter((v) => v.trim() && v.trim() !== '—').length,
    'every row must carry a served value, not a placeholder').toBeGreaterThanOrEqual(6);

  await context.close();
});
