package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nimiqshop/internal/money"
)

// Complete real quote transitions through three sign-ins. The stake and earned
// ledger live only on C; each payout still belongs exclusively to its buyer.
func TestStakeGroupFulfillmentSharedBenefitsSeparateRecipients(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	var users []User
	var wallets []string
	for i := 0; i < 3; i++ {
		wallets = append(wallets, stakeTestWallet(t, fmt.Sprintf("fulfill-group-%d", i)))
		u, e := s.FindOrCreateUserByAddress(wallets[i])
		if e != nil {
			t.Fatal(e)
		}
		users = append(users, u)
	}
	if _, err = s.AddStakeAddress(users[0].ID, wallets[1], now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddStakeAddress(users[1].ID, wallets[2], now); err != nil {
		t.Fatal(err)
	}
	s.SetStakerLookupDetailed(func(_ context.Context, addr string) (StakerStake, error) {
		if canonicalStakeAddr(addr) == canonicalStakeAddr(wallets[2]) {
			return StakerStake{Staked: true, StakeLuna: 10000000, BaseBps: 100}, nil
		}
		return StakerStake{}, nil
	})
	if _, err = s.ApplyProfitSnapshot(wallets[2], now.Format("2006-01"), 10000000, 1, 0, 1, 10000000, now); err != nil {
		t.Fatal(err)
	}
	for i, u := range users {
		q := Quote{ID: fmt.Sprintf("group-order-%d", i), UserID: u.ID, Status: "awaiting_payment", ProductID: "Steam", Quantity: 1, ProductUSD: money.FromFloat(10), EstimatedNIM: 10, NimUsdRate: 1, CreatedAt: now}
		if err = s.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
			t.Fatal(err)
		}
		if err = s.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
			t.Fatal(err)
		}
		cb, e := s.GetCashbackByQuote(q.ID)
		if e != nil {
			t.Fatal(e)
		}
		if cb.UserID != u.ID || canonicalStakeAddr(cb.Recipient) != canonicalStakeAddr(wallets[i]) || cb.CashbackSource != "ledger" || cb.BoostLuna <= 0 {
			t.Fatalf("wrong payout for login %d: %+v", i, cb)
		}
		rows, e := s.ListCashbacksByUser(u.ID, 0)
		if e != nil || len(rows) != 1 || rows[0].QuoteID != q.ID {
			t.Fatalf("cashbacks leaked for login %d: %+v %v", i, rows, e)
		}
	}
	for _, addr := range wallets {
		ss, e := s.ReadSharedSpend(addr, now)
		if e != nil || ss.HDay != 3000 {
			t.Fatalf("group spend %s: %+v %v", addr, ss, e)
		}
	}
}

// Membership can change while the external stake lookup is in flight. A
// detached wallet's old answer must not grant the buyer a stale staker payout.
func TestStakeGroupFulfillmentRejectsStaleLookupAfterDetach(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	a := stakeTestWallet(t, "race-fulfill-a")
	b := stakeTestWallet(t, "race-fulfill-b")
	u, err := s.FindOrCreateUserByAddress(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddStakeAddress(u.ID, b, now); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s.SetStakerLookupDetailed(func(ctx context.Context, addr string) (StakerStake, error) {
		if canonicalStakeAddr(addr) != canonicalStakeAddr(b) {
			return StakerStake{}, nil
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return StakerStake{}, ctx.Err()
		}
		return StakerStake{Staked: true, StakeLuna: 10000000, BaseBps: 100}, nil
	})
	q := Quote{ID: "detached-during-lookup", UserID: u.ID, Status: "awaiting_payment", ProductID: "Steam", Quantity: 1, ProductUSD: money.FromFloat(10), EstimatedNIM: 10, NimUsdRate: 1, CreatedAt: now}
	if err = s.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.CompleteQuoteWithFulfillment(q.ID, nil) }()
	<-entered
	if _, err = s.RemoveStakeAddress(u.ID, b); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	cb, err := s.GetCashbackByQuote(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cb.Boosted || cb.StakeLuna != 0 || cb.BoostLuna != 0 {
		t.Fatalf("stale stake paid after detach: %+v", cb)
	}
}
