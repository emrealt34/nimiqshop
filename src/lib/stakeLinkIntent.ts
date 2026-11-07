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
 *
 * The file also holds the address comparison and the signer choice for the link
 * flow. They live here rather than in lib/hub.ts so they are pure: no window, no
 * provider, no network — which is what lets scripts/test-stake-link-host.mjs pin
 * them on every push instead of only in a browser.
 */

const KEY = 'nimshop.stakeLinkIntent';
const PARAM = 'link_stake';

/** The canonical Nimiq address form the backend stores (unspaced, upper case). */
const ADDRESS_RE = /^NQ\d{2}[0-9A-HJ-NP-VXY]{32}$/;

/**
 * Comparison form of an address: no spaces, upper case, NO validation.
 *
 * Validation and comparison are different questions. `normalizeStakeAddress`
 * answers "is this a Nimiq address the backend can store"; this answers "are
 * these two strings the same wallet". A host that reports an address in a form
 * this file does not recognise still has to be comparable, or the shop would
 * refuse a wallet it can perfectly well read.
 */
export function canonicalStakeAddress(raw: string | null | undefined): string {
  return String(raw || '').replace(/\s+/g, '').toUpperCase();
}

/** Accepts the spaced display form too; returns the canonical form or ''. */
export function normalizeStakeAddress(raw: string | null | undefined): string {
  const canonical = canonicalStakeAddress(raw);
  return ADDRESS_RE.test(canonical) ? canonical : '';
}

/** True when both sides name a real wallet and it is the same one. */
export function sameStakeAddress(a: string | null | undefined, b: string | null | undefined): boolean {
  const left = canonicalStakeAddress(a);
  return left !== '' && left === canonicalStakeAddress(b);
}

/**
 * Which of the host wallet's accounts signs a link — the decision that keeps
 * Nimiq Pay and Nimiq Hub from being asked for something only the other can do.
 *
 * Hub shows a picker, so an expected address is a hint it can honour and the
 * signed result reports who actually signed. Nimiq Pay's provider signs with
 * the account it holds: `sign(message)` takes no address. So when a link names
 * a wallet, that wallet has to be one the host really holds — guessing is
 * worse than refusing, because the signature comes back valid, the link is
 * recorded, and the buyer only learns it was the wrong wallet when his stake
 * never moves the rate.
 *
 * Returns the account to sign with, in the host's own spelling (that is the
 * string the backend is sent), or '' when the host cannot produce one. Without
 * an expectation the first account is the choice — the same one login makes.
 */
export function pickHostSigner(held: readonly string[] | null | undefined, expected?: string | null): string {
  const accounts = (Array.isArray(held) ? held : [])
    .map((account) => String(account || '').trim())
    .filter(Boolean);
  if (!accounts.length) return '';
  const want = canonicalStakeAddress(expected);
  if (!want) return accounts[0];
  return accounts.find((account) => canonicalStakeAddress(account) === want) || '';
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
