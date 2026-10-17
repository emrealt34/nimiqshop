package db

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"nimiqshop/internal/money"
	"nimiqshop/internal/nimiq"
)

// stakeTestWallet derives a stable Nimiq address from a seed string. Tests need
// two DIFFERENT valid wallets (the one an account signs in with and the one that
// stakes); deriving them keeps the addresses valid by construction instead of
// hardcoding strings whose checksum nobody re-checks.
func stakeTestWallet(t *testing.T, seed string) string {
	t.Helper()
	sum := sha256.Sum256([]byte("nimshop linked stake test " + seed))
	priv := ed25519.NewKeyFromSeed(sum[:])
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("ed25519: unexpected public key type")
	}
	addr, err := nimiq.AddressFromPublicKey(pub)
	if err != nil {
		t.Fatalf("address from public key: %v", err)
	}
	return nimiq.NormalizeAddress(addr)
}

// TestStakeAddressLinkPrimitives covers the store-level contract the linking
// endpoint relies on: normalisation, the "linking your own wallet is not a
// link" rule, clearing, and the reverse lookup.
func TestStakeAddressLinkPrimitives(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	account := stakeTestWallet(t, "account")
	stakeWallet := stakeTestWallet(t, "staker")
	other := stakeTestWallet(t, "other")

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}

	// Before any link the earning address IS the account address.
	if got := u.EffectiveStakeAddress(); got != account {
		t.Fatalf("EffectiveStakeAddress() = %q, want the login address %q", got, account)
	}
	if u.HasLinkedStakeAddress() {
		t.Fatal("a fresh account must not report a linked stake wallet")
	}

	// A spaced/lowercase spelling of the same wallet must be stored canonically,
	// otherwise the signed message and the stored address disagree.
	linked, err := store.SetStakeAddress(u.ID, stakeWallet, now)
	if err != nil {
		t.Fatalf("SetStakeAddress: %v", err)
	}
	if linked.EffectiveStakeAddress() != stakeWallet {
		t.Fatalf("EffectiveStakeAddress() = %q, want the linked wallet %q", linked.EffectiveStakeAddress(), stakeWallet)
	}
	if !linked.HasLinkedStakeAddress() {
		t.Fatal("HasLinkedStakeAddress() = false after linking a different wallet")
	}
	if !linked.StakeAddressLinkedAt.Equal(now) {
		t.Fatalf("StakeAddressLinkedAt = %v, want %v", linked.StakeAddressLinkedAt, now)
	}
	// The login address is untouched: payouts keep going to it.
	if linked.NimiqAddress != account {
		t.Fatalf("linking changed the account address to %q", linked.NimiqAddress)
	}

	// Reloaded from disk, not just from the returned struct.
	reloaded, err := store.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if reloaded.EffectiveStakeAddress() != stakeWallet {
		t.Fatalf("link did not persist: EffectiveStakeAddress() = %q", reloaded.EffectiveStakeAddress())
	}

	// Reverse lookup: a stake observation about the linked wallet must find the
	// buyer it belongs to.
	byStake, err := store.GetUserByStakeAddress(stakeWallet)
	if err != nil {
		t.Fatalf("GetUserByStakeAddress: %v", err)
	}
	if byStake.ID != u.ID {
		t.Fatalf("GetUserByStakeAddress returned %q, want %q", byStake.ID, u.ID)
	}
	if _, err := store.GetUserByStakeAddress(other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlinked wallet: err = %v, want ErrNotFound", err)
	}

	// Linking the wallet the account already signs in with is a no-op, not a
	// duplicate: HasLinkedStakeAddress must stay honest.
	same, err := store.SetStakeAddress(u.ID, account, now)
	if err != nil {
		t.Fatalf("SetStakeAddress(own address): %v", err)
	}
	if same.HasLinkedStakeAddress() || same.StakeAddress != "" {
		t.Fatalf("linking the login address stored a link: %+v", same)
	}

	// Re-link, then clear: the account earns from its own wallet again and the
	// link no longer resolves in reverse.
	if _, err := store.SetStakeAddress(u.ID, stakeWallet, now); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	cleared, err := store.ClearStakeAddress(u.ID)
	if err != nil {
		t.Fatalf("ClearStakeAddress: %v", err)
	}
	if cleared.EffectiveStakeAddress() != account || cleared.HasLinkedStakeAddress() {
		t.Fatalf("after clearing: %+v", cleared)
	}
	if _, err := store.GetUserByStakeAddress(stakeWallet); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared link still resolves: err = %v, want ErrNotFound", err)
	}

	// A malformed address must never be stored.
	if _, err := store.SetStakeAddress(u.ID, "NQ00 NOT AN ADDRESS", now); err == nil {
		t.Fatal("SetStakeAddress accepted a malformed address")
	}
	if _, err := store.SetStakeAddress("", stakeWallet, now); err == nil {
		t.Fatal("SetStakeAddress accepted an empty user id")
	}
}

// TestLinkedStakeWalletEarnsButTheAccountWalletIsPaid is the money-path guard
// for the whole feature. A shop account IS an address, so a buyer who stakes
// from his regular wallet but checks out through Nimiq Pay is a DIFFERENT
// account: the sign-in wallet has no stake and the staking wallet has no
// account. After linking, two things must both be true —
//
//	earn from  the LINKED wallet  (stake lookup, loyalty ledger, boost rate)
//	pay to     the ACCOUNT wallet (cashback recipient)
//
// Getting the first wrong pays a real staker the base rate. Getting the second
// wrong sends money to a wallet this session never authenticated with.
func TestLinkedStakeWalletEarnsButTheAccountWalletIsPaid(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	account := stakeTestWallet(t, "pay-from")
	stakeWallet := stakeTestWallet(t, "stake-from")
	now := time.Now().UTC()

	var asked []string
	store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		asked = append(asked, canonicalStakeAddr(address))
		if canonicalStakeAddr(address) == canonicalStakeAddr(stakeWallet) {
			return StakerStake{StakeLuna: 2_000_00000, Staked: true, BaseBps: 100}, nil
		}
		return StakerStake{}, nil // the sign-in wallet has never staked
	})

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := store.SetStakeAddress(u.ID, stakeWallet, now); err != nil {
		t.Fatalf("link: %v", err)
	}

	// The loyalty ledger belongs to the STAKING wallet: it is the wallet that
	// collects pool fees. Seed it with credit so a boost can be paid at all.
	if err := store.ObserveStakeLedger(stakeWallet, 2_000_00000, now); err != nil {
		t.Fatalf("observe ledger: %v", err)
	}
	if _, err := store.ApplyProfitSnapshot(stakeWallet, now.Format("2006-01"), 5_000_000, 0.012, 400, 0.5, 2_000_00000, now); err != nil {
		t.Fatalf("profit snapshot: %v", err)
	}

	q := Quote{
		ID: "q-linked", UserID: u.ID, Status: "awaiting_payment",
		ProductID: "Steam", Quantity: 1,
		ProductUSD: money.FromFloat(50), EstimatedNIM: 4166, NimUsdRate: 0.012,
		CreatedAt: now,
	}
	if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	if err := store.CompleteQuoteWithFulfillment("q-linked", nil); err != nil {
		t.Fatalf("fulfill: %v", err)
	}

	rows, err := store.ListCashbacksByUser(u.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("want exactly one cashback row, got %d (%v)", len(rows), err)
	}
	row := rows[0]

	// 1. The stake lookup went to the LINKED wallet, never to the sign-in one.
	for _, addr := range asked {
		if addr == canonicalStakeAddr(account) {
			t.Fatalf("the stake lookup asked about the sign-in wallet %q; a linked staker's stake lives in %q", account, stakeWallet)
		}
	}
	sawStakeWallet := false
	for _, addr := range asked {
		if addr == canonicalStakeAddr(stakeWallet) {
			sawStakeWallet = true
		}
	}
	if !sawStakeWallet {
		t.Fatalf("the stake lookup never asked about the linked wallet %q (asked: %v)", stakeWallet, asked)
	}

	// 2. It earned the staker rate, i.e. the stake was recognised.
	if row.Status != CashbackQueued {
		t.Fatalf("status = %q, want queued (skip reason %q)", row.Status, row.SkipReason)
	}
	if row.Bps <= 0 {
		t.Fatalf("bps = %d, want the staker rate — the linked wallet's stake was not recognised", row.Bps)
	}
	if row.StakeLuna != 2_000_00000 {
		t.Fatalf("stake_luna = %d, want the linked wallet's 200000000", row.StakeLuna)
	}

	// 3. The ledger boost came from the LINKED wallet's book. Under the old
	// single-address code this reads the sign-in wallet's (non-existent) ledger
	// and the boost is silently 0.
	if row.BoostLuna <= 0 {
		t.Fatalf("boost_luna = %d, want > 0: the ledger was read from the wrong wallet", row.BoostLuna)
	}
	if _, ok, err := store.ReadStakeLedger(account); err != nil || ok {
		t.Fatalf("the sign-in wallet must not have a ledger (ok=%v err=%v)", ok, err)
	}

	// 4. The payout still goes to the wallet the buyer authenticated with.
	if canonicalStakeAddr(row.Recipient) != canonicalStakeAddr(account) {
		t.Fatalf("recipient = %q, want the account wallet %q — cashback must never be sent to a linked wallet",
			row.Recipient, account)
	}
}

// TestStakeRecheckRevivesCashbackForALinkedWallet is the regression test for a
// silent money loss the link feature would otherwise introduce: the
// post-fulfillment re-ask recorded the EARNING address, and the repair path
// resolved the account FROM that address. For a linked wallet there is no
// account at that address, so GetUserByAddress returned ErrNotFound, the repair
// never ran, the row stayed skipped at zero rate and the buyer was never paid —
// with no error anywhere to notice.
func TestStakeRecheckRevivesCashbackForALinkedWallet(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	account := stakeTestWallet(t, "recheck-account")
	stakeWallet := stakeTestWallet(t, "recheck-staker")
	now := time.Now().UTC()

	indexed := false
	store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		if indexed && canonicalStakeAddr(address) == canonicalStakeAddr(stakeWallet) {
			return StakerStake{StakeLuna: 2_000_00000, Staked: true, BaseBps: 50}, nil
		}
		return StakerStake{}, nil
	})

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := store.SetStakeAddress(u.ID, stakeWallet, now); err != nil {
		t.Fatalf("link: %v", err)
	}

	q := Quote{
		ID: "q-recheck", UserID: u.ID, Status: "awaiting_payment",
		ProductID: "Steam", Quantity: 1,
		ProductUSD: money.FromFloat(50), EstimatedNIM: 4166, NimUsdRate: 0.012,
		CreatedAt: now,
	}
	if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
		t.Fatalf("create quote: %v", err)
	}

	// Delivered while the pool has not indexed the stake yet.
	if err := store.CompleteQuoteWithFulfillment("q-recheck", nil); err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	recs, err := store.ListStakeRechecks()
	if err != nil || len(recs) != 1 {
		t.Fatalf("want one scheduled re-check, got %d (%v)", len(recs), err)
	}
	if canonicalStakeAddr(recs[0].Address) != canonicalStakeAddr(stakeWallet) {
		t.Fatalf("re-check address = %q, want the linked earning wallet %q", recs[0].Address, stakeWallet)
	}
	if recs[0].UserID != u.ID {
		t.Fatalf("re-check user = %q, want %q (the user-scoped repair needs it)", recs[0].UserID, u.ID)
	}

	// The pool indexes the stake; the re-ask must now repair the row.
	indexed = true
	resolved, _ := store.RunStakeRechecks(now.Add(time.Minute))
	if resolved != 1 {
		t.Fatalf("re-check resolved = %d, want 1 — the repair could not find the account behind the linked wallet", resolved)
	}

	rows, _ := store.ListCashbacksByUser(u.ID, 0)
	if len(rows) != 1 {
		t.Fatalf("want one cashback row, got %d", len(rows))
	}
	got := rows[0]
	if got.Status != CashbackQueued {
		t.Errorf("status = %q, want queued (skip reason %q)", got.Status, got.SkipReason)
	}
	if got.Bps != 50 {
		t.Errorf("bps = %d, want 50 (staker base)", got.Bps)
	}
	if got.AmountLuna <= 0 {
		t.Errorf("amount = %d, want > 0", got.AmountLuna)
	}
	// Revived AND payable — to the account wallet, not the linked one.
	if canonicalStakeAddr(got.Recipient) != canonicalStakeAddr(account) {
		t.Errorf("recipient = %q, want the account wallet %q", got.Recipient, account)
	}
	if remaining, _ := store.ListStakeRechecks(); len(remaining) != 0 {
		t.Errorf("the resolved re-check was not cleared: %d left", len(remaining))
	}
}
