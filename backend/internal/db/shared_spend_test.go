package db

import (
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/stakeledger"
)

// TestSharedEligibleSpendBudgetIsOnePoolAcrossLinkedWallets pins the whole
// point of the account-level spend budget: a buyer with linked staking
// wallets gets ONE $500/day + $1000/month eligible-spend allowance, not one
// per book — and an order of $600 charges at most $500 of it in total.
func TestSharedEligibleSpendBudgetIsOnePoolAcrossLinkedWallets(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	account := stakeTestWallet(t, "shared-spend-account")
	stakeWallet := stakeTestWallet(t, "shared-spend-linked")

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	linked, err := store.AddStakeAddress(u.ID, stakeWallet, now)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	addrs := linked.EarningStakeAddresses()
	if len(addrs) != 2 {
		t.Fatalf("earning set = %v, want [account linked]", addrs)
	}

	month := now.Format("2006-01")
	for _, address := range addrs {
		if _, err := store.ApplyProfitSnapshot(address, month, 1_000_000, 0.05, 400, 1.0, 100_000_000_000, now); err != nil {
			t.Fatalf("profit snapshot for %s: %v", address, err)
		}
	}
	p := stakeledger.Normalize(stakeledger.Defaults)

	spend := func(xUSD float64, at time.Time) (int64, int, error) {
		var luna int64
		var bps int
		updateErr := store.Update(func(tx *badger.Txn) error {
			var e error
			luna, bps, e = purchaseEarningSetInTx(tx, account, addrs, xUSD, 0.05, p, at)
			return e
		})
		return luna, bps, updateErr
	}

	// A $600 order: only $500 of it is eligible, and the budget drains ONCE —
	// not once per earning wallet.
	luna1, bps1, err := spend(600, now)
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	if luna1 <= 0 || bps1 <= 0 {
		t.Fatalf("first order paid %d luna / %d bps, want a positive boost", luna1, bps1)
	}
	ss, err := store.ReadSharedSpend(account, now)
	if err != nil {
		t.Fatalf("read shared spend: %v", err)
	}
	if ss.HDay != 50000 || ss.HMonth != 50000 {
		t.Fatalf("budget after a $600 order = day %d / month %d, want 50000 / 50000 (one shared $500 cap)", ss.HDay, ss.HMonth)
	}

	// Books must have been debited too (real money moved), with the same
	// eligible share each.
	for _, address := range addrs {
		l, ok, err := store.ReadStakeLedger(address, now)
		if err != nil || !ok {
			t.Fatalf("read book %s: ok=%v err=%v", address, ok, err)
		}
		if l.HDay != 50000 {
			t.Fatalf("book %s HDay = %d, want 50000 (the shared eligible amount)", address, l.HDay)
		}
	}

	// A second boost on the same UTC day: the shared budget is spent.
	luna2, bps2, err := spend(100, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second order: %v", err)
	}
	if luna2 != 0 || bps2 != 0 {
		t.Fatalf("second order paid %d luna / %d bps, want 0 / 0 (shared budget exhausted)", luna2, bps2)
	}

	// Refill the books with freshly settled fees (delta snapshot), then the
	// next UTC day: the budget rolls over and the boost flows again.
	for _, address := range addrs {
		if _, err := store.ApplyProfitSnapshot(address, month, 2_000_000, 0.05, 400, 1.0, 100_000_000_000, now); err != nil {
			t.Fatalf("top-up snapshot for %s: %v", address, err)
		}
	}
	luna3, _, err := spend(100, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("next-day order: %v", err)
	}
	if luna3 <= 0 {
		t.Fatal("next-day order paid 0, want a positive boost after the daily rollover")
	}
	ss3, err := store.ReadSharedSpend(account, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("read shared spend next day: %v", err)
	}
	if ss3.HDay != 10000 {
		t.Fatalf("next-day budget spent = %d, want 10000 ($100), i.e. the counter reset at midnight", ss3.HDay)
	}
}

// TestSharedSpendBudgetFollowsTheAccountNotTheWallet pins that the budget is
// keyed by the ACCOUNT: linking or unlinking a wallet on the same account
// does not refill the daily allowance.
func TestSharedSpendBudgetFollowsTheAccountNotTheWallet(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	account := stakeTestWallet(t, "shared-spend-key-account")
	first := stakeTestWallet(t, "shared-spend-key-linked-1")
	second := stakeTestWallet(t, "shared-spend-key-linked-2")

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := store.AddStakeAddress(u.ID, first, now); err != nil {
		t.Fatalf("link first: %v", err)
	}
	month := now.Format("2006-01")
	for _, address := range []string{account, first, second} {
		if _, err := store.ApplyProfitSnapshot(address, month, 1_000_000, 0.05, 10, 1.0, 100_000_000_000, now); err != nil {
			t.Fatalf("profit snapshot for %s: %v", address, err)
		}
	}

	// Drain the shared budget with the first linked wallet in the set.
	p := stakeledger.Normalize(stakeledger.Defaults)
	err = store.Update(func(tx *badger.Txn) error {
		var e error
		addrs := []string{account, first}
		_, _, e = purchaseEarningSetInTx(tx, account, addrs, 600, 0.05, p, now)
		return e
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	// Link ANOTHER wallet — a fresh book with zero history. Under the old
	// per-book caps this would hand the account a new allowance; the shared
	// budget must say no.
	u2, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	linked2, err := store.AddStakeAddress(u2.ID, second, now)
	if err != nil {
		t.Fatalf("link second: %v", err)
	}
	var luna int64
	var bps int
	err = store.Update(func(tx *badger.Txn) error {
		var e error
		luna, bps, e = purchaseEarningSetInTx(tx, account, linked2.EarningStakeAddresses(), 600, 0.05, p, now)
		return e
	})
	if err != nil {
		t.Fatalf("post-link order: %v", err)
	}
	if luna != 0 || bps != 0 {
		t.Fatalf("after linking a fresh wallet the boost paid %d luna / %d bps, want 0 / 0 (budget is the account's, not the wallet's)", luna, bps)
	}
}
