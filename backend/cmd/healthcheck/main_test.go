package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nimiqshop/internal/loopback"
)

func TestProbe(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		wantOK bool
	}{
		{"healthy", http.StatusOK, `{"ok":true,"uptime_s":12}`, true},
		{"reports not ok", http.StatusOK, `{"ok":false}`, false},
		{"non-200", http.StatusServiceUnavailable, `{"ok":true}`, false},
		{"garbage body", http.StatusOK, `<html>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/health" {
					t.Errorf("probed %s, want /api/health", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			addr := strings.TrimPrefix(srv.URL, "http://")
			err := probe(addr, 2*time.Second)
			if tc.wantOK && err != nil {
				t.Fatalf("probe(%s): unexpected error %v", addr, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("probe(%s): expected an error", addr)
			}
		})
	}
}

func TestProbeConnectionRefused(t *testing.T) {
	// A closed port must fail fast, within the timeout.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	start := time.Now()
	if err := probe(addr, time.Second); err == nil {
		t.Fatal("expected an error for a closed port")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("probe took %s, expected it to fail fast", time.Since(start))
	}
}

func TestHostPort(t *testing.T) {
	cases := map[string]string{
		":8084":            loopback.HostPort("8084"),
		"0.0.0.0:9000":     loopback.HostPort("9000"),
		"[::]:9001":        loopback.HostPort("9001"),
		"10.0.0.5:8084":    "10.0.0.5:8084",
		"":                 loopback.HostPort("8084"),
		"not-an-address":   loopback.HostPort("8084"),
		"shop.example:443": "shop.example:443",
	}
	for in, want := range cases {
		if got := hostPort(in); got != want {
			t.Errorf("hostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("HEALTHCHECK_TEST_VAR", "  :9999 ")
	if got := envOr("HEALTHCHECK_TEST_VAR", ":1"); got != ":9999" {
		t.Fatalf("envOr trimmed = %q", got)
	}
	t.Setenv("HEALTHCHECK_TEST_VAR", "")
	if got := envOr("HEALTHCHECK_TEST_VAR", ":1"); got != ":1" {
		t.Fatalf("envOr default = %q", got)
	}
}
