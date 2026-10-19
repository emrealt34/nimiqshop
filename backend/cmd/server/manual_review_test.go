package main

import (
	"net/http"
	"testing"
)

/* manual_review_test.go — the operator's queue is not the buyer's waiting
 * room.
 *
 * Owner (2026-10-19): "elle incelemeler yeni ürünleri bloklamamalı … elle
 * incelemede kalmalılar ama yeni şeyleri bloklamamalılar." A quote flagged
 * for manual review keeps its status — the buyer sees "under review", the
 * console keeps it in the review list — and the buyer is still allowed to
 * buy something else. Holding the whole shop shut for a stuck order bought
 * neither side anything: refusing the next order never fixed the first one.
 */
func TestAQuoteUnderManualReviewDoesNotBlockANewPurchase(t *testing.T) {
	s := bootStack(t, stackOptions{})
	auth, _ := s.signIn()

	// --- 1. one purchase, then the operator flags it for review ------------
	first := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, auth)
	if first.status >= 300 {
		t.Fatalf("first quote: %d %s", first.status, truncate(first.body))
	}
	stuck := quoteIDFrom(t, first)
	if err := s.h.Store.MarkQuoteManualReview(stuck, "test: the supplier needs a human on this one"); err != nil {
		t.Fatalf("flag for review: %v", err)
	}
	// It IS a review now, and it stays one — nothing here silently closes it.
	read := s.do(http.MethodGet, "/api/quotes/"+stuck, nil, auth)
	if read.status >= 300 {
		t.Fatalf("read the stuck quote: %d %s", read.status, truncate(read.body))
	}
	if q, _ := read.json(t)["quote"].(map[string]any); q["status"] != "manual_review" {
		t.Fatalf("status = %v, want manual_review", q["status"])
	}

	// --- 2. the SAME buyer buys something ELSE -----------------------------
	second := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-airbnb", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 25, "email": "buyer@example.com",
	}, auth)
	if second.status >= 300 {
		t.Fatalf("a manual review must not block a new purchase: %d %s", second.status, truncate(second.body))
	}
	if other := quoteIDFrom(t, second); other == stuck {
		t.Fatal("the buyer must get a NEW quote, not the stuck one handed back")
	}

	// --- 3. and the review is still there for the operator to work ---------
	after := s.do(http.MethodGet, "/api/quotes/"+stuck, nil, auth)
	q, _ := after.json(t)["quote"].(map[string]any)
	if q["status"] != "manual_review" {
		t.Fatalf("the review must stay open for the operator, got %v", q["status"])
	}
}
