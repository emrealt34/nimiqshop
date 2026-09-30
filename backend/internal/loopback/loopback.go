// Package loopback is the one place that decides whether a host or address
// refers to this machine. The development-only exceptions (plain-HTTP
// origins under ALLOW_HTTP_LOCAL, the local supplier mock, the default
// trusted-proxy list) all consult it, so the definition lives here once
// instead of as address literals scattered through the code base.
package loopback

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"
)

// IPv4 is the IPv4 loopback address.
var IPv4 = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// IPv6 is the IPv6 loopback address.
var IPv6 = netip.IPv6Loopback()

// resolveTimeout bounds the name lookup IsHost performs for non-literal
// hosts. Loopback names come from the hosts file, so this is generous.
const resolveTimeout = 2 * time.Second

// DefaultProxyCIDRs is the trusted-proxy allow-list used when
// TRUSTED_PROXY_CIDRS is unset: only a reverse proxy running on this machine
// may supply forwarded-for headers.
func DefaultProxyCIDRs() []string {
	return []string{
		netip.PrefixFrom(IPv4, IPv4.BitLen()).String(),
		netip.PrefixFrom(IPv6, IPv6.BitLen()).String(),
	}
}

// HostPort joins the IPv4 loopback address with port, for listeners that must
// never be reachable from another machine.
func HostPort(port string) string {
	return net.JoinHostPort(IPv4.String(), port)
}

// IsHost reports whether a URL hostname refers to this machine: a loopback IP
// literal (127.0.0.0/8, ::1, the IPv4-mapped forms, with or without brackets)
// or a name whose every address resolves to loopback — which is what
// "localhost" is on any sane system, and what also covers hosts-file aliases
// developers set up. A name that resolves to anything else, or that cannot be
// resolved at all, is NOT loopback: callers use this to fence development
// exceptions, so failing closed is the right default.
func IsHost(h string) bool {
	h = strings.TrimSuffix(strings.Trim(strings.TrimSpace(h), "[]"), ".")
	if h == "" || len(h) > 253 {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().IsLoopback()
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !a.Unmap().IsLoopback() {
			return false
		}
	}
	return true
}
