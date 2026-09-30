package clientip

import (
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func requestContext(peer net.IP, headers map[string][]string) *fasthttp.RequestCtx {
	var request fasthttp.Request
	request.SetRequestURI("https://shop.example.com/api/health")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	ctx := new(fasthttp.RequestCtx)
	ctx.Init(&request, &net.TCPAddr{IP: peer, Port: 1234}, nil)
	return ctx
}

func TestPolicyValidationAndIPParsing(t *testing.T) {
	for _, p := range []Policy{{}, {HeaderMode: "forwarded"}, {HeaderMode: "cloudflare", TrustedProxyCIDRs: []string{"10.0.0.0/8"}, SharedSecret: strings.Repeat("s", 32)}} {
		if err := p.Validate(); err != nil {
			t.Errorf("valid policy: %v", err)
		}
	}
	for _, p := range []Policy{{HeaderMode: "all"}, {TrustedProxyCIDRs: make([]string, 33)}, {TrustedProxyCIDRs: []string{"bad"}}, {TrustedProxyCIDRs: []string{"0.0.0.0/0"}}, {SharedSecret: "short"}, {SharedSecret: strings.Repeat("x", 513)}, {SharedSecret: strings.Repeat("x", 32) + " "}} {
		if p.Validate() == nil {
			t.Errorf("unsafe policy accepted: %+v", p)
		}
	}
	if (Policy{}).trusts(netip.Addr{}) || (Policy{TrustedProxyCIDRs: []string{"bad"}}).trusts(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("invalid policy/address trusted")
	}
	for _, raw := range []string{"", "host.example.com", "1.2.3.4:80", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "fe80::1%eth0", "1.2.3.4,5.6.7.8"} {
		if _, err := ParseIP(raw); err == nil {
			t.Errorf("invalid IP %q accepted", raw)
		}
	}
	got, err := ParseIP(" ::ffff:192.0.2.1 ")
	if err != nil || got.String() != "192.0.2.1" {
		t.Fatalf("unmap: %v %v", got, err)
	}
	for raw, want := range map[string]string{" tr ": "TR", "XX": "", "x": "", "1A": "", "A1": "", "ABC": "", "US": "US"} {
		if got := country(raw); got != want {
			t.Errorf("country %q: %q", raw, got)
		}
	}
}

func TestResolveTrustBoundaries(t *testing.T) {
	secret := strings.Repeat("s", 32)
	for _, tc := range []struct {
		name, peer     string
		trust          bool
		p              Policy
		headers        map[string][]string
		wantIP, source string
		fail, cf       bool
	}{
		{name: "socket ignores spoofing", peer: "203.0.113.1", headers: map[string][]string{"X-Forwarded-For": {"1.2.3.4"}}, wantIP: "203.0.113.1", source: "socket"},
		{name: "untrusted forwarding ignored", peer: "203.0.113.1", trust: true, wantIP: "203.0.113.1", source: "socket"},
		{name: "untrusted mandatory proxy", peer: "203.0.113.1", trust: true, p: Policy{SharedSecret: secret}, wantIP: "203.0.113.1", source: "socket", fail: true},
		{name: "trusted no header", peer: "127.0.0.1", trust: true, wantIP: "127.0.0.1", source: "socket"},
		{name: "nearest untrusted wins", peer: "127.0.0.1", trust: true, headers: map[string][]string{"X-Forwarded-For": {"1.2.3.4, 203.0.113.2, 127.0.0.2"}}, wantIP: "203.0.113.2", source: "trusted-proxy"},
		{name: "repeated header", peer: "127.0.0.1", trust: true, headers: map[string][]string{"X-Forwarded-For": {"1.2.3.4", "2.3.4.5"}}, fail: true},
		{name: "oversized header", peer: "127.0.0.1", trust: true, headers: map[string][]string{"X-Forwarded-For": {strings.Repeat("1", 2049)}}, fail: true},
		{name: "long chain", peer: "127.0.0.1", trust: true, headers: map[string][]string{"X-Forwarded-For": {strings.Repeat("127.0.0.1,", 32) + "1.2.3.4"}}, fail: true},
		{name: "malformed chain", peer: "127.0.0.1", trust: true, headers: map[string][]string{"X-Forwarded-For": {"host.invalid"}}, fail: true},
		{name: "missing secret", peer: "127.0.0.1", trust: true, p: Policy{SharedSecret: secret}, fail: true},
		{name: "wrong secret", peer: "127.0.0.1", trust: true, p: Policy{SharedSecret: secret}, headers: map[string][]string{ProxySecretHeader: {"wrong"}}, fail: true},
		{name: "missing normalized IP", peer: "127.0.0.1", trust: true, p: Policy{SharedSecret: secret}, headers: map[string][]string{ProxySecretHeader: {secret}}, fail: true},
		{name: "authenticated forwarded", peer: "127.0.0.1", trust: true, p: Policy{SharedSecret: secret}, headers: map[string][]string{ProxySecretHeader: {secret}, "X-Forwarded-For": {"203.0.113.8"}}, wantIP: "203.0.113.8", source: "trusted-proxy"},
		{name: "verified CF metadata", peer: "127.0.0.1", trust: true, p: Policy{SharedSecret: secret}, headers: map[string][]string{ProxySecretHeader: {secret}, "X-Forwarded-For": {"203.0.113.8"}, "CF-Ray": {"fixture"}, "CF-Connecting-IP": {"203.0.113.8"}, "CF-IPCountry": {"TR"}}, wantIP: "203.0.113.8", source: "cloudflare", cf: true},
		{name: "inconsistent CF metadata", peer: "127.0.0.1", trust: true, p: Policy{SharedSecret: secret}, headers: map[string][]string{ProxySecretHeader: {secret}, "X-Forwarded-For": {"203.0.113.8"}, "CF-Ray": {"fixture"}, "CF-Connecting-IP": {"203.0.113.9"}}, fail: true},
		{name: "direct CF", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, headers: map[string][]string{"CF-Connecting-IP": {"203.0.113.8"}, "CF-IPCountry": {"TR"}}, wantIP: "203.0.113.8", source: "cloudflare", cf: true},
		{name: "CF missing", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, fail: true},
		{name: "CF invalid", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, headers: map[string][]string{"CF-Connecting-IP": {"bad"}}, fail: true},
		{name: "cross zone worker", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, headers: map[string][]string{"CF-Connecting-IP": {"2a06:98c0:3600::103"}}, fail: true},
		{name: "pseudo IPv4 missing real", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, headers: map[string][]string{"CF-Connecting-IP": {"240.0.0.1"}}, fail: true},
		{name: "pseudo IPv4 wrong real", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, headers: map[string][]string{"CF-Connecting-IP": {"240.0.0.1"}, "CF-Connecting-IPv6": {"1.2.3.4"}}, fail: true},
		{name: "pseudo IPv4 real IPv6", peer: "127.0.0.1", trust: true, p: Policy{HeaderMode: "cloudflare"}, headers: map[string][]string{"CF-Connecting-IP": {"240.0.0.1"}, "CF-Connecting-IPv6": {"2001:db8::8"}}, wantIP: "2001:db8::8", source: "cloudflare", cf: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := requestContext(net.ParseIP(tc.peer), tc.headers)
			got := Resolve(ctx, tc.trust, tc.p)
			if (got.Err != nil) != tc.fail || got.IP != tc.wantIP || got.Source != tc.source || got.Cloudflare != tc.cf {
				t.Fatalf("resolve: %+v", got)
			}
		})
	}
	for _, peer := range []net.IP{{1, 2, 3}, net.IPv4zero, net.ParseIP("224.0.0.1")} {
		if got := Resolve(requestContext(peer, nil), false); got.Err == nil {
			t.Errorf("invalid peer accepted: %+v", got)
		}
	}
}

func TestGateAndCachedIdentity(t *testing.T) {
	p := Policy{HeaderMode: "cloudflare"}
	for _, agent := range []string{"", "fixture-agent", strings.Repeat("a", 4097)} {
		ctx := requestContext(net.ParseIP("127.0.0.1"), map[string][]string{"CF-Connecting-IP": {"203.0.113.8"}})
		if agent != "" {
			ctx.Request.Header.SetUserAgent(agent)
		}
		calls := 0
		Gate(true, p, func(ctx *fasthttp.RequestCtx) {
			calls++
			ctx.Request.Header.Set("CF-Connecting-IP", "203.0.113.99")
			if got := Resolve(ctx, false); got.IP != "203.0.113.8" || got.Source != "cloudflare" {
				t.Errorf("cached identity changed: %+v", got)
			}
		})(ctx)
		if agent == "fixture-agent" {
			if calls != 1 {
				t.Fatal("valid request blocked")
			}
		} else if calls != 0 || ctx.Response.StatusCode() != fasthttp.StatusForbidden || !strings.Contains(string(ctx.Response.Body()), "UNVERIFIED_PROXY") {
			t.Fatal("invalid user agent accepted")
		}
	}
}
