/**
 * Crash tests: every screen in every language with the full crash guard,
 * then a monkey pass that hammers every control twice in a row with no waits.
 */
import { test, expect, open, contentText } from './support/fixtures';
import { BASE, LANGS, SCREENS, NOT_FOUND_MARK, RAW_KEY_RE, path } from './support/data';

test.describe('every screen × every language loads without errors @smoke', () => {
  for (const lang of LANGS) {
    test.describe(lang, () => {
      test.use({ lang });
      for (const screen of SCREENS) {
        test(screen.name, async ({ page }) => {
          await open(page, path(screen.url));
          const text = await contentText(page);
          expect(text.length).toBeGreaterThan(10);
          if (screen.name !== 'not-found') expect(text).not.toMatch(NOT_FOUND_MARK);
          expect((await page.locator('body').innerText()).match(RAW_KEY_RE) || [], 'raw i18n keys on screen').toEqual([]);
        });
      }
    });
  }
});

test.describe('monkey: double-press every control, no waits', () => {
  test.describe.configure({ timeout: 60_000 });
  for (const screen of SCREENS.filter((s) => s.name !== 'not-found')) {
    test(screen.name, async ({ page, context }) => {
      context.on('page', (p) => { if (p !== page) p.close().catch(() => {}); });
      await open(page, path(screen.url));
      const count = Math.min(await page.locator('button:visible').count(), 40);
      for (let i = 0; i < count; i++) {
        const btn = page.locator('button:visible').nth(i);
        if (!(await btn.count()) || (await btn.isDisabled().catch(() => true))) continue;
        await btn.click({ timeout: 1000, noWaitAfter: true }).catch(() => {});
        await btn.click({ timeout: 500, noWaitAfter: true }).catch(() => {});
        await page.keyboard.press('Escape').catch(() => {});
        const u = new URL(page.url());
        expect(u.pathname.startsWith(BASE), `control #${i} left the site: ${page.url()}`).toBe(true);
        if (u.pathname + u.search !== path(screen.url)) await open(page, path(screen.url));
      }
      await page.waitForTimeout(300);
      expect(await contentText(page)).not.toMatch(NOT_FOUND_MARK);
    });
  }
});
