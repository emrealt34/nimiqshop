/**
 * debugFlags.ts — who is allowed to see on-screen diagnostics.
 *
 * Owner (2026-10-25): "Bir de 'Balance debug' satırı kullanıcıya açık
 * görünüyor." The wallet strip used to print its entire reading —
 * `inPay=… signedIn=…`, the wallet address, every host account, the luna
 * figures, error strings — in a `<details>` block for every buyer running
 * inside Nimiq Pay. That is internal bookkeeping: it tells a shopper nothing
 * about their purchase, and it puts wallet identifiers on a screen that may be
 * sharing/screenshotting.
 *
 * Diagnostics are still needed when a balance is reported wrong (that is how
 * the 2026-10-05 and 2026-10-25 reports were diagnosed), so they are GATED
 * rather than deleted:
 *
 *   ?nimshopDebug=1        in the URL (works on a phone inside Nimiq Pay,
 *                          where there may be no console to open)
 *   localStorage['nimshop.debug'] = '1'   sticky, per browser, opt-in
 *
 * Nothing here is a security boundary — it only decides what the shop
 * VOLUNTEERS on screen. Support can hand a buyer the URL and ask for the
 * screenshot; nobody sees it by accident.
 */

/** The URL parameter that turns diagnostics on for one visit. */
export const DEBUG_PARAM = 'nimshopDebug';

/** The opt-in key that turns diagnostics on until it is cleared. */
export const DEBUG_STORAGE_KEY = 'nimshop.debug';

/**
 * True only when this visitor explicitly asked for diagnostics. Safe to call
 * during SSR and in the first client paint (it never touches the network and
 * swallows storage errors from private mode).
 */
export function debugEnabled(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    const params = new URLSearchParams(window.location.search);
    const flag = (params.get(DEBUG_PARAM) || '').toLowerCase();
    if (flag === '1' || flag === 'true') return true;
  } catch {
    /* no location: fall through to storage */
  }
  try {
    return String(localStorage.getItem(DEBUG_STORAGE_KEY) || '').toLowerCase() === '1';
  } catch {
    /* private mode / disabled storage: diagnostics stay off */
    return false;
  }
}
