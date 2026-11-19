/**
 * Safe-area insets, kept current across rotation.
 *
 * `viewport-fit=cover` (Base.astro) lets the page paint under the notch and
 * the home indicator, so the layout depends on `env(safe-area-inset-*)`. Those
 * values live in CSS custom properties (`--sat/--sab/--sal/--sar`, app.css
 * `:root`) which Chromium, WebKit and Firefox only re-resolve on their own
 * when the *style* is invalidated — in practice they keep the values they
 * were computed with until something forces a recalc. Turning the phone swaps
 * which edges are inset (portrait: top+bottom; landscape: left+right), so
 * without a nudge the bottom bar keeps portrait's insets and its first and
 * last tabs land under the notch (owner report, 2026-11-19).
 *
 * The probe element carries one inset per edge as four separate paddings and
 * is read back; the custom properties are then rewritten on <html> (which has
 * a higher specificity than `:root`'s own declarations). The probe sits in a
 * zero-size, clipped, aria-hidden container, so it never paints or scrolls.
 */

type Insets = { top: number; bottom: number; left: number; right: number };

const VARS: Record<keyof Insets, string> = {
  top: '--sat',
  bottom: '--sab',
  left: '--sal',
  right: '--sar',
};

function measure(): Insets | null {
  if (typeof document === 'undefined') return null;
  const host = document.createElement('div');
  host.setAttribute('aria-hidden', 'true');
  host.style.cssText =
    'position:fixed;top:0;left:0;width:0;height:0;overflow:hidden;visibility:hidden;pointer-events:none;contain:strict';
  const probe = document.createElement('div');
  probe.style.cssText =
    'padding-top:env(safe-area-inset-top,0px);padding-bottom:env(safe-area-inset-bottom,0px);' +
    'padding-left:env(safe-area-inset-left,0px);padding-right:env(safe-area-inset-right,0px)';
  host.appendChild(probe);
  document.body.appendChild(host);
  const cs = getComputedStyle(probe);
  const out: Insets = {
    top: parseFloat(cs.paddingTop) || 0,
    bottom: parseFloat(cs.paddingBottom) || 0,
    left: parseFloat(cs.paddingLeft) || 0,
    right: parseFloat(cs.paddingRight) || 0,
  };
  host.remove();
  return out;
}

/** Write the current insets onto <html>. Returns true when they changed. */
export function syncSafeAreaInsets(): boolean {
  if (typeof document === 'undefined') return false;
  const insets = measure();
  if (!insets) return false;
  const root = document.documentElement;
  let changed = false;
  (Object.keys(VARS) as (keyof Insets)[]).forEach((edge) => {
    const value = `${Math.round(insets[edge])}px`;
    if (root.style.getPropertyValue(VARS[edge]) !== value) {
      root.style.setProperty(VARS[edge], value);
      changed = true;
    }
  });
  return changed;
}

/** Keep the insets fresh: rotation, resize, and the on-screen keyboard. */
export function watchSafeAreaInsets(): () => void {
  if (typeof window === 'undefined') return () => {};
  syncSafeAreaInsets();
  let frame = 0;
  const queue = () => {
    if (frame) return;
    frame = window.requestAnimationFrame(() => {
      frame = 0;
      syncSafeAreaInsets();
    });
  };
  window.addEventListener('resize', queue);
  window.addEventListener('orientationchange', queue);
  window.addEventListener('pageshow', queue);
  window.visualViewport?.addEventListener('resize', queue);
  return () => {
    if (frame) window.cancelAnimationFrame(frame);
    window.removeEventListener('resize', queue);
    window.removeEventListener('orientationchange', queue);
    window.removeEventListener('pageshow', queue);
    window.visualViewport?.removeEventListener('resize', queue);
  };
}
