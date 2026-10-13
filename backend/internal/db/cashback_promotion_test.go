package db

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"nimiqshop/internal/money"
)

// Historical ordinary quotes carried a base rate in CashbackCodeBps. An
// absent code must never suppress their earned-ledger debit at fulfillment.
func TestBlankPromotionDoesNotSuppressEarnedCashback(t *testing.T) {
	for _, legacyBps := range []int{0, 100} {
		t.Run(strconv.Itoa(legacyBps), func(t *testing.T) {
			store, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			const address = "NQ20 TSB0 DFSM UH9C 15GQ GAGJ TTE4 D3MA 859E"
			user, err := store.FindOrCreateUserByAddress(address)
			if err != nil {
				t.Fatal(err)
			}
			store.SetStakerLookupDetailed(func(context.Context, string) (StakerStake, error) {
				return StakerStake{Staked: true, StakeLuna: 10000000, BaseBps: 100}, nil
			})
			now := time.Now().UTC()
			credit, err := store.ApplyProfitSnapshot(address, now.Format("2006-01"), 1000000, 1, 0, 1, 10000000, now)
			if err != nil || credit != 8 {
				t.Fatalf("earned credit %v %v", credit, err)
			}
			q := Quote{ID: "earned-purchase", UserID: user.ID, Status: "awaiting_payment", ProductID: "Steam", Quantity: 1, ProductUSD: money.FromFloat(50), EstimatedNIM: 50.195, NimUsdRate: 1, CashbackCodeBps: legacyBps, CreatedAt: now}
			if err = store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if err = store.CompleteQuoteWithFulfillment(q.ID, nil); err != nil && (i == 0 || !errors.Is(err, ErrConflict)) {
					t.Fatal(err)
				}
			}
			rows, err := store.ListCashbacksByUser(user.ID, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows %+v %v", rows, err)
			}
			row := rows[0]
			if row.CashbackSource != "ledger" || row.BoostLuna != 400000 || row.AmountLuna != 450195 || row.Bps != 900 {
				t.Fatalf("earned cashback suppressed %+v", row)
			}
			ledger, ok, err := store.ReadStakeLedger(address)
			if err != nil || !ok || math.Abs(ledger.A-4) > 1e-9 {
				t.Fatalf("ledger debit %+v %v", ledger, err)
			}
		})
	}
}
