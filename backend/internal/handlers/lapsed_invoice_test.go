package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"

	"github.com/valyala/fasthttp"
)

/* lapsed_invoice_test.go — what the shop does with a dead, unpaid invoice.
 *
 * A single-use invoice that ran out of time no longer blocks the buyer: they
 * may start again. What the shop does about the ORDER behind it is the whole
 * question, and the owner's answer (2026-10-19) is blunt: "sipariş iptali
 * öldürme yapılmaz." Nothing cancels an order automatically — not the
 * tracker's sweep, not this pre-check. Cancelling is one operator, one order,
 * one button in the admin console.
 *
 * What the shop may still do is ASK the supplier, because a payment pushed
 * through at the very edge of the window is money only the supplier can see.
 * These tests pin the difference: a GET is allowed, a DELETE is not.
 */

// supplierSpy is a stand-in for the partner API that remembers every call the
// shop makes and answers every order read with `answer`.
func supplierSpy(t *testing.T, answer map[string]any) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(answer)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(calls))
		copy(out, calls)
		return out
	}
}

// lapsedQuote is an order the buyer never paid: its invoice ran out, its
// supplier order is real, and no money was ever seen for it.
func lapsedQuote(id string) db.Quote {
	now := time.Now().UTC()
	return db.Quote{
		ID:                  id,
		UserID:              "buyer",
		Status:              "awaiting_payment",
		ProductID:           "amazon",
		ProductCountry:      "TR",
		Denomination:        "100 TRY",
		Quantity:            1,
		CustomerEmail:       "buyer@example.test",
		Coin:                "BTC",
		Network:             "bitcoin",
		WalletAddress:       "bc1qtest",
		CoinAmount:          "0.00042",
		SupplierOrderID:     "sup-" + id,
		SupplierStatus:      "WaitingForPayment",
		PaymentExpiry:       now.Add(-time.Minute),
		ExpiresAt:           now.Add(-time.Minute),
		CreatedAt:           now.Add(-30 * time.Minute),
		UpdatedAt:           now.Add(-30 * time.Minute),
		EndUserIP:           "203.0.113.7",
		PurchaseFingerprint: "cart-fingerprint",
	}
}

func lapsedHarness(t *testing.T, answer map[string]any) (*Handlers, *db.Store, func() []string) {
	t.Helper()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv, calls := supplierSpy(t, answer)
	h := &Handlers{
		Store: store,
		CR: cryptorefills.NewClient(cryptorefills.ClientOptions{
			BaseURL:    srv.URL,
			PartnerID:  "partner-test",
			PartnerKey: "cr_pk_test",
		}),
	}
	return h, store, calls
}

func askedAndNeverCancelled(calls []string) error {
	gets := 0
	for _, c := range calls {
		if len(c) >= 3 && c[:3] == "DEL" {
			return errCancelled
		}
		if len(c) >= 3 && c[:3] == "GET" {
			gets++
		}
	}
	if gets == 0 {
		return errNotAsked
	}
	return nil
}

var errCancelled = &lapsedViolation{"the shop cancelled an order"}
var errNotAsked = &lapsedViolation{"the shop never asked the supplier"}

type lapsedViolation struct{ why string }

func (e *lapsedViolation) Error() string { return e.why }

// A payment that landed at the very edge of the window is money: the buyer
// must not be handed a second order to pay for it — and the shop still must
// not touch the order itself.
func TestAPaymentTheSupplierCanSeeStillBlocksTheBuyer(t *testing.T) {
	h, store, calls := lapsedHarness(t, map[string]any{
		"order_id": "sup-q-late", "status": "PAYMENT_RECEIVED", "payment_state": "PAYMENT_RECEIVED",
	})
	q := lapsedQuote("q-late")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	blocked := h.existingCheckout(ctx, "buyer", "idem-key-late-payment", "fp-request", "cart-fingerprint", false)
	if err := askedAndNeverCancelled(calls()); err != nil {
		t.Fatalf("%v: %v", err, calls())
	}
	if !blocked {
		t.Fatalf("money the supplier can see must keep blocking, got %d %q, calls=%v",
			ctx.Response.StatusCode(), ctx.Response.Body(), calls())
	}
	if code := statusCodeOf(ctx); code != 409 {
		t.Fatalf("status = %d, want 409 ACTIVE_CHECKOUT", code)
	}
	// The answer was APPLIED, not just read: the order is settling now.
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "payment_received" {
		t.Fatalf("status = %q, the supplier's answer must be applied to the order", after.Status)
	}
	if after.SupplierCancelState != "" {
		t.Fatalf("supplier_cancel_state = %q, nothing may cancel a paid order", after.SupplierCancelState)
	}
}

// The ordinary case: the invoice ran out, nothing was paid, the buyer moves
// on. The shop asks, learns there is no money, and leaves the order alone.
func TestALapsedUnpaidInvoiceIsAskedAboutAndLeftAlone(t *testing.T) {
	h, store, calls := lapsedHarness(t, map[string]any{
		"order_id": "sup-q-dead", "status": "WaitingForPayment", "payment_state": "WaitingForPayment",
	})
	q := lapsedQuote("q-dead")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	blocked := h.existingCheckout(ctx, "buyer", "idem-key-dead-invoice", "fp-request", "cart-fingerprint", false)
	if err := askedAndNeverCancelled(calls()); err != nil {
		t.Fatalf("%v: %v", err, calls())
	}
	if blocked {
		t.Fatalf("a lapsed unpaid invoice must release the buyer, got %d %q, calls=%v",
			ctx.Response.StatusCode(), ctx.Response.Body(), calls())
	}
	if len(ctx.Response.Body()) != 0 {
		t.Fatalf("the pre-check answered the buyer (%d %q) although it did not block",
			ctx.Response.StatusCode(), ctx.Response.Body())
	}
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != q.Status || after.SupplierCancelState != "" {
		t.Fatalf("the order was touched: status=%q supplier_cancel_state=%q", after.Status, after.SupplierCancelState)
	}
}

// The supplier being unreachable is not a reason to do anything to the order
// either. The buyer is free; the order stays exactly as it was.
func TestAnUnreachableSupplierNeverCostsTheBuyerTheirOrder(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	h := &Handlers{Store: store, CR: cryptorefills.NewClient(cryptorefills.ClientOptions{
		BaseURL: srv.URL, PartnerID: "partner-test", PartnerKey: "cr_pk_test",
	})}
	q := lapsedQuote("q-offline")
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	if h.existingCheckout(ctx, "buyer", "idem-key-offline", "fp-request", "cart-fingerprint", false) {
		t.Fatalf("the buyer must not be blocked by a supplier that cannot answer, got %d %q",
			ctx.Response.StatusCode(), ctx.Response.Body())
	}
	after, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != q.Status || after.SupplierCancelState != "" {
		t.Fatalf("the order was touched: status=%q supplier_cancel_state=%q", after.Status, after.SupplierCancelState)
	}
}

// A simulated (admin test-centre) order is not a real supplier order, so the
// shop must not ask about it at all — there is nobody on the other end.
func TestASimulatedOrderIsNeverAskedAbout(t *testing.T) {
	h, store, calls := lapsedHarness(t, map[string]any{"order_id": "sup-x", "status": "WaitingForPayment"})
	q := lapsedQuote("q-sim")
	q.SupplierOrderID = db.SimulatedSupplierPrefix + "123"
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx := newTestCtx()
	if h.existingCheckout(ctx, "buyer", "idem-key-simulated", "fp-request", "cart-fingerprint", false) {
		t.Fatalf("a simulated order must never block, got %d %q", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("the supplier was called for a simulated order: %v", got)
	}
}

// Small helpers for driving a handler without a live socket.
func newTestCtx() *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/api/quote")
	return ctx
}

func statusCodeOf(ctx *fasthttp.RequestCtx) int { return ctx.Response.StatusCode() }
