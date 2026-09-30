/**
 * Deep responsive scanner.
 *
 * audit(page)   — measures EVERY visible element on the page and returns the
 *                 layout problems it finds (see Issue kinds below).
 * explore(page) — presses every button / menu / tab / toggle on the page one
 *                 by one, audits the page in each opened state (menus, sheets,
 *                 dialogs, dropdowns, accordions), screenshots it, and puts
 *                 the page back.
 * shoot(...)    — writes screenshots to screenshots/<device>/<screen>/<lang>/
 *                 (uploaded by GitHub Actions as the "responsive-screenshots"
 *                 artifact: desktop/ tablet/ phone/ + index.html gallery).
 *
 * Opt-outs for intentional designs (use sparingly, they are visible in code):
 *   data-allow-truncate  — ellipsis on purpose (addresses, hashes)
 *   data-allow-overflow  — element may extend past its clip box (marquee…)
 *   data-scan-skip       — subtree ignored by the scanner
 */
import type { Page } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

export type Issue = { kind: string; detail: string };

export type AuditOptions = {
  device: 'desktop' | 'tablet' | 'phone';
  minFont: number;          // px — smallest readable text
  minTap: number;           // px — smallest tap target (touch devices)
};

export const AUDIT_DEFAULTS: Record<AuditOptions['device'], AuditOptions> = {
  desktop: { device: 'desktop', minFont: 10, minTap: 0 },
  tablet: { device: 'tablet', minFont: 10, minTap: 24 },
  phone: { device: 'phone', minFont: 10, minTap: 24 },
};

/** Runs inside the page. Must be self-contained (serialised by Playwright). */
function auditInPage(opts: AuditOptions & { rawKeyRe: string }): Issue[] {
  const issues: Issue[] = [];
  const add = (kind: string, el: Element | null, extra = '') => {
    const d = el ? describe(el) : '';
    if (issues.length < 200) issues.push({ kind, detail: `${d}${extra ? ' — ' + extra : ''}` });
  };
  const vw = document.documentElement.clientWidth;
  const round = (n: number) => Math.round(n);

  function describe(el: Element): string {
    const h = el as HTMLElement;
    const cls = typeof h.className === 'string' ? h.className.trim().split(/\s+/).slice(0, 3).join('.') : '';
    const txt = (h.innerText || h.getAttribute?.('aria-label') || h.getAttribute?.('alt') || '').trim().replace(/\s+/g, ' ').slice(0, 40);
    return `<${el.tagName.toLowerCase()}${cls ? '.' + cls : ''}>${txt ? ' "' + txt + '"' : ''}`;
  }
  const skipped = (el: Element) => !!el.closest('[data-scan-skip]');
  const isVisible = (el: Element) => {
    const h = el as HTMLElement;
    if (!h.getClientRects().length) return false;
    // Content of a closed <details> (and any content-visibility:hidden subtree)
    // still reports a layout box in Chromium — but nobody can see it.
    if (typeof h.checkVisibility === 'function' && !h.checkVisibility({ opacityProperty: true, visibilityProperty: true })) return false;
    const cs = getComputedStyle(h);
    if (cs.visibility === 'hidden' || cs.display === 'none') return false;
    // walk up for opacity 0 / aria-hidden off-canvas
    for (let a: Element | null = h; a; a = a.parentElement) {
      const s = getComputedStyle(a);
      if (parseFloat(s.opacity) === 0) return false;
    }
    const r = h.getBoundingClientRect();
    return r.width > 0.5 && r.height > 0.5;
  };
  const isSrOnly = (el: Element) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return (r.width <= 1 && r.height <= 1) || cs.clip === 'rect(0px, 0px, 0px, 0px)' || cs.clipPath === 'inset(50%)';
  };
  /** Nearest ancestor with position:fixed (overlays/header) — the stacking
   *  "layer" an element lives in. Text in different layers may overlap. */
  const layerOf = (el: Element): Element => {
    for (let a: Element | null = el; a; a = a.parentElement) {
      const cs = getComputedStyle(a);
      const p = cs.position;
      if (p === 'fixed' || p === 'sticky') return a;
      // popovers / dropdowns float above the page on purpose
      if (p === 'absolute' && cs.zIndex !== 'auto' && parseInt(cs.zIndex, 10) > 0) return a;
    }
    return document.documentElement;
  };
  /** Horizontal clip box from ancestors; null = inside a scroller (content
   *  may legitimately extend, the user scrolls to it). */
  const clipBox = (el: Element): { left: number; right: number } | null => {
    let left = 0, right = vw;
    for (let a = el.parentElement; a && a !== document.documentElement; a = a.parentElement) {
      const s = getComputedStyle(a);
      if (s.overflowX === 'auto' || s.overflowX === 'scroll') return null;
      if (s.overflowX === 'hidden' || s.overflowX === 'clip') {
        const r = a.getBoundingClientRect();
        left = Math.max(left, r.left); right = Math.min(right, r.right);
      }
      if (s.position === 'fixed') break;
    }
    return { left, right };
  };

  // 1. page scrolls sideways
  const sw = document.documentElement.scrollWidth;
  if (sw > window.innerWidth + 1) {
    // find the widest culprits
    const wide = [...document.querySelectorAll('body *')].filter((e) => isVisible(e) && e.getBoundingClientRect().right > window.innerWidth + 1 && clipBox(e) !== null)
      .sort((a, b) => b.getBoundingClientRect().right - a.getBoundingClientRect().right).slice(0, 3).map(describe).join(', ');
    issues.push({ kind: 'horizontal-scroll', detail: `document is ${sw}px wide in a ${window.innerWidth}px viewport; widest: ${wide}` });
  }

  const all = [...document.querySelectorAll('body *')].filter((e) => !skipped(e) && isVisible(e));

  // text leaves: elements with their own non-empty text node
  const textEls = all.filter((e) => [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent!.trim()) && !isSrOnly(e) && !e.closest('svg'));

  const textSet = new Set(textEls);
  for (const el of all) {
    const r = el.getBoundingClientRect();

    // 2. cut off by a clipping ancestor / the viewport (partially visible)
    if (!el.closest('[data-allow-overflow]')) {
      const cb = clipBox(el);
      if (cb) {
        const partlyIn = r.right > cb.left + 1 && r.left < cb.right - 1;
        const sticksOut = r.right > cb.right + 1.5 || r.left < cb.left - 1.5;
        const leaf = el.children.length === 0 || textSet.has(el) || /^(IMG|BUTTON|INPUT|SELECT|TEXTAREA|SVG|A)$/i.test(el.tagName);
        if (partlyIn && sticksOut && leaf) add('cut-off', el, `box ${round(r.left)}..${round(r.right)} vs visible ${round(cb.left)}..${round(cb.right)}`);
      }
    }

    // 3. images: broken or distorted
    if (el.tagName === 'IMG') {
      const img = el as HTMLImageElement;
      if (!img.complete || img.naturalWidth === 0) add('broken-image', el, img.currentSrc.slice(0, 80));
      else {
        const fit = getComputedStyle(img).objectFit;
        if ((fit === 'fill' || !fit) && !img.currentSrc.startsWith('data:image/svg')) {
          const want = img.naturalWidth / img.naturalHeight, got = r.width / r.height;
          if (Math.abs(got - want) / want > 0.06) add('distorted-image', el, `rendered ${round(r.width)}×${round(r.height)}, natural ${img.naturalWidth}×${img.naturalHeight}`);
        }
      }
    }

    // 4. tap targets on touch devices
    if (opts.minTap && el.matches('button, [role="button"], a.btn, .btn, input:not([type="hidden"]), select, [role="tab"], [role="menuitem"]')) {
      // a checkbox/radio inside its <label>: the label is the tap target
      const target = el.matches('input[type="checkbox"], input[type="radio"]') && el.closest('label') ? el.closest('label')!.getBoundingClientRect() : r;
      if (Math.min(target.width, target.height) < opts.minTap - 0.5 && !el.closest('p, li > span')) add('tap-target', el, `${round(r.width)}×${round(r.height)}px < ${opts.minTap}px`);
    }
  }

  // 5. text: too small, truncated
  for (const el of textEls) {
    const cs = getComputedStyle(el);
    const fs = parseFloat(cs.fontSize);
    if (fs < opts.minFont - 0.05) add('font-too-small', el, `${fs.toFixed(1)}px < ${opts.minFont}px`);
    const h = el as HTMLElement;
    const clips = cs.overflowX !== 'visible' || cs.textOverflow === 'ellipsis';
    if (clips && !el.closest('[data-allow-truncate]') && !h.title && h.scrollWidth > h.clientWidth + 1) {
      add('text-truncated', el, `needs ${h.scrollWidth}px, has ${h.clientWidth}px`);
    }
    // -webkit-line-clamp is an explicit design decision (lede teasers) — not reported
  }

  // 6. overlapping text (real glyph boxes via Range) within the same layer
  type Box = { el: Element; layer: Element; l: number; r: number; t: number; b: number };
  /** The part of the screen where this element's content can be seen:
   *  itself + every ancestor that clips (overflow ≠ visible). Text cut off
   *  by an ellipsis must not count as "overlapping" its neighbours. */
  const visibleRect = (el: Element) => {
    let l = -1e9, r = 1e9, t = -1e9, b = 1e9;
    for (let a: Element | null = el; a && a !== document.documentElement; a = a.parentElement) {
      const s = getComputedStyle(a);
      if (s.overflowX !== 'visible' || s.overflowY !== 'visible') {
        const rr = a.getBoundingClientRect();
        if (s.overflowX !== 'visible') { l = Math.max(l, rr.left); r = Math.min(r, rr.right); }
        if (s.overflowY !== 'visible') { t = Math.max(t, rr.top); b = Math.min(b, rr.bottom); }
      }
      if (s.position === 'fixed') break;
    }
    return { l, r, t, b };
  };
  const boxes: Box[] = [];
  for (const el of textEls) {
    const vis = visibleRect(el);
    const layer = layerOf(el);
    for (const n of el.childNodes) {
      if (n.nodeType !== 3 || !n.textContent!.trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(n);
      for (const rr of range.getClientRects()) {
        const bx = { el, layer, l: Math.max(rr.left, vis.l), r: Math.min(rr.right, vis.r), t: Math.max(rr.top, vis.t), b: Math.min(rr.bottom, vis.b) };
        if (bx.r - bx.l > 1 && bx.b - bx.t > 1) boxes.push(bx);
      }
    }
  }
  boxes.sort((x, y) => x.t - y.t);
  const reported = new Set<string>();
  for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length && boxes[j].t < boxes[i].b; j++) {
    const a = boxes[i], b = boxes[j];
    if (a.el === b.el || a.layer !== b.layer) continue;
    if (a.el.contains(b.el) || b.el.contains(a.el)) continue;
    const ox = Math.min(a.r, b.r) - Math.max(a.l, b.l), oy = Math.min(a.b, b.b) - Math.max(a.t, b.t);
    if (ox > 2 && oy > Math.min(a.b - a.t, b.b - b.t) * 0.35) {
      const key = describe(a.el) + '|' + describe(b.el);
      if (!reported.has(key)) { reported.add(key); add('text-overlap', a.el, `overlaps ${describe(b.el)}`); }
    }
  }

  // 7. overlapping controls (buttons/links/inputs on top of each other)
  const ctrls = all.filter((e) => e.matches('button, a[href], input:not([type="hidden"]), select, textarea, [role="button"]') && !isSrOnly(e));
  for (let i = 0; i < ctrls.length; i++) for (let j = i + 1; j < ctrls.length; j++) {
    const a = ctrls[i], b = ctrls[j];
    if (a.contains(b) || b.contains(a) || layerOf(a) !== layerOf(b)) continue;
    const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect();
    const ox = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left), oy = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top);
    if (ox > 3 && oy > 3) add('control-overlap', a, `overlaps ${describe(b)}`);
  }

  // 8. Nimiq identicons must be the real generated SVG, never a fallback
  document.querySelectorAll('[data-identicon], .identicon, [class*="identicon" i]').forEach((host) => {
    if (!isVisible(host)) return;
    const img = host.tagName === 'IMG' ? (host as HTMLImageElement) : host.querySelector('img');
    const svg = host.tagName === 'svg' ? host : host.querySelector('svg');
    if (img) {
      const src = img.currentSrc || img.src;
      let ok = src.startsWith('data:image/svg+xml');
      if (ok) {
        try {
          const raw = src.includes(';base64,') ? atob(src.split(',')[1]) : decodeURIComponent(src.split(',')[1]);
          ok = raw.length > 1200 && /<svg/i.test(raw);
        } catch { ok = false; }
      }
      if (!ok || img.dataset.fallback !== undefined || /fallback|placeholder|default/i.test(img.className + ' ' + src)) add('identicon-fallback', host, src.slice(0, 60));
    } else if (!svg || svg.innerHTML.length < 1200) {
      add('identicon-fallback', host, 'no generated identicon image');
    }
  });

  // 9. raw i18n keys on screen
  const re = new RegExp(opts.rawKeyRe, 'g');
  const leaked = (document.body.innerText.match(re) || []).filter((k, i, a) => a.indexOf(k) === i);
  if (leaked.length) issues.push({ kind: 'raw-i18n-key', detail: leaked.slice(0, 10).join(', ') });

  return issues;
}

/** `bottomBar: false` in explored states — an open sheet/modal scrolls itself
 *  and legitimately sits in front of the page (and its fixed bars). */
export async function audit(page: Page, opts: AuditOptions, rawKeyRe: RegExp, { bottomBar = true } = {}): Promise<Issue[]> {
  // Lazy images below the fold have not loaded yet — load them all first, so
  // "broken image" means broken, not "not scrolled to yet".
  await page.evaluate(async () => {
    const imgs = [...document.images];
    imgs.forEach((i) => { if (i.loading === 'lazy') i.loading = 'eager'; });
    await Promise.race([
      Promise.all(imgs.map((i) => (i.complete ? null : i.decode().catch(() => null)))),
      new Promise((r) => setTimeout(r, 3000)),
    ]);
  });
  const issues = await page.evaluate(auditInPage, { ...opts, rawKeyRe: rawKeyRe.source });
  return bottomBar ? issues.concat(await page.evaluate(coveredAtBottom)) : issues;
}

/** Scrolled all the way down, the last content (footer, final card) must not
 *  sit underneath a fixed bottom bar (tab bar, cookie/checkout bar). Catches a
 *  missing bottom padding/margin at the widths where such a bar is shown. */
async function coveredAtBottom(): Promise<Issue[]> {
  const scroller = [document.scrollingElement, document.body, document.documentElement]
    .find((el) => el && el.scrollHeight > el.clientHeight + 1 && getComputedStyle(el).overflowY !== 'hidden') as HTMLElement | undefined;
  const vh = window.innerHeight, vw = window.innerWidth;
  const bars = [...document.querySelectorAll<HTMLElement>('body *')].filter((el) => {
    const cs = getComputedStyle(el);
    if (cs.position !== 'fixed' || cs.visibility === 'hidden' || cs.display === 'none' || +cs.opacity === 0) return false;
    const r = el.getBoundingClientRect();
    return r.height > 0 && r.height < vh / 3 && r.width > vw / 2 && r.bottom >= vh - 2 && r.top > vh / 2;
  });
  if (!bars.length) return [];
  const prev = scroller ? scroller.scrollTop : 0;
  // setTimeout, not rAF: rAF never fires while the tab is in the background.
  if (scroller) { scroller.scrollTop = scroller.scrollHeight; await new Promise((r) => setTimeout(r, 60)); }
  const out: Issue[] = [];
  const barTop = Math.min(...bars.map((b) => b.getBoundingClientRect().top));
  const walker = document.createTreeWalker(document.querySelector('main, #main')?.parentElement || document.body, NodeFilter.SHOW_TEXT);
  const seen = new Set<Element>();
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (!el || seen.has(el) || !n.textContent?.trim() || bars.some((b) => b.contains(el))) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || el.closest('[aria-hidden="true"], [hidden]')) continue;
    if (typeof el.checkVisibility === 'function' && !el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) continue;
    // Only content in normal flow — other fixed/sticky layers are not "the page".
    let p: Element | null = el, layered = false;
    while (p && p !== document.body) { const pos = getComputedStyle(p).position; if (pos === 'fixed' || pos === 'sticky') { layered = true; break; } p = p.parentElement; }
    if (layered) continue;
    const range = document.createRange(); range.selectNodeContents(n);
    const r = range.getBoundingClientRect();
    if (r.height && r.bottom > barTop + 1 && r.top < vh) {
      seen.add(el);
      out.push({ kind: 'covered-by-fixed-bar', detail: `<${el.tagName.toLowerCase()}${el.className && typeof el.className === 'string' ? '.' + el.className.split(' ')[0] : ''}> "${n.textContent.trim().slice(0, 30)}" is under a fixed bar at the bottom of the page (text ${Math.round(r.top)}..${Math.round(r.bottom)}, bar from ${Math.round(barTop)})` });
      if (out.length >= 5) break;
    }
  }
  if (scroller) scroller.scrollTop = prev;
  return out;
}

export function formatIssues(issues: Issue[]) {
  return issues.map((i) => `  [${i.kind}] ${i.detail}`).join('\n');
}

/* ------------------------------------------------------------ screenshots */

export const SHOT_ROOT = join(process.cwd(), 'screenshots');

/**
 * Full-page capture that is correct for this app: <body> is the scroller, so
 * Playwright's fullPage mode leaves fixed bars (header, tab bar) stranded in
 * the middle. Instead the viewport is grown to the page height for the shot.
 */
export async function shoot(page: Page, device: string, screen: string, lang: string, name: string, fullPage = true) {
  const file = join(SHOT_ROOT, device, screen, lang, `${name}.jpg`);
  mkdirSync(dirname(file), { recursive: true });
  const vp = page.viewportSize()!;
  let buf: Buffer;
  if (fullPage) {
    const h = await page.evaluate(() => Math.max(document.body.scrollHeight, document.documentElement.scrollHeight));
    const tall = Math.min(Math.max(h, vp.height), 12000);
    if (tall > vp.height) { await page.setViewportSize({ width: vp.width, height: tall }); await page.waitForTimeout(150); }
    buf = await page.screenshot({ type: 'jpeg', quality: 70, animations: 'disabled', caret: 'hide', timeout: 20_000 });
    if (tall > vp.height) { await page.setViewportSize(vp); await page.waitForTimeout(100); }
  } else {
    buf = await page.screenshot({ type: 'jpeg', quality: 70, animations: 'disabled', caret: 'hide', timeout: 20_000 });
  }
  writeFileSync(file, buf);
  return file;
}

export function writeIssues(device: string, screen: string, lang: string, text: string) {
  const file = join(SHOT_ROOT, device, screen, lang, 'issues.txt');
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text || 'no issues ✓\n');
}

/* --------------------------------------------------------------- explorer */

/** Elements that open or change something. */
const OPENERS = [
  'button', '[role="button"]', 'summary', '[aria-haspopup]', '[aria-expanded]', '[role="tab"]',
  '[role="switch"]', 'select', 'label.toggle', '.lang-switch', '.theme-toggle',
].join(', ');

/** Anything that looks like an opened layer. */
const LAYERS = [
  '[role="dialog"]', '[role="menu"]', '[role="listbox"]', '[aria-modal="true"]', 'dialog[open]', 'details[open] > :not(summary)',
  '.modal', '.sheet', '.drawer', '.dropdown', '.popover', '.menu', '.pt-overlay', '.cart-sheet', '.acct-menu', '.lang-menu',
].join(', ');

export type Explored = { label: string; opened: boolean; issues: Issue[]; shot?: string };

async function openersSignature(page: Page) {
  return page.evaluate((sel) => {
    const seen = new Set<string>();
    const out: { key: string; index: number }[] = [];
    const els = [...document.querySelectorAll(sel)];
    els.forEach((el, index) => {
      const h = el as HTMLElement;
      if (!h.getClientRects().length || h.closest('[data-scan-skip]')) return;
      const r = h.getBoundingClientRect();
      if (r.width < 2 || r.height < 2 || (h as HTMLButtonElement).disabled) return;
      if (getComputedStyle(h).visibility === 'hidden') return;
      const label = (h.getAttribute('aria-label') || h.innerText || h.getAttribute('title') || h.className || h.tagName).trim().replace(/\s+/g, ' ').slice(0, 50);
      const key = label + '|' + (typeof h.className === 'string' ? h.className : '');
      if (seen.has(key)) return;
      seen.add(key);
      out.push({ key: label, index });
    });
    return out;
  }, OPENERS);
}

async function layerState(page: Page) {
  return page.evaluate((sel) => [...document.querySelectorAll(sel)].filter((e) => {
    const h = e as HTMLElement; const r = h.getBoundingClientRect();
    return h.getClientRects().length && r.width > 2 && r.height > 2 && getComputedStyle(h).visibility !== 'hidden';
  }).length, LAYERS);
}

/** Opened layer (dialog/menu/sheet) must fit the viewport, or scroll inside. */
async function layerIssues(page: Page): Promise<Issue[]> {
  return page.evaluate((sel) => {
    const out: { kind: string; detail: string }[] = [];
    const vw = document.documentElement.clientWidth, vh = window.innerHeight;
    document.querySelectorAll(sel).forEach((e) => {
      const h = e as HTMLElement;
      if (!h.getClientRects().length) return;
      const r = h.getBoundingClientRect();
      if (r.width < 3 || r.height < 3) return;
      const fixed = (() => { for (let a: Element | null = h; a; a = a.parentElement) if (getComputedStyle(a).position === 'fixed') return true; return false; })();
      if (r.left < -1 || r.right > vw + 1) out.push({ kind: 'layer-offscreen-x', detail: `<${h.tagName.toLowerCase()}.${String(h.className).split(' ')[0]}> spans ${Math.round(r.left)}..${Math.round(r.right)} of ${vw}px` });
      if (fixed && r.height > vh + 1) {
        // taller than the screen: it (or a child) must scroll
        const scrolls = [h, ...h.querySelectorAll('*')].some((x) => { const s = getComputedStyle(x); return (s.overflowY === 'auto' || s.overflowY === 'scroll') && x.scrollHeight > x.clientHeight; });
        if (!scrolls) out.push({ kind: 'layer-unreachable', detail: `<${h.tagName.toLowerCase()}.${String(h.className).split(' ')[0]}> is ${Math.round(r.height)}px tall in a ${vh}px screen and cannot scroll` });
      }
    });
    return out;
  }, LAYERS);
}

/**
 * Press every opener, audit the opened state, screenshot it, restore.
 * `reload` must bring the page back to its initial state.
 */
export async function explore(
  page: Page,
  opts: { auditOpts: AuditOptions; rawKeyRe: RegExp; reload: () => Promise<void>; shotBase?: { device: string; screen: string; lang: string; size: string }; max?: number },
): Promise<Explored[]> {
  const results: Explored[] = [];
  const startUrl = page.url();
  const openers = (await openersSignature(page)).slice(0, opts.max ?? 30);
  const baseLayers = await layerState(page);
  // A control that opens a new window/tab (e.g. "Open in Nimiq Pay") would push
  // this page into the background, where it stops painting — screenshots and
  // rAF then hang. Popups are recorded and closed at once.
  const popups: string[] = [];
  const onPopup = async (p: Page) => { popups.push(p.url()); await p.close().catch(() => {}); await page.bringToFront().catch(() => {}); };
  page.on('popup', onPopup);
  let n = 0;
  const dbg = process.env.SCAN_DEBUG ? (m: string) => console.log(`[explore ${Date.now() % 100000}] ${m}`) : () => {};
  try {
  for (const op of openers) {
    dbg(`opener ${op.key}`);
    const el = page.locator(OPENERS).nth(op.index);
    if (!(await el.isVisible().catch(() => false))) continue;
    const before = await page.evaluate(() => document.body.innerHTML.length);
    const popupsBefore = popups.length;
    await el.click({ timeout: 1500 }).catch(() => {});
    await page.waitForTimeout(180);
    if (popups.length > popupsBefore) { await page.bringToFront().catch(() => {}); await page.waitForTimeout(100); }
    const moved = page.url() !== startUrl;
    dbg(`clicked moved=${moved} popups=${popups.length}`);
    const layers = await layerState(page);
    const after = await page.evaluate(() => document.body.innerHTML.length);
    const opened = layers > baseLayers;
    const changed = opened || Math.abs(after - before) > 200;
    const entry: Explored = { label: op.key, opened, issues: [] };
    if (!moved && changed) {
      entry.issues = [...(await audit(page, opts.auditOpts, opts.rawKeyRe, { bottomBar: false })), ...(await layerIssues(page))];
      dbg(`audited ${entry.issues.length}`);
      if (opts.shotBase && (opened || entry.issues.length)) {
        n++;
        const safe = op.key.toLowerCase().replace(/[^a-z0-9ğüşöçıİ]+/gi, '-').replace(/^-|-$/g, '').slice(0, 30) || 'control';
        entry.shot = await shoot(page, opts.shotBase.device, opts.shotBase.screen, opts.shotBase.lang, `${opts.shotBase.size}--${String(n).padStart(2, '0')}-${safe}`, false);
      }
    }
    results.push(entry);
    dbg('shot/pushed');
    // restore
    if (moved) { await opts.reload(); continue; }
    // (in-page state changes — tabs, filters, toggles — are kept, like a real
    //  user would; only a stuck layer or a navigation costs a reload)
    if (opened) {
      await page.keyboard.press('Escape').catch(() => {});
      await page.waitForTimeout(120);
      if ((await layerState(page)) > baseLayers) { await el.click({ timeout: 1000 }).catch(() => {}); await page.waitForTimeout(120); }
      if ((await layerState(page)) > baseLayers) await opts.reload();
    }
  }
  } finally {
    page.off('popup', onPopup);
  }
  return results;
}
