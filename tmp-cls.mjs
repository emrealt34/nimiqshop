import { chromium } from 'playwright';
const EXE = '/home/user/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome';
const port = process.argv[2] || '8819';
const N = Number(process.argv[3] || 3);
const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox'] });
for (let i = 0; i < N; i++) {
  const ctx = await browser.newContext({ viewport: { width: 412, height: 915 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  const cdp = await ctx.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 150, downloadThroughput: 1.6 * 1024 * 1024 / 8, uploadThroughput: 750 * 1024 / 8 });
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  await page.addInitScript(() => {
    window.__s = [];
    const nm = (el) => { if (!el) return '?'; const c = typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/)[0] : ''; return el.tagName + (el.id ? '#' + el.id : '') + c; };
    new PerformanceObserver((l) => { for (const e of l.getEntries()) { if (e.hadRecentInput) continue; window.__s.push({ t: Math.round(e.startTime), v: +e.value.toFixed(4), src: (e.sources || []).map((s) => `${nm(s.node)} [y ${Math.round(s.previousRect.y)}→${Math.round(s.currentRect.y)}]`) }); } }).observe({ type: 'layout-shift', buffered: true });
  });
  await page.goto(`http://127.0.0.1:${port}/index.html`, { waitUntil: 'load' });
  await page.waitForTimeout(5000);
  const s = await page.evaluate(() => window.__s);
  const total = s.reduce((a, x) => a + x.v, 0);
  console.log(`run ${i + 1}: CLS=${total.toFixed(4)}  shifts=${s.length}`);
  for (const x of s) console.log(`   ${String(x.t).padStart(5)}ms v=${x.v}  ${x.src.join(' | ')}`);
  await ctx.close();
}
await browser.close();
