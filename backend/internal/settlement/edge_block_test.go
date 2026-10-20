package settlement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

/* edge_block_test.go — the tracker against a WAF page instead of the API.
 *
 * Live, 2026-10-20 09:18Z: a burst of order polls right after a deploy got
 * nginx's "403 Forbidden" HTML page for about ten seconds. The tracker read
 * every one of those as "the supplier refuses to serve this order" and
 * parked twenty payable invoices under manual review — the buyer of any of
 * them would have been told the order is no longer available for payment,
 * while the supplier was still happily waiting for the money.
 *
 * An HTML page is not the API's answer. The order must stay payable, keep
 * being polled with the ordinary transient backoff, and recover on its own
 * once the API answers again.
 */

const nginxForbidden = "<html>\r\n<head><title>403 Forbidden</title></head>\r\n<body>\r\n<center><h1>403 Forbidden</h1></center>\r\n</body>\r\n</html>\r\n"

func TestAWAFPageDoesNotParkAPayableInvoice(t *testing.T) {
	var blocked atomic.Bool
	blocked.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blocked.Load() {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(nginxForbidden))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "sup-edge", "status": "WaitingForPayment",
			"payment_expires_at": time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339),
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
		ID: "q-edge", UserID: "buyer", Status: "awaiting_payment",
		ProductID: "amazon", ProductCountry: "FR", Denomination: "95 EUR", Quantity: 1,
		CustomerEmail: "buyer@example.test", Coin: "BTC", Network: "Lightning",
		WalletAddress: "lnbc1test", CoinAmount: "0.00136356",
		SupplierOrderID: "sup-edge", SupplierStatus: "WaitingForPayment",
		PaymentExpiry: now.Add(20 * time.Minute), ExpiresAt: now.Add(20 * time.Minute),
		CreatedAt: now, UpdatedAt: now, EndUserIP: "203.0.113.7",
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

	// The WAF answers: the order must come through untouched.
	w.tick(context.Background())
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "awaiting_payment" {
		t.Fatalf("status = %q after an HTML 403, want awaiting_payment: the API never looked at this order", after.Status)
	}
	if after.PaymentBlocked {
		t.Fatal("an HTML 403 must not block payment of a live invoice")
	}
	if !after.SupplierUnknownAt.IsZero() {
		t.Fatal("an HTML page is not the supplier saying it has no such order")
	}
	if !after.NeedsSupplierPoll() {
		t.Fatal("the order must stay in the poll set")
	}
	w.pollMu.Lock()
	consec, next := w.consecErrors[q.ID], w.nextPollAt[q.ID]
	w.pollMu.Unlock()
	if consec != 1 {
		t.Fatalf("an edge answer is a transient failure, want 1 consecutive error, got %d", consec)
	}
	if next.Before(time.Now()) || next.After(time.Now().Add(2*time.Minute)) {
		t.Fatalf("want the first transient backoff (~30s), got next poll at %s", time.Until(next).Round(time.Second))
	}

	// The API is back: the next poll succeeds and the order recovers alone.
	blocked.Store(false)
	w.pollMu.Lock()
	w.nextPollAt[q.ID] = time.Now().Add(-time.Second)
	w.pollMu.Unlock()
	w.tick(context.Background())
	after, err = store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "awaiting_payment" {
		t.Fatalf("status = %q after the API answered again, want awaiting_payment", after.Status)
	}
	w.pollMu.Lock()
	consec = w.consecErrors[q.ID]
	w.pollMu.Unlock()
	if consec != 0 {
		t.Fatalf("a successful poll must clear the error streak, got %d", consec)
	}
}

// The API's own JSON 403 is still a decision and still flags the order.
func TestAJSON403StillFlagsManualReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	q := db.Quote{
		ID: "q-json-403", UserID: "buyer", Status: "awaiting_payment",
		ProductID: "amazon", ProductCountry: "FR", Denomination: "95 EUR", Quantity: 1,
		CustomerEmail: "buyer@example.test", Coin: "BTC", Network: "Lightning",
		WalletAddress: "lnbc1test", CoinAmount: "0.00136356",
		SupplierOrderID: "sup-json-403", SupplierStatus: "WaitingForPayment",
		PaymentExpiry: now.Add(20 * time.Minute), ExpiresAt: now.Add(20 * time.Minute),
		CreatedAt: now, UpdatedAt: now, EndUserIP: "203.0.113.7",
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
	if after.Status != "manual_review" {
		t.Fatalf("status = %q, want manual_review: a JSON 403 is the API's own decision", after.Status)
	}
}
