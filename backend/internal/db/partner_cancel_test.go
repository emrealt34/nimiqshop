package db

import (
	"testing"
	"time"
)

/* partner_cancel_test.go — the eligibility filter behind
 * DELETE /v6/partner/orders/{id}.
 *
 * The partner API lets the shop cancel an abandoned order instead of letting
 * it hold a payment slot until it lapses on its own — and it REFUSES (409) as
 * soon as any payment has been detected. That refusal is the safety mechanism,
 * so the shop must never ASK for an order that money might be arriving for.
 * These tests pin the filter that decides what is ever offered.
 */

func cancelCandidate(id, status string, setup ...func(*Quote)) Quote {
	now := time.Now().UTC()
	q := Quote{
		ID:              id,
		UserID:          "u-" + id,
		Status:          status,
		SupplierOrderID: "sup-" + id,
		ProductID:       "unipin",
		ProductCountry:  "TR",
		Denomination:    "5 TRY",
		Quantity:        1,
		CustomerEmail:   "buyer@example.com",
		CreatedAt:       now,
		UpdatedAt:       now,
		ExpiresAt:       now.Add(25 * time.Minute),
	}
	for _, fn := range setup {
		if fn != nil {
			fn(&q)
		}
	}
	return q
}

func TestOnlyDeadAndUnpaidOrdersAreOfferedForCancellation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()

	// The one order that SHOULD be offered: locally dead, never paid.
	dead := cancelCandidate("q-dead", "expired")
	// A live order must never be cancelled: the buyer may still pay it.
	live := cancelCandidate("q-live", "awaiting_payment")
	// Money was seen locally — cancelling could strand it.
	paid := cancelCandidate("q-paid", "expired", func(q *Quote) { q.PaymentObserved = true })
	// The supplier itself reports money.
	upstreamPaid := cancelCandidate("q-upstream", "expired", func(q *Quote) { q.SupplierStatus = "PAYMENT_RECEIVED" })
	// A failed payment is as dead as an expired one.
	failedPay := cancelCandidate("q-failed", "failed")
	// No supplier order (never created) — nothing to cancel upstream.
	noOrder := cancelCandidate("q-noorder", "expired", func(q *Quote) { q.SupplierOrderID = "" })
	// A simulated (admin test-centre) order is not a real supplier order.
	simulated := cancelCandidate("q-sim", "expired", func(q *Quote) { q.SupplierOrderID = SimulatedSupplierPrefix + "123" })
	for _, q := range []Quote{dead, live, paid, upstreamPaid, failedPay, noOrder, simulated} {
		if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
			t.Fatalf("seed %s: %v", q.ID, err)
		}
	}

	offered := func() map[string]bool {
		got, err := store.ListQuotesPendingCancel(10, 30*time.Minute)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := map[string]bool{}
		for _, q := range got {
			out[q.ID] = true
		}
		return out
	}

	set := offered()
	for _, id := range []string{"q-dead", "q-failed"} {
		if !set[id] {
			t.Fatalf("%s is dead and unpaid, so it must be offered for cancellation", id)
		}
	}
	for _, id := range []string{"q-live", "q-paid", "q-upstream", "q-noorder", "q-sim"} {
		if set[id] {
			t.Fatalf("%s must never be offered for cancellation", id)
		}
	}

	// A cancelled order is asked for ONCE: recording the outcome removes it.
	if err := store.MarkSupplierCancel("q-dead", "requested"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if offered()["q-dead"] {
		t.Fatal("an order the supplier already cancelled must not be offered again")
	}
	// A definitive refusal is final too — retrying a 409 would just hammer
	// the supplier about an order it has already told us about.
	if err := store.MarkSupplierCancel("q-failed", "refused"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if offered()["q-failed"] {
		t.Fatal("a refused cancellation must not be retried")
	}
}

func TestATransientCancelFailureIsRetriedOnlyAfterItsCoolOff(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	q := cancelCandidate("q-retry", "expired")
	if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.MarkSupplierCancel(q.ID, "failed"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	// Too soon: the supplier was busy (429/503), so the cool-off applies.
	got, err := store.ListQuotesPendingCancel(10, time.Hour)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a transient failure must wait for its cool-off, got %d", len(got))
	}
	// The cool-off elapsed, so the order is offered again.
	got, err = store.ListQuotesPendingCancel(10, -time.Second)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != q.ID {
		t.Fatalf("after the cool-off the order must be offered again, got %+v", got)
	}
	// A later failure must not overwrite a recorded success.
	if err := store.MarkSupplierCancel(q.ID, "requested"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := store.MarkSupplierCancel(q.ID, "failed"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err = store.ListQuotesPendingCancel(10, -time.Second)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a confirmed cancellation must never be downgraded by a late failure")
	}
}
