/**
 * fitText.ts — re-fits width-budgeted labels whenever the text or the box
 * changes.
 *
 * Why this exists: the header nav, the brand wordmark, the mobile tab bar and
 * the shared "come back" banner are all width-budgeted with CSS alone. CSS
 * cannot know how long the *current* language's label is ("Shop" vs "Boutique"
 * vs "Mağaza"), so a language switch used to leave the previous fit in place:
 * French/German/Turkish labels stayed ellipsized ("BOUTI…", "ACTI…") until a
 * reload re-laid the whole shell out. The same happened whenever the browser's
 * base font size changed, because the labels scale with it (rem) while the
 * paddings, gaps and control widths they share the rail with are px-based.
 *
 * This module measures the real boxes and shrinks the type until a label fits,
 * then re-runs on every event that can change the answer:
 *
 *   · language change — onLangChange() plus the <html data-lang> attribute
 *     (the latter also covers the separate static-shell bundle and other tabs),
 *   · layout changes — window/visualViewport resize, orientationchange and a
 *     ResizeObserver on every fitted element,
 *   · late webfonts — document.fonts (+ loadingdone): metrics land after paint,
 *   · late DOM — MutationObserver for islands, the tab-bar portal and sheets,
 *   · tab restore — visibilitychange / pageshow.
 *
 * Markup opt-in:
 *
 *   <nav data-fit-group data-fit-min="9">        one size shared by a rail
 *     <a data-fit-item>Shop</a> …                items measured together
 *   </nav>
 *   <span data-fit data-fit-min="11">shop.example.com</span>
 *   <span data-fit="wrap">long sentence…</span>  shrink, then wrap (never cut)
 *   <div data-fit-row>…</div>                    row that tightens itself when
 *                                                its rail runs out of room
 *
 * Nothing is shrunk below data-fit-min (or the 0.72×base floor), so a label can
 * never become unreadable, and a rail that cannot fit even then makes its row
 * take the compact shape the narrow-desktop CSS band already uses instead of
 * ending up with six ellipsized links.
 *
 * Re-running is idempotent: every pass first clears the inline sizes it wrote
 * last time, so the CSS base size is always the starting point.
 */

import { onLangChange } from '../i18n';

/** Smallest size any label may be shrunk to, unless data-fit-min says otherwise. */
const MIN_PX = 10; // the e2e scan fails any text under 10px
/** …and a relative floor so a large base size never collapses to MIN_PX. */
const MIN_RATIO = 0.72;
/** Extra breathing room so sub-pixel rounding never leaves a 1px clip. */
const RELAX = 0.995;
/** Measurements per element: the first one is model-based, so a couple is plenty. */
const MAX_STEPS = 5;
/** Attribute a row carries while it is too tight for its rail. */
const TIGHT = 'data-fit-tight';
/** What a rail's CSS base size was, and what it had to settle for. */
const FIT_BASE = 'data-fit-base';
const FIT_SIZE = 'data-fit-size';
/** Set on a row while a pass measures it: CSS turns its transitions off.
 *  (Under prefers-reduced-motion every element gets `transition: all 0.01ms`,
 *  so a gap / max-width / padding written by the row budget only reaches its
 *  new value on the next frame — a pass then measured the old layout, decided
 *  from it, and the next ResizeObserver tick decided the opposite, forever.) */
const BUSY = 'data-fit-busy';
/** Below this share of its base size a rail is "squeezed": the row should give
 *  it room (compact wallet button) rather than leave the links unreadably small. */
const SQUEEZE_AT = 0.86;
/** How far a row may be tightened (1: icon-only wallet button, 2: + brand cap). */
const MAX_TIGHT = 2;

/** Parse one computed length, falling back instead of returning NaN. */
export function pxOf(value: string | null | undefined, fallback: number): number {
  const n = parseFloat(value == null ? '' : String(value));
  return Number.isFinite(n) ? n : fallback;
}

/**
 * The size one step closer to fitting `need` px of text into `avail` px.
 * Exported for tests: it is pure arithmetic, no layout involved.
 */
export function nextFontSize(current: number, need: number, avail: number, floor: number): number {
  if (!(current > 0) || !(need > 0) || !(avail > 0)) return current;
  const scaled = current * (avail / need) * RELAX;
  const stepped = Math.min(current - 0.05, scaled);
  return Math.max(floor, stepped);
}

/** Floor for one element: data-fit-min, else max(MIN_PX, base × MIN_RATIO). */
export function fitFloor(el: Element, base: number): number {
  const attr = parseFloat(el.getAttribute('data-fit-min') || '');
  const floor = Number.isFinite(attr) ? attr : Math.max(MIN_PX, base * MIN_RATIO);
  return Math.min(floor, base);
}

/**
 * A pass writes font sizes, and any `transition` on the label would (a) delay
 * the new fit and (b) make every following measurement read a mid-transition
 * value — exactly the bug that kept the French nav ellipsized. So the elements
 * a pass touches are frozen for the duration of that pass and thawed on the
 * next frame, once the new size has been committed without a transition.
 */
const frozen = new Map<HTMLElement, string>();
let thawQueued = false;

function freeze(el: HTMLElement) {
  if (!frozen.has(el)) frozen.set(el, el.style.transition);
  el.style.transition = 'none';
}

function thawSoon() {
  if (thawQueued) return;
  thawQueued = true;
  const thaw = () => {
    thawQueued = false;
    frozen.forEach((original, el) => { el.style.transition = original; });
    frozen.clear();
    document.querySelectorAll(`[${BUSY}]`).forEach((row) => row.removeAttribute(BUSY));
  };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(thaw);
  else setTimeout(thaw, 0);
}

/** Write the fitted size — as `important`, because a few of these labels are
 *  pinned by an author `!important` rule (the mobile footer credit line is) and
 *  the fit is the authority for the size these elements render at. */
function setSize(el: HTMLElement, size: number) {
  el.style.setProperty('font-size', `${size.toFixed(2)}px`, 'important');
}

/** Padding on the inline axis of an element. */
function padX(el: HTMLElement): number {
  const cs = getComputedStyle(el);
  return pxOf(cs.paddingLeft, 0) + pxOf(cs.paddingRight, 0);
}

/** The box the text has to fit in: the client box without the padding. */
function contentBox(el: HTMLElement): number {
  return el.clientWidth - padX(el);
}

/**
 * Width the text really needs: the glyph box from a Range, plus the trailing
 * letter-spacing the browser adds after the last character.
 *
 * Comparing scrollWidth to clientWidth instead — which is what the browser's
 * own ellipsis test does — makes a shrink loop chase its own tail: scrollWidth
 * carries that trailing spacing (and its rounding) on top of a box that is
 * itself sized from the text, so a ~1px "deficit" survives every step and the
 * font would be walked all the way down to the floor for nothing.
 */
function textWidth(el: HTMLElement): number {
  const space = Math.max(0, pxOf(getComputedStyle(el).letterSpacing, 0));
  try {
    const range = document.createRange();
    range.selectNodeContents(el);
    const width = range.getBoundingClientRect().width;
    if (width > 0) return width + space;
  } catch { /* no Range — fall back to the scroll box */ }
  return Math.max(el.scrollWidth, el.clientWidth);
}

/**
 * The browser's own ellipsis test — this is the one that decides whether a
 * label is actually cut off on screen, so it is the loop's exit condition.
 * The Range ratio above only decides *how far* to step.
 */
function ellipsized(el: HTMLElement, tolerance = 0.5): boolean {
  return el.scrollWidth > el.clientWidth + tolerance;
}

/** Space between the items of a rail — the CSS gap, with a rect-based backup. */
function gapBetween(container: HTMLElement, items: HTMLElement[]): number {
  const cs = getComputedStyle(container);
  const declared = pxOf(cs.columnGap || cs.gap, NaN);
  if (Number.isFinite(declared) && declared >= 0) return declared;
  let total = 0;
  for (let i = 1; i < items.length; i += 1) {
    const gap = items[i].getBoundingClientRect().left - items[i - 1].getBoundingClientRect().right;
    if (gap > 0) total += gap;
  }
  return total / Math.max(1, items.length - 1);
}

/* ---------------------------------------------------------------------------
 * BATCHED FITTING
 *
 * The first version of this module fitted every rail and every label in its
 * own little loop: write a size, measure, write, measure. A measurement taken
 * right after a style write forces the browser to recompute layout
 * synchronously (the "forced reflow" PageSpeed insight), and with ~15 fitted
 * elements, up to five steps each and three relax rounds, a single pass
 * produced dozens of synchronous layout flushes (~60 ms attributed on a
 * phone, together with the resize handler).
 *
 * The state machine below runs the SAME algorithm with the DOM access
 * synchronised: each step is one WRITE phase (apply the pending sizes of all
 * active targets) followed by one READ phase (measure all active targets),
 * so a step costs a single layout flush instead of one per element. A target
 * drops out the moment its own loop would have broken, so the fitted sizes
 * are unchanged.
 * ------------------------------------------------------------------------- */

interface GroupFit {
  container: HTMLElement;
  items: HTMLElement[];
  base: number;
  floor: number;
  room: number;
  size: number;
  text: number;
  ell: boolean;
  active: boolean;
  pending: number | null;
}

interface SingleFit {
  el: HTMLElement;
  base: number;
  floor: number;
  mode: string | null;
  size: number;
  box: number;
  /** textWidth() of the current step — read in the read phase, used in compute. */
  textWidthCache: number;
  ell: boolean;
  active: boolean;
  pending: number | null;
}

export function makeGroupFit(container: HTMLElement): GroupFit | null {
  const items = Array.from(container.querySelectorAll<HTMLElement>('[data-fit-item]'));
  if (!items.length) return null;
  return { container, items, base: 0, floor: 0, room: 0, size: 0, text: 0, ell: false, active: false, pending: null };
}

export function makeSingleFit(el: HTMLElement): SingleFit {
  return { el, base: 0, floor: 0, mode: el.getAttribute('data-fit'), size: 0, box: 0, textWidthCache: 0, ell: false, active: false, pending: null };
}

/* ---- WRITE phases: style mutations only, never a read afterwards -------- */

function resetGroups(gs: GroupFit[]) {
  for (const g of gs) {
    g.items.forEach((item) => { freeze(item); item.style.removeProperty('font-size'); });
    g.active = true;
    g.pending = null;
  }
}

function resetSingles(ss: SingleFit[]) {
  for (const f of ss) {
    freeze(f.el);
    f.el.style.removeProperty('font-size');
    f.el.style.removeProperty('white-space');
    f.el.style.removeProperty('overflow-wrap');
    f.active = true;
    f.pending = null;
  }
}

function writePending(gs: GroupFit[], ss: SingleFit[]) {
  for (const g of gs) {
    if (g.pending !== null) { const size = g.pending; g.items.forEach((item) => setSize(item, size)); g.pending = null; }
  }
  for (const f of ss) {
    if (f.pending !== null) { setSize(f.el, f.pending); f.pending = null; }
  }
}

/* ---- READ phases: measurements only, never a write in between ----------- */

function measureGroups(gs: GroupFit[]) {
  for (const g of gs) {
    if (!g.active) continue;
    const avail = contentBox(g.container);
    if (!(avail > 0)) { g.active = false; continue; }
    const base = pxOf(getComputedStyle(g.items[0]).fontSize, 12);
    const gaps = gapBetween(g.container, g.items) * (g.items.length - 1);
    const fixed = gaps + g.items.reduce((sum, item) => sum + padX(item), 0);
    g.base = base;
    g.floor = fitFloor(g.container, base);
    g.room = avail - fixed;
    g.size = base;
    g.container.setAttribute(FIT_BASE, base.toFixed(2));
  }
}

function measureSingles(ss: SingleFit[]) {
  for (const f of ss) {
    if (!f.active) continue;
    f.base = pxOf(getComputedStyle(f.el).fontSize, 12);
    f.floor = fitFloor(f.el, f.base);
    f.mode = f.el.getAttribute('data-fit');
    f.size = f.base;
    f.textWidthCache = 0;
  }
}

function readStep(gs: GroupFit[], ss: SingleFit[]) {
  for (const g of gs) {
    if (!g.active) continue;
    g.text = g.items.reduce((sum, item) => sum + textWidth(item), 0);
    g.ell = g.items.some((item) => ellipsized(item));
  }
  for (const f of ss) {
    if (!f.active) continue;
    f.ell = ellipsized(f.el);
    // Only pay for the width measurement when a shrink is actually possible.
    f.box = f.ell && f.size > f.floor ? contentBox(f.el) : 0;
    if (f.box) f.textWidthCache = textWidth(f.el);
  }
}

/* ---- COMPUTE: pure maths, decides the next write ------------------------ */

function computeStep(gs: GroupFit[], ss: SingleFit[]) {
  for (const g of gs) {
    if (!g.active) continue;
    if (!g.ell || g.size <= g.floor) { g.active = false; continue; }
    // Headroom left for the text: what the rail has minus the fixed parts.
    const target = g.room > 0 && g.text > 0 ? g.base * (g.room / g.text) * RELAX : g.floor;
    const next = Math.max(g.floor, Math.min(g.size - 0.05, target));
    if (!(next < g.size)) { g.active = false; continue; }
    g.size = next;
    g.pending = next;
  }
  for (const f of ss) {
    if (!f.active) continue;
    if (!f.ell || f.size <= f.floor) { f.active = false; continue; }
    const tw = f.textWidthCache || 1;
    const next = Math.max(f.floor, Math.min(f.size - 0.05, f.base * (f.box / tw) * RELAX));
    if (!(next < f.size)) { f.active = false; continue; }
    f.size = next;
    f.pending = next;
  }
}

/** mode "wrap" (data-fit="wrap") falls back to wrapping when the shrink floor
 *  is not enough, so no text is ever lost; one long word in a tab-bar cell
 *  breaks rather than gets cut. Two extra read/write pairs, batched. */
function wrapFallback(ss: SingleFit[]) {
  const wrappers = ss.filter((f) => f.mode === 'wrap');
  if (!wrappers.length) return;
  for (const f of wrappers) f.ell = ellipsized(f.el);
  const first = wrappers.filter((f) => f.ell);
  if (!first.length) return;
  for (const f of first) f.el.style.setProperty('white-space', 'normal', 'important');
  for (const f of first) f.ell = ellipsized(f.el);
  for (const f of first) {
    if (f.ell) f.el.style.setProperty('overflow-wrap', 'anywhere', 'important');
  }
}

/** True while any rail still shows an ellipsized item. A 1px tolerance here on
 *  purpose: the row must only take the compact shape for a *visible* cut, not
 *  for the sub-pixel rounding every long label has. */
function railClipped(): boolean {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-fit-group]')).some((group) =>
    Array.from(group.querySelectorAll<HTMLElement>('[data-fit-item]')).some((item) => ellipsized(item, 1)));
}

/** True while a rail had to shrink past SQUEEZE_AT of its base size to fit.
 *  Nine-pixel links are not a fit, they are a surrender — the row gives them
 *  room instead (icon-only wallet button, tighter gaps). */
function railSqueezed(): boolean {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-fit-group]')).some((group) => {
    const base = pxOf(group.getAttribute(FIT_BASE), 0);
    const size = pxOf(group.getAttribute(FIT_SIZE), 0);
    return base > 0 && size > 0 && size < base * SQUEEZE_AT;
  });
}

/** Does the row have to give its rail more room? */
function rowOutOfRoom(): boolean {
  return railClipped() || railSqueezed();
}

/** One relax: two full rounds (rails + labels) plus a rails-only round, the
 *  same pass count the per-element loops used. Rounds restart from the CSS
 *  base size, so a pass stays idempotent and labels can grow back. */
function relax(gs: GroupFit[], ss: SingleFit[]) {
  for (let round = 0; round < 2; round += 1) {
    resetGroups(gs);
    resetSingles(ss);
    measureGroups(gs);
    measureSingles(ss);
    for (let step = 0; step < MAX_STEPS; step += 1) {
      if (!gs.some((g) => g.active) && !ss.some((f) => f.active)) break;
      readStep(gs, ss);
      computeStep(gs, ss);
      writePending(gs, ss);
    }
    wrapFallback(ss);
  }
  resetGroups(gs);
  measureGroups(gs);
  for (let step = 0; step < MAX_STEPS; step += 1) {
    if (!gs.some((g) => g.active)) break;
    readStep(gs, []);
    computeStep(gs, []);
    writePending(gs, []);
  }
  // Only rails that were actually measured carry a settled size; a hidden rail
  // (nav below its breakpoint) must not advertise 0.00 — railSqueezed() reads
  // this attribute and 0 would mask a real squeeze from an earlier pass.
  for (const g of gs) {
    if (g.base > 0) g.container.setAttribute(FIT_SIZE, g.size.toFixed(2));
  }
}

/**
 * Run one fit pass over the whole document.
 *
 * Two layers, both measurement-driven:
 *   · the labels shrink their type (never below their floor), and
 *   · if a rail is *still* clipped at its floor, its row takes the compact
 *     shape instead of living with six ellipsized links — the same shape the
 *     1000–1180px CSS band already uses (icon-only wallet button, tighter
 *     gaps, and on the second step a brand cap the wordmark re-fits into).
 * Every round restarts from the CSS base size, so passes are idempotent and a
 * label can also grow back when a previous round was pessimistic.
 */

export function fitNow() {
  if (typeof document === 'undefined') return;
  const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-fit-row]'));
  const groups = Array.from(document.querySelectorAll<HTMLElement>('[data-fit-group]'));
  const singles = Array.from(document.querySelectorAll<HTMLElement>('[data-fit]'));

  if (!groups.length && !singles.length) return;

  rows.forEach((row) => row.setAttribute(BUSY, ''));

  // 0. every pass starts from the same unfitted state. The rail and the
  //    single labels share one flex row, so a size left over from the previous
  //    pass (e.g. the wordmark at its full size from the tight shape) changes
  //    how much room the other one measures. Starting from leftovers made the
  //    roomy decision depend on the previous result: the header flipped
  //    between tight=2 (wordmark clipped) and roomy (all six nav labels
  //    ellipsized) forever at 1200px in French, every ResizeObserver tick.
  //    Resetting first makes a pass a pure function of the layout, so a
  //    re-run reproduces the same sizes and the observers go quiet.
  singles.forEach((el) => {
    freeze(el);
    el.style.removeProperty('font-size');
    el.style.removeProperty('white-space');
    el.style.removeProperty('overflow-wrap');
  });
  groups.forEach((group) => group.querySelectorAll<HTMLElement>('[data-fit-item]').forEach((item) => {
    freeze(item);
    item.style.removeProperty('font-size');
  }));

  // 1. the roomy state: how the row looks with everything it has
  rows.forEach((row) => row.removeAttribute(TIGHT));
  const gStates = groups.map(makeGroupFit).filter((g): g is GroupFit => g !== null);
  const sStates = singles.map(makeSingleFit);
  relax(gStates, sStates);

  // 2. a rail that cannot fit — or that had to shrink past its squeeze point —
  //    means the row is out of room. Tighten one step at a time and stop as
  //    soon as the rail is comfortable, so the row never takes a harsher shape
  //    than the current language actually needs. The decision is always taken
  //    from the roomy measurement, so it is stable from pass to pass.
  for (let level = 1; level <= MAX_TIGHT && rowOutOfRoom(); level += 1) {
    rows.forEach((row) => row.setAttribute(TIGHT, String(level)));
    relax(gStates, sStates);
  }
  thawSoon();
}

let queued = false;

/**
 * Schedule a pass after the current frame, so it always measures the DOM the
 * language switch (or the island render) has already produced.
 */
export function scheduleFit() {
  if (typeof window === 'undefined' || queued) return;
  queued = true;
  const run = () => { queued = false; fitNow(); };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => requestAnimationFrame(run));
  else setTimeout(run, 0);
}

let started = false;

/** Called by the layout once, on the client. Safe to call repeatedly. */
export function initFit() {
  if (started || typeof document === 'undefined') return;
  started = true;

  fitNow();
  onLangChange(() => scheduleFit());

  try {
    // The language switcher writes <html data-lang>; so does the static-shell
    // bundle and any other tab, which is why this is watched in addition to
    // onLangChange(). Theme changes can alter font stacks, so they re-fit too.
    new MutationObserver(scheduleFit).observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-lang', 'data-theme', 'class', 'style'],
    });
  } catch { /* no MutationObserver — the other triggers still cover us */ }

  try {
    // Islands, the tab-bar portal and any sheet that mounts later.
    new MutationObserver(scheduleFit).observe(document.body, { childList: true, subtree: true });
  } catch { /* same */ }

  try {
    if (typeof ResizeObserver === 'function') {
      const ro = new ResizeObserver(scheduleFit);
      const observe = () => {
        document.querySelectorAll<HTMLElement>('[data-fit-row], [data-fit-group], [data-fit]').forEach((el) => ro.observe(el));
      };
      observe();
      // Newly mounted labels (tab bar, product grids) join the observer too.
      onLangChange(observe);
      setTimeout(observe, 1500);
    }
  } catch { /* ResizeObserver optional */ }

  window.addEventListener('resize', scheduleFit, { passive: true });
  window.addEventListener('orientationchange', () => setTimeout(scheduleFit, 150), { passive: true });
  window.addEventListener('pageshow', scheduleFit);
  document.addEventListener('visibilitychange', scheduleFit);

  try {
    const vv = (window as any).visualViewport;
    if (vv && vv.addEventListener) vv.addEventListener('resize', scheduleFit, { passive: true });
  } catch { /* optional */ }

  try {
    const fonts: any = (document as any).fonts;
    if (fonts) {
      if (fonts.ready && fonts.ready.then) fonts.ready.then(() => scheduleFit());
      if (fonts.addEventListener) fonts.addEventListener('loadingdone', () => scheduleFit());
    }
  } catch { /* optional */ }
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => { initFit(); }, { once: true });
  } else {
    initFit();
  }
}
