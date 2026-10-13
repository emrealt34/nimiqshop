package db

import (
	"context"
	"nimiqshop/internal/money"
	"testing"
	"time"
)

func TestActualPromotionRemainsExclusiveOfEarnedLedger(t *testing.T) {
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
	q := Quote{ID: "promotion-purchase", UserID: user.ID, Status: "awaiting_payment", ProductID: "Steam", Quantity: 1, ProductUSD: money.FromFloat(50), EstimatedNIM: 50.195, NimUsdRate: 1, CashbackCode: "PROMO", CashbackCodeBps: 500, CreatedAt: now}
	if err = store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now, QuoteOptions{CashbackCode: q.CashbackCode}); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListCashbacksByUser(user.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("cashback rows %+v %v", rows, err)
	}
	if row := rows[0]; row.CashbackSource != "code" || row.Bps != 500 || row.BoostLuna != 0 || row.AmountLuna != 250975 {
		t.Fatalf("promotion changed %+v", row)
	}
	ledger, ok, err := store.ReadStakeLedger(address)
	if err != nil || !ok || ledger.A != 8 {
		t.Fatalf("promotion consumed earned ledger %+v %v", ledger, err)
	}
}
