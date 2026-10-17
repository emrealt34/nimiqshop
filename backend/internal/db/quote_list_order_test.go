package db

import (
	"testing"
	"time"
)

/*
 * quote_list_order_test.go — the tracker's list order is load-bearing.
 *
 * settlement.track.go documents "within a class, keep DB order (newest
 * first)" and then truncates the pass to MaxPollsPerTick. The list used to be
 * sorted ASCENDING while its own comment claimed "newest-waiting first", so
 * the quote a buyer was actively paying sat at the END and was truncated out
 * of every pass — a paid order that never left awaiting_payment.
 */

func TestListQuotesByStatusesIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	// Inserted oldest-first on purpose: the answer must not depend on the
	// order the rows happen to sit in the status index.
	for _, age := range []time.Duration{3 * time.Hour, 2 * time.Hour, time.Hour} {
		id := "q-" + age.String()
		q := Quote{
			ID: id, UserID: "buyer", Status: "awaiting_payment",
			SupplierOrderID: "CR-" + id,
			CreatedAt:       now.Add(-age),
			UpdatedAt:       now.Add(-age),
			PaymentExpiry:   now.Add(time.Hour),
			ExpiresAt:       now.Add(time.Hour),
		}
		if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now.Add(time.Hour)); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	got, err := store.ListQuotesByStatuses([]string{"awaiting_payment"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 quotes, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].UpdatedAt.After(got[i-1].UpdatedAt) {
			t.Fatalf("not newest-first: %s (%s) precedes %s (%s)",
				got[i-1].ID, got[i-1].UpdatedAt, got[i].ID, got[i].UpdatedAt)
		}
	}
	if got[0].ID != "q-1h0m0s" {
		t.Fatalf("the most recently updated quote must lead the list, got %s", got[0].ID)
	}
}
