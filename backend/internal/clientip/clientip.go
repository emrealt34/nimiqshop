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

// ClientCountryHeader is how the deployment's OWN edge re-states the visitor's
// country when Cloudflare's CF-* headers do not survive the hop. A Cloudflare
// Tunnel drops them (verified live on this shop: the visitor IP arrives through
// the normalized X-Forwarded-For, while CF-Ray and CF-IPCountry never do), so
// the Pages function / Node proxy sets this instead, from request.cf.country —
// and both of them DELETE any client-supplied copy before forwarding.
//
// It is read only from an already-trusted hop, only as a COUNTRY: the visitor
// IP is settled from the peeled chain and no header can move it. What a forged
// value could change is display-only — which catalog country a visitor is
// DEFAULTED to (the shop has a country picker anyway) and the country label on
// their own row in the operator console — while the address, which is what
// actually identifies a person, cannot be chosen.
const ClientCountryHeader = "X-Nimshop-Client-Country"

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
		// secret: Cloudflare Pages in front of the tunnel, which is how this
		// shop is deployed. Demanding the secret here meant `forwarded` mode
		// had NO country at all, so the operator console's "IP · country"
		// showed an address with a blank flag no matter how often someone
		// visited. Country ONLY, and only from an already-trusted hop: the
		// visitor IP was settled from the peeled chain above.
		if len(ctx.Request.Header.Peek("CF-Ray")) > 0 {
			// Cloudflare answered this request itself.
			result.Cloudflare = true
		}
		if result.Country == "" {
			result.Country = country(string(ctx.Request.Header.Peek("CF-IPCountry")))
		}
		if result.Country == "" {
			// ...and when Cloudflare's own geolocation header does not survive
			// the hop (a tunnel drops it), our edge's restatement of it
			// (request.cf.country) does — see ClientCountryHeader.
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
