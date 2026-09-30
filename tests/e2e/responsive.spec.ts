/**
 * RESPONSIVE DEEP SCAN
 *
 * For every screen × every language × every device class (desktop / tablet /
 * phone) this test:
 *   1. loads the screen once, then sweeps every viewport size of the class,
 *   2. at each size audits every visible element (support/scan.ts: sideways
 *      scroll, cut-off content, truncated/clamped/overlapping text, tiny
 *      fonts, tiny tap targets, overlapping controls, broken or distorted
 *      images, fallback identicons, raw i18n keys),
 *   3. takes a full-page screenshot,
 *   4. presses every button / menu / tab / toggle on the screen one by one,
 *      audits + screenshots each opened state, and restores the page.
 * Any finding fails the test. The crash guard (fixtures.ts) fails it on any
 * exception or console.error on the way.
 *
 * Step 4 (pressing every control) is what costs the time — about 80% of a
 * run — and the controls behave the same in every language, so by default it
 * runs in English only; the other languages still get the full audit and
 * screenshot at every size, which is where translations actually differ
 * (overflow, truncation, clipped labels). SCAN_EXPLORE_LANGS=all restores the
 * exhaustive sweep, or name the languages: SCAN_EXPLORE_LANGS=en,de.
 *
 * Screenshots: screenshots/<desktop|tablet|phone>/<screen>/<lang>/<W>x<H>.jpg
 * Run one class locally:  npx playwright test --project=phone
 */
import { test, expect, open } from './support/fixtures';
import { DEVICES, LANGS, SCREENS, RAW_KEY_RE, NOT_FOUND_MARK, path, type DeviceClass } from './support/data';
import { audit, explore, shoot, writeIssues, formatIssues, AUDIT_DEFAULTS, type Issue } from './support/scan';

// SCAN_EXPLORE=0 → audit + screenshots only (quick local triage)
const EXPLORE = process.env.SCAN_EXPLORE !== '0';
// Languages whose controls are pressed one by one (see header); 'all' = every language.
const EXPLORE_LANGS = (process.env.SCAN_EXPLORE_LANGS || 'en').split(',').map((l) => l.trim());
const exploreIn = (lang: string) => EXPLORE && (EXPLORE_LANGS.includes('all') || EXPLORE_LANGS.includes(lang));

for (const screen of SCREENS) {
  for (const lang of LANGS) {
    test.describe(`${screen.name} · ${lang}`, () => {
      test.use({ lang });

      // Per-test budget: see the desktop/tablet/phone project timeouts in
      // playwright.config.ts (45 s + 40 s per viewport size of the class).
      test(`responsive deep scan`, async ({ page }, testInfo) => {
        const cls = (testInfo.project.metadata?.device || 'desktop') as DeviceClass;
        const sizes = DEVICES[cls];
        const auditOpts = AUDIT_DEFAULTS[cls];

        const url = path(screen.url);
        await page.setViewportSize(sizes[0]);
        await open(page, url);
        if (screen.name !== 'not-found') expect(await page.locator('#page-content').innerText(), 'renders the page, not the 404 screen').not.toMatch(NOT_FOUND_MARK);

        const failures: string[] = [];
        for (const size of sizes) {
          const tag = `${size.width}x${size.height}`;
          await test.step(`${cls} ${tag}`, async () => {
            await page.setViewportSize(size);
            // fresh load at this size (catches first-paint-only layout bugs and
            // resets anything the previous size's exploration toggled)
            if (size !== sizes[0]) await open(page, url);
            await page.waitForTimeout(250); // media queries + runtime fitText re-flow
            await page.evaluate(() => window.scrollTo(0, 0));

            const issues: Issue[] = await audit(page, auditOpts, RAW_KEY_RE);
            await shoot(page, cls, screen.name, lang, tag);
            if (issues.length) failures.push(`${tag} (page):\n${formatIssues(issues)}`);

            const explored = !exploreIn(lang) ? [] : await explore(page, {
              auditOpts, rawKeyRe: RAW_KEY_RE,
              reload: async () => { await open(page, url); await page.setViewportSize(size); await page.waitForTimeout(150); },
              shotBase: { device: cls, screen: screen.name, lang, size: tag },
            });
            for (const e of explored) if (e.issues.length) failures.push(`${tag} after pressing "${e.label}":\n${formatIssues(e.issues)}`);
            testInfo.annotations.push({ type: tag, description: `${explored.length} controls pressed, ${explored.filter((e) => e.opened).length} layers opened` });
          });
        }

        writeIssues(cls, screen.name, lang, failures.join('\n\n'));
        if (failures.length) await testInfo.attach('responsive-issues.txt', { body: failures.join('\n\n'), contentType: 'text/plain' });
        expect(failures, `responsive problems on ${screen.name} (${lang}, ${cls})`).toEqual([]);
      });
    });
  }
}
