package db

import (
	"context"
	"testing"
	"time"

	"nimiqshop/internal/money"
	"nimiqshop/internal/nimiq"
)

// TestReconcileRevivedCashbackHasRecipient guards the "no missed cashback"
// guarantee for the exact race the shop worries about: a buyer who stakes and
// buys before the pool has indexed the stake. At delivery the pool says "not
// staked", so the cashback row is booked as a zero-rate SKIP (no recipient is
// ever set on a zero-rate skip). When the pool later reports the stake, the
// post-fulfillment re-check must REVIVE that row to the staker rate AND set a
// payout recipient — otherwise the payout worker (which refuses Recipient=="")
// silently never sends it, and the buyer loses the cashback they earned.
func TestReconcileRevivedCashbackHasRecipient(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	const wallet = "NQ20 TSB0 DFSM UH9C 15GQ GAGJ TTE4 D3MA 859E"

	// The pool resolver flips from "not indexed yet" to "staked".
	staked := false
	store.SetStakerLookupDetailed(func(_ context.Context, _ string) (StakerStake, error) {
		if !staked {
			return StakerStake{}, nil // pool: not staked (index lag)
		}
		return StakerStake{StakeLuna: 2_000_00000, Staked: true, BaseBps: 50}, nil
	})

	now := time.Now().UTC()
	u, err := store.FindOrCreateUserByAddress(nimiq.NormalizeAddress(wallet))
	if err != nil {
		t.Fatalf("user: %v", err)
	}

	q := Quote{
		ID: "q1", UserID: u.ID, Status: "awaiting_payment",
		ProductID: "Steam", Quantity: 1,
		ProductUSD: money.FromFloat(50), EstimatedNIM: 4166, NimUsdRate: 0.012,
		CreatedAt: now,
	}
	if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
		t.Fatalf("create quote: %v", err)
	}

	// Delivered while the pool still says "not staked".
	if err := store.CompleteQuoteWithFulfillment("q1", nil); err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	rows, _ := store.ListCashbacksByUser(u.ID, 0)
	if len(rows) != 1 || rows[0].Status != CashbackSkipped || rows[0].AmountLuna != 0 {
		t.Fatalf("want one zero-rate skipped row, got %+v", rows)
	}
	if recs, _ := store.ListStakeRechecks(); len(recs) != 1 {
		t.Fatalf("want one scheduled re-check, got %d", len(recs))
	}

	// The pool now indexes the stake; the re-check must recover the cashback.
	staked = true
	resolved, _ := store.RunStakeRechecks(now)
	if resolved != 1 {
		t.Fatalf("re-check resolved = %d, want 1", resolved)
	}

	rows, _ = store.ListCashbacksByUser(u.ID, 0)
	got := rows[0]
	if got.Status != CashbackQueued {
		t.Errorf("status = %q, want queued", got.Status)
	}
	if got.Bps != 50 {
		t.Errorf("bps = %d, want 50 (staker base)", got.Bps)
	}
	if got.AmountLuna <= 0 {
		t.Errorf("amount = %d, want > 0", got.AmountLuna)
	}
	// The crux: a revived cashback the worker can actually pay.
	if got.Recipient == "" {
		t.Fatal("revived cashback has no recipient — payout worker would silently skip it (missed cashback)")
	}
	if err := nimiq.ValidateAddress(got.Recipient); err != nil {
		t.Errorf("revived recipient %q is not a valid address: %v", got.Recipient, err)
	}
}

func TestBurnCashbackDestinationRoutesToBurnWallet(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	const wallet = "NQ20 TSB0 DFSM UH9C 15GQ GAGJ TTE4 D3MA 859E"
	store.SetStakerLookupDetailed(func(_ context.Context, _ string) (StakerStake, error) {
		return StakerStake{StakeLuna: 2_000_00000, Staked: true, BaseBps: 50}, nil
	})

	now := time.Now().UTC()
	u, err := store.FindOrCreateUserByAddress(nimiq.NormalizeAddress(wallet))
	if err != nil {
		t.Fatalf("user: %v", err)
	}

	q := Quote{
		ID: "q-burn", UserID: u.ID, Status: "awaiting_payment",
		ProductID: "Steam", Quantity: 1,
		ProductUSD: money.FromFloat(50), EstimatedNIM: 4166, NimUsdRate: 0.012,
		CashbackDestination: CashbackDestBurn,
		CreatedAt:           now,
	}
	if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	if err := store.CompleteQuoteWithFulfillment("q-burn", nil); err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	rows, _ := store.ListCashbacksByUser(u.ID, 0)
	if len(rows) != 1 {
		t.Fatalf("want 1 cashback row, got %d", len(rows))
	}
	if rows[0].CashbackDestination != CashbackDestBurn {
		t.Errorf("destination = %q, want %q", rows[0].CashbackDestination, CashbackDestBurn)
	}
	if rows[0].Recipient != BurnNIMAddress {
		t.Errorf("recipient = %q, want burn wallet %q", rows[0].Recipient, BurnNIMAddress)
	}
}
