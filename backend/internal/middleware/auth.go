package middleware

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/auth"
)

const userIDContextKey = "user_id"

// authViaCookieContextKey records whether the accepted credential arrived in a
// cookie. Handlers need it for two reasons: a logout must clear cookies (and
// only cookies, if that is how the caller authenticated), and a response that
// re-issues a session must know whether it is talking to a browser or to a
// server-to-server client that wants the token in the body.
const authViaCookieContextKey = "auth_via_cookie"

// AuthOptions carries what the auth middleware needs beyond the signing
// secret. It exists so that adding a knob does not mean changing the signature
// of every call site again.
type AuthOptions struct {
	// AllowedOrigins mirrors the CORS allowlist. It is used only for the
	// Origin check on cookie-authenticated writes; see session.go.
	AllowedOrigins []string
	// AllowTunnels permits https://*.trycloudflare.com, matching the CORS
	// behaviour so a preview deployment is not half-working.
	AllowTunnels bool
}

func (o AuthOptions) csrf() CSRFVerifier {
	return CSRFVerifier(o)
}

// AuthedViaCookie reports whether the credential accepted on this request came
// from the session cookie rather than an Authorization header.
func AuthedViaCookie(ctx *fasthttp.RequestCtx) bool {
	v, _ := ctx.UserValue(authViaCookieContextKey).(bool)
	return v
}

// RequireAuth validates the session credential and stashes the user id on the
// fasthttp.RequestCtx for downstream handlers to read via UserID(ctx).
//
// The credential may arrive either as a cookie or as an Authorization: Bearer
// header. Cookie delivery is what browsers use and is the reason the JWT is no
// longer exposed to page script; Bearer delivery is kept because mobile and
// integration clients have no cookie jar and because it is how every existing
// API consumer and test authenticates.
//
// When the credential came from a cookie and the method can change state, the
// request must also pass CSRF verification. That check is skipped for Bearer
// callers: CSRF is an attack on credentials the browser attaches by itself, and
// a header the client chose to send is not one. See session.go for the full
// argument and the three layers involved.
func RequireAuth(secret string, opts AuthOptions, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	verifier := opts.csrf()
	return func(ctx *fasthttp.RequestCtx) {
		raw, fromCookie := SessionToken(ctx)
		if raw == "" {
			ctx.Error(`{"error":"missing authentication"}`, fasthttp.StatusUnauthorized)
			return
		}

		claims, err := ParseTokenBounded(secret, raw)
		if err != nil {
			ctx.Error(`{"error":"invalid or expired token"}`, fasthttp.StatusUnauthorized)
			return
		}

		if fromCookie && IsStateChanging(ctx.Method()) {
			if err := verifier.Check(ctx); err != nil {
				// 403 rather than 401: the caller IS authenticated, the
				// request is just not one this session may make from here.
				// The body deliberately does not say which layer failed,
				// because that would let an attacker probe the Origin
				// allowlist one guess at a time.
				ctx.Error(`{"error":"cross-site request rejected"}`, fasthttp.StatusForbidden)
				return
			}
		}

		ctx.SetUserValue(userIDContextKey, claims.UserID)
		ctx.SetUserValue(authViaCookieContextKey, fromCookie)
		next(ctx)
	}
}

func UserID(ctx *fasthttp.RequestCtx) string {
	v := ctx.UserValue(userIDContextKey)
	if v == nil {
		return ""
	}
	return v.(string)
}

// OptionalAuth attaches the caller's user id when a valid credential is
// present, and otherwise passes the request through untouched. It never
// rejects: the endpoint it guards is public, and the identity is only used to
// make an attribution more precise.
//
// Used by /api/presence, where a signed-in heartbeat should count the ACCOUNT
// (one entry however many tabs the shopper has open) and an anonymous one
// falls back to the verified client IP.
//
// A cookie-authenticated state-changing request that fails CSRF verification is
// downgraded to anonymous rather than refused. Refusing would change the
// contract of a middleware whose whole purpose is to never reject a public
// endpoint; downgrading still denies the attacker the thing they were after,
// which is the account attribution.
func OptionalAuth(secret string, opts AuthOptions, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	verifier := opts.csrf()
	return func(ctx *fasthttp.RequestCtx) {
		raw, fromCookie := SessionToken(ctx)
		if raw != "" {
			if claims, err := ParseTokenBounded(secret, raw); err == nil {
				if !fromCookie || !IsStateChanging(ctx.Method()) || verifier.Check(ctx) == nil {
					ctx.SetUserValue(userIDContextKey, claims.UserID)
					ctx.SetUserValue(authViaCookieContextKey, fromCookie)
				}
			}
		}
		next(ctx)
	}
}

// maxBearerLen bounds the token we are willing to parse. A JWT for this shop
// is ~200 bytes; anything larger is a hostile payload and must not be handed
// to the JSON/base64 machinery on a public endpoint. The same bound applies to
// the cookie value, which is why it lives on the shared parse path.
const maxBearerLen = 4096

// ParseTokenBounded is auth.ParseToken with a length ceiling in front of it.
func ParseTokenBounded(secret, raw string) (*auth.Claims, error) {
	if len(raw) > maxBearerLen {
		return nil, errTokenTooLarge
	}
	return auth.ParseToken(secret, raw)
}

var errTokenTooLarge = &tokenError{"bearer token too large"}

type tokenError struct{ msg string }

func (e *tokenError) Error() string { return e.msg }
