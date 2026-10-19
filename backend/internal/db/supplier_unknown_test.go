package db

import (
	"errors"
	"testing"
	"time"
)

/* supplier_unknown_test.go — the marker behind "the supplier has no such
 * order" (a bare 404 on the v6 PARTNER API).
 *
 * It is an ANSWER, not an error. Every order the shop created before it
 * moved onto the partner API answers this way, and each one used to be
 * polled again and again and flagged for manual review on every pass.
 *
 * The marker takes the order out of the poll set — and it is CLEARED the
 * moment a read succeeds, so fixing the key heals the order by itself.
 */

func TestAnOrderTheSupplierCannotSeeLeavesThePollSet(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	q := Quote{
		ID: "q-unknown", UserID: "buyer", Status: "awaiting_payment",
		SupplierOrderID: "sup-unknown", SupplierStatus: "WaitingForPayment",
		ProductID: "amazon", ProductCountry: "TR", Denomination: "100 TRY", Quantity: 1,
		CustomerEmail: "buyer@example.test", PaymentExpiry: now.Add(20 * time.Minute),
		ExpiresAt: now.Add(20 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	if !q.NeedsSupplierPoll() {
		t.Fatal("a live unpaid order is polled to begin with")
	}
	if err := store.MarkSupplierOrderUnknown(q.ID); err != nil {
		t.Fatal(err)
	}
	marked, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.SupplierUnknownAt.IsZero() {
		t.Fatal("the marker was not written")
	}
	if marked.NeedsSupplierPoll() {
		t.Fatal("an order the supplier cannot see must not be polled any more")
	}
	// Recording it twice is the same fact, not a new one.
	first := marked.SupplierUnknownAt
	time.Sleep(2 * time.Millisecond)
	if err := store.MarkSupplierOrderUnknown(q.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := store.GetQuote(q.ID)
	if !again.SupplierUnknownAt.Equal(first) {
		t.Fatalf("marking twice must not move the timestamp: %s then %s", first, again.SupplierUnknownAt)
	}
	// A successful read clears it, so polling resumes on its own.
	if err := store.ClearSupplierOrderUnknown(q.ID); err != nil {
		t.Fatal(err)
	}
	healed, err := store.GetQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !healed.SupplierUnknownAt.IsZero() {
		t.Fatal("a successful read must clear the marker")
	}
	if !healed.NeedsSupplierPoll() {
		t.Fatal("once the supplier can see the order again, polling must resume")
	}
	// Clearing something that is not set stays silent (the common case).
	if err := store.ClearSupplierOrderUnknown(q.ID); err != nil {
		t.Fatalf("clearing an unmarked order must be a no-op, got %v", err)
	}
}

func TestMarkingAnUnknownQuoteReportsItAsMissing(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.MarkSupplierOrderUnknown("never-existed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking a quote that does not exist must report ErrNotFound, got %v", err)
	}
}
