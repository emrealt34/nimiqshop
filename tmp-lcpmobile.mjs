import { chromium, devices } from 'playwright';
const EXE = '/home/user/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome';
const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox'] });
for (const [name, opts] of [['mobile 412x823', { viewport: { width: 412, height: 823 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1.75, userAgent: devices['Pixel 5'].userAgent }],
                            ['desktop 1366', { viewport: { width: 1366, height: 900 } }]]) {
  const ctx = await browser.newContext(opts);
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    window.__lcp = [];
    new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lcp.push({ t: Math.round(e.startTime), size: e.size, el: e.element ? (e.element.tagName + (e.element.className ? '.' + String(e.element.className).slice(0, 24) : '')) : e.url }); }).observe({ type: 'largest-contentful-paint', buffered: true });
  });
  await page.goto('https://shop.nimiqbase.com/', { waitUntil: 'load' });
  await page.waitForTimeout(5000);
  const out = await page.evaluate(() => ({
    lcp: window.__lcp,
    hero: (() => { const h = document.querySelector('section.hero h1') || document.querySelector('h1'); if (!h) return 'no h1'; const r = h.getBoundingClientRect(); const cs = getComputedStyle(h); return { rect: [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)], display: cs.display, visibility: cs.visibility, opacity: cs.opacity, font: cs.fontFamily.slice(0, 30), text: h.textContent.slice(0, 40) }; })(),
    islandDisplay: getComputedStyle(document.querySelector('astro-island')).display,
    contentVisibility: (() => { const el = document.querySelector('section.hero'); return el ? getComputedStyle(el).contentVisibility : 'none'; })(),
  }));
  console.log(`\n=== ${name}`);
  console.log('LCP entries:', JSON.stringify(out.lcp));
  console.log('hero h1:', JSON.stringify(out.hero));
  await ctx.close();
}
await browser.close();
