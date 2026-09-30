/**
 * The downloadable impact-card PNG (canvas export). fillText() is
 * instrumented so every string's measured box is checked: nothing leaves the
 * receipt paper, nothing overlaps — every language × every tree-count size.
 */
import { test, expect, open } from './support/fixtures';
import { LANGS, TREE_COUNTS, path } from './support/data';
import type { Page } from '@playwright/test';
import { readFileSync } from 'node:fs';

type Drawn = { text: string; font: string; left: number; right: number; top: number; bottom: number };
const PAPER = { left: 54 + 20, right: 1026 - 20, top: 54, bottom: 1296 - 20 };

async function instrument(page: Page) {
  await page.addInitScript(() => {
    const w = window as any; w.__drawn = []; w.__strokes = 0;
    const proto = CanvasRenderingContext2D.prototype;
    const fillText = proto.fillText;
    proto.fillText = function (this: CanvasRenderingContext2D, text: string, x: number, y: number, mw?: number) {
      if (this.canvas.width === 1080 && this.canvas.height === 1350) {
        const m = this.measureText(text);
        const left = this.textAlign === 'right' || this.textAlign === 'end' ? x - m.width : this.textAlign === 'center' ? x - m.width / 2 : x;
        const asc = m.actualBoundingBoxAscent || parseFloat(this.font.match(/(\d+(?:\.\d+)?)px/)?.[1] || '0') * 0.75;
        w.__drawn.push({ text, font: this.font, left, right: left + m.width, top: y - asc, bottom: y + (m.actualBoundingBoxDescent || 0) });
      }
      return (fillText as any).call(this, text, x, y, mw);
    };
    const stroke = proto.stroke;
    proto.stroke = function (this: CanvasRenderingContext2D, ...a: any[]) { if (this.canvas.width === 1080 && a[0] instanceof Path2D) w.__strokes++; return (stroke as any).apply(this, a); };
  });
}

async function exportPng(page: Page) {
  await open(page, path('/plant-trees'));
  await page.locator('section.pt-impact .pt-actions .btn').first().click();
  const img = page.locator('.pt-overlay img').first();
  await expect(img).toBeVisible({ timeout: 10_000 });
  await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth)).toBeGreaterThan(0);
  return img;
}

for (const lang of LANGS) {
  for (const trees of TREE_COUNTS) {
    test.describe(`PNG · ${lang} · ${trees} trees`, () => {
      test.use({ lang, api: { trees, planted: trees > 20 ? 12 : 0 }, viewport: { width: 1280, height: 900 } });
      test('every string on the receipt, nothing overlaps, brand fonts', async ({ page }) => {
        await instrument(page);
        const img = await exportPng(page);
        expect(await img.evaluate((i: HTMLImageElement) => [i.naturalWidth, i.naturalHeight])).toEqual([1080, 1350]);
        const drawn: Drawn[] = await page.evaluate(() => (window as any).__drawn);
        expect(drawn.length).toBeGreaterThan(15);
        for (const d of drawn) {
          expect(d.left, `"${d.text}" left`).toBeGreaterThanOrEqual(PAPER.left - 1);
          expect(d.right, `"${d.text}" right`).toBeLessThanOrEqual(PAPER.right + 1);
          expect(d.top, `"${d.text}" top`).toBeGreaterThanOrEqual(PAPER.top);
          expect(d.bottom, `"${d.text}" bottom`).toBeLessThanOrEqual(PAPER.bottom);
        }
        const overlaps: string[] = [];
        for (let i = 0; i < drawn.length; i++) for (let j = i + 1; j < drawn.length; j++) {
          const a = drawn[i], b = drawn[j];
          if (Math.min(a.right, b.right) - Math.max(a.left, b.left) > 2 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 2) overlaps.push(`"${a.text}" × "${b.text}"`);
        }
        expect(overlaps).toEqual([]);
        expect(drawn.map((d) => d.text).join(' | ')).not.toMatch(/NİMİQ|İ\.COM/);
        expect(drawn.some((d) => d.font.includes('FrauncesLocal'))).toBe(true);
        expect(drawn.some((d) => d.font.includes('NunitoLocal'))).toBe(true);
        expect(await page.evaluate(() => (window as any).__strokes)).toBeGreaterThanOrEqual(8);
        const png = await img.evaluate((i: HTMLImageElement) => { const c = document.createElement('canvas'); c.width = i.naturalWidth; c.height = i.naturalHeight; c.getContext('2d')!.drawImage(i, 0, 0); return c.toDataURL('image/png'); });
        await test.info().attach(`impact-${lang}-${trees}.png`, { body: Buffer.from(png.split(',')[1], 'base64'), contentType: 'image/png' });
      });
    });
  }
}

test.describe('PNG download & share @smoke', () => {
  test.use({ viewport: { width: 1280, height: 900 } });
  test('Download gives a real PNG file', async ({ page }) => {
    await exportPng(page);
    const [dl] = await Promise.all([page.waitForEvent('download'), page.locator('.pt-overlay-actions a[download]').click()]);
    expect(dl.suggestedFilename()).toBe('nimshop-impact-card.png');
    const buf = readFileSync((await dl.path())!);
    expect(buf.subarray(0, 8).toString('hex')).toBe('89504e470d0a1a0a');
    expect(buf.length).toBeGreaterThan(20_000);
  });
  test('Share hands the PNG file to the share sheet', async ({ page }) => {
    await page.addInitScript(() => {
      const w = window as any; w.__shared = null;
      Object.defineProperty(navigator, 'canShare', { configurable: true, value: (d: any) => Array.isArray(d?.files) });
      Object.defineProperty(navigator, 'share', { configurable: true, value: async (d: any) => { w.__shared = { files: (d.files || []).map((f: File) => ({ name: f.name, type: f.type, size: f.size })), text: d.text }; } });
    });
    await exportPng(page);
    await page.locator('.pt-overlay-actions button').click();
    await expect.poll(() => page.evaluate(() => (window as any).__shared)).not.toBeNull();
    const s = await page.evaluate(() => (window as any).__shared);
    expect(s.files).toHaveLength(1);
    expect(s.files[0]).toMatchObject({ name: 'nimshop-impact-card.png', type: 'image/png' });
  });
  test('exporting twice works and revokes the old blob', async ({ page }) => {
    // Record revocations: Firefox may still resolve a revoked blob: URL inside
    // the same document for a while, so "does it still load" is not portable.
    await page.addInitScript(() => {
      const w = window as any; w.__revoked = [];
      const orig = URL.revokeObjectURL.bind(URL);
      URL.revokeObjectURL = (u: string) => { w.__revoked.push(u); orig(u); };
    });
    await exportPng(page);
    const first = await page.locator('.pt-overlay img').getAttribute('src');
    await page.keyboard.press('Escape');
    await expect(page.locator('.pt-overlay')).toHaveCount(0);
    await page.locator('section.pt-impact .pt-actions .btn').first().click();
    await expect(page.locator('.pt-overlay img')).toBeVisible();
    expect(await page.locator('.pt-overlay img').getAttribute('src')).not.toBe(first);
    if (first?.startsWith('blob:')) {
      await expect.poll(() => page.evaluate(() => (window as any).__revoked as string[]), { message: 'old PNG blob URL revoked' }).toContain(first);
    }
  });
});
