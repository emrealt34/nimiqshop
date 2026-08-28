package clientip

import (
	"net"
	"testing"

	"github.com/valyala/fasthttp"
)

func newCtx(remoteIP string, headers map[string]string) *fasthttp.RequestCtx {
	var ctx fasthttp.RequestCtx
	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.Header.SetHost("shop.example.com")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	ctx.Init(&req, &net.TCPAddr{IP: net.ParseIP(remoteIP)}, nil)
	return &ctx
}

func TestResolve_Direct(t *testing.T) {
	ctx := newCtx("203.0.113.10", nil)
	info := Resolve(ctx, true)
	if info.IP != "203.0.113.10" {
		t.Fatalf("direct mode: expected 203.0.113.10, got %q", info.IP)
	}
	if info.Cloudflare || info.Country != "" {
		t.Fatalf("direct mode: expected cloudflare=false country='' got %+v", info)
	}
}

func TestResolve_Cloudflare(t *testing.T) {
	ctx := newCtx("10.0.0.5", map[string]string{
		"cf-ray":           "abc123",
		"CF-Connecting-IP": "203.0.113.99",
		"CF-IPCountry":     "TR",
	})
	info := Resolve(ctx, true)
	if info.IP != "203.0.113.99" {
		t.Fatalf("cloudflare: expected 203.0.113.99 got %q", info.IP)
	}
	if !info.Cloudflare {
		t.Fatal("cloudflare: expected cloudflare=true")
	}
	if info.Country != "TR" {
		t.Fatalf("cloudflare: expected country TR, got %q", info.Country)
	}
}

func TestResolve_XForwardedFor(t *testing.T) {
	ctx := newCtx("10.0.0.5", map[string]string{
		"X-Forwarded-For": "198.51.100.7, 10.0.0.5",
	})
	info := Resolve(ctx, true)
	if info.IP != "198.51.100.7" {
		t.Fatalf("xff: expected 198.51.100.7 got %q", info.IP)
	}
	if info.Cloudflare {
		t.Fatal("xff: no cf-ray, expected cloudflare=false")
	}
}

func TestResolve_DirectNoTrust(t *testing.T) {
	// trustProxy=false => never trust forwarded headers.
	ctx := newCtx("203.0.113.10", map[string]string{
		"cf-ray":           "abc",
		"CF-Connecting-IP": "198.51.100.200",
		"CF-IPCountry":     "US",
	})
	info := Resolve(ctx, false)
	if info.IP != "203.0.113.10" {
		t.Fatalf("no-trust: expected RemoteIP 203.0.113.10 got %q", info.IP)
	}
	if info.Cloudflare || info.Country != "" {
		t.Fatalf("no-trust: expected cloudflare=false country='' got %+v", info)
	}
}
