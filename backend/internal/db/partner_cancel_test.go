package db

import (
	"errors"
	"testing"
	"time"
)

/* partner_cancel_test.go — the record behind DELETE /v6/partner/orders/{id}.
 *
 * There is NO automatic cancellation any more (owner, 2026-10-19: "sipariş
 * iptali öldürme yapılmaz"). The only thing that ever cancels an order is an
 * operator pressing the button in the admin console, on an order they are
 * looking at, after the shop has re-read the supplier. What is left to pin
 * here is the RECORD of that attempt: once the supplier has confirmed a
 * cancellation — or definitively refused one — the row must not be rewritten
 * by a late or repeated answer, because that state is what tells an operator
 * whether the order is still theirs to cancel.
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

func TestACancellationRecordIsFinalOnceTheSupplierAnswered(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	seed := func(id string) {
		if err := store.CreateQuoteWithPurchaseLimits(cancelCandidate(id, "expired"), 0, 0, 0, time.Now().UTC()); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	state := func(id string) string {
		q, err := store.GetQuote(id)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return q.SupplierCancelState
	}
	seed("q-confirmed")
	seed("q-refused")
	seed("q-transient")

	// A confirmed cancellation is terminal: a late transport failure, a
	// retry that came back 5xx, a second click — none of them may downgrade
	// "the supplier cancelled it" back to "we failed to ask".
	if err := store.MarkSupplierCancel("q-confirmed", "requested"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := store.MarkSupplierCancel("q-confirmed", "failed"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if got := state("q-confirmed"); got != "requested" {
		t.Fatalf("state = %q, a confirmed cancellation must stay confirmed", got)
	}
	// A definitive refusal is terminal too: the usual reason is money the
	// supplier can already see, and that order must be left to settle.
	if err := store.MarkSupplierCancel("q-refused", "refused"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := store.MarkSupplierCancel("q-refused", "failed"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if got := state("q-refused"); got != "refused" {
		t.Fatalf("state = %q, a refusal must stay a refusal", got)
	}
	// A transient failure is not terminal: the operator may press again and
	// the supplier may then confirm.
	if err := store.MarkSupplierCancel("q-transient", "failed"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := store.MarkSupplierCancel("q-transient", "requested"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if got := state("q-transient"); got != "requested" {
		t.Fatalf("state = %q, a retry that succeeded must be recorded", got)
	}
	// Nothing to record on a quote that does not exist.
	if err := store.MarkSupplierCancel("q-never-seeded", "requested"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking an unknown quote must report ErrNotFound, got %v", err)
	}
}
