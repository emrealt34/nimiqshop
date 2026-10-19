package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
)

/* partner_v6_test.go — the wire-level proof that the shop really is on the
 * v6 PARTNER API when a partner key is configured.
 *
 * Everything here runs against the local supplier mock (extended to speak the
 * partner endpoints), so no order is ever placed with the real supplier. What
 * it pins is the part a unit test cannot: that a buyer's checkout actually
 * reaches POST /v6/partner/orders, authenticated, carrying the quote id as the
 * idempotency key and the buyer's IP — the two things that make the order earn
 * commission and a retry safe.
 */

type capturedCall struct {
	Method string
	Path   string
	Header http.Header
	Body   string
}

type callCapture struct {
	mu    sync.Mutex
	calls []capturedCall
}

func (c *callCapture) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.calls = append(c.calls, capturedCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: string(raw)})
		c.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		next.ServeHTTP(w, r)
	})
}

func (c *callCapture) matching(method, path string) []capturedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []capturedCall
	for _, call := range c.calls {
		if call.Method == method && call.Path == path {
			out = append(out, call)
		}
	}
	return out
}

func TestPartnerCheckoutCreatesTheOrderOnTheV6Endpoint(t *testing.T) {
	cap := &callCapture{}
	s := bootStack(t, stackOptions{
		env:          map[string]string{"CRYPTOREFILLS_PARTNER_KEY": "cr_pk_test_integration"},
		supplierWrap: cap.wrap,
	})
	auth, _ := s.signIn()

	res := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, auth)
	if res.status >= 300 {
		t.Fatalf("quote: %d %s", res.status, truncate(res.body))
	}
	quoteID := quoteIDFrom(t, res)

	creates := cap.matching(http.MethodPost, "/v6/partner/orders")
	if len(creates) != 1 {
		var got []string
		for _, c := range cap.calls {
			got = append(got, c.Method+" "+c.Path)
		}
		t.Fatalf("exactly one partner create expected, got %v", got)
	}
	create := creates[0]
	if got := create.Header.Get("X-CR-Partner-Key"); got != "cr_pk_test_integration" {
		t.Fatalf("the partner key must authenticate the create, got %q", got)
	}
	if create.Header.Get("X-CR-Forwarded-For") == "" {
		t.Fatal("the end-customer IP is required by the partner API (400 missing_required_header without it)")
	}
	var payload struct {
		ExternalOrderID string `json:"external_order_id"`
		User            struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(create.Body), &payload); err != nil {
		t.Fatalf("bad create body: %v", err)
	}
	// The whole point: a retry (crash recovery, timeout) must return THIS
	// order instead of opening a second unpaid one for the same cart.
	if payload.ExternalOrderID != quoteID {
		t.Fatalf("external_order_id = %q, want the quote id %q", payload.ExternalOrderID, quoteID)
	}
	if payload.User.Email != "buyer@example.com" {
		t.Fatalf("the buyer's email must reach the supplier, got %q", payload.User.Email)
	}

	// The order is readable on the partner path and the buyer can pay it.
	get := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, auth)
	if get.status >= 300 {
		t.Fatalf("read quote: %d %s", get.status, truncate(get.body))
	}
	q, _ := get.json(t)["quote"].(map[string]any)
	if q["status"] != "awaiting_payment" {
		t.Fatalf("status = %v, want awaiting_payment", q["status"])
	}
	if q["supplier_order_id"] == nil || q["supplier_order_id"] == "" {
		t.Fatalf("the partner order id must be stored: %v", q["supplier_order_id"])
	}
}

func TestWithoutAPartnerKeyTheSameCheckoutStaysOnV5(t *testing.T) {
	cap := &callCapture{}
	s := bootStack(t, stackOptions{supplierWrap: cap.wrap})
	auth, _ := s.signIn()

	res := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, auth)
	if res.status >= 300 {
		t.Fatalf("quote: %d %s", res.status, truncate(res.body))
	}
	if got := len(cap.matching(http.MethodPost, "/v6/partner/orders")); got != 0 {
		t.Fatalf("without a partner key nothing may be created on the partner endpoint, got %d", got)
	}
	if got := len(cap.matching(http.MethodPost, "/v5/orders")); got != 1 {
		t.Fatalf("one v5 create expected, got %d", got)
	}
	if got := cap.matching(http.MethodPost, "/v5/orders")[0].Header.Get("X-CR-Partner-Key"); got != "" {
		t.Fatalf("no partner header may be sent on v5, got %q", got)
	}
}
