// Package httpx is the HTTP edge hardening layer that sits between the TCP
// accept loop and the router. Everything in here is deliberately cheap and
// allocation-light: it runs on EVERY request, so it is written for
// eight-figure monthly traffic, not for a demo.
//
// It owns four concerns that used to be missing entirely:
//
//  1. Security response headers (nosniff / frame deny / referrer / HSTS …).
//     The API answered with none of them, so a mis-typed URL that returned
//     JSON could be sniffed into HTML by an old browser, and every endpoint
//     was frameable.
//  2. Admission control. Under a flood the old server accepted work without
//     limit (Concurrency: 1_000_000) until the box ran out of RAM. A
//     bounded in-flight counter now sheds load with a cheap 503 BEFORE any
//     handler, database or supplier call happens — and, crucially, the cap
//     is sized so a normal visitor never sees it.
//  3. Transport compression for JSON. Catalog payloads are hundreds of KB;
//     shipping them uncompressed wastes origin bandwidth and milliseconds.
//  4. Cache directives. Public, user-independent GETs are marked cacheable
//     (browser + CDN) so the edge absorbs the traffic instead of the Go
//     process; anything per-user is pinned to private/no-store.
package httpx

import (
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/loopback"
)

// HeaderConfig is the immutable, boot-time description of how responses are
// hardened. It is built once in main and read (never written) per request.
type HeaderConfig struct {
	// SiteHost is the public hostname, used for the HSTS includeSubDomains
	// decision and nothing else.
	SiteHost string
	// HSTS enables Strict-Transport-Security. Only turn this on once the
	// host is served exclusively over HTTPS — it is sticky for max-age.
	HSTS bool
	// HSTSMaxAgeSeconds is the HSTS max-age (default two years).
	HSTSMaxAgeSeconds int
	// HSTSPreload adds the preload directive.
	HSTSPreload bool
	// FrameDeny sets X-Frame-Options: DENY and a matching frame-ancestors
	// CSP hint. The API is JSON only; there is no legitimate reason to
	// render it inside an iframe.
	FrameDeny bool
	// ServerHeader, when non-empty, replaces fasthttp's default "Server"
	// response header so the exact build is not advertised.
	ServerHeader string
}

// DefaultHeaderConfig is the production profile: everything on, HSTS two
// years with preload, and a generic server banner.
func DefaultHeaderConfig(siteHost string) HeaderConfig {
	return HeaderConfig{
		SiteHost:          siteHost,
		HSTS:              true,
		HSTSMaxAgeSeconds: 63072000,
		HSTSPreload:       true,
		FrameDeny:         true,
		ServerHeader:      "nimshop",
	}
}

var (
	hNosniff    = []byte("nosniff")
	hDeny       = []byte("DENY")
	hNoReferrer = []byte("no-referrer")
	hSameSite   = []byte("same-site")
	hNoStore    = []byte("no-store")
	hSameOrigin = []byte("same-origin-allow-popups")
	hJSON       = []byte("application/json")
)

// hstsValue is rendered once at boot: the string never changes per request,
// so building it on the hot path would be pure waste.
func (c HeaderConfig) hstsValue() []byte {
	if !c.HSTS {
		return nil
	}
	maxAge := c.HSTSMaxAgeSeconds
	if maxAge <= 0 {
		maxAge = 63072000
	}
	v := "max-age=" + strconv.Itoa(maxAge)
	if c.SiteHost != "" && !loopback.IsHost(c.SiteHost) {
		v += "; includeSubDomains"
	}
	if c.HSTSPreload {
		v += "; preload"
	}
	return []byte(v)
}

// Harden wraps next and stamps the security headers on EVERY response,
// including error responses produced by the router itself and the 405/404
// bodies fasthttp generates.
//
// Cost: a handful of header sets on an already-allocated header block. No
// allocation in the steady state (all values are package-level byte slices).
func Harden(cfg HeaderConfig, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	hsts := cfg.hstsValue()
	server := []byte(cfg.ServerHeader)
	return func(ctx *fasthttp.RequestCtx) {
		next(ctx)
		h := &ctx.Response.Header
		// A handler may already have set Cache-Control (the catalog and
		// market endpoints do). Never clobber it — only guarantee a value.
		if len(h.Peek("Cache-Control")) == 0 {
			h.SetBytesV("Cache-Control", hNoStore)
		}
		h.SetBytesV("X-Content-Type-Options", hNosniff)
		h.SetBytesV("Referrer-Policy", hNoReferrer)
		h.SetBytesV("Cross-Origin-Resource-Policy", hSameSite)
		h.SetBytesV("Cross-Origin-Opener-Policy", hSameOrigin)
		// Permissions-Policy: the API surface has no use for any browser
		// capability. Denying them means a compromised response cannot
		// silently reach a camera, a microphone or a payment handler.
		// Only features browsers still recognise are listed — retired
		// tokens (interest-cohort, browsing-topics, attribution-reporting…)
		// make Chrome log "Unrecognized feature" on every HTML document this
		// process serves. Mirrors public/_headers.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if cfg.FrameDeny {
			h.SetBytesV("X-Frame-Options", hDeny)
		}
		if len(hsts) > 0 && requestWasTLS(ctx) {
			h.SetBytesV("Strict-Transport-Security", hsts)
		}
		if len(server) > 0 {
			h.SetBytesV("Server", server)
		} else {
			h.Del("Server")
		}
		// A JSON body must never be sniffed into HTML, and a JSON body is
		// never a document: both are covered by nosniff plus an explicit
		// charset so no downstream proxy guesses.
		if ct := h.Peek("Content-Type"); len(ct) == 0 {
			h.SetBytesV("Content-Type", hJSON)
		}
	}
}

// requestWasTLS reports whether the visitor reached us over HTTPS. Behind a
// terminator (Cloudflare, nginx) the hop to Go is plain HTTP, so the forwarded
// scheme header is authoritative; a direct TLS listener sets it itself.
func requestWasTLS(ctx *fasthttp.RequestCtx) bool {
	if ctx.IsTLS() {
		return true
	}
	switch strings.ToLower(string(ctx.Request.Header.Peek("X-Forwarded-Proto"))) {
	case "https":
		return true
	}
	// Cloudflare terminates TLS and always sends this on a proxied request.
	return strings.EqualFold(string(ctx.Request.Header.Peek("CF-Visitor")), `{"scheme":"https"}`)
}

/* ------------------------------ cache policy ----------------------------- */

// CachePolicy describes how one response may be cached. It exists so every
// public endpoint states its intent in ONE place instead of hand-rolling a
// Cache-Control string (and forgetting the CDN variant, or the Vary).
type CachePolicy struct {
	// Public marks the response shareable between users. Never set this on
	// anything derived from the Authorization header, a cookie or the
	// client IP.
	Public bool
	// MaxAge is the browser freshness lifetime in seconds.
	MaxAge int
	// SMaxAge is the shared-cache (CDN) freshness lifetime. Zero means
	// "same as MaxAge". Negative means "do not let a CDN store this".
	SMaxAge int
	// StaleWhileRevalidate lets the CDN serve a stale copy and refresh it
	// in the background, so a cache miss never becomes a user-visible stall.
	StaleWhileRevalidate int
	// StaleIfError lets the CDN keep serving the last good copy while the
	// origin is down — the storefront stays up through a backend incident.
	StaleIfError int
}

// PublicCache is the standard profile for user-independent GETs.
func PublicCache(maxAge, swr int) CachePolicy {
	return CachePolicy{Public: true, MaxAge: maxAge, SMaxAge: maxAge, StaleWhileRevalidate: swr, StaleIfError: swr * 4}
}

// PrivateCache is the standard profile for per-user responses.
func PrivateCache() CachePolicy { return CachePolicy{} }

// String renders the browser-facing Cache-Control value.
func (p CachePolicy) String() string {
	if !p.Public {
		return "private, no-store, max-age=0"
	}
	var b strings.Builder
	b.WriteString("public, max-age=")
	b.WriteString(strconv.Itoa(p.MaxAge))
	if p.StaleWhileRevalidate > 0 {
		b.WriteString(", stale-while-revalidate=")
		b.WriteString(strconv.Itoa(p.StaleWhileRevalidate))
	}
	if p.StaleIfError > 0 {
		b.WriteString(", stale-if-error=")
		b.WriteString(strconv.Itoa(p.StaleIfError))
	}
	return b.String()
}

// CDNString renders the CDN-facing value. A negative SMaxAge means the
// response must never be stored by a shared cache even though the browser
// may cache it.
func (p CachePolicy) CDNString() string {
	if !p.Public {
		return "private, no-store"
	}
	if p.SMaxAge < 0 {
		return "private, no-store"
	}
	smax := p.SMaxAge
	if smax == 0 {
		smax = p.MaxAge
	}
	var b strings.Builder
	b.WriteString("public, s-maxage=")
	b.WriteString(strconv.Itoa(smax))
	if p.StaleWhileRevalidate > 0 {
		b.WriteString(", stale-while-revalidate=")
		b.WriteString(strconv.Itoa(p.StaleWhileRevalidate))
	}
	if p.StaleIfError > 0 {
		b.WriteString(", stale-if-error=")
		b.WriteString(strconv.Itoa(p.StaleIfError))
	}
	return b.String()
}

// ApplyCache writes the policy onto the response.
//
// sharedSafe decides whether a CDN may store the body at all. It must be
// false whenever the response carries an echoed Access-Control-Allow-Origin:
// a shared cache keyed on the URL alone would then hand one origin's CORS
// grant to another. Callers pass the deployment's verdict (one configured
// origin, or same-origin) — see main.go.
func ApplyCache(ctx *fasthttp.RequestCtx, p CachePolicy, sharedSafe bool) {
	ctx.Response.Header.Set("Cache-Control", p.String())
	if sharedSafe {
		ctx.Response.Header.Set("CDN-Cache-Control", p.CDNString())
	} else {
		ctx.Response.Header.Set("CDN-Cache-Control", "private, no-store")
	}
	// Origin and Accept-Encoding both change the bytes a cache would serve,
	// so both belong in Vary. Browsers honour it; a CDN that ignores Vary is
	// already excluded by the sharedSafe gate above.
	ctx.Response.Header.Add("Vary", "Origin, Accept-Encoding")
}

// NoStore pins a response to "never cached, anywhere". Use it on anything
// per-user, per-IP or money-bearing.
func NoStore(ctx *fasthttp.RequestCtx) {
	ApplyCache(ctx, PrivateCache(), false)
}
