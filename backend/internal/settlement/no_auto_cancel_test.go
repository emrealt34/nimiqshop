package settlement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

/* no_auto_cancel_test.go — the tracker's side of the rule.
 *
 * There used to be a sweep here: an order the buyer abandoned before paying
 * was cancelled at the supplier in batches, on a timer, so it would not "hold
 * a payment slot". It is gone (owner, 2026-10-19: "sipariş iptali öldürme
 * yapılmaz" — an order is never killed automatically).
 *
 * Withdrawing an order is a one-way door: once it is cancelled, a payment
 * that was already on its way has nowhere to land, and the buyer is left
 * holding money for an order the shop destroyed. An abandoned order lapses on
 * its own at the supplier; accelerating that is not the shop's business, and
 * it is never worth the risk. The only cancellation left is a human pressing
 * the button on one order they are looking at, in the admin console.
 *
 * This test runs a real tracker pass against an abandoned order and asserts
 * the one thing that matters: the supplier is asked, never told to delete.
 */

func TestTheTrackerNeverCancelsAnAbandonedOrder(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The buyer never paid, and the supplier says so.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"order_id": "sup-abandoned", "status": "WaitingForPayment", "payment_state": "WaitingForPayment",
		})
	}))
	defer srv.Close()

	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	q := db.Quote{
		ID:                  "q-abandoned",
		UserID:              "buyer",
		Status:              "expired",
		ProductID:           "amazon",
		ProductCountry:      "TR",
		Denomination:        "100 TRY",
		Quantity:            1,
		CustomerEmail:       "buyer@example.test",
		Coin:                "BTC",
		Network:             "bitcoin",
		WalletAddress:       "bc1qtest",
		CoinAmount:          "0.00042",
		SupplierOrderID:     "sup-abandoned",
		SupplierStatus:      "WaitingForPayment",
		PaymentExpiry:       now.Add(-2 * time.Hour),
		ExpiresAt:           now.Add(-2 * time.Hour),
		CreatedAt:           now.Add(-3 * time.Hour),
		UpdatedAt:           now.Add(-3 * time.Hour),
		EndUserIP:           "203.0.113.7",
		PurchaseFingerprint: "cart-fingerprint",
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}

	w := &OrderTracker{
		Store: store,
		CR: cryptorefills.NewClient(cryptorefills.ClientOptions{
			BaseURL:    srv.URL,
			PartnerID:  "partner-test",
			PartnerKey: "cr_pk_test",
		}),
		Interval:   5 * time.Second,
		StaleAfter: time.Hour,
		MaxOrders:  50,
		nextPollAt: map[string]time.Time{
			// Already due: this pass must actually poll the order.
			q.ID: now.Add(-time.Minute),
		},
		consecErrors: map[string]int{},
		lastErrLog:   map[string]errLog{},
	}
	for i := 0; i < 3; i++ {
		w.tick(context.Background())
	}

	mu.Lock()
	seen := make([]string, len(calls))
	copy(seen, calls)
	mu.Unlock()
	for _, c := range seen {
		if len(c) >= 3 && c[:3] == "DEL" {
			t.Fatalf("the tracker cancelled an abandoned order: %v", seen)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the tracker never polled the order, so this test proves nothing")
	}
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SupplierCancelState != "" {
		t.Fatalf("supplier_cancel_state = %q: only an operator may ask to cancel", after.SupplierCancelState)
	}
	if after.Status != q.Status {
		t.Fatalf("status = %q, want the abandoned order left where it was (%q)", after.Status, q.Status)
	}
}
