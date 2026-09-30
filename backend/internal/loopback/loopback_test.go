package loopback

import (
	"net"
	"strings"
	"testing"
)

func TestIsHostLiterals(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{IPv4.String(), true},
		{"127.0.0.2", true},
		{"127.255.255.254", true},
		{IPv6.String(), true},
		{"[" + IPv6.String() + "]", true},
		{"::ffff:" + IPv4.String(), true},
		{" " + IPv4.String() + " ", true},
		{"", false},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"fe80::1", false},
		{"2001:db8::1", false},
		{"128.0.0.1", false},
		// Syntactically invalid names never reach a resolver.
		{"not a host", false},
		{"a..b", false},
	} {
		if got := IsHost(tc.in); got != tc.want {
			t.Errorf("IsHost(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Every name the system itself maps to the loopback address (the hosts-file
// entry for 127.0.0.1) must be recognized, in any letter case and with or
// without the trailing dot of a fully-qualified name.
func TestIsHostResolvedLoopbackNames(t *testing.T) {
	names, err := net.LookupAddr(IPv4.String())
	if err != nil || len(names) == 0 {
		t.Skip("no reverse mapping for the loopback address on this machine")
	}
	for _, n := range names {
		for _, v := range []string{n, strings.ToUpper(n), strings.TrimSuffix(n, ".") + "."} {
			if !IsHost(v) {
				t.Errorf("IsHost(%q) = false, want true", v)
			}
		}
	}
}

func TestDefaultProxyCIDRs(t *testing.T) {
	got := DefaultProxyCIDRs()
	if len(got) != 2 || got[0] != IPv4.String()+"/32" || got[1] != IPv6.String()+"/128" {
		t.Fatalf("unexpected default CIDRs: %v", got)
	}
}

func TestHostPort(t *testing.T) {
	if got := HostPort("9020"); got != IPv4.String()+":9020" {
		t.Fatalf("HostPort = %q", got)
	}
}
