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

// Owner (2026-10-18): "aynı ürünü, önceki faturası hâlâ geçerliyken tekrar
// almaya çalışırsa dükkân SORMALI — o siparişe mi gidelim, yeni mi açalım?"
// The storefront can only ask that question if the shop tells it that a
// checkout POST handed back an EARLIER order instead of creating one. So, on
// the real pipeline (test-mode quotes never block, so they never get handed
// back): every replay of a live, unpaid invoice — same key or a fresh key for
// the same cart — carries `reused: true`, a genuinely new order never does,
// and the buyer's "open a new one" (ack_active_checkout under a fresh key)
// creates a second payable order while the first stays exactly as it was.
func TestLiveSameCartIsHandedBackAsReusedAndAckOpensASecondOrder(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false})
	supplierOrder := func(t *testing.T, res resp) string {
		t.Helper()
		sid, _ := res.json(t)["supplier_order_id"].(string)
		if sid == "" {
			t.Fatalf("supplier order id missing: %s", truncate(res.body))
		}
		return sid
	}
	mock := func(t *testing.T, sid, action string, body string) {
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
	item := map[string]any{"product_id": "test-airbnb", "country": "US", "quantity": 1, "denomination": "range", "product_value": 25}
	single := map[string]any{"product_id": "test-airbnb", "country": "US", "quantity": 1, "denomination": "range", "product_value": 25, "email": "buyer@example.com"}
	batch := map[string]any{"items": []map[string]any{item, {"product_id": "test-airbnb", "country": "US", "quantity": 1, "denomination": "range", "product_value": 50}}, "email": "buyer@example.com"}
	acked := func(cart map[string]any) map[string]any {
		out := map[string]any{"ack_active_checkout": true}
		for k, v := range cart {
			out[k] = v
		}
		return out
	}
	for _, tc := range []struct {
		name, path string
		cart       map[string]any
	}{{"single", "/api/quotes", single}, {"batch", "/api/quotes/batch", batch}} {
		t.Run(tc.name, func(t *testing.T) {
			// One buyer per endpoint: the live order the previous subtest
			// leaves behind would (correctly) hold this one's new cart.
			auth, _ := s.signIn()
			withKey := func(k string) map[string]string {
				return map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": k}
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
			cartKey := newIdempotencyKey(t)
			first := s.do(http.MethodPost, tc.path, tc.cart, withKey(cartKey))
			firstID := quoteIDFrom(t, first)
			if first.status != 201 || first.json(t)["can_pay"] != true {
				t.Fatalf("first checkout must be a payable creation: %d %s", first.status, truncate(first.body))
			}
			if _, present := first.json(t)["reused"]; present {
				t.Fatalf("a freshly created order must not say reused: %s", truncate(first.body))
			}

			// The buyer comes back to the same cart while the invoice is still
			// live. The storefront sends the same cart-derived key: the shop
			// hands the live order back AND says so.
			again := s.do(http.MethodPost, tc.path, tc.cart, withKey(cartKey))
			if id := quoteIDFrom(t, again); id != firstID {
				t.Fatalf("same cart under the live key must hand back %s, got %s", firstID, id)
			}
			if body := again.json(t); again.status != 201 || body["reused"] != true || body["status"] != "awaiting_payment" || body["can_pay"] != true {
				t.Fatalf("handback of a live invoice must be 201 + reused:true + payable: %d %s", again.status, truncate(again.body))
			}
			if exp, _ := again.json(t)["payment_expires_at"].(string); exp == "" {
				t.Fatalf("the handback must carry payment_expires_at so the prompt can say how long the order stays open: %s", truncate(again.body))
			}
			// A fresh key for the same cart (another device, cleared storage)
			// is the same handback, with the same marker.
			if res := s.do(http.MethodPost, tc.path, tc.cart, withKey(newIdempotencyKey(t))); quoteIDFrom(t, res) != firstID || res.json(t)["reused"] != true {
				t.Fatalf("same cart under a fresh key must hand back %s as reused: %d %s", firstID, res.status, truncate(res.body))
			}

			// "Open a new order": the acknowledgement under a fresh key opens
			// a SECOND payable order beside the live one.
			ackKey := newIdempotencyKey(t)
			second := s.do(http.MethodPost, tc.path, acked(tc.cart), withKey(ackKey))
			secondID := quoteIDFrom(t, second)
			if second.status != 201 || second.json(t)["can_pay"] != true || secondID == firstID {
				t.Fatalf("ack under a fresh key must open a new payable order: %d %s", second.status, truncate(second.body))
			}
			if _, present := second.json(t)["reused"]; present {
				t.Fatalf("the new order must not say reused: %s", truncate(second.body))
			}
			// The first order is untouched: still live, still payable.
			if got := s.do(http.MethodGet, "/api/quotes/"+firstID, nil, auth).json(t); quoteStatus(got) != "awaiting_payment" || got["can_pay"] != true {
				t.Fatalf("opening a new order must leave the first one payable: %s", truncate(mustJSON(t, got)))
			}
			// The retry contract still holds for the new order: the same ack
			// request under the same key replays IT (as a reuse), never the
			// first order and never a third one.
			if res := s.do(http.MethodPost, tc.path, acked(tc.cart), withKey(ackKey)); quoteIDFrom(t, res) != secondID || res.json(t)["reused"] != true {
				t.Fatalf("retry of the ack request must replay %s as reused: %d %s", secondID, res.status, truncate(res.body))
			}
			// And the original cart key still points at the first order.
			if res := s.do(http.MethodPost, tc.path, tc.cart, withKey(cartKey)); quoteIDFrom(t, res) != firstID || res.json(t)["reused"] != true {
				t.Fatalf("the cart key must keep replaying %s as reused: %d %s", firstID, res.status, truncate(res.body))
			}
			// The acknowledgement is an answer to the gate, not part of the
			// purchase: the new order's own key retried WITHOUT the flag is
			// still a retry of that order, never an IDEMPOTENCY_MISMATCH.
			if res := s.do(http.MethodPost, tc.path, tc.cart, withKey(ackKey)); quoteIDFrom(t, res) != secondID || res.json(t)["reused"] != true {
				t.Fatalf("retry of the new order's key without the ack flag must replay %s as reused: %d %s", secondID, res.status, truncate(res.body))
			}
			// The first order is paid and delivered; the second stays live.
			mock(t, supplierOrder(t, first), "pay", "{}")
			mock(t, supplierOrder(t, first), "advance", "{}")
			waitStatus(firstID, "fulfilled")
			// The same cart again — fresh key, no flag — is the question
			// about the order that IS still open (the second one), not a
			// replay of the delivered one and not the ACTIVE_CHECKOUT wall.
			// This was the gap: the second order's fingerprint carried the
			// ack flag, so the same cart never matched it again.
			for _, key := range []string{newIdempotencyKey(t), cartKey} {
				res := s.do(http.MethodPost, tc.path, tc.cart, withKey(key))
				if res.status != 201 || quoteIDFrom(t, res) != secondID || res.json(t)["reused"] != true || res.json(t)["can_pay"] != true {
					t.Fatalf("same cart with %s open and %s delivered must hand back the open one as reused: %d %s", secondID, firstID, res.status, truncate(res.body))
				}
			}
			// The second order is paid and delivered too.
			mock(t, supplierOrder(t, second), "pay", "{}")
			mock(t, supplierOrder(t, second), "advance", "{}")
			waitStatus(secondID, "fulfilled")
			// Once both are over, the cart key is spent: the same cart opens
			// a third, new order (the 2026-10-18 rebuy rule).
			if res := s.do(http.MethodPost, tc.path, tc.cart, withKey(cartKey)); res.status != 201 || quoteIDFrom(t, res) == firstID || quoteIDFrom(t, res) == secondID {
				t.Fatalf("rebuy after both orders settled must be a new order: %d %s", res.status, truncate(res.body))
			}
		})
	}
}
