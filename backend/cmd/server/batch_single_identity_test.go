package main

import (
	"net/http"
	"testing"
)

// Two identical rows collapse into the single path. It must still enforce the
// exact selected brand and carry the anonymous and active-checkout choices.
// The supplier is the in-process mock; this test never places a real order.
func TestMergedBatchRetainsBrandAndPrivacyInSupplierFlow(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false})
	auth, _ := s.signIn()
	headers := func() map[string]string {
		return map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)}
	}
	row := func(brand, brandID string, value int) map[string]any {
		return map[string]any{"product_id": "test-airbnb", "country": "US", "brand": brand, "brand_id": brandID, "denomination": "range", "product_value": value, "quantity": 1}
	}
	request := func(brand, id string, value int, ack bool) map[string]any {
		return map[string]any{"items": []map[string]any{row(brand, id, value), row(brand, id, value)}, "email": "buyer@example.com", "anonymous": true, "ack_active_checkout": ack}
	}
	rejected := s.do(http.MethodPost, "/api/quotes/batch", request("not-the-brand", "not-the-id", 10, false), headers())
	if rejected.status != http.StatusConflict {
		t.Fatalf("invalid brand was not refused: %d %s", rejected.status, rejected.body)
	}
	if n := supplierOrders(t, s); n != 0 {
		t.Fatalf("invalid brand created %d supplier orders", n)
	}
	first := s.do(http.MethodPost, "/api/quotes/batch", request("test-airbnb", "mock-test-airbnb", 10, false), headers())
	if first.status != http.StatusCreated {
		t.Fatalf("first batch: %d %s", first.status, first.body)
	}
	id := quoteIDFrom(t, first)
	q, err := s.h.Store.GetQuote(id)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Anonymous || q.Quantity != 2 {
		t.Fatalf("anonymous or quantity lost: %+v", q)
	}
	blocked := s.do(http.MethodPost, "/api/quotes/batch", request("test-airbnb", "mock-test-airbnb", 11, false), headers())
	if blocked.status != http.StatusConflict || blocked.json(t)["code"] != "ACTIVE_CHECKOUT" {
		t.Fatalf("expected active checkout hold: %d %s", blocked.status, blocked.body)
	}
	approved := s.do(http.MethodPost, "/api/quotes/batch", request("test-airbnb", "mock-test-airbnb", 11, true), headers())
	if approved.status != http.StatusCreated {
		t.Fatalf("buyer override lost: %d %s", approved.status, approved.body)
	}
}
