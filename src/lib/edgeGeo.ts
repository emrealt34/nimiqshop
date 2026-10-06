/**
 * edgeGeo.ts — the visitor's country, as Cloudflare's own edge reports it.
 *
 * WHY THIS EXISTS
 *
 * The operator console shows every customer's IP and, next to it, the country
 * the visit came from. The IP is settled server-side from the forwarded chain.
 * The country used to be read from Cloudflare's CF-IPCountry header — but that
 * header does not survive this deployment: the shop sits behind Cloudflare as a
 * proxy while the application runs on an edge that rebuilds the request, so by
 * the time a handler runs there is no CF-* header left at all (verified live:
 * /api/geo answered {"country":""}). The country therefore has to travel on a
 * carrier that does survive, and the browser is the one place that can read
 * Cloudflare's own answer FOR THIS REQUEST: /cdn-cgi/trace is served by the edge
 * itself, on the shop's own origin, and its `loc=` field is exactly the value
 * CF-IPCountry would have carried. Nothing leaves the site: it is a same-origin
 * request to the edge that is already answering the visitor, not a third-party
 * geo API.
 *
 * WHAT TRAVELS
 *
 * One header, X-Nimshop-Country-Hint, holding two upper-case letters — or
 * nothing at all. It is a HINT by construction, and the backend treats it as
 * one: prices, orders, catalogs and the storefront's own country come from the
 * shop's country picker and host, and the server-verified country carrier still
 * wins whenever it is present. Its only consumer is the origin note behind the
 * People panel, and it can never move the resolved visitor IP.
 *
 * COST
 *
 * One small same-origin GET per browsing session: the answer is cached in
 * sessionStorage for 12 h, so a visitor pays for it once. A shop that is not
 * behind Cloudflare simply never parses a trace document, and every failure is
 * silent — a missing country must never be able to break a page or a request.
 */

const CACHE_KEY = 'nq.edge-cc';
const CACHE_TTL_MS = 12 * 60 * 60 * 1000;
const COUNTRY = /^[A-Z]{2}$/;
/** Cloudflare's "unknown" placeholder, never a country. */
const UNKNOWN = 'XX';

/**
 * The header the shop sends the hint in. It lives next to the reader so the two
 * cannot drift apart: the API client and the session client both import it.
 */
export const COUNTRY_HINT_HEADER = 'X-Nimshop-Country-Hint';

let current = '';
let inflight: Promise<string> | null = null;

/** Two upper-case letters, or '' — the only shape that may be sent or stored. */
function valid(raw: unknown): string {
  const cc = String(raw == null ? '' : raw).trim().toUpperCase();
  return COUNTRY.test(cc) && cc !== UNKNOWN ? cc : '';
}

function readCache(): string {
  try {
    const raw = window.sessionStorage.getItem(CACHE_KEY);
    if (!raw) return '';
    const hit = JSON.parse(raw) as { cc?: string; at?: number };
    if (!hit || typeof hit.at !== 'number' || Date.now() - hit.at > CACHE_TTL_MS) return '';
    return valid(hit.cc);
  } catch {
    // Private browsing, a full quota, a hand-edited value: no cache, no hint.
    return '';
  }
}

function writeCache(cc: string): void {
  try {
    window.sessionStorage.setItem(CACHE_KEY, JSON.stringify({ cc, at: Date.now() }));
  } catch {
    /* not remembering the hint is fine; sending it is what matters */
  }
}

/**
 * loadCountryHint — read the edge's country once per browsing session.
 *
 * Resolves to the two-letter code, or to '' when the edge did not answer (the
 * shop is not behind Cloudflare, the network failed, the response was not a
 * trace document). Never rejects: a visitor must not see an error because the
 * operator console could not get a country.
 */
export function loadCountryHint(): Promise<string> {
  if (typeof window === 'undefined') return Promise.resolve('');
  if (current) return Promise.resolve(current);
  if (inflight) return inflight;
  const cached = readCache();
  if (cached) {
    current = cached;
    return Promise.resolve(cached);
  }
  inflight = (async () => {
    try {
      const res = await fetch('/cdn-cgi/trace', { cache: 'no-store', credentials: 'same-origin' });
      if (!res.ok) return '';
      const text = await res.text();
      const m = /^loc=([A-Za-z]{2})$/m.exec(text);
      const cc = m ? valid(m[1]) : '';
      if (cc) {
        current = cc;
        writeCache(cc);
      }
      return cc;
    } catch {
      return '';
    } finally {
      inflight = null;
    }
  })();
  return inflight;
}

/**
 * countryHint — what is known RIGHT NOW, without waiting: '' until the edge has
 * answered (or forever, when it never does). Synchronous so request builders
 * can spread it in without turning into async functions.
 */
export function countryHint(): string {
  return current;
}

/** The hint as a headers object, spreadable into any fetch. */
export function hintHeaders(): Record<string, string> {
  const cc = countryHint();
  return cc ? { [COUNTRY_HINT_HEADER]: cc } : {};
}

// Start the read as soon as anything imports this module (the API client does,
// and so does session restore). The first request of a page load is the one
// that notes a browse-only visitor's origin, so the hint has to be on its way
// before that request is built; it lands in time for everything after it, and
// the heartbeat refreshes the store within the minute either way.
if (typeof window !== 'undefined') void loadCountryHint();
