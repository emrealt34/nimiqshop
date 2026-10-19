package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

/* admin_sync_unknown_test.go — the operator's "Sync with supplier" button
 * against an order the supplier cannot see.
 *
 * Live bug (2026-10-19): the button answered 502 "could not verify supplier
 * status" for every order the v6 PARTNER API did not recognise — every order
 * the shop created before it moved onto the partner API. A 502 says "the
 * supplier is down", so the natural operator response is to press it again,
 * and the answer never changes.
 *
 * A 404 is the supplier answering, and its answer is final. It is now
 * reported as what it is (409, SUPPLIER_ORDER_UNKNOWN), the order is marked
 * so nobody keeps asking, and the local record is left exactly as it was.
 */

func syncHarness(t *testing.T, status int, body map[string]any) (*Handlers, *db.Store) {
	t.Helper()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(srv.Close)
	h := &Handlers{
		Store: store,
		CR: cryptorefills.NewClient(cryptorefills.ClientOptions{
			BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test",
		}),
	}
	return h, store
}

func unknownOrderCandidate(id string) db.Quote {
	now := time.Now().UTC()
	return db.Quote{
		ID:              id,
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
		SupplierOrderID: "sup-" + id,
		SupplierStatus:  "WaitingForPayment",
		PaymentExpiry:   now.Add(20 * time.Minute),
		ExpiresAt:       now.Add(20 * time.Minute),
		CreatedAt:       now,
		UpdatedAt:       now,
		EndUserIP:       "203.0.113.7",
	}
}

func TestAnOrderTheSupplierCannotSeeIsNotA502(t *testing.T) {
	h, store := syncHarness(t, http.StatusNotFound, nil)
	q := unknownOrderCandidate("q-unknown")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	ctx.SetUserValue("id", q.ID)
	h.AdminSyncOrder(ctx)

	if got := ctx.Response.StatusCode(); got != 409 {
		t.Fatalf("status = %d, want 409 (the supplier answered; it is not an outage): %s", got, ctx.Response.Body())
	}
	var body struct {
		Code           string `json:"code"`
		UpstreamStatus int    `json:"upstream_status"`
		Retryable      bool   `json:"retryable"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
		t.Fatalf("response is not the structured error: %v (%s)", err, ctx.Response.Body())
	}
	if body.Code != "SUPPLIER_ORDER_UNKNOWN" {
		t.Fatalf("code = %q, want SUPPLIER_ORDER_UNKNOWN", body.Code)
	}
	if body.UpstreamStatus != 404 || body.Retryable {
		t.Fatalf("upstream_status=%d retryable=%v, want 404 and false", body.UpstreamStatus, body.Retryable)
	}
	// The order is marked, so the tracker and the next click stop asking.
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SupplierUnknownAt.IsZero() {
		t.Fatal("the order must be marked unknown upstream, so nothing keeps polling it")
	}
	// And it is left exactly as it was: no status change, no cancellation.
	if after.Status != q.Status || after.SupplierCancelState != "" {
		t.Fatalf("the local record was changed: status=%q supplier_cancel_state=%q", after.Status, after.SupplierCancelState)
	}
}

// A supplier that really is down is still a 502 — and is NOT marked, because
// next time it may answer.
func TestARealSupplierOutageIsStillA502AndDoesNotMarkTheOrder(t *testing.T) {
	h, store := syncHarness(t, http.StatusBadGateway, nil)
	q := unknownOrderCandidate("q-outage")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	ctx.SetUserValue("id", q.ID)
	h.AdminSyncOrder(ctx)

	if got := ctx.Response.StatusCode(); got != 502 {
		t.Fatalf("status = %d, want 502 for a genuine outage: %s", got, ctx.Response.Body())
	}
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SupplierUnknownAt.IsZero() {
		t.Fatal("a transient outage must not mark the order as unknown upstream")
	}
}

// A successful read heals the marker: the order is visible again the moment
// the supplier says so (a key fixed, an order that appeared).
func TestASuccessfulSyncClearsTheUnknownMarker(t *testing.T) {
	h, store := syncHarness(t, http.StatusOK, map[string]any{
		"order_id": "sup-q-healed", "status": "WaitingForPayment", "payment_state": "WaitingForPayment",
	})
	q := unknownOrderCandidate("q-healed")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSupplierOrderUnknown(q.ID); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	ctx.SetUserValue("id", q.ID)
	h.AdminSyncOrder(ctx)
	if got := ctx.Response.StatusCode(); got != 200 {
		t.Fatalf("status = %d, want 200: %s", got, ctx.Response.Body())
	}
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SupplierUnknownAt.IsZero() {
		t.Fatal("a successful read must clear the unknown marker, so polling resumes by itself")
	}
}

// The console must never offer a cancel button for an order the supplier has
// never heard of: there is nothing at the supplier to cancel.
func TestAnUnknownOrderIsNotOfferedForCancellation(t *testing.T) {
	h, store := syncHarness(t, http.StatusOK, map[string]any{"order_id": "sup-x", "status": "WaitingForPayment"})
	q := unknownOrderCandidate("q-nocancel")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !h.canCancelPartnerOrder(q) {
		t.Fatal("a partner order with no money should normally be cancellable")
	}
	if err := store.MarkSupplierOrderUnknown(q.ID); err != nil {
		t.Fatal(err)
	}
	marked, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.canCancelPartnerOrder(marked) {
		t.Fatal("an order the supplier cannot see must not be offered for cancellation")
	}
}
