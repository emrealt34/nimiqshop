/**
 * A staking-wallet link that arrives as a URL.
 *
 *   https://shop.nimiqbase.com/cashback?link_stake=NQ73…
 *
 * The URL says WHICH wallet to link and nothing more. It is captured once,
 * kept in sessionStorage so it survives the sign-in hand-off (a phone's Hub
 * round trip, or a Nimiq Pay in-app browser reload), and removed from the
 * address bar. It never links anything by itself: the buyer still signs in,
 * still taps "Link", and Hub still asks the stake wallet to sign. A link that
 * linked on arrival would let anyone who can send a URL make a buyer's account
 * claim a wallet the buyer never chose to attach.
 */

const KEY = 'nimshop.stakeLinkIntent';
const PARAM = 'link_stake';

/** The canonical Nimiq address form the backend stores (unspaced, upper case). */
const ADDRESS_RE = /^NQ\d{2}[0-9A-HJ-NP-VXY]{32}$/;

/** Accepts the spaced display form too; returns the canonical form or ''. */
export function normalizeStakeAddress(raw: string | null | undefined): string {
  const canonical = String(raw || '').replace(/\s+/g, '').toUpperCase();
  return ADDRESS_RE.test(canonical) ? canonical : '';
}

/**
 * Read `?link_stake=` from the current URL, remember it, and clean the URL.
 * Returns the stored intent (possibly from an earlier capture) or ''.
 * A malformed value is dropped silently rather than shown as an error: a
 * damaged link should land the buyer on the normal page, not a red banner.
 */
export function captureStakeLinkIntent(): string {
  if (typeof window === 'undefined') return '';
  const url = new URL(window.location.href);
  if (url.searchParams.has(PARAM)) {
    const address = normalizeStakeAddress(url.searchParams.get(PARAM));
    url.searchParams.delete(PARAM);
    try {
      window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
    } catch {
      /* a locked-down embed may refuse replaceState; the intent is still stored */
    }
    if (address) {
      try { sessionStorage.setItem(KEY, address); } catch { /* private mode: best effort */ }
      return address;
    }
  }
  return peekStakeLinkIntent();
}

export function peekStakeLinkIntent(): string {
  try {
    return normalizeStakeAddress(sessionStorage.getItem(KEY));
  } catch {
    return '';
  }
}

export function clearStakeLinkIntent(): void {
  try { sessionStorage.removeItem(KEY); } catch { /* nothing stored */ }
}
