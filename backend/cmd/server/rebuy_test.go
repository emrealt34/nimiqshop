package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The live bug (owner, 2026-10-18): "unipin 5 TL aldım, sonra tekrar aynı
// ürünü almaya çalışınca beni tamamlanmış order sayfasına attı". The
// storefront derives its Idempotency-Key from the CART CONTENTS, so the
// second purchase of the same product arrived under the key of the first,
// already-fulfilled order — and the shop replayed that finished order as if
// it had just been created. A key whose order is over (paid and delivered,
// refunded, or dead without money) must start a FRESH order instead.
func TestRebuyingTheSameCartAfterFulfilmentStartsAFreshOrder(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})
	auth, address := s.signIn()
	if res := s.do(http.MethodPost, "/api/admin/test-accounts", map[string]any{"address": address}, s.adminHeaders()); res.status != 201 {
		t.Fatalf("list test wallet: %d %s", res.status, res.body)
	}
	cart := map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}
	key := map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)}

	first := s.do(http.MethodPost, "/api/quotes", cart, key)
	firstID := quoteIDFrom(t, first)
	if first.status != 201 {
		t.Fatalf("first checkout should be a payable creation (201), got %d %s", first.status, truncate(first.body))
	}

	// A retry of the SAME request while the invoice is live is a retry: the
	// same order comes back, no second order is opened.
	retry := s.do(http.MethodPost, "/api/quotes", cart, key)
	if id := quoteIDFrom(t, retry); id != firstID {
		t.Fatalf("retry under a live key must replay the live order %s, got %s", firstID, id)
	}

	// Pay it (simulated) and wait for delivery.
	if pay := s.do(http.MethodPost, "/api/quotes/"+firstID+"/test-pay", map[string]any{}, auth); pay.status != 200 {
		t.Fatalf("test-pay: %d %s", pay.status, pay.body)
	}
	status := ""
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && status != "fulfilled"; {
		status = quoteStatus(s.do(http.MethodGet, "/api/quotes/"+firstID, nil, auth).json(t))
		if status != "fulfilled" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if status != "fulfilled" {
		t.Fatalf("first order never reached fulfilled (status %q)", status)
	}
	// The owner's own order page says so too: the checkout is settled.
	if got := s.do(http.MethodGet, "/api/quotes/"+firstID, nil, auth).json(t); got["checkout_settled"] != true {
		t.Fatalf("a fulfilled order must report checkout_settled=true: %s", truncate(mustJSON(t, got)))
	}

	// Buying the same cart again — same key, because that is what the
	// storefront sends — must be a NEW, payable order, not the finished one.
	second := s.do(http.MethodPost, "/api/quotes", cart, key)
	secondID := quoteIDFrom(t, second)
	if secondID == firstID {
		t.Fatalf("rebuy replayed the fulfilled order %s instead of opening a new one: %s", firstID, truncate(second.body))
	}
	if second.status != 201 || second.json(t)["status"] != "awaiting_payment" || second.json(t)["can_pay"] != true {
		t.Fatalf("rebuy must land on a payable invoice: %d %s", second.status, truncate(second.body))
	}
	// The finished order is untouched and still the buyer's.
	if st := quoteStatus(s.do(http.MethodGet, "/api/quotes/"+firstID, nil, auth).json(t)); st != "fulfilled" {
		t.Fatalf("first order changed state after the rebuy: %q", st)
	}
	// And from now on the key follows the NEW order: a retry replays it,
	// never the old one and never a third order.
	if id := quoteIDFrom(t, s.do(http.MethodPost, "/api/quotes", cart, key)); id != secondID {
		t.Fatalf("retry after rebuy must replay the new order %s, got %s", secondID, id)
	}
	// The same rule holds for the cart (batch) endpoint.
	batchCart := map[string]any{"items": []map[string]any{{"product_id": "test-steam", "country": "US", "quantity": 1, "denomination": "50 USD", "product_value": 50}}, "email": "buyer@example.com"}
	batchKey := map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)}
	b1 := quoteIDFrom(t, s.do(http.MethodPost, "/api/quotes/batch", batchCart, batchKey))
	if pay := s.do(http.MethodPost, "/api/quotes/"+b1+"/test-pay", map[string]any{}, auth); pay.status != 200 {
		t.Fatalf("batch test-pay: %d %s", pay.status, pay.body)
	}
	status = ""
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && status != "fulfilled"; {
		status = quoteStatus(s.do(http.MethodGet, "/api/quotes/"+b1, nil, auth).json(t))
		if status != "fulfilled" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if status != "fulfilled" {
		t.Fatalf("batch order never reached fulfilled (status %q)", status)
	}
	if b2 := quoteIDFrom(t, s.do(http.MethodPost, "/api/quotes/batch", batchCart, batchKey)); b2 == b1 {
		t.Fatalf("batch rebuy replayed the fulfilled order %s", b1)
	}
}

// The same rule on the REAL pipeline (TEST_MODE off, in-process supplier
// mock): a delivered order and a lapsed-unpaid order are both over, so the
// cart-derived key starts a new order after either — while a live invoice
// keeps being replayed, under its own key or a fresh one.
func TestRebuyAfterRealSupplierDeliveryOrExpiryStartsAFreshOrder(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false})
	auth, _ := s.signIn()
	cart := map[string]any{
		"product_id": "test-airbnb", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 25, "email": "buyer@example.com",
	}
	withKey := func(k string) map[string]string {
		return map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": k}
	}
	supplierOrder := func(res resp) string {
		sid, _ := res.json(t)["supplier_order_id"].(string)
		if sid == "" {
			t.Fatalf("supplier order id missing: %s", truncate(res.body))
		}
		return sid
	}
	mock := func(sid, action string, body string) {
		t.Helper()
		r, err := http.Post(s.supplier.URL+"/mock/orders/"+sid+"/"+action, "application/json", strings.NewReader(body)) //nolint:noctx // in-process fixture
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("supplier fixture %s: %d", action, r.StatusCode)
		}
	}
	waitStatus := func(id, want string) {
		t.Helper()
		status := ""
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && status != want; {
			s.do(http.MethodPost, "/api/quotes/"+id+"/refresh", nil, auth)
			status = quoteStatus(s.do(http.MethodGet, "/api/quotes/"+id, nil, auth).json(t))
			if status != want {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if status != want {
			t.Fatalf("quote %s never reached %s (status %q)", id, want, status)
		}
	}

	key := newIdempotencyKey(t)
	first := s.do(http.MethodPost, "/api/quotes", cart, withKey(key))
	firstID := quoteIDFrom(t, first)
	if first.status != 201 {
		t.Fatalf("first live checkout: %d %s", first.status, truncate(first.body))
	}
	mock(supplierOrder(first), "pay", "{}")
	mock(supplierOrder(first), "advance", "{}")
	waitStatus(firstID, "fulfilled")

	// Delivered → the same key opens a new, payable order.
	second := s.do(http.MethodPost, "/api/quotes", cart, withKey(key))
	secondID := quoteIDFrom(t, second)
	if secondID == firstID || second.status != 201 || second.json(t)["can_pay"] != true {
		t.Fatalf("rebuy after delivery must be a new payable order: %d %s", second.status, truncate(second.body))
	}

	// A fresh key for the same cart while that invoice is LIVE is a retry:
	// the live order comes back, nothing new is opened.
	alias := newIdempotencyKey(t)
	if res := s.do(http.MethodPost, "/api/quotes", cart, withKey(alias)); quoteIDFrom(t, res) != secondID || res.json(t)["reused"] != true {
		t.Fatalf("same cart under a fresh key must reuse the live order %s: %d %s", secondID, res.status, truncate(res.body))
	}

	// The supplier lets the invoice lapse unpaid.
	mock(supplierOrder(second), "status", `{"status":"Expired"}`)
	waitStatus(secondID, "expired")

	// Lapsed unpaid → over: the key opens a new order again…
	third := s.do(http.MethodPost, "/api/quotes", cart, withKey(key))
	thirdID := quoteIDFrom(t, third)
	if thirdID == secondID || thirdID == firstID || third.status != 201 {
		t.Fatalf("rebuy after expiry must be a new payable order: %d %s", third.status, truncate(third.body))
	}
	// …and the alias, whose own order is dead, moves onto the LIVE order
	// instead of handing back the dead one or opening a fourth.
	if res := s.do(http.MethodPost, "/api/quotes", cart, withKey(alias)); quoteIDFrom(t, res) != thirdID {
		t.Fatalf("alias key must follow the live order %s: %d %s", thirdID, res.status, truncate(res.body))
	}
	for _, id := range []string{firstID, secondID} {
		if got := s.do(http.MethodGet, "/api/quotes/"+id, nil, auth).json(t); got["checkout_settled"] != true {
			t.Errorf("finished order %s must report checkout_settled=true", id)
		}
	}
	if got := s.do(http.MethodGet, "/api/quotes/"+thirdID, nil, auth).json(t); got["checkout_settled"] != false {
		t.Errorf("the live order must report checkout_settled=false")
	}
}
