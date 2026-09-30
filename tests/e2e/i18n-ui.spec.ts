/**
 * Hard-coded English on screen.
 *
 * Each screen is rendered in English and then in every other language. Any
 * sentence that is IDENTICAL in both (and is not a brand name / data / an
 * allow-listed term) is text that never went through t() — the build fails.
 * Allow-list real exceptions in tests/e2e/i18n-allowlist.json.
 */
import { test, expect, open } from './support/fixtures';
import { LANGS, SCREENS, path } from './support/data';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const ALLOW: { exact: string[]; patterns: string[] } = JSON.parse(readFileSync(fileURLToPath(new URL('./i18n-allowlist.json', import.meta.url)), 'utf8'));
const allowRe = ALLOW.patterns.map((p) => new RegExp(p, 'i'));

async function sentences(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const out = new Set<string>();
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const el = n.parentElement!;
      if (!el || el.closest('script, style, noscript, code, pre, [data-i18n-skip], [translate="no"]')) continue;
      if (!el.getClientRects().length || getComputedStyle(el).visibility === 'hidden') continue;
      const t = n.textContent!.replace(/\s+/g, ' ').trim();
      // a "sentence": ≥2 words with ≥3 letters each
      if ((t.match(/\p{L}{3,}/gu) || []).length >= 2) out.add(t);
    }
    document.querySelectorAll('[aria-label], [title], [placeholder], img[alt]').forEach((e) => {
      for (const a of ['aria-label', 'title', 'placeholder', 'alt']) {
        const t = (e.getAttribute(a) || '').replace(/\s+/g, ' ').trim();
        if ((t.match(/\p{L}{3,}/gu) || []).length >= 2) out.add(`[${a}] ${t}`);
      }
    });
    return [...out];
  });
}

// /admin is the operator console: an internal tool written in English by
// design (AdminPage has no t() at all). Everything customer-facing is checked.
const OPERATOR_ONLY = new Set(['admin']);

// /support is a deliberately English FAQ: it quotes the supplier's code,
// refund and replacement policy verbatim (CryptoRefills is the merchant of
// record), and a mistranslation of that policy is worse than English. Its
// text is still checked in the language it ships in — the crash, links and
// responsive specs all visit it in every language.
const ENGLISH_ONLY = new Set(['support']);

test.describe.configure({ timeout: 90_000 });

for (const screen of SCREENS.filter((s) => !OPERATOR_ONLY.has(s.name))) {
  test(`${screen.name}: no untranslated hard-coded text`, async ({ page }) => {
    test.skip(ENGLISH_ONLY.has(screen.name), 'English-only screen by design (see ENGLISH_ONLY)');
    await page.setViewportSize({ width: 1280, height: 900 });
    await open(page, path(screen.url));
    const en = new Set(await sentences(page));
    const problems: string[] = [];
    for (const lang of LANGS.filter((l) => l !== 'en')) {
      await page.evaluate((l) => sessionStorage.setItem('e2e.lang', l), lang);
      await open(page, path(screen.url));
      expect(await page.getAttribute('html', 'lang'), 'html[lang] follows the language').toMatch(new RegExp('^' + lang, 'i'));
      // Non-English dictionaries arrive as their own chunk; wait until the
      // bundle actually applied them (src/i18n sets data-i18n-ready then).
      await page.waitForFunction((l) => document.documentElement.getAttribute('data-i18n-ready') === l, lang);
      for (const s of await sentences(page)) {
        if (!en.has(s)) continue;
        const bare = s.replace(/^\[[a-z-]+\] /, '');
        if (ALLOW.exact.includes(bare) || allowRe.some((r) => r.test(bare))) continue;
        problems.push(`${lang}: "${s}"`);
      }
    }
    if (problems.length) await test.info().attach('untranslated.txt', { body: problems.join('\n'), contentType: 'text/plain' });
    expect(problems, 'English text shown unchanged in other languages (missing t())').toEqual([]);
  });
}
