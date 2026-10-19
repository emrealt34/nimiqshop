package db

import (
	"errors"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/money"
)

/*
checkout_settled_test.go — when an Idempotency-Key has done its job.

The live bug of 2026-10-18 (owner: "unipin 5 TL aldım, sonra tekrar aynı
ürünü almaya çalışınca beni tamamlanmış order sayfasına attı"): the
storefront's checkout key is derived from the cart, so a second purchase of
the same product arrived under the key of the first, already-fulfilled
order — and the key index honoured it forever. These tests pin the rule in
both directions:

  - a key bound to a SETTLED order (delivered, refunded, lapsed unpaid past
    the grace buffer, rejected before any invoice) is spent: the same key
    starts a new quote and moves onto it;
  - a key bound to an UNRESOLVED order (payable, paid, in delivery, under
    review, inside the grace buffer) is a retry key: creation is refused and
    the existing quote is what the buyer gets back.
*/

func TestCheckoutSettledIsExactlyNoPaymentAndNoMoneyInPlay(t *testing.T) {
	live := func(mod func(*Quote)) Quote {
		q := Quote{
			ID: "q1", UserID: "u1", Status: "awaiting_payment",
			SupplierOrderID: "sup-1", WalletAddress: "lnbc1invoice", CoinAmount: "0.001",
			PaymentExpiry: safetyNow.Add(20 * time.Minute), ExpiresAt: safetyNow.Add(20 * time.Minute),
		}
		if mod != nil {
			mod(&q)
		}
		return q
	}
	cases := []struct {
		name    string
		q       Quote
		settled bool
	}{
		{"payable invoice stays the buyer's checkout", live(nil), false},
		{"window still open: the buyer may still pay, so nothing is over", live(nil), false},
		{"window closed with nothing seen: the supplier answers, no buffer is waited out", live(func(q *Quote) {
			q.PaymentExpiry = safetyNow.Add(-time.Minute)
			q.ExpiresAt = q.PaymentExpiry
		}), true},
		{"lapsed invoice with money observed is NOT over", live(func(q *Quote) {
			q.PaymentExpiry = safetyNow.Add(-24 * time.Hour)
			q.ExpiresAt = q.PaymentExpiry
			q.PaymentObserved = true
		}), false},
		{"supplier order still being created", live(func(q *Quote) { q.Status = "order_creating" }), false},
		{"payment started", live(func(q *Quote) { q.Status = "payment_started" }), false},
		{"payment received", live(func(q *Quote) { q.Status = "payment_received" }), false},
		{"delivering", live(func(q *Quote) { q.Status = "delivering" }), false},
		// A manual review is spent as far as the cart's key is concerned: the
		// buyer may buy the same cart again, because the point of the key is
		// to protect ONE unresolved purchase — and a review is the operator's
		// queue, not the buyer's waiting room. The review itself stays open.
		{"manual review", live(func(q *Quote) { q.Status = "manual_review"; q.PaymentBlocked = true }), true},
		{"delivered", live(func(q *Quote) { q.Status = "fulfilled" }), true},
		{"refunded", live(func(q *Quote) { q.Status = "refunded" }), true},
		{"expired unpaid", live(func(q *Quote) { q.Status = "expired"; q.PaymentExpiry = safetyNow.Add(-time.Hour) }), true},
		{"expired with money observed", live(func(q *Quote) { q.Status = "expired"; q.PaymentObserved = true }), false},
		{"rejected before any invoice existed", Quote{ID: "q1", UserID: "u1", Status: "failed"}, true},
		{"failed payment setup, nothing charged", Quote{ID: "q1", UserID: "u1", Status: "failed", SupplierOrderID: "sup-1", SupplierStatus: cryptorefills.StatusPaymentSetupFailed}, true},
		{"failed after the supplier may have taken money", Quote{ID: "q1", UserID: "u1", Status: "failed", SupplierOrderID: "sup-1", SupplierStatus: "Expired"}, false},
		{"simulated test order delivered", live(func(q *Quote) { q.Status = "fulfilled"; q.TestMode = true; q.SupplierOrderID = "TESTSIM-1" }), true},
		{"simulated test order still payable", live(func(q *Quote) { q.TestMode = true; q.SupplierOrderID = "TESTSIM-1" }), false},
	}
	for _, c := range cases {
		if got := CheckoutSettledAt(c.q, safetyNow); got != c.settled {
			t.Errorf("%s: CheckoutSettledAt = %v, want %v", c.name, got, c.settled)
		}
		// The predicate is, by definition, the complement of "open": a
		// settled checkout can neither be paid nor block a new purchase.
		if c.settled && (c.q.CanPay(safetyNow) || BlocksNewPurchaseAt(c.q, safetyNow)) {
			t.Errorf("%s: settled but still payable/blocking", c.name)
		}
	}
}

// settledKeyQuote is the owner's cart as the handler persists it.
func settledKeyQuote(id, user, key string, at time.Time) Quote {
	return Quote{
		ID: id, UserID: user, IdempotencyKey: key, RequestFingerprint: "fp-" + key, PurchaseFingerprint: "pfp",
		ProductID: "unipin", ProductCountry: "TR", Denomination: "5 TRY", Quantity: 1, ProductUSD: money.FromFloat(0.15),
		CustomerEmail: "buyer@example.com", Status: "order_creating", CreatedAt: at, UpdatedAt: at,
		ExpiresAt: at.Add(25 * time.Minute),
	}
}

func TestSpentIdempotencyKeyStartsANewQuoteAndMovesOntoIt(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	const user, key = "u-owner", "cart-derived-key-0001"

	first := settledKeyQuote("q-first", user, key, now)
	if err := store.CreateQuoteWithPurchaseLimits(first, 0, 0, 0, now); err != nil {
		t.Fatalf("first creation: %v", err)
	}
	if err := store.AttachQuotePayment(first.ID, "sup-first", "lnbc1first", "BTC", "0.001", "Lightning", now.Add(25*time.Minute)); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Retry while the invoice is live: refused, and the key still names the
	// live quote — that is the double-order protection.
	retry := settledKeyQuote("q-retry", user, key, now)
	if err := store.CreateQuoteWithPurchaseLimits(retry, 0, 0, 0, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry under a live key must conflict, got %v", err)
	}
	if got, _, err := store.GetQuoteByIdempotencyRequest(user, key); err != nil || got.ID != first.ID {
		t.Fatalf("key must still resolve to the live quote: %v %q", err, got.ID)
	}

	// Deliver the first order.
	if _, err := store.ApplySupplierOrder(first.ID, &cryptorefills.Order{ID: "sup-first", Status: cryptorefills.StatusDone, Coin: "BTC", CoinAmount: "0.001"}); err != nil {
		t.Fatalf("fulfil: %v", err)
	}
	if got, _ := store.GetQuote(first.ID); got.Status != "fulfilled" || !CheckoutSettled(got) {
		t.Fatalf("first order should be fulfilled and settled, got %q", got.Status)
	}

	// The same key now opens a NEW quote and follows it.
	second := settledKeyQuote("q-second", user, key, now.Add(time.Minute))
	if err := store.CreateQuoteWithPurchaseLimits(second, 0, 0, 0, now.Add(time.Minute)); err != nil {
		t.Fatalf("rebuy under a spent key must create: %v", err)
	}
	got, fp, err := store.GetQuoteByIdempotencyRequest(user, key)
	if err != nil || got.ID != second.ID || fp != second.RequestFingerprint {
		t.Fatalf("key must now resolve to the new quote with its fingerprint: %v %q %q", err, got.ID, fp)
	}
	if old, _ := store.GetQuote(first.ID); old.Status != "fulfilled" || old.IdempotencyKey != key {
		t.Fatalf("the delivered order must be untouched: %q %q", old.Status, old.IdempotencyKey)
	}
	// And the new quote is a retry key again: no third order while it is open.
	third := settledKeyQuote("q-third", user, key, now.Add(2*time.Minute))
	if err := store.CreateQuoteWithPurchaseLimits(third, 0, 0, 0, now.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry under the new live key must conflict, got %v", err)
	}

	// Alias binding follows the same rule: a key may be re-pointed only
	// while its current quote is settled.
	other := settledKeyQuote("q-other", user, "another-key-000000001", now)
	other.ProductID = "steam"
	// A different cart beside the still-open second quote: the buyer's
	// explicit acknowledgement is what lets it through the one-unresolved-
	// checkout rule, exactly as the handler passes it.
	if err := store.CreateQuoteWithPurchaseLimits(other, 0, 0, 0, now, QuoteOptions{AckActiveCheckout: true}); err != nil {
		t.Fatalf("other creation: %v", err)
	}
	if err := store.BindQuoteIdempotency(user, key, "fp-x", other.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-pointing a key away from a live quote must conflict, got %v", err)
	}
	if err := store.MarkSupplierFailure(second.ID, "supplier rejected creation"); err != nil {
		t.Fatalf("fail second: %v", err)
	}
	if err := store.BindQuoteIdempotency(user, key, "fp-x", other.ID); err != nil {
		t.Fatalf("re-pointing a spent key must succeed: %v", err)
	}
	if got, fp, err := store.GetQuoteByIdempotencyRequest(user, key); err != nil || got.ID != other.ID || fp != "fp-x" {
		t.Fatalf("key must follow the alias: %v %q %q", err, got.ID, fp)
	}
	// Re-binding the SAME quote with a different fingerprint is still a
	// conflict: one key never answers for two different requests.
	if err := store.BindQuoteIdempotency(user, key, "fp-y", other.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("same quote, different fingerprint must conflict, got %v", err)
	}
}
