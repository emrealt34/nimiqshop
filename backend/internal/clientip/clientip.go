package clientip

import (
	"strings"

	"github.com/valyala/fasthttp"
)

// Info is the resolved client identity for one request.
type Info struct {
	// IP is the real client IP, resolved through the fronting proxy/CDN when
	// trustProxy is enabled. Always the caller's best-effort identity.
	IP string
	// Cloudflare reports whether the request came through Cloudflare's edge
	// (detected from the cf-ray header). Used only for display so the frontend
	// can label the source of the country value.
	Cloudflare bool
	// Country is Cloudflare's GeoIP country code (CF-IPCountry) when Cloudflare
	// is in front; empty in direct mode. This needs no external API or DB.
	Country string
}

// Resolve returns the real client IP (and Cloudflare geo hints) from a
// fasthttp request. Rules, in priority order, only when trustProxy is true:
//
//  1. CF-Connecting-IP if the request is genuinely from Cloudflare (cf-ray).
//     Cloudflare always sets both on proxied requests and overwrites any
//     client-supplied value, so this cannot be spoofed through the proxy.
//  2. The first entry of X-Forwarded-For for a generic trusted reverse proxy.
//  3. RemoteIP as a last resort.
//
// When trustProxy is false, only RemoteIP is used and country stays empty
// (the API never trusts client headers).
func Resolve(ctx *fasthttp.RequestCtx, trustProxy bool) Info {
	ip := ctx.RemoteIP().String()
	info := Info{IP: ip}

	if !trustProxy {
		return info
	}

	// Cloudflare connects as an HTTP proxy and sets both cf-ray and
	// CF-Connecting-IP on every request it forwards to the origin.
	cfRay := strings.TrimSpace(string(ctx.Request.Header.Peek("cf-ray")))
	if cfRay != "" {
		info.Cloudflare = true
		if cip := strings.TrimSpace(string(ctx.Request.Header.Peek("CF-Connecting-IP"))); cip != "" {
			// Free text — take the first token and strip a possible port.
			info.IP = firstToken(cip)
		} else if wrapped := strings.TrimSpace(string(ctx.Request.Header.Peek("True-Client-IP"))); wrapped != "" {
			info.IP = firstToken(wrapped)
		}
		if cc := strings.TrimSpace(string(ctx.Request.Header.Peek("CF-IPCountry"))); cc != "" {
			info.Country = strings.ToUpper(cc)
		}
		return info
	}

	// Generic reverse proxy (nginx, node live-proxy, …): use the left-most
	// (client-facing) X-Forwarded-For entry.
	if xff := strings.TrimSpace(string(ctx.Request.Header.Peek("X-Forwarded-For"))); xff != "" {
		if first := firstToken(xff); first != "" {
			info.IP = first
		}
	}
	return info
}

// firstToken returns the first comma/space separated token, stripping an IPv4
// port suffix so "1.2.3.4:1234" becomes "1.2.3.4".
func firstToken(s string) string {
	for _, sep := range []string{",", " "} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	// Trim a port for bare IPv4 (IPv6 literals keep the port for validity).
	if i := strings.LastIndex(s, ":"); i >= 0 && strings.Count(s, ":") == 1 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
