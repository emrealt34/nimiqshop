/**
 * The boot language hold — "the page must never be seen in the wrong
 * language".
 *
 * A static build serves English HTML to everyone; before this hold existed a
 * Turkish visitor watched the whole page flip from English to Turkish the
 * moment React hydrated (measured: English text at t≈60 ms, Turkish at
 * t≈500 ms). Base.astro now marks the document with `data-i18n-hold` before
 * paint and src/i18n removes it in the same commit as the visitor's own
 * strings — the body is not painted until then.
 *
 * What this spec locks down:
 *   1. a non-English visitor IS held (the attribute appears before React runs)
 *   2. the hold IS released once the dictionary is applied
 *   3. at the moment it lifts, the DOM is already translated — the first frame
 *      the visitor can see is not English (no flash, only a loader)
 *   4. an English visitor is never held at all (nothing to wait for)
 */
import { test, expect } from './support/fixtures';
import { path } from './support/data';

/** Records when the hold attribute appears/disappears, and the text at release. */
const WATCH_HOLD = () => {
  (window as any).__hold = { seen: false, released: false, textAtRelease: '' };
  const check = () => {
    const root = document.documentElement;
    if (!root) return;
    if (root.hasAttribute('data-i18n-hold')) {
      (window as any).__hold.seen = true;
    } else if ((window as any).__hold.seen) {
      (window as any).__hold.released = true;
      (window as any).__hold.textAtRelease = (document.body?.innerText || '').replace(/\s+/g, ' ').slice(0, 400);
    }
  };
  const wait = () => {
    if (!document.documentElement) return requestAnimationFrame(wait);
    check();
    try {
      new MutationObserver(check).observe(document.documentElement, {
        attributes: true,
        attributeFilter: ['data-i18n-hold'],
      });
    } catch { /* very old browser — the assertions below still read the final state */ }
    // The attribute can also be removed before the observer fires (identical
    // tick): keep polling for the first moments of the page's life.
    let n = 0;
    const iv = setInterval(() => { check(); if (++n > 200) clearInterval(iv); }, 20);
  };
  wait();
};

test.describe('non-English visitor', () => {
  test.use({ lang: 'tr' });

  test('page is held until the Turkish strings are in place, then released translated', async ({ page }) => {
    await page.addInitScript(WATCH_HOLD);
    await page.goto(path('/'), { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => (window as any).__hold.released, null, { timeout: 20_000 });

    const hold = await page.evaluate(() => (window as any).__hold);
    expect(hold.seen, 'non-English visitor is held before paint').toBe(true);
    expect(hold.released, 'hold is released once the dictionary lands').toBe(true);

    // The first visible frame is NOT English: the English hero copy never made
    // it into the text snapshot taken the moment the hold was lifted.
    expect(hold.textAtRelease).not.toContain('Spend NIM on gift cards');

    // And the page the visitor ends up with is fully Turkish + interactive.
    await expect(page.locator('#page-content')).toBeVisible();
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-i18n-hold'));
    expect(await page.getAttribute('html', 'lang')).toBe('tr');
    expect(await page.getAttribute('html', 'data-i18n-ready')).toBe('tr');
  });

  test('the stored language is not overwritten by the English boot render', async ({ page }) => {
    await page.addInitScript(WATCH_HOLD);
    await page.goto(path('/'), { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => (window as any).__hold.released, null, { timeout: 20_000 });
    const stored = await page.evaluate(() => ({
      ls: localStorage.getItem('nimshop.lang'),
      cookie: document.cookie.match(/nimshop-lang=([^;]+)/)?.[1],
    }));
    expect(stored.ls, 'localStorage keeps the visitor’s language').toBe('tr');
    expect(stored.cookie, 'the backend cookie keeps the visitor’s language').toBe('tr');
  });
});

/**
 * The dictionary cache made the hold obsolete for anyone who has visited in
 * this language before: src/i18n writes the dictionary into localStorage and
 * Base.astro skips the hold when it is there. That is the case the owner asked
 * for — no loader, nothing hidden — so it gets its own acceptance test:
 * nothing may be hidden on the second visit, and the first paintable frame
 * must still not be English (the cache has to be applied before paint, not
 * after).
 */
const WATCH_BOOT = () => {
  (window as any).__boot = [];
  (window as any).__heldEver = false;
  const sample = () => {
    const el = document.documentElement;
    if (!el) return;
    const held = el.hasAttribute('data-i18n-hold');
    if (held) (window as any).__heldEver = true;
    const snap = { lang: el.getAttribute('lang') || '', held, text: !!(document.body && document.body.firstElementChild) };
    const list = (window as any).__boot as unknown[];
    const last = list[list.length - 1] as { lang: string; held: boolean; text: boolean } | undefined;
    if (!last || last.lang !== snap.lang || last.held !== snap.held || last.text !== snap.text) list.push(snap);
  };
  const iv = setInterval(sample, 1);
  setTimeout(() => clearInterval(iv), 8000);
};

test.describe('returning visitor', () => {
  test.use({ lang: 'tr' });

  test('with a cached dictionary is never held, and still never sees English', async ({ page }) => {
    // First visit fills the cache (fresh context per test: nothing is stored yet).
    await page.goto(path('/'), { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.documentElement.getAttribute('data-i18n-ready') === 'tr', null, { timeout: 20_000 });
    expect(await page.evaluate(() => !!localStorage.getItem('nimshop.dict.tr')), 'the dictionary was cached').toBe(true);

    // addInitScript applies to every later navigation in this context.
    await page.addInitScript(WATCH_BOOT);
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.documentElement.getAttribute('data-i18n-ready') === 'tr', null, { timeout: 20_000 });

    const boot = await page.evaluate(() => ({ heldEver: (window as any).__heldEver, frames: (window as any).__boot }));
    expect(boot.heldEver, 'nothing may be hidden on a cached visit').toBe(false);
    const englishFrames = (boot.frames as { lang: string; held: boolean; text: boolean }[]).filter(
      (f) => f.text && !f.held && f.lang === 'en'
    );
    expect(englishFrames, 'no English frame may be paintable, even without the hold').toEqual([]);
    const labelled = [...new Set((boot.frames as { lang: string; text: boolean }[]).filter((f) => f.text).map((f) => f.lang))];
    expect(labelled, 'the markup was only ever labelled Turkish').toEqual(['tr']);
  });
});

test.describe('English visitor', () => {
  test.use({ lang: 'en' });

  test('is never held (English is what the server already rendered)', async ({ page }) => {
    await page.addInitScript(WATCH_HOLD);
    await page.goto(path('/'), { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.documentElement.hasAttribute('data-i18n-ready'), null, { timeout: 20_000 });
    const hold = await page.evaluate(() => (window as any).__hold);
    expect(hold.seen, 'no hold for English').toBe(false);
  });
});
