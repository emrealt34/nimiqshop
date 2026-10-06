// Package clientip is the single trust boundary for visitor attribution.
// Header presence (including CF-Ray) is NEVER proof that a proxy is trusted.
package clientip

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/loopback"
)

const ProxySecretHeader = "X-Nimshop-Proxy-Secret"

// ClientCountryHeader is how a deployment's OWN edge re-states the visitor's
// country when the Cloudflare headers are not available to it. Both hops that
// ship in this repo set it — functions/api/[[path]].js from request.cf.country,
// scripts/proxy.mjs from the edge's header — and both DELETE any client-supplied
// copy before forwarding.
//
// It is a defence-in-depth carrier, NOT the live deployment's only source. An
// earlier revision of this comment claimed Cloudflare's headers never reach a
// handler on the live shop ("no Cloudflare header reaches a handler at all");
// that was measured wrong on 2026-10-06. A plain GET of the live /api/geo
// answers country:"US" with no hint header sent at all, i.e. CF-IPCountry does
// arrive through the Pages hop and the Railway edge. This header stays because
// it is the carrier for the shape where a hop REBUILDS the request (a
// standalone proxy, a Worker) and the CF-* set is dropped.
//
// Read only from an already-trusted hop, and only as a COUNTRY: the visitor IP
// is settled from the peeled chain and no header can move it. A forged value
// could change display only — the country label on the visitor's own row in the
// operator console — never the address that identifies them.
const ClientCountryHeader = "X-Nimshop-Client-Country"

// CountryHintHeader carries the country the visitor's own BROWSER read from the
// edge: the shop's frontend fetches Cloudflare's same-origin /cdn-cgi/trace and
// forwards its `loc=` value here (see src/lib/edgeGeo.ts). It exists because the
// frontend reaches the API through a hop that re-originates the request, so the
// browser's own reading of the edge is the one witness that survives every
// shape of that hop — including the ones where CF-IPCountry is dropped. Where
// both arrive they are cross-checked in the operator console's origin note.
//
// Name aside it is still a hint, not evidence — a script can set any header —
// which is exactly why it is NOT read inside Resolve: the visitor IP and
// everything that hangs off it stay independent of it, and only the operator
// console's origin note spends it.
const CountryHintHeader = "X-Nimshop-Country-Hint"

// CountryHint returns the validated country the browser reported, or "".
func CountryHint(ctx *fasthttp.RequestCtx) string {
	return country(string(ctx.Request.Header.Peek(CountryHintHeader)))
}

// ClientIPHeader is how the deployment's edge states the address it actually
// saw the request arrive from — the Pages function sets it from the edge's own
// attribution of the connection (the same value Cloudflare's CF-Connecting-IP
// carries) after deleting any client-supplied copy; scripts/proxy.mjs mirrors
// that.
//
// It exists because some deployment shapes RE-ORIGINATE the request on an inner
// hop (a standalone proxy, a tunnel, a Worker), so the chain the backend can
// peel ends at that hop's egress and every visitor would be noted with the same
// address (deploy/railway.md records the trade-off). The deployment's edge is
// the one hop that sees the browser itself, so it is the only place that value
// can come from.
//
// It is likewise not the live shop's only source. Measured 2026-10-06: a plain
// GET of /api/geo keeps the visitor's own address in the peeled chain (the
// response reported the caller's real egress IP), so the header is spent only
// when a trusted hop set it AND the resolved value would otherwise be one of
// ours.
const ClientIPHeader = "X-Nimshop-Client-IP"

// ClientIPHint returns the visit address the edge reported, or "". Private,
// loopback, link-local and synthetic (Cloudflare's Pseudo IPv4, 240/4) ranges
// are refused, so a misconfigured hop cannot put a meaningless value in the
// operator console.
//
// Like CountryHint it is deliberately NOT read inside Resolve: the resolved
// address, and every decision that hangs off it (rate limits, the supplier
// payload, audit lines), keeps using the peeled chain, and only the presence
// note spends the edge's word — on a display label.
func ClientIPHint(ctx *fasthttp.RequestCtx) string {
	ip, err := ParseIP(string(ctx.Request.Header.Peek(ClientIPHeader)))
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		netip.MustParsePrefix("240.0.0.0/4").Contains(ip) {
		return ""
	}
	return ip.String()
}

type Info struct {
	IP         string
	Cloudflare bool
	Country    string
	Source     string
	Err        error
}

type Policy struct {
	// Only these immediate TCP peers may supply forwarding headers. Empty
	// means loopback, not all private networks and never the entire Internet.
	TrustedProxyCIDRs []string
	// forwarded: normalized XFF from our Node/nginx proxy; cloudflare: a
	// direct, network-restricted cloudflared connector forwarding CF headers.
	HeaderMode string
	// Optional in direct-cloudflared deployments; required by the supplied
	// combined-stack launcher for the private Node -> Go hop. Never sent to CR.
	SharedSecret string
}

func (p Policy) CIDRs() []string {
	if len(p.TrustedProxyCIDRs) == 0 {
		return loopback.DefaultProxyCIDRs()
	}
	return p.TrustedProxyCIDRs
}
func (p Policy) Validate() error {
	if p.HeaderMode != "" && p.HeaderMode != "forwarded" && p.HeaderMode != "cloudflare" {
		return errors.New("PROXY_HEADER_MODE must be forwarded or cloudflare")
	}
	if len(p.CIDRs()) > 32 {
		return errors.New("too many trusted proxy CIDRs")
	}
	for _, raw := range p.CIDRs() {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Bits() == 0 {
			return fmt.Errorf("invalid/unsafe trusted proxy CIDR %q", raw)
		}
	}
	if p.SharedSecret != "" && (len(p.SharedSecret) < 32 || len(p.SharedSecret) > 512 || strings.ContainsAny(p.SharedSecret, " \t\r\n")) {
		return errors.New("FORWARDED_HEADER_SECRET must be 32-512 non-whitespace characters")
	}
	return nil
}
func (p Policy) trusts(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	for _, raw := range p.CIDRs() {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err == nil && prefix.Bits() > 0 && prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// ParseIP accepts ONE literal IP, no lists, ports, zones, hostnames or free text.
// Unmap keeps ::ffff:192.0.2.1 consistent with IPv4 addresses and CIDR checks.
func ParseIP(raw string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, errors.New("invalid IP header")
	}
	return ip.Unmap(), nil
}
func country(raw string) string {
	c := strings.ToUpper(strings.TrimSpace(raw))
	if len(c) != 2 || c == "XX" || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		return ""
	}
	return c
}
func singleHeader(ctx *fasthttp.RequestCtx, name string) (string, error) {
	values := ctx.Request.Header.PeekAll(name)
	if len(values) != 1 {
		return "", errors.New("missing or repeated forwarding header")
	}
	return strings.TrimSpace(string(values[0])), nil
}
func cloudflareIP(ctx *fasthttp.RequestCtx) (netip.Addr, error) {
	raw, err := singleHeader(ctx, "CF-Connecting-IP")
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := ParseIP(raw)
	if err != nil {
		return netip.Addr{}, err
	}
	// Pseudo IPv4 Overwrite Headers uses Class E, preserving the actual IPv6
	// in CF-Connecting-IPv6. Never send the synthetic IPv4 to Cryptorefills.
	pseudo := netip.MustParsePrefix("240.0.0.0/4")
	if pseudo.Contains(ip) {
		v6, err := singleHeader(ctx, "CF-Connecting-IPv6")
		if err != nil {
			return netip.Addr{}, errors.New("real IPv6 missing behind Pseudo IPv4")
		}
		real, err := ParseIP(v6)
		if err != nil || !real.Is6() {
			return netip.Addr{}, errors.New("invalid real IPv6")
		}
		return real, nil
	}
	return ip, nil
}

type resolvedKey struct{}

// Resolve is also used by direct handler tests. The HTTP gate caches its
// decision so rate limiting, geo, audit logs and supplier headers all agree.
func Resolve(ctx *fasthttp.RequestCtx, trustProxy bool, policies ...Policy) Info {
	if cached, ok := ctx.UserValue(resolvedKey{}).(Info); ok {
		return cached
	}
	peer, ok := netip.AddrFromSlice(ctx.RemoteIP())
	if !ok {
		return Info{Err: errors.New("invalid TCP peer")}
	}
	peer = peer.Unmap()
	// Defense in depth: the kernel never delivers an unspecified or
	// multicast peer on a real socket, so anything else here is a broken
	// (or forged) context — refuse to attribute it rather than echoing a
	// malformed value into logs, supplier calls or rate-limit keys.
	if !peer.IsValid() || peer.IsUnspecified() || peer.IsMulticast() {
		return Info{Err: errors.New("invalid TCP peer")}
	}
	result := Info{IP: peer.String(), Source: "socket"}
	p := Policy{}
	if len(policies) > 0 {
		p = policies[0]
	}
	if !trustProxy {
		return result
	}
	if !p.trusts(peer) {
		if p.SharedSecret != "" || p.HeaderMode == "cloudflare" {
			result.Err = errors.New("untrusted proxy peer")
		}
		return result // raw forwarding headers are ignored for direct clients
	}
	if p.SharedSecret != "" {
		secret, err := singleHeader(ctx, ProxySecretHeader)
		if err != nil || subtle.ConstantTimeCompare([]byte(secret), []byte(p.SharedSecret)) != 1 {
			return Info{Err: errors.New("unverified private proxy hop")}
		}
	}
	if p.HeaderMode == "cloudflare" {
		ip, err := cloudflareIP(ctx)
		if err != nil {
			return Info{Err: err}
		}
		if ip.String() == "2a06:98c0:3600::103" {
			return Info{Err: errors.New("cross-zone Worker does not provide the original visitor IP")}
		}
		return Info{IP: ip.String(), Cloudflare: true, Country: country(string(ctx.Request.Header.Peek("CF-IPCountry"))), Source: "cloudflare"}
	}
	values := ctx.Request.Header.PeekAll("X-Forwarded-For")
	if len(values) == 0 {
		if p.SharedSecret != "" {
			return Info{Err: errors.New("normalized visitor IP missing")}
		}
		return result
	}
	if len(values) != 1 || len(values[0]) > 2048 {
		return Info{Err: errors.New("invalid forwarding chain")}
	}
	parts := strings.Split(string(values[0]), ",")
	if len(parts) > 32 {
		return Info{Err: errors.New("forwarding chain too long")}
	}
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		ip, err := ParseIP(part)
		if err != nil {
			return Info{Err: err}
		}
		chain[i] = ip
	}
	// Peel trusted hops from RIGHT to LEFT. An attacker-controlled leftmost
	// XFF entry can never override the nearest untrusted connecting client.
	chosen := peer
	for i := len(chain) - 1; i >= 0; i-- {
		if !p.trusts(chosen) {
			break
		}
		chosen = chain[i]
	}
	result.IP = chosen.String()
	result.Source = "trusted-proxy"
	// Our authenticated Node proxy may preserve verified CF country hints.
	// Generic proxies without a shared secret cannot vouch for CF metadata.
	if p.SharedSecret != "" && len(ctx.Request.Header.Peek("CF-Ray")) > 0 {
		cip, err := cloudflareIP(ctx)
		if err != nil || cip != chosen {
			return Info{Err: errors.New("inconsistent normalized Cloudflare identity")}
		}
		result.Cloudflare = true
		result.Country = country(string(ctx.Request.Header.Peek("CF-IPCountry")))
		result.Source = "cloudflare"
	} else {
		// The edge's OWN attribution, for proxy hops that carry no shared
		// secret — a Pages function or self-hosted proxy in front of the app.
		// Demanding the secret here meant `forwarded` mode had NO country at
		// all, so the operator console's "IP · country" showed an address with
		// a blank flag no matter how often someone visited. Country ONLY, and
		// only from an already-trusted hop: the visitor IP was settled from the
		// peeled chain above.
		if len(ctx.Request.Header.Peek("CF-Ray")) > 0 {
			// Cloudflare answered this request itself, so its own attribution
			// may be taken. Without the ray trace a bare CF-IPCountry is just a
			// header somebody sent, and it is ignored.
			result.Cloudflare = true
			result.Country = country(string(ctx.Request.Header.Peek("CF-IPCountry")))
		}
		if result.Country == "" {
			// Our edge's restatement (request.cf.country) travels on a header
			// of our own, because Cloudflare's CF-* set does not survive every
			// hop of this stack — see ClientCountryHeader. Both repos' hops
			// delete any client-supplied copy first, so from a trusted hop this
			// is the edge's word, not the visitor's.
			result.Country = country(string(ctx.Request.Header.Peek(ClientCountryHeader)))
		}
	}
	return result
}

// Gate runs BEFORE authentication/rate limiting/router work. A tunnel configured
// to require real identity fails closed instead of forwarding 127.0.0.1 or a
// spoofed string to the supplier when its expected headers disappear.
func Gate(trustProxy bool, p Policy, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		info := Resolve(ctx, trustProxy, p)
		if info.Err == nil && trustProxy && (p.SharedSecret != "" || p.HeaderMode == "cloudflare") {
			agent, err := singleHeader(ctx, "User-Agent")
			if err != nil || len(agent) == 0 || len(agent) > 4096 || strings.ContainsAny(agent, "\r\n\x00") {
				info.Err = errors.New("end-user agent missing or invalid")
			}
		}
		if info.Err != nil {
			ctx.SetStatusCode(fasthttp.StatusForbidden)
			ctx.SetContentType("application/json")
			ctx.SetBodyString(`{"error":"request origin could not be verified; use the secure shop URL","code":"UNVERIFIED_PROXY"}`)
			return
		}
		ctx.SetUserValue(resolvedKey{}, info)
		next(ctx)
	}
}
