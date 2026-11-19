/**
 * Safe areas — nothing of the chrome may land under a notch or the home
 * indicator (`viewport-fit=cover` in Base.astro means those strips are really
 * available to the page; owner report 2026-11-19: the bottom bar ran into the
 * phone's safe area on the left and right when held sideways).
 *
 * The insets are simulated through CDP, because a desktop viewport has none.
 * Only Chromium can do that, so the whole file is Chromium-only.
 */
import { test, expect, type Page } from '@playwright/test';
import { LANGS, path } from './support/data';

type Insets = { top: number; bottom: number; left: number; right: number };

const PORTRAIT: Insets = { top: 47, bottom: 34, left: 0, right: 0 };
const LANDSCAPE: Insets = { top: 0, bottom: 21, left: 47, right: 47 };

async function applyInsets(page: Page, insets: Insets) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setSafeAreaInsetsOverride', { insets });
  // The insets are re-measured by lib/safeArea.ts on these events, which is
  // exactly what a rotation (or an address-bar collapse) does on a phone.
  await page.evaluate(() => {
    window.dispatchEvent(new Event('orientationchange'));
    window.dispatchEvent(new Event('resize'));
  });
  await page.waitForTimeout(150);
}

/** Boxes of the text users read inside the two fixed bars, plus the bar box. */
async function chromeBoxes(page: Page) {
  return page.evaluate(() => {
    const box = (el: Element) => {
      const r = el.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
    };
    const bar = document.querySelector<HTMLElement>('.tabbar');
    const inner = document.querySelector<HTMLElement>('.topbar-inner');
    const tabLabels = [...document.querySelectorAll<HTMLElement>('.tabbar a > span:last-child')];
    const topKids = inner ? [...inner.children].map(box).filter((b) => b.right > b.left) : [];
    return {
      bar: bar ? box(bar) : null,
      barText: tabLabels.length
        ? { left: Math.min(...tabLabels.map((l) => box(l).left)), right: Math.max(...tabLabels.map((l) => box(l).right)) }
        : null,
      topText: topKids.length
        ? { left: Math.min(...topKids.map((b) => b.left)), right: Math.max(...topKids.map((b) => b.right)) }
        : null,
      vars: ['--sat', '--sab', '--sal', '--sar'].map((v) => getComputedStyle(document.documentElement).getPropertyValue(v).trim()),
      docOverflow: document.documentElement.scrollWidth - window.innerWidth,
    };
  });
}

test.describe('safe areas', () => {
  test.skip(({ browserName }) => browserName !== 'chromium', 'CDP inset emulation is Chromium-only');

  test('landscape: bars, page text and skip link stay out of the notch', async ({ page }) => {
    await page.setViewportSize({ width: 852, height: 393 });
    await page.goto(path('/orders/'));
    await applyInsets(page, LANDSCAPE);
    const info = await chromeBoxes(page);
    // The insets reached the layout at all (lib/safeArea.ts).
    expect(info.vars, 'safe-area vars follow the emulated insets').toEqual(['0px', '21px', '47px', '47px']);
    const left = LANDSCAPE.left;
    const right = 852 - LANDSCAPE.right;
    expect(info.barText, 'tab labels inside the safe area').not.toBeNull();
    expect(info.barText!.left, 'first tab clears the notch').toBeGreaterThanOrEqual(left - 0.5);
    expect(info.barText!.right, 'last tab clears the notch').toBeLessThanOrEqual(right + 0.5);
    expect(info.topText!.left, 'header content clears the notch').toBeGreaterThanOrEqual(left - 0.5);
    expect(info.topText!.right, 'header content clears the notch').toBeLessThanOrEqual(right + 0.5);
    expect(info.docOverflow, 'no horizontal scroll').toBeLessThanOrEqual(0);

    const text = await page.evaluate(() => {
      const c = document.querySelector('#page-content .container');
      if (!c) return null;
      const r = c.getBoundingClientRect();
      return { left: r.left, right: r.right };
    });
    if (text) {
      // Cards may paint under the notch; their text may not.
      expect(text.left).toBeGreaterThanOrEqual(left - 0.5);
      expect(text.right).toBeLessThanOrEqual(right + 0.5);
    }

    // The skip link is the first thing a keyboard reaches.
    const skip = await page.evaluate(() => {
      const el = document.querySelector<HTMLElement>('.skip-link');
      if (!el) return null;
      const cs = getComputedStyle(el);
      return { top: parseFloat(cs.top), left: parseFloat(cs.left) };
    });
    if (skip) {
      expect(skip.left).toBeGreaterThanOrEqual(left - 0.5);
      expect(skip.top).toBeGreaterThanOrEqual(LANDSCAPE.top - 0.5);
    }
  });

  test('portrait: the bar sits on the safe-area edge, not under the home indicator', async ({ page }) => {
    await page.setViewportSize({ width: 393, height: 852 });
    await page.goto(path('/orders/'));
    await applyInsets(page, PORTRAIT);
    const info = await chromeBoxes(page);
    expect(info.vars).toEqual(['47px', '34px', '0px', '0px']);
    expect(info.bar, 'bar found').not.toBeNull();
    // The bar paints to the physical bottom edge (the strip below the tabs is
    // bar surface, not page), and its labels sit above the home indicator.
    expect(Math.abs(info.bar!.bottom - 852)).toBeLessThanOrEqual(1);
    const labelsBottom = await page.evaluate(() =>
      Math.max(...[...document.querySelectorAll<HTMLElement>('.tabbar a > span:last-child')].map((l) => l.getBoundingClientRect().bottom)),
    );
    expect(labelsBottom, 'labels above the home indicator').toBeLessThanOrEqual(852 - PORTRAIT.bottom + 0.5);
  });

  for (const lang of LANGS) {
    test(`narrow phone at 320px keeps six one-line tabs inside the safe area (${lang})`, async ({ page }) => {
      await page.setViewportSize({ width: 320, height: 700 });
      await page.goto(path(`/orders/?lang=${lang}`));
      await applyInsets(page, PORTRAIT);
      const info = await page.evaluate(() => {
        const labels = [...document.querySelectorAll<HTMLElement>('.tabbar a > span:last-child')];
        const bar = document.querySelector<HTMLElement>('.tabbar')!;
        return {
          lines: labels.map((l) => l.getClientRects().length),
          sizes: labels.map((l) => parseFloat(getComputedStyle(l).fontSize)),
          left: Math.min(...labels.map((l) => l.getBoundingClientRect().left)),
          right: Math.max(...labels.map((l) => l.getBoundingClientRect().right)),
          bar: bar.getBoundingClientRect().toJSON(),
          sab: parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--sab')) || 0,
        };
      });
      for (const n of info.lines) expect(n, 'one line per tab label').toBe(1);
      for (const s of info.sizes) expect(s, 'tab label px').toBeGreaterThanOrEqual(10);
      expect(info.left).toBeGreaterThanOrEqual(0);
      expect(info.right).toBeLessThanOrEqual(320);
      expect(Math.abs(info.bar.bottom - 700), 'bar on the bottom edge').toBeLessThanOrEqual(1);
      expect(info.sab, 'bottom inset applied').toBeGreaterThan(0);
    });
  }
});
