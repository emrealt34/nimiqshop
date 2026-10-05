/**
 * Theme default (owner, 2026-10-05: "default beyaz tema gelmedi adam
 * seçmediyse").
 *
 * The rule is: a visitor who has never touched the toggle gets the WHITE site,
 * even when their device asks for dark. Dark mode is opt-in, and once chosen
 * it sticks.
 *
 * These tests build their own browser contexts on purpose: the shared `page`
 * fixture pins `nimshop.theme` before every navigation (so screenshots are
 * reproducible) — i.e. it always looks like a visitor who ALREADY chose light.
 */
import { test, expect, mockApi, DEFAULT_API } from './support/fixtures';
import { path } from './support/data';

/** A visitor-shaped context: no app state except the theme we hand it. */
async function freshContext(browser: any, colorScheme: 'dark' | 'light', theme: string | null) {
  const ctx = await browser.newContext({ colorScheme, viewport: { width: 1280, height: 800 } });
  await ctx.addInitScript(
    ({ theme }: { theme: string | null }) => {
      try {
        if (theme === null) localStorage.removeItem('nimshop.theme');
        else localStorage.setItem('nimshop.theme', theme);
        // The language bootstrap outranks localStorage with a cookie; pin it so
        // a non-English host machine cannot hold the page behind the boot loader.
        document.cookie = 'nimshop-lang=en; path=/; SameSite=Lax';
        localStorage.setItem('nimshop.lang', 'en');
      } catch { /* storage disabled */ }
      (window as any).open = () => null;
    },
    { theme },
  );
  await mockApi(ctx, { ...DEFAULT_API });
  return ctx;
}

/** What the browser itself sees: the painted theme and the browser-UI hints. */
async function themeOf(page: any) {
  return page.evaluate(() => ({
    attr: document.documentElement.getAttribute('data-theme'),
    colorScheme: document.documentElement.style.colorScheme,
    meta: document.querySelector('meta[name="theme-color"]')?.getAttribute('content') || '',
    stored: localStorage.getItem('nimshop.theme'),
  }));
}

test.describe('theme default', () => {
  test('a dark-mode device with no saved choice still gets the white site @smoke', async ({ browser }) => {
    const ctx = await freshContext(browser, 'dark', null);
    const page = await ctx.newPage();
    await page.goto(path('/'), { waitUntil: 'load' });

    const t = await themeOf(page);
    expect(t.attr, 'data-theme without a saved choice').toBe('light');
    expect(t.colorScheme, 'color-scheme without a saved choice').toBe('light');
    expect(t.meta, 'browser theme-color').toBe('#E7DAC0');
    // Nothing was chosen, so nothing is written: the default stays the default
    // until the visitor actually flips the toggle.
    expect(t.stored, 'no theme is written just by visiting').toBeNull();
    await ctx.close();
  });

  test('a saved dark choice still wins, even on a light-mode device', async ({ browser }) => {
    const ctx = await freshContext(browser, 'light', 'dark');
    const page = await ctx.newPage();
    await page.goto(path('/'), { waitUntil: 'load' });

    const t = await themeOf(page);
    expect(t.attr, 'data-theme with a saved choice').toBe('dark');
    expect(t.colorScheme).toBe('dark');
    expect(t.meta, 'browser theme-color').toBe('#1a1c20');
    await ctx.close();
  });
});
