/**
 * EVERY PAGE MUST CARRY EVERY SELECTOR — OR THE SITE IS "BROKEN".
 *
 * The build splits the shop's stylesheet in two: a per-page inline block (the
 * rules that page needs at first paint) and ONE shared deferred sheet
 * (`full.<hash>.css`) that every page links. That asymmetry is the whole
 * safety property of the split: the inline block may differ from page to page,
 * the deferred sheet may not, so any selector that exists anywhere must exist
 * on every page through the shared sheet.
 *
 * On 2026-11-03 that property was broken by a build-time "prune": a rule was
 * dropped from the shared sheet whenever ANY page kept the same selector
 * inline, on the assumption that "a page that does not use one has nothing to
 * style". The assumption is false — the pages that "do not use" `.card`,
 * `.pt-card`, `.act-tile` in their static HTML are exactly the ones where
 * React renders that markup after hydration. Measured on the deployed build:
 * the shared sheet went 1648 → 1146 rules, and every page was missing 193–280
 * selectors (`.card`, `.muted`, `.gold-text`, `.faint`, `.btn-lg`, …). Half the
 * shop rendered unstyled — reported as "sayfalar bozuldu, CSS yüklenmiyor".
 *
 * This spec pins the invariant instead of the implementation: load every page,
 * collect the selectors that are actually live in the document, and require
 * each page's set to contain the union. It fails on the cause (a page that
 * lost rules), names the missing selectors, and does not care how the split is
 * implemented.
 */
import { test, expect } from './support/fixtures';
import { BASE } from './support/data';

/** Every built page. `/404` included: it is served on any bad URL. */
const PAGES = ['', 'cart/', 'orders/', 'track/', 'cashback/', 'quiz/', 'support/', 'status/', 'activity/', 'product/', 'admin/'];

/** Selectors live in the document (inline blocks + every linked sheet). */
async function liveSelectors(page: import('@playwright/test').Page): Promise<string[]> {
  return page.evaluate(() => {
    const out: string[] = [];
    const walk = (list: CSSRuleList) => {
      for (const rule of Array.from(list)) {
        const group = rule as CSSGroupingRule;
        if (group.cssRules && !(rule as CSSStyleRule).selectorText) {
          // @media / @supports / @layer — the inner rules carry the selectors.
          walk(group.cssRules);
          continue;
        }
        const sel = (rule as CSSStyleRule).selectorText;
        if (sel) out.push(sel);
      }
    };
    for (const sheet of Array.from(document.styleSheets)) {
      try {
        walk(sheet.cssRules);
      } catch {
        // cross-origin sheet — nothing in this build, and nothing we can read
      }
    }
    // A rule's selector is usually a list; the split works per part, so
    // compare per part too.
    return out.flatMap((sel) => sel.split(',').map((part) => part.trim())).filter(Boolean);
  });
}

test('every page carries every selector the site ships @smoke', async ({ page }) => {
  const perPage = new Map<string, Set<string>>();
  const union = new Set<string>();

  for (const path of PAGES) {
    await page.goto(BASE + path, { waitUntil: 'load' });
    // The deferred sheet is applied a couple of frames after load; wait for the
    // link to have flipped so the comparison sees the final document.
    await page
      .waitForFunction(() => (document.getElementById('late-css') as HTMLLinkElement | null)?.media === 'all', null, {
        timeout: 5000,
      })
      .catch(() => {});
    const sels = await liveSelectors(page);
    const set = new Set(sels);
    perPage.set(path || '/', set);
    for (const s of set) union.add(s);
  }

  const problems: string[] = [];
  for (const [path, set] of perPage) {
    const missing = [...union].filter((sel) => !set.has(sel));
    if (missing.length) {
      const sample = missing.slice(0, 12).join(', ');
      problems.push(`${path} is missing ${missing.length} selector(s): ${sample}${missing.length > 12 ? ', …' : ''}`);
    }
  }

  expect(
    problems,
    'a page lost CSS rules that other pages ship — the shared deferred sheet must contain every rule that is not inlined on EVERY page (see integrations/critical-css.mjs: the sheet is shared, the inline block is not)',
  ).toEqual([]);
});
