import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

test.describe('browser shell geometry @smoke', () => {
  test.use({ api: { authed: false }, lang: 'tr' });

  for (const width of [320, 390, 768, 1024, 1280]) {
    test(`one header offset and one tabbar reservation at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 });
      await open(page, path('/support'));
      const top = await page.evaluate(() => {
        const header = document.querySelector('.topbar')!.getBoundingClientRect();
        const content = document.querySelector('#page-content')!.getBoundingClientRect();
        return content.top - header.bottom;
      });
      expect(top).toBeGreaterThanOrEqual(0); expect(top).toBeLessThanOrEqual(8);
      const checkBottom = async () => {
        await page.evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }));
        await expect.poll(() => page.evaluate(() => {
          const footer = document.querySelector('.footer')!;
          const bar = document.querySelector('.tabbar')!;
          const visible = getComputedStyle(bar).display !== 'none';
          const bottom = visible ? bar.getBoundingClientRect().top : window.innerHeight;
          return Math.abs(footer.getBoundingClientRect().bottom - bottom);
        })).toBeLessThanOrEqual(2);
      };
      await checkBottom();
      if (width < 1200) {
        // Emulate nonzero safe-area padding: reserve the measured height once,
        // not header+inset twice or footer margin+body padding together.
        await page.addStyleTag({ content: '.topbar{padding-top:24px!important}.tabbar{padding-bottom:34px!important;min-height:94px!important}' });
        await expect.poll(() => page.evaluate(() => {
          const bar = document.querySelector('.tabbar')!;
          return Math.abs(parseFloat(getComputedStyle(document.body).paddingBottom) - bar.getBoundingClientRect().height);
        })).toBeLessThanOrEqual(1);
        await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }));
        await expect.poll(() => page.evaluate(() => document.querySelector('#page-content')!.getBoundingClientRect().top - document.querySelector('.topbar')!.getBoundingClientRect().bottom)).toBeLessThanOrEqual(8);
        await checkBottom();
      }
    });
  }

  test('browser toolbar height changes do not programmatically jump a near-bottom reader', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await open(page, path('/support'));
    // Keep a deterministic long document without depending on the live catalog.
    await page.evaluate(() => {
      document.querySelector('#page-content')!.insertAdjacentHTML('beforeend', '<div style="height:2200px"></div>');
      window.scrollTo({ top: document.documentElement.scrollHeight - window.innerHeight - 80, behavior: 'instant' });
    });
    const before = await page.evaluate(() => window.scrollY);
    await page.evaluate(() => {
      (window as any).__resizeScrolls = 0;
      const original = window.scrollTo.bind(window);
      window.scrollTo = ((...args: any[]) => { (window as any).__resizeScrolls++; return (original as any)(...args); }) as typeof window.scrollTo;
    });
    await page.setViewportSize({ width: 390, height: 720 });
    await page.evaluate(() => window.visualViewport?.dispatchEvent(new Event('resize')));
    // Two animation frames allow ResizeObserver/layout callbacks to settle.
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    expect(await page.evaluate(() => (window as any).__resizeScrolls)).toBe(0);
    expect(Math.abs((await page.evaluate(() => window.scrollY)) - before)).toBeLessThanOrEqual(2);
  });
});
