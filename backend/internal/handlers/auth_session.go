package handlers

import (
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/auth"
	"nimiqshop/internal/middleware"
)

// Customer session lifecycle over an HttpOnly cookie.
//
// These three helpers are the whole of the browser-facing change that moves the
// JWT out of localStorage: how the cookie attributes are resolved, how a page
// load asks "am I still signed in", and how a sign-out actually ends the
// session. The login handler in auth_handlers.go uses the same options so the
// cookie that is set and the cookie that is cleared cannot disagree about their
// attributes — if they do, the browser treats them as two different cookies and
// logout silently stops working.

// sessionCookieOptions resolves the cookie attributes from config.
func (h *Handlers) sessionCookieOptions() middleware.SessionCookieOptions {
	return middleware.SessionCookieOptions{
		Secure:     h.Cfg.SessionCookieSecure,
		SameSite:   h.Cfg.SessionCookieSameSite,
		Domain:     h.Cfg.SessionCookieDomain,
		MaxAgeSecs: h.Cfg.JWTExpiryMins * 60,
	}
}

// wantsBearerDelivery reports whether the caller explicitly asked for the JWT
// in the response body instead of only in a cookie.
//
// The default is cookie-only, and that default is the security property. If the
// login response always carried the token, then anything able to observe that
// response — a malicious browser extension, a compromised analytics tag that
// wraps window.fetch, script injected at exactly the wrong moment — would still
// walk away with a seven-day bearer credential, and the HttpOnly flag would
// have bought nothing. Opt-in delivery keeps the credential out of the body for
// every browser while leaving server-to-server and mobile clients, which have no
// cookie jar and cannot use the session at all otherwise, a supported path.
//
// This is safe to expose because obtaining a session in the first place
// requires a Nimiq Hub signature over a server-issued nonce. An attacker cannot
// call hub-login for a victim and read the resulting token: they cannot produce
// the signature. What the header changes is only where an *already earned*
// token is delivered, to the caller that earned it.
func wantsBearerDelivery(ctx *fasthttp.RequestCtx) bool {
	v := strings.TrimSpace(string(ctx.Request.Header.Peek("X-Token-Delivery")))
	return strings.EqualFold(v, "bearer")
}

// GET /api/auth/session
//
// Reports the caller's own session state. This endpoint exists because the JWT
// is now HttpOnly: the page can no longer decode it out of storage to learn
// whether the shopper is signed in, what address they signed in with, or when
// the session ends, so it has to ask. It runs on every page load, which is why
// it is behind the refresh rate tier rather than an unthrottled one.
//
// It always answers 200. A signed-out or expired caller is not an error
// condition — it is the normal state of an anonymous visitor — and making it
// one would turn every first page view into a failed request that the frontend
// has to special-case.
func (h *Handlers) AuthSession(ctx *fasthttp.RequestCtx) {
	raw, _ := middleware.SessionToken(ctx)
	if raw == "" {
		writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"authed": false})
		return
	}

	claims, err := middleware.ParseTokenBounded(h.Cfg.JWTSecret, raw)
	if err != nil {
		// Self-healing. An expired or invalid cookie is cleared so the browser
		// stops sending it on every subsequent request, which is otherwise a
		// permanent source of dead weight and confusing 401s.
		middleware.ClearSession(ctx, h.sessionCookieOptions())
		writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"authed": false, "reason": "expired"})
		return
	}

	// The token is cryptographically valid, but the account behind it may not
	// exist any more (restored snapshot, wiped store, deleted user). Trusting
	// the claims alone would show a signed-in shell whose every subsequent
	// write fails, so the account is checked here.
	user, err := h.Store.GetUser(claims.UserID)
	if err != nil {
		middleware.ClearSession(ctx, h.sessionCookieOptions())
		writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"authed": false, "reason": "unknown_account"})
		return
	}

	// SLIDING RENEWAL. The cookie used to be issued once, at login, and simply
	// ran out — so a shopper who used the shop every day was still thrown out
	// on the fixed schedule, which is what "sürekli giriş yapıyorum çıkıyor"
	// describes (owner, 2026-10-06). A session that is being USED should not
	// expire out from under the person using it. This endpoint already runs on
	// every page load, so it is the natural place to renew: once the token has
	// less than half its life left, a fresh one is issued and the cookie
	// replaced. A session that is never used still expires exactly as before.
	//
	// Only the cookie is rotated; nothing else about the session changes, and
	// an invalid or expired token still takes the self-healing path above.
	csrf := middleware.CSRFCookieValue(ctx)
	remaining, renew := sessionNeedsRenewal(claims, h.Cfg.JWTExpiryMins)
	expiresAt := remaining
	if renew {
		if token, terr := auth.IssueToken(h.Cfg.JWTSecret, user.ID, h.Cfg.JWTExpiryMins); terr == nil {
			if fresh, serr := middleware.SetSession(ctx, h.sessionCookieOptions(), token); serr == nil {
				// The renewal rotates the CSRF cookie too, so the value echoed
				// to the page must be the NEW one — returning the request's
				// copy would leave a page that just recovered from storage with
				// a token the server no longer accepts.
				csrf = fresh
				expiresAt = sessionExpiry(h.Cfg.JWTExpiryMins).Unix()
			}
		}
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"authed": true,
		"user": map[string]string{
			"id":            user.ID,
			"nimiq_address": user.NimiqAddress,
		},
		"expires_at": expiresAt,
		// Echoing the CSRF cookie lets a page that lost its in-memory copy (a
		// hard reload mid-session) recover it without re-authenticating.
		"csrf_token": csrf,
	})
}

// POST /api/auth/logout
//
// Ends the session server-side. Before the cookie existed, signing out was a
// localStorage.removeItem and there was nothing for the server to do; now the
// credential lives where page script cannot reach it, so only the server can
// take it away. Without this endpoint a "signed out" shopper would still be
// carrying a valid seven-day session cookie.
//
// Deliberately not behind RequireAuth. A logout must succeed for a caller whose
// token is already expired or malformed — that is exactly when clearing the
// cookie matters most, and gating it on a valid token would make an expired
// session impossible to dismiss. Forcing a logout on someone is also not a
// meaningful attack: it is an annoyance, it grants nothing, and requiring a
// CSRF token here would lock out the very case the endpoint exists for.
func (h *Handlers) AuthLogout(ctx *fasthttp.RequestCtx) {
	middleware.ClearSession(ctx, h.sessionCookieOptions())
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"ok": true})
}

// sessionExpiry is the wall-clock instant the token being issued now will stop
// working. Returned to the client as a unix timestamp so the UI can schedule
// its own sign-out without being able to read the token it no longer holds.
func sessionExpiry(expiryMins int) time.Time {
	return time.Now().Add(time.Duration(expiryMins) * time.Minute)
}

// sessionNeedsRenewal reports whether the presented token has less than half of
// a full session left, and the instant it currently expires.
//
// Half is the right threshold: renewing on every page load would rotate the
// cookie on every view for no benefit, while renewing on the last request
// before expiry would leave a session that ends between page views. At half,
// an active shopper's cookie is refreshed roughly once per half-life and the
// session never lapses while they keep using the shop; an idle one expires on
// schedule, which is the point of an expiry at all.
func sessionNeedsRenewal(claims *auth.Claims, expiryMins int) (int64, bool) {
	if claims == nil || claims.ExpiresAt == nil || expiryMins <= 0 {
		return 0, false
	}
	expires := claims.ExpiresAt.Unix()
	remaining := time.Until(claims.ExpiresAt.Time)
	half := time.Duration(expiryMins) * time.Minute / 2
	return expires, remaining < half
}
