package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestServeStackForBrowser is not a test of anything by itself. It boots the
// FULL stack — real handlers, Badger, settlement tracker, the in-process
// supplier mock, TEST_MODE off — on a fixed address, signs a buyer in and
// keeps the server running so the Playwright full-stack specs
// (tests/e2e/fullstack-*.spec.ts) can drive the real storefront, in a real
// browser, against the real backend instead of a mocked API. Skipped unless
// BROWSER_STACK_ADDR is set:
//
//	BROWSER_STACK_ADDR=127.0.0.1:8099 BROWSER_STACK_OUT=/tmp/stack.json \
//	  go test ./cmd/server -run TestServeStackForBrowser -count=1 -timeout 40m
//
// It writes {base_url, supplier_url, bearer, uid, address} to
// BROWSER_STACK_OUT and stops as soon as the file named by BROWSER_STACK_STOP
// (default: BROWSER_STACK_OUT + ".stop") exists, or after BROWSER_STACK_TIMEOUT
// (default 20m). See `npm run test:e2e:fullstack`.
func TestServeStackForBrowser(t *testing.T) {
	addr := os.Getenv("BROWSER_STACK_ADDR")
	if addr == "" {
		t.Skip("set BROWSER_STACK_ADDR (e.g. 127.0.0.1:8099) to serve the stack for a browser")
	}
	out := os.Getenv("BROWSER_STACK_OUT")
	if out == "" {
		t.Fatal("BROWSER_STACK_OUT (path of the connection file to write) is required")
	}
	stop := os.Getenv("BROWSER_STACK_STOP")
	if stop == "" {
		stop = out + ".stop"
	}
	timeout := 20 * time.Minute
	if v := os.Getenv("BROWSER_STACK_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("BROWSER_STACK_TIMEOUT: %v", err)
		}
		timeout = d
	}
	_ = os.Remove(stop)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	s := bootStack(t, stackOptions{testMode: false, listener: ln})
	auth, address := s.signIn()
	session := s.do(http.MethodGet, "/api/auth/session", nil, auth).json(t)
	user, _ := session["user"].(map[string]any)
	uid, _ := user["id"].(string)
	if uid == "" {
		t.Fatalf("session did not name the user: %v", session)
	}
	info, err := json.Marshal(map[string]string{
		"base_url":     s.baseURL,
		"supplier_url": s.supplier.URL,
		"bearer":       strings.TrimPrefix(auth["Authorization"], "Bearer "),
		"uid":          uid,
		"address":      address,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, info, 0o600); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("stack serving at %s (supplier mock %s); buyer %s; stop file %s", s.baseURL, s.supplier.URL, address, stop)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stop); err == nil {
			t.Log("stop file seen; shutting down")
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Log("timeout reached; shutting down")
}
