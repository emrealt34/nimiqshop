/**
 * session.ts — client-side session state.
 *
 * WHAT CHANGED AND WHY
 *
 * This module used to keep the session JWT in localStorage and hand it to the
 * API client as an `Authorization: Bearer` header. localStorage is readable by
 * any script on the page, so one XSS — a compromised dependency, a reflected
 * value, a malicious extension with page access — was enough to copy out a
 * bearer credential valid for seven days and use it from anywhere. Possession
 * is authentication for a bearer token, so the attacker needed nothing else:
 * not the password, not the wallet, not a signature.
 *
 * The JWT now lives in an HttpOnly cookie that the server sets at login and
 * that page script cannot read at all. This module therefore no longer holds a
 * credential. What it holds is *session metadata* — the user id, the Nimiq
 * address and the expiry timestamp — none of which authenticates anything. All
 * three are things the page needs in order to render "signed in as NQ…" and to
 * know when to stop, and all three come from the server rather than from
 * decoding a token.
 *
 * Consequences worth knowing when editing this file:
 *
 *   - Sign-in state is authoritative on the server. `bootstrapSession()` asks
 *     `/api/auth/session` on load, because a cookie can exist, expire or be
 *     cleared without this module being told.
 *   - The metadata in localStorage is a cache for rendering, not a source of
 *     truth. If it disagrees with the server, the server wins.
 *   - Sign-out must call the server. Deleting local state used to be enough;
 *     now the credential lives where this code cannot reach, so only
 *     `/api/auth/logout` can actually end the session.
 *   - Any `nimshop.jwt` left in storage by an older build is deleted on load.
 *     Upgrading should not leave a live seven-day bearer token sitting in
 *     localStorage on every returning shopper's browser.
 *
 * The in-memory fallbacks and the cross-tab `storage` synchronisation are
 * retained from the previous implementation: private browsing, embedded wallet
 * WebViews and some iOS contexts still refuse localStorage writes, and a sign-in
 * or sign-out in one tab still has to reach the others.
 */
import { _setSessionGetter, _setCSRFGetter } from './api';
import { CFG } from './config';

/** Non-secret session metadata. Nothing here can authenticate a request. */
export interface SessionInfo {
  /** The user id, i.e. the JWT `uid` claim. Used as the checkout owner scope. */
  uid: string;
  /** Nimiq address, for display. A public blockchain identifier, not a secret. */
  address: string;
  /** Unix seconds at which the server-side session stops being valid. */
  expiresAt: number;
}

const SESSION_KEY = 'nimshop.sess';
const ADDR_KEY = 'nimshop.addr';
/** Legacy keys, purged on load. See the header comment. */
const LEGACY_TOKEN_KEY = 'nimshop.jwt';
/** The double-submit CSRF cookie, set by the server as script-readable. */
const CSRF_COOKIE = 'nimshop_csrf';

/**
 * apiBase resolves the API root the same way api.ts does, from the runtime
 * config in /config.js. It matters that the two agree: the session cookie is
 * scoped to the API origin, so pointing logout or bootstrap at a different
 * base would send them somewhere the cookie is not attached and silently do
 * nothing.
 */
function apiBase(): string {
  return String((CFG && CFG.API_BASE) || '/api').replace(/\/$/, '');
}

function credentialsFor(base: string): RequestCredentials {
  // Same-origin needs no explicit opt-in; a cross-origin API base does, and
  // the server answers it with Access-Control-Allow-Credentials.
  return base.startsWith('/') ? 'same-origin' : 'include';
}

let memory: SessionInfo | null = null;
let memoryAddr = '';

function readStored(): SessionInfo | null {
  try {
    const raw = localStorage.getItem(SESSION_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<SessionInfo>;
    if (!parsed || typeof parsed !== 'object') return null;
    const info: SessionInfo = {
      uid: String(parsed.uid || ''),
      address: String(parsed.address || ''),
      expiresAt: Number(parsed.expiresAt || 0),
    };
    return info.uid || info.address ? info : null;
  } catch {
    return null;
  }
}

function writeStored(info: SessionInfo | null): void {
  try {
    if (info) {
      localStorage.setItem(SESSION_KEY, JSON.stringify(info));
      if (info.address) localStorage.setItem(ADDR_KEY, info.address);
    } else {
      localStorage.removeItem(SESSION_KEY);
      localStorage.removeItem(ADDR_KEY);
    }
  } catch {
    /* private mode: the in-memory copy below still works */
  }
}

/** Remove credentials an older build may have persisted. */
function purgeLegacyToken(): void {
  try {
    localStorage.removeItem(LEGACY_TOKEN_KEY);
  } catch {
    /* ignore */
  }
}

function effective(): SessionInfo | null {
  return readStored() || memory;
}

/**
 * csrfToken reads the double-submit CSRF cookie.
 *
 * Exported because the API client has to echo it in `X-CSRF-Token` on every
 * state-changing request. It is readable by design — the server sets that
 * cookie without HttpOnly precisely so this can happen — and it is not a
 * credential on its own: it authenticates nothing without the session cookie,
 * which script cannot read. A cross-site attacker can make the browser send
 * both cookies but cannot read this one to fill in the header, which is the
 * whole point of the scheme.
 */
export function csrfToken(): string {
  if (typeof document === 'undefined') return '';
  const parts = document.cookie ? document.cookie.split(';') : [];
  for (const part of parts) {
    const i = part.indexOf('=');
    if (i < 0) continue;
    if (part.slice(0, i).trim() === CSRF_COOKIE) {
      return decodeURIComponent(part.slice(i + 1).trim());
    }
  }
  return '';
}

export function isAuthed(): boolean {
  const info = effective();
  if (!info) return false;
  if (info.expiresAt && info.expiresAt * 1000 <= Date.now()) {
    // Locally expired. Drop the cached state; the server will confirm on the
    // next bootstrap and clear the cookie itself.
    signOut(true);
    return false;
  }
  return true;
}

export function getAddress(): string {
  const info = effective();
  return (info && info.address) || memoryAddr || '';
}

/**
 * getUid returns the user id, which is what the checkout idempotency scope is
 * keyed on. It replaces decoding the JWT client-side: the value is the same
 * `uid` claim the server used to put in the token, now delivered as plain data,
 * so previously stored checkout intents keep resolving to the same key.
 */
export function getUid(): string {
  const info = effective();
  return (info && info.uid) || '';
}

/**
 * saveSession records the session metadata returned by the server at login.
 *
 * The signature changed when the token moved into a cookie: there is no token
 * to store any more. Callers pass what `/api/auth/hub-login` returns.
 */
export function saveSession(info: Partial<SessionInfo>): void {
  const next: SessionInfo = {
    uid: String(info.uid || ''),
    address: String(info.address || ''),
    expiresAt: Number(info.expiresAt || 0),
  };
  memory = next;
  memoryAddr = next.address || memoryAddr;
  // Storage first, but never fatally: a WebView with storage disabled must
  // still get a working in-memory session (owner: the session kept "falling
  // out" in Nimiq Pay). writeStored already swallows storage errors.
  writeStored(next);
  scheduleExpiry();
  window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: true, address: next.address } }));
}

/**
 * signOut ends the session.
 *
 * The server call is not optional. The credential is an HttpOnly cookie this
 * module cannot touch, so clearing local state alone would leave the shopper
 * carrying a valid seven-day session while the UI claims they signed out — a
 * shared-machine exposure and exactly the kind of thing a "logout" button is
 * trusted to prevent. The local state is cleared regardless of whether the
 * request succeeds: if the network is down the UI must still reflect the
 * shopper's intent, and the cookie will be rejected or expire on its own.
 */
export function signOut(silent = false): void {
  writeStored(null);
  memory = null;
  memoryAddr = '';
  if (expiryTimer) { clearTimeout(expiryTimer); expiryTimer = null; }
  if (typeof window !== 'undefined') {
    // Fire and forget: a failed logout call must not block the UI, and
    // retrying it would only matter on a machine the user is walking away
    // from, where the expired-or-rejected cookie is the backstop.
    try {
      const base = apiBase();
      fetch(base + '/auth/logout', {
        method: 'POST',
        credentials: credentialsFor(base),
        headers: csrfToken() ? { 'X-CSRF-Token': csrfToken() } : {},
        cache: 'no-store',
      }).catch(() => {});
    } catch {
      /* ignore */
    }
  }
  if (!silent) window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false } }));
}

/**
 * confirmSessionEnded — decide whether a 401 really means "you are signed out".
 *
 * One failed request is not evidence. The server is asked once (with a short
 * cooldown so a burst of 401s cannot stampede it) and its answer is final:
 *
 *   authed: true  → the session is alive; the 401 belonged to that one call.
 *   authed: false → the session is over; the local state is dropped and the UI
 *                   is told why, so the sign-in card can explain itself.
 *   no answer     → keep the session. Being offline is not being signed out.
 */
let sessionCheck: Promise<boolean> | null = null;
let lastSessionCheck = 0;

async function confirmSessionEnded(): Promise<boolean> {
  const now = Date.now();
  if (sessionCheck) return sessionCheck;
  if (now - lastSessionCheck < 4000) return true; // recently verified: keep it
  lastSessionCheck = now;
  sessionCheck = (async () => {
    try {
      const base = apiBase();
      const res = await fetch(base + '/auth/session', {
        method: 'GET',
        credentials: credentialsFor(base),
        cache: 'no-store',
      });
      if (!res.ok) return true; // server problem, not an expired session
      const data = (await res.json()) as { authed?: boolean; reason?: string };
      if (data && data.authed) {
        // Still signed in: refresh the cached metadata the 401 may have made
        // look stale, and say nothing to the user — nothing happened.
        const info = effective();
        if (info) saveSession(info);
        return true;
      }
      signOut(true);
      window.dispatchEvent(new CustomEvent('nimshop:session', {
        detail: { authed: false, expired: data?.reason === 'expired' },
      }));
      return false;
    } catch {
      return true;
    } finally {
      window.setTimeout(() => { sessionCheck = null; }, 0);
    }
  })();
  return sessionCheck;
}

// ---- initial state --------------------------------------------------------

purgeLegacyToken();
memory = readStored();
memoryAddr = (() => {
  try { return localStorage.getItem(ADDR_KEY) || ''; } catch { return ''; }
})();

// The API client reads the double-submit CSRF token through this getter rather
// than importing it, keeping the dependency one-directional.
_setCSRFGetter(csrfToken);

// The API client asks this for the checkout owner scope. It used to return the
// JWT, which was then decoded to extract `uid`; it now returns the uid
// directly, so the scope string — and therefore every stored intent key — is
// byte-for-byte what it was before.
_setSessionGetter(() => {
  const info = effective();
  if (!info || !info.uid) return null;
  if (info.expiresAt && info.expiresAt * 1000 < Date.now()) {
    signOut(true);
    window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false, expired: true } }));
    return null;
  }
  return info.uid;
});

// ---- expiry scheduling ----------------------------------------------------

let expiryTimer: ReturnType<typeof setTimeout> | null = null;

function scheduleExpiry(): void {
  if (expiryTimer) {
    clearTimeout(expiryTimer);
    expiryTimer = null;
  }
  const info = effective();
  if (!info || !info.expiresAt) return;
  const ms = info.expiresAt * 1000 - Date.now();
  if (ms <= 0) {
    signOut(true);
    return;
  }
  const at = info.expiresAt;
  const expire = () => {
    // A newer login may have replaced this session; never sign out the
    // replacement because the old one expired.
    const now = effective();
    if (!now || now.expiresAt !== at) return;
    signOut(true);
    window.dispatchEvent(new CustomEvent('nimshop:session', { detail: { authed: false, expired: true } }));
  };
  expiryTimer = setTimeout(expire, Math.min(ms + 500, 2 ** 31 - 1));
}
scheduleExpiry();

/**
 * bootstrapSession asks the server what the session state actually is.
 *
 * Called on load. The cached metadata can be wrong in both directions — the
 * cookie may have been cleared by the browser or by a sign-out in another
 * profile, or it may still be valid after this module's cache was lost in
 * private browsing — and only the server can settle it. This is also how a
 * returning shopper ends up with the CSRF token in memory.
 */
export async function bootstrapSession(): Promise<boolean> {
  if (typeof window === 'undefined') return false;
  try {
    const base = apiBase();
    const res = await fetch(base + '/auth/session', {
      method: 'GET',
      credentials: credentialsFor(base),
      cache: 'no-store',
    });
    if (!res.ok) return isAuthed();
    const data = (await res.json()) as { authed?: boolean; user?: { id?: string; nimiq_address?: string }; expires_at?: number };
    if (!data || !data.authed) {
      if (effective()) signOut(true);
      return false;
    }
    saveSession({
      uid: String(data.user?.id || ''),
      address: String(data.user?.nimiq_address || ''),
      expiresAt: Number(data.expires_at || 0),
    });
    return true;
  } catch {
    // Offline or blocked: fall back to the cached metadata rather than
    // signing the shopper out because one request failed.
    return isAuthed();
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key !== null && event.key !== SESSION_KEY && event.key !== ADDR_KEY) return;
    memory = readStored();
    try { memoryAddr = localStorage.getItem(ADDR_KEY) || ''; } catch { memoryAddr = ''; }
    scheduleExpiry();
    window.dispatchEvent(new CustomEvent('nimshop:session', {
      detail: { authed: isAuthed(), address: getAddress() },
    }));
  });
  // A single 401 no longer ends the session by itself.
  //
  // Owner (2026-10-06): "sürekli giriş yapıyorum çıkıyor" — the shop kept
  // signing people out. The old handler took ANY 401 from ANY authenticated
  // call as proof the session was over and wiped the local state immediately,
  // so one failing endpoint (a display-only probe, a deploy in progress, a
  // cookie the browser had not attached yet) was enough to log someone out
  // mid-shop. Only the server can say whether a session is over, and
  // /api/auth/session answers exactly that question — so it is asked first.
  // If it cannot be reached, the session is KEPT: an unreachable server is not
  // an expired session, and the failing call still reports its own error.
  window.addEventListener('nimshop:unauthorized', () => {
    void confirmSessionEnded();
  });
  // Ask the server on first load only when a returning session or CSRF cookie
  // exists — anonymous visitors need zero /api/auth/session round-trip.
  const maybeBootstrap = () => {
    if (effective() || memoryAddr || csrfToken()) void bootstrapSession();
  };
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', maybeBootstrap, { once: true });
  } else {
    maybeBootstrap();
  }
}

/** React-friendly subscription for session changes. Returns an unsubscribe fn. */
export function subscribeSession(cb: (detail: { authed: boolean; address?: string; expired?: boolean }) => void): () => void {
  const handler = (e: Event) => {
    const ev = e as CustomEvent;
    cb(ev.detail || {});
  };
  window.addEventListener('nimshop:session', handler);
  return () => window.removeEventListener('nimshop:session', handler);
}
