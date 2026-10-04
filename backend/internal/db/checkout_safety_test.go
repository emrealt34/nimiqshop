package db

import (
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
)

/*
checkout_safety_test.go — the rule that decides whether a lapsed invoice may
still hold a buyer's checkout hostage.

The live bug of 2026-10-04: a buyer with ONE abandoned Lightning invoice was
answered ACTIVE_CHECKOUT for every later purchase, forever. The release branch
existed — in the handler loop, where the atomic gate (which re-asks the same
question through the same index) never saw it. The rule now lives here, and
these tests pin the two halves that must never be confused:

  - a TIMER running out proves nothing: inside the grace buffer the quote still
    blocks, because a payment sent at the edge of the window may not be visible
    to the supplier yet;
  - once the supplier has had the grace buffer to notice such a payment and no
    evidence exists anywhere, the dead invoice stops blocking — but it keeps
    being polled so the shop can expire it and reconcile anything that lands.
*/

var safetyNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func awaiting(setup func(*Quote)) Quote {
	q := Quote{
		ID:              "q1",
		UserID:          "u1",
		Status:          "awaiting_payment",
		SupplierOrderID: "sup-1",
		WalletAddress:   "lnbc1invoice",
		CoinAmount:      "0.001",
		ExpiresAt:       safetyNow.Add(-2 * time.Hour),
	}
	setup(&q)
	return q
}

func TestLapsedInvoiceReleasesOnlyAfterTheSupplierCouldNotHaveSeenPayment(t *testing.T) {
	grace := PaymentGrace
	if grace <= 0 {
		t.Fatalf("PaymentGrace must be positive, got %s", grace)
	}
	cases := []struct {
		name   string
		quote  Quote
		blocks bool
	}{
		{
			name:   "window still open",
			quote:  awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(20 * time.Minute) }),
			blocks: true,
		},
		{
			name:   "just lapsed, supplier may not have seen a last-second payment",
			quote:  awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(-time.Minute) }),
			blocks: true,
		},
		{
			name:   "grace elapsed, nothing seen anywhere",
			quote:  awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(-grace - time.Second) }),
			blocks: false,
		},
		{
			name:   "exactly at the grace boundary the proof is complete",
			quote:  awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(-grace) }),
			blocks: false,
		},
		{
			name: "grace elapsed but money was observed",
			quote: awaiting(func(q *Quote) {
				q.PaymentExpiry = safetyNow.Add(-2 * time.Hour)
				q.PaymentObserved = true
			}),
			blocks: true,
		},
		{
			name: "grace elapsed but an operator hold is in place",
			quote: awaiting(func(q *Quote) {
				q.PaymentExpiry = safetyNow.Add(-2 * time.Hour)
				q.PaymentBlocked = true
			}),
			blocks: true,
		},
		{
			name: "grace elapsed but the supplier reports a payment beyond waiting",
			quote: awaiting(func(q *Quote) {
				q.PaymentExpiry = safetyNow.Add(-2 * time.Hour)
				q.SupplierStatus = cryptorefills.StatusPaymentStarted
			}),
			blocks: true,
		},
		{
			name: "supplier status is only 'waiting for payment' — no money claim",
			quote: awaiting(func(q *Quote) {
				q.PaymentExpiry = safetyNow.Add(-2 * time.Hour)
				q.SupplierStatus = cryptorefills.StatusWaitingForPayment
			}),
			blocks: false,
		},
		{
			name: "no deadline at all — unprovable, so it stays blocking (fail closed)",
			quote: awaiting(func(q *Quote) {
				q.PaymentExpiry = time.Time{}
				q.ExpiresAt = time.Time{}
			}),
			blocks: true,
		},
		{
			name: "only the local expiry is known and it lapsed long ago",
			quote: awaiting(func(q *Quote) {
				q.PaymentExpiry = time.Time{}
				q.ExpiresAt = safetyNow.Add(-time.Hour)
			}),
			blocks: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BlocksNewPurchaseAt(tc.quote, safetyNow); got != tc.blocks {
				t.Fatalf("BlocksNewPurchaseAt = %v, want %v", got, tc.blocks)
			}
		})
	}
}

func TestMoneyInFlightBlocksAtEveryStatusAndTime(t *testing.T) {
	statuses := []string{"order_creating", "payment_started", "payment_received", "delivering", "manual_review"}
	for _, st := range statuses {
		q := Quote{
			ID: "q1", UserID: "u1", Status: st,
			SupplierOrderID: "sup-1",
			PaymentExpiry:   safetyNow.Add(-72 * time.Hour),
			ExpiresAt:       safetyNow.Add(-80 * time.Hour),
		}
		if !BlocksNewPurchaseAt(q, safetyNow) {
			t.Fatalf("%s must block — a human or an in-flight settlement owns this quote", st)
		}
	}
	// expired/failed only matter when money was actually seen.
	if BlocksNewPurchaseAt(Quote{Status: "expired", SupplierOrderID: "sup-1"}, safetyNow) {
		t.Fatal("an expired invoice with nothing charged must not block")
	}
	if !BlocksNewPurchaseAt(Quote{Status: "expired", SupplierOrderID: "sup-1", PaymentObserved: true}, safetyNow) {
		t.Fatal("an expired invoice WITH money observed must keep blocking for reconciliation")
	}
	if BlocksNewPurchaseAt(Quote{Status: "failed", SupplierOrderID: "sup-1", SupplierStatus: cryptorefills.StatusPaymentSetupFailed}, safetyNow) {
		t.Fatal("a failed setup with nothing charged must not block")
	}
	if !BlocksNewPurchaseAt(Quote{Status: "failed", SupplierOrderID: "sup-1", PaymentObserved: true}, safetyNow) {
		t.Fatal("a failed quote with money observed must keep blocking")
	}
	if BlocksNewPurchaseAt(Quote{Status: "fulfilled"}, safetyNow) || BlocksNewPurchaseAt(Quote{Status: "refunded"}, safetyNow) {
		t.Fatal("finished quotes never block")
	}
}

func TestLapsedQuoteStillGetsPolledSoItCanBeExpired(t *testing.T) {
	// The release and the poll schedule are deliberately different questions:
	// dropping the poll would leave lapsed quotes stuck in `awaiting_payment`
	// forever (no display state, no released daily-limit slot).
	lapsed := awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(-time.Hour) })
	if BlocksNewPurchaseAt(lapsed, safetyNow) {
		t.Fatal("lapsed unpaid quote must not block a new purchase")
	}
	if !lapsed.NeedsSupplierPoll() {
		t.Fatal("lapsed unpaid quote must still be polled until the tracker expires it")
	}
	if (Quote{Status: "fulfilled", SupplierOrderID: "sup-1"}).NeedsSupplierPoll() {
		t.Fatal("finished quotes are not polled")
	}
	if (Quote{Status: "awaiting_payment"}).NeedsSupplierPoll() {
		t.Fatal("a quote with no supplier order has nothing to poll")
	}
}

func TestCanPayStillFailsClosedOnTheTimer(t *testing.T) {
	payable := awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(10 * time.Minute) })
	if !payable.CanPay(safetyNow) {
		t.Fatal("an open window must stay payable")
	}
	lapsed := awaiting(func(q *Quote) { q.PaymentExpiry = safetyNow.Add(-time.Second) })
	if lapsed.CanPay(safetyNow) {
		t.Fatal("a lapsed single-use invoice must never be offered for payment again")
	}
}
