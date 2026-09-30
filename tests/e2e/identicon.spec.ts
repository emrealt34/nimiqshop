/**
 * Nimiq identicons must be the REAL generated avatars (@nimiq/identicons,
 * vendored in public/vendor) — never a grey placeholder / fallback. This
 * breaks when /vendor/ is loaded without the /nimiqshop/ base.
 */
import { test, expect, open, ADDRESS } from './support/fixtures';
import { path } from './support/data';

test.describe('real Nimiq identicons @smoke', () => {
  test('the vendored identicon library loads from under the base path', async ({ page }) => {
    const loaded: string[] = [];
    page.on('response', (r) => { if (/\/vendor\/identicons/.test(r.url())) loaded.push(`${r.status()} ${new URL(r.url()).pathname}`); });
    await open(page, path('/plant-trees'));
    await expect.poll(() => loaded.length, { timeout: 8000 }).toBeGreaterThan(0);
    for (const l of loaded) expect(l).toMatch(/^200 \/nimiqshop\/vendor\//);
  });

  for (const route of ['/plant-trees', '/profile', '/']) {
    test(`${route}: every identicon on screen is a generated Nimiq identicon`, async ({ page }) => {
      await open(page, path(route));
      await page.waitForTimeout(1500);
      const found = await page.evaluate(() => [...document.querySelectorAll('img')].filter((i) => /identicon/i.test(i.className + ' ' + (i.parentElement?.className || '') + ' ' + i.alt) || i.src.startsWith('data:image/svg+xml')).map((i) => {
        let svg = '';
        try { svg = i.src.includes(';base64,') ? atob(i.src.split(',')[1]) : decodeURIComponent(i.src.split(',')[1] || ''); } catch {}
        return { cls: i.className, src: i.src.slice(0, 40), svgLen: svg.length, hasSvg: /<svg/i.test(svg), w: i.getBoundingClientRect().width, visible: i.getClientRects().length > 0 };
      }));
      const shown = found.filter((f) => f.visible && f.w > 8);
      if (route !== '/') expect(shown.length, 'identicons rendered on this page').toBeGreaterThan(0);
      for (const f of shown) {
        expect(f.src.startsWith('data:image/svg+xml'), `identicon is an inline SVG (${f.cls})`).toBe(true);
        expect(f.hasSvg && f.svgLen > 1200, `identicon is the generated artwork, not a fallback (${f.cls}, ${f.svgLen} bytes)`).toBe(true);
      }
    });
  }

  test('same address → same identicon, different address → different identicon', async ({ page }) => {
    await open(page, path('/plant-trees'));
    const sigs = await page.evaluate(async (addr) => {
      // @ts-ignore — the vendored ESM, loaded the same way the app loads it
      const mod = await import(/* @vite-ignore */ '/nimiqshop/vendor/identicons.module.js');
      const I = mod.default;
      I.svgPath = '/nimiqshop/vendor/identicons.min.svg';
      // The library gives every SVG a random clipPath id (hexagon-clip-<n>) —
      // compare the artwork, not that id.
      const art = (u: string) => { const b64 = u.split(',')[1] || ''; let x = ''; try { x = atob(b64); } catch { x = decodeURIComponent(b64); } return x.replace(/hexagon-clip-\d+/g, 'hexagon-clip'); };
      const a1 = art(await I.toDataUrl(addr)), a2 = art(await I.toDataUrl(addr)), b = art(await I.toDataUrl('NQ12 3456 7890 ABCD EFGH JKLM NPQR STUV'));
      return { same: a1 === a2, diff: a1 !== b, len: a1.length };
    }, ADDRESS);
    expect(sigs.same).toBe(true);
    expect(sigs.diff).toBe(true);
    expect(sigs.len).toBeGreaterThan(1600);
  });
});
