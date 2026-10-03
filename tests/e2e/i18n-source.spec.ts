/**
 * Where the STARTING language and market come from.
 *
 * Owner's call (2026-10-03): follow the SYSTEM language — never guess from the
 * visitor's IP. Inside Nimiq Pay the host injects the user's own app language
 * before any script runs, at `window.nimiqPay.language` (docs:
 * https://nimiq.dev/mini-apps/features/localization — the SDK's
 * getHostLanguage() is a one-line reader of exactly that field), so the host
 * outranks the device locale.
 *
 * The chains must stay identical in the pre-paint boot script (Base.astro) and
 * in detectLang() (src/i18n). These tests drive the real browser, so they cover
 * both paths at once — and every case checks that no English frame was ever
 * paintable in between (that flash is the one thing the owner asked us to kill
 * for good).
 */
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

/** A first-time visitor: nothing pinned by the fixture, nothing chosen yet. */
const FRESH = `
  try {
    localStorage.removeItem('nimshop.lang');
    localStorage.removeItem('nimshop.lang.user');
    localStorage.removeItem('nimshop_country');
  } catch (e) {}
  document.cookie = 'nimshop-lang=; Max-Age=0; path=/';
`;

/** Pin the device locale deterministically (don't rely on the emulation). */
const deviceLocale = (tag: string) => `
  Object.defineProperty(navigator, 'language', { get: function () { return '${tag}'; } });
  Object.defineProperty(navigator, 'languages', { get: function () { return ['${tag}']; } });
`;

const inPay = (lang: string) => `window.nimiqPay = { language: '${lang}' };`;

/**
 * Samples the document from the first parser tick: the language the visitor
 * WOULD have seen English in, and whether the pre-paint hold was up. A sample
 * with rendered markup, no hold and lang="en" is an English frame — the exact
 * flash this chain exists to prevent.
 */
const WATCH_BOOT = `
  window.__boot = [];
  (function () {
    function sample() {
      var e = document.documentElement;
      if (!e) return;
      var b = document.body;
      var s = {
        lang: e.getAttribute('lang') || '',
        held: e.hasAttribute('data-i18n-hold'),
        text: !!(b && b.firstElementChild),
      };
      var last = window.__boot[window.__boot.length - 1];
      if (!last || last.lang !== s.lang || last.held !== s.held || last.text !== s.text) window.__boot.push(s);
    }
    var iv = setInterval(sample, 1);
    setTimeout(function () { clearInterval(iv); }, 5000);
  })();
`;

async function expectNoEnglishFrame(page: import('@playwright/test').Page, expected: string) {
  const samples = await page.evaluate(() => (window as any).__boot as { lang: string; held: boolean; text: boolean }[]);
  const englishFrames = samples.filter((s) => s.text && !s.held && s.lang === 'en');
  expect(englishFrames, `no English frame may be paintable for a ${expected} visitor`).toEqual([]);
  const languages = [...new Set(samples.filter((s) => s.text).map((s) => s.lang))];
  expect(languages, 'the markup was only ever labelled with the visitor’s own language').toEqual([expected]);
}

const lang = (page: import('@playwright/test').Page) => page.getAttribute('html', 'lang');
const ready = (page: import('@playwright/test').Page) => page.getAttribute('html', 'data-i18n-ready');

test.describe('inside Nimiq Pay', () => {
  test('the host language beats the saved value and the device locale', async ({ page }) => {
    await page.addInitScript({ content: FRESH + WATCH_BOOT + deviceLocale('en-US') + inPay('de') });
    await open(page, path('/'));
    expect(await lang(page)).toBe('de');
    expect(await ready(page), 'the German dictionary actually applied').toBe('de');
    await expectNoEnglishFrame(page, 'de');
  });

  test('an "en" host falls through to a device language we ship (Nimiq Pay has no Turkish)', async ({ page }) => {
    await page.addInitScript({ content: FRESH + WATCH_BOOT + deviceLocale('tr-TR') + inPay('en') });
    await open(page, path('/'));
    expect(await lang(page)).toBe('tr');
    await expectNoEnglishFrame(page, 'tr');
  });

  test('?lang= still wins over the host (a deliberately shared link)', async ({ page }) => {
    await page.addInitScript({ content: FRESH + WATCH_BOOT + deviceLocale('en-US') + inPay('de') });
    await open(page, path('/') + '?lang=fr');
    expect(await lang(page)).toBe('fr');
    await expectNoEnglishFrame(page, 'fr');
  });

  test('the visitor’s own switcher pick survives a different host language', async ({ page }) => {
    await page.addInitScript({ content: FRESH + WATCH_BOOT + deviceLocale('en-US') + inPay('de') + "localStorage.setItem('nimshop.lang.user', 'es');" });
    await open(page, path('/'));
    expect(await lang(page)).toBe('es');
    await expectNoEnglishFrame(page, 'es');
  });
});

test.describe('the visitor’s own pick', () => {
  test('travels to another open tab', async ({ context, page }) => {
    await page.addInitScript({ content: WATCH_BOOT });
    const other = await context.newPage();
    await open(page, path('/'));
    await open(other, path('/'));
    await expect.poll(() => ready(other), { message: 'the second tab hydrated' }).toBe('en');

    // Drive the real switcher: open the globe, pick Türkçe.
    await page.click('.lang-toggle');
    await page.click('button[lang="tr"]');
    await expect.poll(() => ready(page)).toBe('tr');

    // The already-open tab follows through the storage event (there is no
    // reload and no shared memory between documents).
    await expect.poll(() => ready(other), { timeout: 10_000 }).toBe('tr');
  });
});

test.describe('outside Nimiq Pay', () => {
  test('the device language is the default (tr-TR → Turkish)', async ({ page }) => {
    await page.addInitScript({ content: FRESH + WATCH_BOOT + deviceLocale('tr-TR') });
    await open(page, path('/'));
    expect(await lang(page)).toBe('tr');
    expect(await ready(page)).toBe('tr');
    await expectNoEnglishFrame(page, 'tr');
  });

  test('the market follows the device region, and no IP lookup happens', async ({ page, requests }) => {
    await page.addInitScript({ content: FRESH + deviceLocale('de-DE') });
    const shelfFiles: string[] = [];
    page.on('request', (r) => { const u = r.url(); if (u.includes('/data/catalog/brands/')) shelfFiles.push(u); });
    await open(page, path('/'));
    // de-DE is a market we serve: the German shelf snapshot is what loads…
    await expect.poll(() => shelfFiles.some((u) => u.endsWith('/brands/DE.json'))).toBe(true);
    // …and the IP-based country suggestion is gone for good.
    expect(requests.filter((r) => r.includes('/geo')), 'no /api/geo call').toEqual([]);
  });
});
