/* session.js — client-side session state (JWT bearer token + address).
 *
 * Storage: localStorage. See SECURITY_ANALYSIS.md for the trade-off
 * discussion (any same-origin XSS could read it; the server-side controls —
 * per-user ownership checks, rate limits, short challenge lifetimes — are the
 * primary defense, and the backend never trusts this token beyond identity).
 */
import { _setSessionGetter } from './api.js';

const TOKEN_KEY = 'nimshop.jwt';
const ADDR_KEY = 'nimshop.addr';

function rawToken() {
  try { return localStorage.getItem(TOKEN_KEY); } catch { return null; }
}

function jwtExp(token) {
  try {
    const part = token.split('.')[1];
    const json = JSON.parse(atob(part.replace(/-/g, '+').replace(/_/g, '/')));
    return json.exp ? json.exp * 1000 : 0;
  } catch { return 0; }
}

export function isAuthed() {
  const t = rawToken();
  if (!t) return false;
  const exp = jwtExp(t);
  if (exp && exp < Date.now()) {
    signOut(true);
    return false;
  }
  return true;
}
export function getAddress() {
  try { return localStorage.getItem(ADDR_KEY) || ''; } catch { return ''; }
}

export function saveSession(token, address) {
  try {
    localStorage.setItem(TOKEN_KEY, token);
    if (address) localStorage.setItem(ADDR_KEY, address);
  } catch { /* private mode: keep in-memory fallback below */ }
  memoryToken = token;
  memoryAddr = address;
  window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: true, address } }));
}

export function signOut(silent = false) {
  try {
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(ADDR_KEY);
  } catch { /* ignore */ }
  memoryToken = null;
  memoryAddr = '';
  if (!silent) window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false } }));
}

/* In-memory fallback when localStorage is unavailable */
let memoryToken = rawToken();
let memoryAddr = getAddress();

function effectiveToken() {
  return rawToken() || memoryToken;
}

_setSessionGetter(() => {
  const t = effectiveToken();
  if (!t) return null;
  const exp = jwtExp(t);
  if (exp && exp < Date.now()) {
    signOut(true);
    window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false, expired: true } }));
    return null;
  }
  return t;
});

/* Auto-invalidate exactly when the JWT expires */
function scheduleExpiry() {
  const t = effectiveToken();
  if (!t) return;
  const exp = jwtExp(t);
  if (!exp) return;
  const ms = exp - Date.now();
  if (ms <= 0) { signOut(true); return; }
  setTimeout(() => {
    signOut(true);
    window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false, expired: true } }));
  }, Math.min(ms + 500, 2 ** 31 - 1));
}
scheduleExpiry();

/* If any authenticated call gets a 401, drop the session and re-render. */
window.addEventListener('nimshop:unauthorized', () => {
  signOut(true);
  window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false, expired: true } }));
});

/* ---------------- Stored delivery email ----------------
 * One copy of the buyer's saved email, shared by every checkout path
 * (product page, home, cart). Used to prefill the delivery step; only ever
 * sent for email-delivered products (gift cards / eSIMs) — never top-ups.
 */
export function getStoredEmail() { try { return localStorage.getItem('nimshop_email') || ''; } catch { return ''; } }
export function setStoredEmail(email) { try { localStorage.setItem('nimshop_email', email); } catch {} }
