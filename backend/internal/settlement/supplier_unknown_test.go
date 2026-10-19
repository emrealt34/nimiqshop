package settlement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

/* supplier_unknown_test.go — the tracker against an order the supplier has
 * never heard of.
 *
 * Every order the shop created before it moved to the v6 PARTNER API answers
 * a bare 404. The tracker used to read that as a permanent supplier refusal
 * and flag the order for manual review on every pass (5-minute backoff, for
 * as long as the order was polled) — a queue full of rows nobody could act
 * on, all saying the same thing.
 *
 * Now the 404 is recorded once, the order stops being polled at all, and it
 * is only flagged for a human when money could actually be sitting on it.
 */

func TestAPolledOrderTheSupplierCannotSeeStopsBeingPolled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // bare 404, exactly like the partner API
	}))
	defer srv.Close()

	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	q := db.Quote{
		ID:              "q-invisible",
		UserID:          "buyer",
		Status:          "awaiting_payment",
		ProductID:       "amazon",
		ProductCountry:  "TR",
		Denomination:    "100 TRY",
		Quantity:        1,
		CustomerEmail:   "buyer@example.test",
		Coin:            "BTC",
		Network:         "bitcoin",
		WalletAddress:   "bc1qtest",
		CoinAmount:      "0.00042",
		SupplierOrderID: "sup-invisible",
		SupplierStatus:  "WaitingForPayment",
		PaymentExpiry:   now.Add(20 * time.Minute),
		ExpiresAt:       now.Add(20 * time.Minute),
		CreatedAt:       now,
		UpdatedAt:       now,
		EndUserIP:       "203.0.113.7",
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}

	w := &OrderTracker{
		Store: store,
		CR: cryptorefills.NewClient(cryptorefills.ClientOptions{
			BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test",
		}),
		Interval: 5 * time.Second, StaleAfter: time.Hour, MaxOrders: 50,
		nextPollAt:   map[string]time.Time{q.ID: now.Add(-time.Minute)},
		consecErrors: map[string]int{},
		lastErrLog:   map[string]errLog{},
	}
	w.tick(context.Background())

	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SupplierUnknownAt.IsZero() {
		t.Fatal("the supplier's 404 must be recorded on the order")
	}
	if after.NeedsSupplierPoll() {
		t.Fatal("an order the supplier cannot see must drop out of the poll set")
	}
	// Money was possible here (the buyer may have paid an invoice the shop
	// can no longer read), so a human is told — once.
	if after.Status != "manual_review" {
		t.Fatalf("status = %q, want manual_review: a possibly-paid order nobody can read needs an operator", after.Status)
	}
}

// A long-dead, unpaid invoice the supplier cannot see is simply finished:
// flagging it would fill the review queue with rows nobody can act on.
func TestADeadUnpaidInvoiceTheSupplierCannotSeeIsNotFlagged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	q := db.Quote{
		ID: "q-dead-invisible", UserID: "buyer", Status: "expired",
		ProductID: "amazon", ProductCountry: "TR", Denomination: "100 TRY", Quantity: 1,
		CustomerEmail: "buyer@example.test", SupplierOrderID: "sup-dead",
		SupplierStatus: "WaitingForPayment", PaymentExpiry: now.Add(-time.Hour),
		ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
		UpdatedAt: now.Add(-2 * time.Hour), EndUserIP: "203.0.113.7",
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	w := &OrderTracker{
		Store: store,
		CR: cryptorefills.NewClient(cryptorefills.ClientOptions{
			BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test",
		}),
		Interval: 5 * time.Second, StaleAfter: time.Hour, MaxOrders: 50,
		nextPollAt:   map[string]time.Time{q.ID: now.Add(-time.Minute)},
		consecErrors: map[string]int{},
		lastErrLog:   map[string]errLog{},
	}
	w.tick(context.Background())

	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SupplierUnknownAt.IsZero() {
		t.Fatal("the supplier's 404 must still be recorded")
	}
	if after.Status != "expired" {
		t.Fatalf("status = %q, want the dead unpaid invoice left alone (expired)", after.Status)
	}
}
