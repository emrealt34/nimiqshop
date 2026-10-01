/**
 * test-storefront-ui.mjs — consolidated storefront regression probe.
 *
 * Runs against the built dist/ served by scripts/static-server.mjs (:8085).
 * Lives in the repo (not /tmp) so a sandbox reset can never lose it again.
 * Covers the guarantees previous passes fought for:
 *   1. build-time shelf seed: real cards in the DOM at commit, no skeleton
 *   2. hydration clean
 *   3. uniform card geometry, stable over time (no late layout states);
 *      meta stack = country chip row + single-line clamped price row
 *   4. e-money KYC stamp on the right cards only
 *   5. country switch refills without wasting a default-country fetch
 *   6. cashback calculator works with the backend DOWN: no retry wall,
 *      100K→1B range, total-rate row, degraded note spacing
 *
 * Usage: node scripts/static-server.mjs &  then  node scripts/test-storefront-ui.mjs
 */
import { chromium } from 'playwright-core';

const BASE = process.env.BASE || 'http://127.0.0.1:8085';
let fails = 0;
const check = (n, ok, d = '') => {
  if (!ok) fails++;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? '  — ' + d : ''}`);
};

const browser = await chromium.launch({ args: ['--no-sandbox'] });

/* ------------------------------------------------ 1-3) home grid */
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'tr-TR' });
  const page = await ctx.newPage();
  const consoleErrors = [];
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 120)); });
  const reqs = [];
  page.on('request', (r) => reqs.push(r.url()));

  await page.goto(BASE + '/', { waitUntil: 'commit' });
  const atCommit = await page.evaluate(() => document.querySelectorAll('a.product-card').length).catch(() => -1);
  check('1 seed cards in DOM at commit', atCommit >= 20, `${atCommit} cards`);

  await page.waitForLoadState('load');
  await page.waitForFunction(() => document.documentElement.getAttribute('data-app-ready') === '1', null, { timeout: 20000 }).catch(() => {});
  await page.waitForTimeout(2500);

  const cards = await page.locator('a.product-card').count();
  const skeletons = await page.locator('.skeleton-card').count();
  check('2 full list rendered, no skeleton', cards >= 20 && skeletons === 0, `cards=${cards} skeletons=${skeletons}`);

  const geo = await page.evaluate(() => {
    const els = [...document.querySelectorAll('a.product-card')];
    const heights = [...new Set(els.map((e) => Math.round(e.getBoundingClientRect().height)))];
    const metas = els.map((e) => {
      const meta = e.querySelector('.p-meta');
      const price = e.querySelector('.p-price');
      const cs = price ? getComputedStyle(price) : null;
      return {
        rows: meta ? meta.children.length : 0,
        priceOneLine: price ? price.getBoundingClientRect().height < parseFloat(cs.lineHeight) * 1.6 : false,
      };
    });
    return {
      heights,
      allTwoRowMeta: metas.every((m) => m.rows === 2),
      allOneLinePrice: metas.every((m) => m.priceOneLine),
    };
  });
  check('3 uniform card heights', geo.heights.length === 1, `heights=${geo.heights.join(',')}`);
  check('3b meta = chip row + one-line price on every card', geo.allTwoRowMeta && geo.allOneLinePrice, '');

  const geo2 = await page.evaluate(() => [...new Set([...document.querySelectorAll('a.product-card')].map((e) => Math.round(e.getBoundingClientRect().height)))]);
  check('3c geometry stable over time', JSON.stringify(geo2) === JSON.stringify(geo.heights), geo2.join(','));

  /* ---------------------------------------------- 4) KYC stamp */
  const kyc = await page.evaluate(() => {
    const cardsEls = [...document.querySelectorAll('a.product-card')];
    const em = cardsEls.find((c) => /Rewarble VISA USD|CashtoCode|CASHlib|bitsa|flexepin|Netease/i.test(c.querySelector('.p-name')?.textContent || ''));
    const plain = cardsEls.find((c) => (c.querySelector('.p-name')?.textContent || '').trim() === 'Amazon.com.tr');
    return {
      emBadge: em ? (em.querySelector('.kyc-badge')?.textContent || '').trim() : null,
      plainBadge: plain ? !!plain.querySelector('.kyc-badge') : null,
    };
  });
  check('4 KYC stamp on e-money only', /KYC/i.test(kyc.emBadge || '') && kyc.plainBadge === false, `em="${kyc.emBadge}" plain=${kyc.plainBadge}`);

  /* ------------------------------------- 5) country switch waste */
  reqs.length = 0;
  await page.click('.btn-country');
  const idx = reqs.length;
  await page.locator('#countryPick >> text=/Almanya|Germany/').first().click();
  await page.waitForFunction(() => {
    const chips = [...document.querySelectorAll('a.product-card .chip-txt')];
    return chips.length > 0 && chips.every((c) => /Almanya|Germany/.test(c.textContent || ''));
  }, null, { timeout: 20000 });
  await page.waitForTimeout(1000);
  const settled = reqs.length;
  await page.waitForTimeout(1000);
  const wasted = reqs.slice(settled).filter((u) => u.includes('/data/catalog/brands/TR.json') || u.includes('country=TR'));
  check('5 DE switch clean, no settled-state TR waste', wasted.length === 0 && reqs.slice(idx).some((u) => u.includes('/data/catalog/brands/DE.json')), wasted[0] || 'clean');

  const hydration = consoleErrors.filter((t) => /hydrat/i.test(t));
  check('2b no hydration errors', hydration.length === 0, hydration[0] || '');
  await ctx.close();
}

/* --------------------------------- 6) cashback calculator, backend down */
{
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'tr-TR' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/cashback', { waitUntil: 'load' });
  await page.waitForFunction(() => document.documentElement.getAttribute('data-app-ready') === '1', null, { timeout: 20000 }).catch(() => {});
  await page.waitForTimeout(2500);
  check('6 calculator renders with backend down', (await page.locator('.cb-calc').count()) === 1);
  check('6b no retry wall', (await page.getByRole('button', { name: /Tekrar dene/ }).count()) === 0);
  const note = await page.locator('[role="status"]').first().innerText().catch(() => '');
  const noteMargin = await page.locator('[role="status"]').first().evaluate((el) => getComputedStyle(el).marginTop).catch(() => '');
  check('6c degraded note + 16px top room', /sunucusuna|unreachable/i.test(note) && noteMargin === '16px', `margin-top=${noteMargin}`);
  const defVal = await page.locator('.cb-slider-input').first().inputValue();
  check('6d default stake 100,000', defVal.replace(/[^0-9]/g, '') === '100000', defVal);
  const kv = await page.locator('.cb-calc-kv').innerText();
  check('6e total cashback % row', /Toplam cashback oranı/i.test(kv) && /%/.test(kv));
  await ctx.close();
}

/* ------------------------------------------------ 7) come-back banner rail */
{
  for (const [w, h] of [[390, 844], [1280, 900]]) {
    const ctx = await browser.newContext({ viewport: { width: w, height: h } });
    const page = await ctx.newPage();
    await page.goto(BASE + '/', { waitUntil: 'load' });
    await page.waitForFunction(() => document.documentElement.getAttribute('data-app-ready') === '1', null, { timeout: 20000 }).catch(() => {});
    await page.waitForTimeout(800);
    const m = await page.locator('.come-back').first().evaluate((el) => {
      const svg = el.querySelector('svg');
      const txt = el.querySelector('.come-back-txt');
      const cta = el.querySelector('.come-back-cta');
      if (!svg || !txt || !cta) return null;
      const r = (e) => e.getBoundingClientRect();
      const mid = (e) => (r(e).top + r(e).bottom) / 2;
      const ctaCs = getComputedStyle(cta);
      return {
        beside: r(svg).right <= r(txt).left + 2 && r(cta).left >= r(txt).right - 2,
        ctaOneLine: r(cta).height <= parseFloat(ctaCs.lineHeight || '16') * 2.2,
        height: Math.round(r(el).height),
        verticallyCentered: Math.abs(mid(svg) - mid(el)) < r(el).height / 2 && Math.abs(mid(cta) - mid(el)) < r(el).height / 2,
      };
    });
    check(`7 banner rail side-by-side @${w}`, !!m && m.beside && m.ctaOneLine && m.verticallyCentered && m.height <= 110, m ? JSON.stringify(m) : 'missing');
    await ctx.close();
  }
}

/* ------------------------------------------------ 8) topbar nav centering */
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await page.goto(BASE + '/', { waitUntil: 'load' });
  await page.waitForFunction(() => document.documentElement.getAttribute('data-app-ready') === '1', null, { timeout: 20000 }).catch(() => {});
  const m = await page.evaluate(() => {
    const nav = document.querySelector('.mainnav');
    const inner = document.querySelector('.topbar-inner');
    const brand = inner.firstElementChild;
    const controls = document.querySelector('.topbar-spacer')?.nextElementSibling;
    if (!nav || !brand || !controls) return null;
    const nb = nav.getBoundingClientRect();
    const bb = brand.getBoundingClientRect();
    const cb = controls.getBoundingClientRect();
    const ib = inner.getBoundingClientRect();
    return {
      offCenter: +((nb.left + nb.width / 2) - (ib.left + ib.width / 2)).toFixed(1),
      clearLeft: nb.left > bb.right,
      clearRight: nb.right < cb.left,
    };
  });
  check('8 nav optically centered in the bar', !!m && Math.abs(m.offCenter) <= 2, m ? `off-center ${m.offCenter}px` : 'missing nodes');
  check('8b nav clears brand and controls at 1280', !!m && m.clearLeft && m.clearRight, '');
  await page.setViewportSize({ width: 1200, height: 900 });
  await page.waitForTimeout(400);
  const m2 = await page.evaluate(() => {
    const nav = document.querySelector('.mainnav');
    const inner = document.querySelector('.topbar-inner');
    const brand = inner.firstElementChild;
    const controls = document.querySelector('.topbar-spacer')?.nextElementSibling;
    const nb = nav.getBoundingClientRect();
    const bb = brand.getBoundingClientRect();
    const cb = controls.getBoundingClientRect();
    return { clearLeft: nb.left > bb.right, clearRight: nb.right < cb.left };
  });
  check('8c no overlap at the 1200px breakpoint', m2.clearLeft && m2.clearRight, '');
  await ctx.close();
}

await browser.close();
console.log(fails === 0 ? '\nSTOREFRONT UI: ALL CHECKS PASSED' : `\nSTOREFRONT UI: ${fails} FAILURES`);
process.exit(fails ? 1 : 0);
