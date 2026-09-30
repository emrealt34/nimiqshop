import { chromium, devices } from 'playwright';
const EXE = '/home/user/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome';
const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox'] });
for (const [tag, url] of [['local-v11', 'http://127.0.0.1:8834/index.html'], ['live', 'https://shop.nimiqbase.com/']]) {
  const ctx = await browser.newContext({ viewport: { width: 412, height: 823 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1.75, userAgent: devices['Pixel 5'].userAgent });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    window.__lcp = [];
    new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lcp.push({ t: Math.round(e.startTime), size: e.size, el: e.element ? e.element.tagName : e.url }); }).observe({ type: 'largest-contentful-paint', buffered: true });
  });
  await page.goto(url, { waitUntil: 'load' });
  await page.waitForTimeout(3500);
  const r = await page.evaluate(() => ({ scrollY: window.scrollY, lcp: window.__lcp, heroTop: document.querySelector('section.hero')?.getBoundingClientRect().top }));
  console.log(`${tag}: scrollY=${r.scrollY} heroTop=${r.heroTop} LCP=${JSON.stringify(r.lcp)}`);
  await ctx.close();
}
await browser.close();
