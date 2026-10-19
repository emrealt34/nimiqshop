package handlers

import (
	"testing"
	"time"

	"nimiqshop/internal/db"
)

/* rating_identity_test.go — the admin console's "who left this comment".
 *
 * A rating is keyed by the purchase, and the comment itself records no
 * identity at all — correct for a public list, useless for a moderator who
 * has to tell one buyer from another. addressOfPurchase is the admin-only
 * bridge back to the account, and it must never invent one.
 */

func TestARatingIsTracedBackToTheBuyersWallet(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}

	user, err := store.FindOrCreateUserByAddress("NQ24 2XCD PK1C 7DR5 25U5 3PER U64V USR2 LPRK")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	q := db.Quote{
		ID: "q-rated", UserID: user.ID, Status: "fulfilled",
		ProductID: "amazon", ProductCountry: "TR", Denomination: "100 TRY", Quantity: 1,
		CustomerEmail: "buyer@example.test", WalletAddress: "bc1q-one-time-invoice",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, now); err != nil {
		t.Fatal(err)
	}

	got := h.addressOfPurchase("quote", q.ID)
	if got != user.NimiqAddress {
		t.Fatalf("address = %q, want the buyer's wallet %q", got, user.NimiqAddress)
	}
	// The buyer's wallet, never the supplier's one-time invoice address.
	if got == q.WalletAddress {
		t.Fatal("the rating must resolve to the buyer's wallet, not the payment address")
	}
	// A purchase nobody can find carries no address — and never a guess.
	if got := h.addressOfPurchase("quote", "q-never-existed"); got != "" {
		t.Fatalf("an unknown purchase must resolve to no address, got %q", got)
	}
	// Same for a quote whose user record is gone.
	orphan := q
	orphan.ID = "q-orphan"
	orphan.UserID = "user-deleted"
	if err := store.CreateQuoteWithDailyLimits(orphan, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	if got := h.addressOfPurchase("quote", orphan.ID); got != "" {
		t.Fatalf("a purchase with no user record must show no address, got %q", got)
	}
}
