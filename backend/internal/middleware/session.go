package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

// Cookie-based session delivery and CSRF defence.
//
// WHY THIS FILE EXISTS
//
// The customer session JWT used to be handed to the browser in a JSON body and
// stored in localStorage, then replayed as an Authorization: Bearer header.
// That design has one specific and serious flaw: localStorage is readable by
// any script running on the page. A single XSS — a compromised dependency, a
// reflected value in a template, a malicious browser extension with page
// access — reads the token out and exfiltrates a session that is valid for
// thirty days (JWT_EXPIRY_MINS defaults to 43200; owner, 2026-10-05:
// "1 ay cookie tutsun"). The token is a bearer
// credential, so possession is authentication: the attacker does not need the
// user's password, wallet, or signature to spend the session, and because it
// left no cookie behind the victim's browser keeps working normally while the
// copy is used elsewhere.
//
// An HttpOnly cookie cannot be read by script at all, which removes that
// entire class of exfiltration rather than making it harder. The price is that
// cookies are attached by the browser rather than chosen by the code, which is
// exactly the condition CSRF exploits — so the two changes must land together.
//
// DEFENCE LAYERS, in the order they stop an attack
//
//  1. SameSite on the session cookie. Lax (the default) means the browser
//     does not attach the cookie to a cross-site POST at all, so a forged
//     form or fetch from evil.example arrives unauthenticated and fails on
//     missing credentials before any CSRF logic runs. This is the layer that
//     does most of the work.
//  2. Origin/Referer verification for every state-changing request that was
//     authenticated by a cookie. SameSite is a browser feature of uneven
//     vintage and can be configured away; the Origin header is sent by every
//     browser on every cross-origin POST and cannot be forged by script.
//  3. A double-submit CSRF token: a random value in a script-readable cookie
//     that must be echoed back in X-CSRF-Token. A cross-site attacker can
//     cause the browser to send the cookie but cannot read it to fill in the
//     header, since it belongs to another origin.
//
// Bearer-authenticated requests are deliberately exempt from layers 2 and 3.
// CSRF is an attack on *ambient* credentials the browser attaches
// automatically; a token a client puts in a header itself is not ambient, and
// demanding a CSRF token from a server-to-server caller would break it for no
// security gain. That exemption is also what keeps the API usable for mobile
// and integration clients, which cannot hold a cookie jar.
//
// What is NOT claimed here: this does not make XSS harmless. An attacker with
// script execution on the page can still issue authenticated fetches through
// the browser (reading the CSRF cookie and setting the header), and can still
// deface the page or steal whatever the DOM contains. What it does is stop the
// session credential itself from being *copied and taken away*, which is the
// difference between an attack that ends when the victim closes the tab and
// one that lasts thirty days.

const (
	// SessionCookieName carries the JWT. HttpOnly, so no script can read it.
	SessionCookieName = "nimshop_session"
	// CSRFCookieName carries the double-submit token. Deliberately NOT
	// HttpOnly: the frontend has to read it in order to echo it back in a
	// header, and that is the whole mechanism. It is not a credential on its
	// own — it authenticates nothing without the session cookie.
	CSRFCookieName = "nimshop_csrf"
	// CSRFHeaderName is where the frontend echoes CSRFCookieName.
	CSRFHeaderName = "X-CSRF-Token"
)

// SessionCookieOptions controls how the session cookie is written. It is
// resolved once from config at startup and passed down, so that the handler
// that sets a cookie and the one that clears it cannot disagree about its
// attributes — a mismatch there means the browser keeps the old cookie and the
// user cannot log out.
type SessionCookieOptions struct {
	// Secure restricts the cookie to HTTPS. Must be true in production; it is
	// only false for a local plaintext deployment.
	Secure bool
	// SameSite selects the SameSite attribute. "" means Lax.
	//
	// "none" is required only when the frontend and the API are on different
	// registrable domains, because a Lax cookie is not attached to a
	// cross-site request at all. Browsers reject SameSite=None without Secure,
	// so that combination is refused at configuration time.
	SameSite string
	// Domain scopes the cookie. Empty means host-only, which is the safer
	// default: a host-only cookie is not sent to sibling subdomains.
	Domain string
	// MaxAgeSecs bounds the cookie's life. It should match the JWT expiry so
	// the browser drops the cookie at roughly the moment it stops working.
	MaxAgeSecs int
}

func (o SessionCookieOptions) sameSiteMode() fasthttp.CookieSameSite {
	switch strings.ToLower(strings.TrimSpace(o.SameSite)) {
	case "none":
		return fasthttp.CookieSameSiteNoneMode
	case "strict":
		return fasthttp.CookieSameSiteStrictMode
	default:
		return fasthttp.CookieSameSiteLaxMode
	}
}

// SessionToken returns the presented credential and whether it arrived in a
// cookie. The second value is what decides whether CSRF checks apply.
//
// An explicit Authorization header wins over the cookie when both are present.
// The header is a deliberate act by the client, whereas the cookie is attached
// by the browser whether or not the code wanted it; preferring the deliberate
// credential keeps server-to-server callers working even if they happen to be
// sharing a jar with a browser session.
func SessionToken(ctx *fasthttp.RequestCtx) (raw string, fromCookie bool) {
	if header := ctx.Request.Header.Peek("Authorization"); len(header) > 0 {
		if tok, ok := strings.CutPrefix(string(header), "Bearer "); ok && strings.TrimSpace(tok) != "" {
			return strings.TrimSpace(tok), false
		}
	}
	if v := ctx.Request.Header.Cookie(SessionCookieName); len(v) > 0 {
		if tok := strings.TrimSpace(string(v)); tok != "" {
			return tok, true
		}
	}
	return "", false
}

// NewCSRFToken returns 32 bytes of CSPRNG output, URL-safe encoded. It is
// generated fresh at every login rather than derived from the session, so that
// rotating a session also rotates the CSRF token and a token leaked from an
// old session cannot be replayed against a new one.
func NewCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SetSession writes the session cookie and a matching CSRF cookie, and returns
// the CSRF token so the caller can also put it in the response body. Returning
// it as well as setting the cookie is not redundant: on a cross-origin
// deployment the script-readable cookie may be partitioned or blocked, and the
// body copy lets the frontend keep working from memory.
func SetSession(ctx *fasthttp.RequestCtx, o SessionCookieOptions, token string) (string, error) {
	csrf, err := NewCSRFToken()
	if err != nil {
		return "", err
	}

	session := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(session)
	session.SetKey(SessionCookieName)
	session.SetValue(token)
	session.SetPath("/")
	session.SetHTTPOnly(true) // the whole point
	session.SetSecure(o.Secure)
	session.SetSameSite(o.sameSiteMode())
	if o.Domain != "" {
		session.SetDomain(o.Domain)
	}
	if o.MaxAgeSecs > 0 {
		session.SetMaxAge(o.MaxAgeSecs)
	}
	ctx.Response.Header.SetCookie(session)

	// Same Site/Secure/Path/Domain as the session cookie, and NOT HttpOnly, so
	// the page can read it. If these attributes ever diverge from the session
	// cookie's, the browser may send one and not the other, which shows up as
	// an unexplained 403 on every write.
	double := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(double)
	double.SetKey(CSRFCookieName)
	double.SetValue(csrf)
	double.SetPath("/")
	double.SetHTTPOnly(false)
	double.SetSecure(o.Secure)
	double.SetSameSite(o.sameSiteMode())
	if o.Domain != "" {
		double.SetDomain(o.Domain)
	}
	if o.MaxAgeSecs > 0 {
		double.SetMaxAge(o.MaxAgeSecs)
	}
	ctx.Response.Header.SetCookie(double)

	return csrf, nil
}

// cookieEpoch is the conventional "already expired" instant used to delete a
// cookie: Thu, 01 Jan 1970 00:00:00 GMT.
var cookieEpoch = time.Unix(0, 0).UTC()

// ClearSession deletes both cookies. The attributes must match what SetSession
// used or the browser treats this as a different cookie and leaves the session
// in place — the classic "logout does not work" bug.
//
// Deletion uses an Expires in the past rather than SetMaxAge(-1), because
// fasthttp serialises Max-Age only when it is strictly positive and otherwise
// falls through to Expires (cookie.go:279). SetMaxAge(-1) therefore emits
// NEITHER attribute: the browser receives an empty-valued cookie with no expiry
// at all and stores it as a session cookie that lingers until the browser
// closes. The empty value does stop the credential being usable, so this is not
// a wide-open hole, but it leaves dead weight in the jar on every subsequent
// request and relies on the browser honouring an overwrite instead of the
// cookie actually being gone. A past Expires is the standard deletion mechanism
// and makes the removal explicit.
func ClearSession(ctx *fasthttp.RequestCtx, o SessionCookieOptions) {
	for _, name := range []string{SessionCookieName, CSRFCookieName} {
		c := fasthttp.AcquireCookie()
		c.SetKey(name)
		c.SetValue("")
		c.SetPath("/")
		c.SetHTTPOnly(name == SessionCookieName)
		c.SetSecure(o.Secure)
		c.SetSameSite(o.sameSiteMode())
		if o.Domain != "" {
			c.SetDomain(o.Domain)
		}
		c.SetExpire(cookieEpoch)
		ctx.Response.Header.SetCookie(c)
		fasthttp.ReleaseCookie(c)
	}
}

// CSRFCookieValue exposes the double-submit token so a handler can include it
// in a JSON body.
func CSRFCookieValue(ctx *fasthttp.RequestCtx) string {
	return strings.TrimSpace(string(ctx.Request.Header.Cookie(CSRFCookieName)))
}

// IsStateChanging reports whether a method can mutate server state. GET and
// HEAD must not, and this codebase relies on that: a Lax cookie IS attached to
// a cross-site top-level GET navigation, so any state change reachable by GET
// would be CSRF-able no matter what this file does.
func IsStateChanging(method []byte) bool {
	switch string(method) {
	case "GET", "HEAD", "OPTIONS", "TRACE":
		return false
	default:
		return true
	}
}

// CSRFVerifier validates the origin of a cookie-authenticated state-changing
// request. Constructed once from config.
type CSRFVerifier struct {
	AllowedOrigins []string
	AllowTunnels   bool
}

var (
	ErrCSRFOrigin     = errors.New("request origin is not permitted for a cookie-authenticated write")
	ErrCSRFOriginMiss = errors.New("cookie-authenticated write without a verifiable Origin or Referer")
	ErrCSRFToken      = errors.New("missing or mismatched CSRF token")
)

// Check enforces layers 2 and 3 above. It is only called for requests whose
// credential came from a cookie; see the file comment for why Bearer callers
// are exempt.
func (v CSRFVerifier) Check(ctx *fasthttp.RequestCtx) error {
	if !v.originAllowed(ctx) {
		// Distinguish "present but wrong" from "absent" only for logging; both
		// are refusals, and the message must not leak the allowlist.
		if v.requestOrigin(ctx) == "" {
			return ErrCSRFOriginMiss
		}
		return ErrCSRFOrigin
	}

	cookie := CSRFCookieValue(ctx)
	header := strings.TrimSpace(string(ctx.Request.Header.Peek(CSRFHeaderName)))
	// Both must be present. An empty cookie with an empty header would compare
	// equal, which is precisely the bypass to avoid, so require non-empty
	// first and then compare in constant time.
	if cookie == "" || header == "" {
		return ErrCSRFToken
	}
	if subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
		return ErrCSRFToken
	}
	return nil
}

// requestOrigin returns the best available origin: the Origin header, or the
// scheme+host of the Referer when Origin is absent. Some browsers omit Origin
// on same-origin form submissions but always send Referer there.
func (v CSRFVerifier) requestOrigin(ctx *fasthttp.RequestCtx) string {
	if o := strings.TrimSpace(string(ctx.Request.Header.Peek("Origin"))); o != "" {
		return o
	}
	ref := strings.TrimSpace(string(ctx.Request.Header.Peek("Referer")))
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (v CSRFVerifier) originAllowed(ctx *fasthttp.RequestCtx) bool {
	origin := v.requestOrigin(ctx)
	if origin == "" {
		return false
	}
	ou, err := url.Parse(origin)
	if err != nil || ou.Host == "" {
		return false
	}

	// With no configured allowlist the deployment is same-origin (nginx
	// proxying /api on the same host), so the correct comparison is against
	// the Host the request itself arrived on. The scheme is deliberately not
	// constrained here: a same-origin check is a host identity check, and
	// refusing plain HTTP would break the local development deployment that
	// ALLOW_HTTP_LOCAL exists for. Transport security is HSTS's job, and it is
	// already sent on every response.
	if len(v.AllowedOrigins) == 0 {
		host := strings.TrimSpace(string(ctx.Request.Header.Host()))
		if host == "" {
			return false
		}
		// Compare both with and without the port, because Origin omits a
		// default port (https://x.com) while Host may carry it (x.com:443) and
		// vice versa. Getting this wrong fails closed, which means logout and
		// checkout silently 403 — worth the two comparisons.
		if strings.EqualFold(ou.Host, host) {
			return true
		}
		return strings.EqualFold(ou.Hostname(), hostOnly(host))
	}

	for _, allowed := range v.AllowedOrigins {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if origin == allowed {
			return true
		}
		// Wildcard subdomain, same convention as applyCORS in main.go so the
		// two do not disagree about what an allowed origin is.
		if strings.HasPrefix(allowed, "https://*.") {
			domain := strings.TrimPrefix(allowed, "https://*.")
			if ou.Scheme == "https" && (strings.EqualFold(ou.Hostname(), domain) ||
				strings.HasSuffix(strings.ToLower(ou.Hostname()), "."+strings.ToLower(domain))) {
				return true
			}
		}
	}
	if v.AllowTunnels && ou.Scheme == "https" &&
		strings.HasSuffix(strings.ToLower(ou.Hostname()), ".trycloudflare.com") {
		return true
	}
	return false
}

// hostOnly strips a trailing ":port" from an authority. IPv6 literals are
// bracketed, so the colon to cut is the last one after the closing bracket;
// LastIndex on the raw string would cut inside the address.
func hostOnly(hostport string) string {
	if strings.HasSuffix(hostport, "]") {
		return hostport
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[:i]
	}
	return hostport
}
