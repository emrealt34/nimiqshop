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
// several DIFFERENT valid wallets (the one an account signs in with and the ones
// that stake); deriving them keeps the addresses valid by construction instead
// of hardcoding strings whose checksum nobody re-checks.
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
// endpoint relies on: normalisation, the "linking your own wallet is not a link"
// rule, several wallets at once, the cap, removing one of them, clearing, and
// the reverse lookup.
func TestStakeAddressLinkPrimitives(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	account := stakeTestWallet(t, "account")
	stakeWallet := stakeTestWallet(t, "staker")
	second := stakeTestWallet(t, "staker-two")
	other := stakeTestWallet(t, "other")

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}

	// Before any link the only earning wallet IS the account wallet.
	if got := u.EffectiveStakeAddress(); got != account {
		t.Fatalf("EffectiveStakeAddress() = %q, want the login address %q", got, account)
	}
	if got := u.EarningStakeAddresses(); len(got) != 1 || got[0] != account {
		t.Fatalf("EarningStakeAddresses() = %v, want just the login address", got)
	}
	if u.HasLinkedStakeAddress() {
		t.Fatal("a fresh account must not report a linked stake wallet")
	}

	// A spaced/lowercase spelling of the same wallet must be stored canonically,
	// otherwise the signed message and the stored address disagree.
	linked, err := store.AddStakeAddress(u.ID, stakeWallet, now)
	if err != nil {
		t.Fatalf("AddStakeAddress: %v", err)
	}
	if linked.EffectiveStakeAddress() != stakeWallet {
		t.Fatalf("EffectiveStakeAddress() = %q, want the linked wallet %q", linked.EffectiveStakeAddress(), stakeWallet)
	}
	if !linked.HasLinkedStakeAddress() {
		t.Fatal("HasLinkedStakeAddress() = false after linking a different wallet")
	}
	if got := linked.StakeAddressLinkedAt(stakeWallet); !got.Equal(now) {
		t.Fatalf("StakeAddressLinkedAt(%s) = %v, want %v", stakeWallet, got, now)
	}
	if !linked.StakeAddressLinkedAt(other).IsZero() {
		t.Fatal("StakeAddressLinkedAt reported a wallet that was never linked")
	}
	// The earning set is the sign-in wallet plus the link, own address first so
	// the primary never changes when wallets are added or removed.
	want := []string{account, stakeWallet}
	if got := linked.EarningStakeAddresses(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("EarningStakeAddresses() = %v, want %v", got, want)
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

	// A SECOND wallet joins: both earn, and the primary stays the first link so
	// the buyer's rate card does not jump around between reads.
	two, err := store.AddStakeAddress(u.ID, second, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("AddStakeAddress(second): %v", err)
	}
	if len(two.LinkedStakeWallets()) != 2 {
		t.Fatalf("LinkedStakeWallets() = %d, want 2", len(two.LinkedStakeWallets()))
	}
	if two.EffectiveStakeAddress() != stakeWallet {
		t.Fatalf("adding a second wallet moved the primary to %q", two.EffectiveStakeAddress())
	}
	if got := two.EarningStakeAddresses(); len(got) != 3 {
		t.Fatalf("EarningStakeAddresses() = %v, want three wallets", got)
	}

	// Adding the same wallet again is a no-op, not a duplicate: a double click
	// or a page refresh must not consume a slot or corrupt the list.
	again, err := store.AddStakeAddress(u.ID, stakeWallet, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("AddStakeAddress(duplicate): %v", err)
	}
	if len(again.LinkedStakeWallets()) != 2 {
		t.Fatalf("a duplicate link was stored: %d wallets", len(again.LinkedStakeWallets()))
	}
	if got := again.StakeAddressLinkedAt(stakeWallet); !got.Equal(now) {
		t.Fatalf("a duplicate link rewrote LinkedAt to %v, want the original %v", got, now)
	}

	// Reverse lookup: a stake observation about ANY linked wallet must find the
	// buyer it belongs to.
	for _, addr := range []string{stakeWallet, second} {
		byStake, err := store.GetUserByStakeAddress(addr)
		if err != nil {
			t.Fatalf("GetUserByStakeAddress(%s): %v", addr, err)
		}
		if byStake.ID != u.ID {
			t.Fatalf("GetUserByStakeAddress(%s) returned %q, want %q", addr, byStake.ID, u.ID)
		}
	}
	if _, err := store.GetUserByStakeAddress(other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlinked wallet: err = %v, want ErrNotFound", err)
	}

	// Linking the wallet the account already signs in with is a no-op, not a
	// link: it always earns, and storing it would double-count its stake.
	same, err := store.AddStakeAddress(u.ID, account, now)
	if err != nil {
		t.Fatalf("AddStakeAddress(own address): %v", err)
	}
	if len(same.LinkedStakeWallets()) != 2 {
		t.Fatalf("linking the login address stored a link: %+v", same.LinkedStakeWallets())
	}

	// The cap. Filling the remaining slots must succeed; the next one must be
	// refused with the sentinel the handler turns into a 409 that names the
	// limit — a silent failure here would look like a broken wallet.
	for i := len(same.LinkedStakeWallets()); i < MaxLinkedStakeAddresses; i++ {
		if _, err := store.AddStakeAddress(u.ID, stakeTestWallet(t, "filler-"+string(rune('a'+i))), now); err != nil {
			t.Fatalf("AddStakeAddress(slot %d): %v", i+1, err)
		}
	}
	full, err := store.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got := len(full.LinkedStakeWallets()); got != MaxLinkedStakeAddresses {
		t.Fatalf("LinkedStakeWallets() = %d, want the cap %d", got, MaxLinkedStakeAddresses)
	}
	if _, err := store.AddStakeAddress(u.ID, other, now); !errors.Is(err, ErrTooManyStakeAddresses) {
		t.Fatalf("past the cap: err = %v, want ErrTooManyStakeAddresses", err)
	}

	// Removing ONE wallet keeps the others earning and frees a slot.
	after, err := store.RemoveStakeAddress(u.ID, second)
	if err != nil {
		t.Fatalf("RemoveStakeAddress: %v", err)
	}
	if len(after.LinkedStakeWallets()) != MaxLinkedStakeAddresses-1 {
		t.Fatalf("after removing one: %d wallets, want %d", len(after.LinkedStakeWallets()), MaxLinkedStakeAddresses-1)
	}
	if _, err := store.GetUserByStakeAddress(second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the removed wallet still resolves in reverse: %v", err)
	}
	if _, err := store.RemoveStakeAddress(u.ID, second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing a wallet that is not linked: err = %v, want ErrNotFound", err)
	}
	if _, err := store.AddStakeAddress(u.ID, other, now); err != nil {
		t.Fatalf("the freed slot was not reusable: %v", err)
	}

	// Clear: the account earns from its own wallet again and no link resolves.
	cleared, err := store.ClearStakeAddresses(u.ID)
	if err != nil {
		t.Fatalf("ClearStakeAddresses: %v", err)
	}
	if cleared.EffectiveStakeAddress() != account || cleared.HasLinkedStakeAddress() {
		t.Fatalf("after clearing: %+v", cleared)
	}
	if got := cleared.EarningStakeAddresses(); len(got) != 1 || got[0] != account {
		t.Fatalf("after clearing: EarningStakeAddresses() = %v, want just the login address", got)
	}
	if _, err := store.GetUserByStakeAddress(stakeWallet); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared link still resolves: err = %v, want ErrNotFound", err)
	}

	// A malformed address must never be stored.
	if _, err := store.AddStakeAddress(u.ID, "NQ00 NOT AN ADDRESS", now); err == nil {
		t.Fatal("AddStakeAddress accepted a malformed address")
	}
	if _, err := store.AddStakeAddress("", stakeWallet, now); err == nil {
		t.Fatal("AddStakeAddress accepted an empty user id")
	}
	if _, err := store.RemoveStakeAddress(u.ID, "NQ00 NOT AN ADDRESS"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RemoveStakeAddress(malformed) = %v, want ErrNotFound", err)
	}
}

// TestAggregateStakerStakeSumsWalletsAndCountsTheBaseOnce is the rule the whole
// multi-wallet feature stands on: a buyer who splits 300 NIM over three wallets
// earns exactly what 300 NIM in one wallet earns — the stakes add up, the flat
// staker base is applied ONCE, and loyalty is the longest streak.
func TestAggregateStakerStakeSumsWalletsAndCountsTheBaseOnce(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	account := stakeTestWallet(t, "agg-account")
	wallets := []string{
		stakeTestWallet(t, "agg-one"),
		stakeTestWallet(t, "agg-two"),
		stakeTestWallet(t, "agg-three"),
	}
	stakes := map[string]int64{
		canonicalStakeAddr(wallets[0]): 100_00000, // 100 NIM
		canonicalStakeAddr(wallets[1]): 150_00000, // 150 NIM
		canonicalStakeAddr(wallets[2]): 50_00000,  //  50 NIM
	}
	const stakerBase = 137 // an odd number: a base counted per wallet cannot hide

	// The sign-in wallet has never staked, exactly like a Nimiq Pay shopper.
	store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		if luna, ok := stakes[canonicalStakeAddr(address)]; ok {
			return StakerStake{StakeLuna: luna, Staked: true, BaseBps: stakerBase}, nil
		}
		return StakerStake{}, nil
	})

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	for i, wallet := range wallets {
		if _, err := store.AddStakeAddress(u.ID, wallet, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
	}
	u, err = store.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got := len(u.EarningStakeAddresses()); got != 4 {
		t.Fatalf("EarningStakeAddresses() = %d wallets, want 4 (sign-in + three links)", got)
	}

	// Seed the loyalty clocks BEFORE the first aggregate: observing a stake at
	// `now` and then trying to back-date it would be asking the watch to move
	// its first-seen clock backwards, which it deliberately does not do.
	old := now.Add(-90 * 24 * time.Hour)
	for _, wallet := range wallets {
		if _, err := store.ObserveStake(wallet, stakes[canonicalStakeAddr(wallet)], old); err != nil {
			t.Fatalf("seed watch %s: %v", wallet, err)
		}
	}

	ctx := context.Background()
	total, report := store.AggregateStakerStake(ctx, u.EarningStakeAddresses())
	if !total.Staked {
		t.Fatal("aggregate says not staked although three wallets stake")
	}
	if want := int64(300_00000); total.StakeLuna != want {
		t.Fatalf("StakeLuna = %d, want the SUM %d — wallets were not added up", total.StakeLuna, want)
	}
	if total.BaseBps != stakerBase {
		t.Fatalf("BaseBps = %d, want %d exactly once: the flat staker base must never be multiplied by the wallet count",
			total.BaseBps, stakerBase)
	}
	if report.Failed != 0 || report.Answered != 4 {
		t.Fatalf("report = %+v, want four answered wallets and no failures", report)
	}
	if len(report.PerWallet) != 4 {
		t.Fatalf("PerWallet = %d rows, want one per earning wallet", len(report.PerWallet))
	}
	// The wallet that does not stake contributes nothing but is still reported,
	// so a page can say "this wallet has no stake" instead of hiding it.
	if row := report.PerWallet[0]; row.Address != account || row.Staked || row.StakeLuna != 0 {
		t.Fatalf("sign-in wallet row = %+v, want a reported non-staker", row)
	}

	// Loyalty: the longest streak wins, and it survives a wallet whose own
	// clock restarts — a wallet linked (or restaked) today cannot shorten the
	// account's streak, which is what "longest" has to mean here.
	if total.LockedDays < 88 {
		t.Fatalf("LockedDays = %d, want the LONGEST streak (~90 days)", total.LockedDays)
	}
	if _, err := store.ObserveStake(wallets[2], 0, now); err != nil {
		t.Fatalf("lapse the newest wallet: %v", err)
	}
	if _, err := store.ObserveStake(wallets[2], stakes[canonicalStakeAddr(wallets[2])], now); err != nil {
		t.Fatalf("restart the newest wallet: %v", err)
	}
	total, _ = store.AggregateStakerStake(ctx, u.EarningStakeAddresses())
	if total.LockedDays < 88 {
		t.Fatalf("LockedDays = %d after one wallet restarted, want the other wallets' ~90-day streak to stand",
			total.LockedDays)
	}

	// An outage on one wallet must not erase the others, and must not be read
	// as a zero: the wallet that could not be asked simply does not answer.
	failing := canonicalStakeAddr(wallets[1])
	store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		if canonicalStakeAddr(address) == failing {
			return StakerStake{}, errors.New("pool unreachable")
		}
		if luna, ok := stakes[canonicalStakeAddr(address)]; ok {
			return StakerStake{StakeLuna: luna, Staked: true, BaseBps: stakerBase}, nil
		}
		return StakerStake{}, nil
	})
	total, report = store.AggregateStakerStake(ctx, u.EarningStakeAddresses())
	if report.Failed != 1 {
		t.Fatalf("report.Failed = %d, want 1 (one wallet could not be asked)", report.Failed)
	}
	if want := int64(150_00000); total.StakeLuna != want {
		t.Fatalf("StakeLuna during a partial outage = %d, want %d from the two wallets that answered", total.StakeLuna, want)
	}
	if !total.Staked {
		t.Fatal("a partial outage turned a staker into a non-staker")
	}
	// With every wallet unreachable, the durable positive observations are the
	// circuit breaker: the buyer keeps being treated as the staker they are.
	store.SetStakerLookupDetailed(func(_ context.Context, _ string) (StakerStake, error) {
		return StakerStake{}, errors.New("pool unreachable")
	})
	_, report = store.AggregateStakerStake(ctx, u.EarningStakeAddresses())
	if report.Failed != 4 || report.Answered != 0 {
		t.Fatalf("report = %+v, want four failures and no answers", report)
	}
	cached := store.CachedStakeAggregate(u.EarningStakeAddresses(), time.Now().UTC())
	if !cached.Staked || cached.StakeLuna <= 0 {
		t.Fatalf("CachedStakeAggregate = %+v, want the last positive observations summed", cached)
	}
	// The durable cache does not remember the pool's number: it applies the
	// operator's current staker base (stakewatch.go), which is also what the
	// payout path uses. What must hold is that it is applied ONCE, not once per
	// wallet — so compare it with a single wallet's cached base.
	if single := store.CachedStakerStake(wallets[0], time.Now().UTC()); cached.BaseBps != single.BaseBps {
		t.Fatalf("cached BaseBps = %d across three wallets but %d for one: the base was multiplied by the wallet count",
			cached.BaseBps, single.BaseBps)
	}
	if cached.BaseBps <= 0 {
		t.Fatalf("cached BaseBps = %d, want the operator's staker base", cached.BaseBps)
	}
}

// linkedStakeBuyer is the shared setup for the money-path tests: an account that
// signs in with one wallet and earns from several others, with a $50 quote ready
// to be fulfilled.
type linkedStakeBuyer struct {
	store   *Store
	user    User
	account string
	wallets []string
	now     time.Time
	quoteID string
}

func newLinkedStakeBuyer(t *testing.T, seed string, walletStakes []int64) *linkedStakeBuyer {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	account := stakeTestWallet(t, seed+"-account")
	// One wallet per entry, in order: a slice keeps the stake amounts matched to
	// the wallets they belong to, which a map walk never could.
	wallets := make([]string, 0, len(walletStakes))
	stakes := make(map[string]int64, len(walletStakes))
	for i, luna := range walletStakes {
		wallet := stakeTestWallet(t, seed+"-wallet-"+string(rune('a'+i)))
		wallets = append(wallets, wallet)
		stakes[canonicalStakeAddr(wallet)] = luna
	}
	store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		if luna, ok := stakes[canonicalStakeAddr(address)]; ok {
			return StakerStake{StakeLuna: luna, Staked: true, BaseBps: 100}, nil
		}
		return StakerStake{}, nil
	})

	u, err := store.FindOrCreateUserByAddress(account)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	for j, wallet := range wallets {
		if _, err := store.AddStakeAddress(u.ID, wallet, now.Add(time.Duration(j)*time.Minute)); err != nil {
			t.Fatalf("link %d: %v", j, err)
		}
	}
	u, err = store.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	quoteID := "q-" + seed
	q := Quote{
		ID: quoteID, UserID: u.ID, Status: "awaiting_payment",
		ProductID: "Steam", Quantity: 1,
		ProductUSD: money.FromFloat(50), EstimatedNIM: 4166, NimUsdRate: 0.012,
		CreatedAt: now,
	}
	if err := store.CreateQuoteWithPurchaseLimits(q, 0, 0, 0, now); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	return &linkedStakeBuyer{store: store, user: u, account: account, wallets: wallets, now: now, quoteID: quoteID}
}

// fulfill delivers the quote and returns the single cashback row it produced.
func (b *linkedStakeBuyer) fulfill(t *testing.T) Cashback {
	t.Helper()
	if err := b.store.CompleteQuoteWithFulfillment(b.quoteID, nil); err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	rows, err := b.store.ListCashbacksByUser(b.user.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("want exactly one cashback row, got %d (%v)", len(rows), err)
	}
	return rows[0]
}

// TestLinkedStakeWalletsEarnButTheAccountWalletIsPaid is the money-path guard
// for the whole feature. A shop account IS an address, so a buyer who stakes
// from his regular wallets but checks out through Nimiq Pay is a DIFFERENT
// account: the sign-in wallet has no stake and the staking wallets have no
// account. After linking, two things must both be true —
//
//	earn from  EVERY linked wallet (stake lookup, loyalty ledger, boost rate)
//	pay to     the ACCOUNT wallet  (cashback recipient)
//
// Getting the first wrong pays a real staker the base rate. Getting the second
// wrong sends money to a wallet this session never authenticated with.
func TestLinkedStakeWalletsEarnButTheAccountWalletIsPaid(t *testing.T) {
	b := newLinkedStakeBuyer(t, "paid", []int64{200_00000, 120_00000})
	var asked []string
	b.store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		asked = append(asked, canonicalStakeAddr(address))
		if canonicalStakeAddr(address) == canonicalStakeAddr(b.wallets[0]) {
			return StakerStake{StakeLuna: 200_00000, Staked: true, BaseBps: 100}, nil
		}
		if canonicalStakeAddr(address) == canonicalStakeAddr(b.wallets[1]) {
			return StakerStake{StakeLuna: 120_00000, Staked: true, BaseBps: 100}, nil
		}
		return StakerStake{}, nil // the sign-in wallet has never staked
	})

	// Each staking wallet keeps its OWN loyalty ledger: it is the wallet that
	// collects pool fees. Seed both with credit so a boost can be paid at all.
	for i, wallet := range b.wallets {
		luna := int64(200_00000)
		if i == 1 {
			luna = 120_00000
		}
		if err := b.store.ObserveStakeLedger(wallet, luna, b.now); err != nil {
			t.Fatalf("observe ledger: %v", err)
		}
		if _, err := b.store.ApplyProfitSnapshot(wallet, b.now.Format("2006-01"), 5_000_000, 0.012, 400, 0.5, luna, b.now); err != nil {
			t.Fatalf("profit snapshot: %v", err)
		}
	}

	row := b.fulfill(t)

	// 1. Every earning wallet was asked about — the sign-in wallet (it may
	//    stake too) and both links.
	seen := map[string]bool{}
	for _, addr := range asked {
		seen[addr] = true
	}
	for _, wallet := range append([]string{b.account}, b.wallets...) {
		if !seen[canonicalStakeAddr(wallet)] {
			t.Fatalf("the stake lookup never asked about %q (asked: %v)", wallet, asked)
		}
	}

	// 2. It earned the staker rate, i.e. the stakes were recognised and summed.
	if row.Status != CashbackQueued {
		t.Fatalf("status = %q, want queued (skip reason %q)", row.Status, row.SkipReason)
	}
	if row.Bps <= 0 {
		t.Fatalf("bps = %d, want the staker rate — the linked wallets' stake was not recognised", row.Bps)
	}
	if want := int64(320_00000); row.StakeLuna != want {
		t.Fatalf("stake_luna = %d, want the SUM of both wallets (%d)", row.StakeLuna, want)
	}

	// 3. The ledger boost came from the LINKED wallets' books. Under the old
	// single-address code this reads the sign-in wallet's (non-existent) ledger
	// and the boost is silently 0.
	if row.BoostLuna <= 0 {
		t.Fatalf("boost_luna = %d, want > 0: the ledger was read from the wrong wallet", row.BoostLuna)
	}
	// The sign-in wallet is part of the earning set now, so it does get a book
	// — an EMPTY one. The invariant that matters is that it contributed no
	// credit: every Luna of the boost came from the wallets that actually stake.
	if l, ok, err := b.store.ReadStakeLedger(b.account); err != nil {
		t.Fatalf("read the sign-in wallet's ledger: %v", err)
	} else if ok && l.A > 0 {
		t.Fatalf("the sign-in wallet's ledger holds %v NIM of credit although it has never staked", l.A)
	}

	// 4. The payout still goes to the wallet the buyer authenticated with.
	if canonicalStakeAddr(row.Recipient) != canonicalStakeAddr(b.account) {
		t.Fatalf("recipient = %q, want the account wallet %q — cashback must never be sent to a linked wallet",
			row.Recipient, b.account)
	}
}

// TestSplitStakeCountsTheBaseOnce is the buyer-visible form of the aggregation
// rule: 300 NIM in one wallet and 300 NIM split over three wallets must earn
// the SAME cashback on the same order. If the staker base were applied per
// wallet, splitting a stake would triple the shop's payout for identical stake —
// and a buyer would be told to split wallets to earn more.
func TestSplitStakeCountsTheBaseOnce(t *testing.T) {
	one := newLinkedStakeBuyer(t, "base-one", []int64{300_00000})
	split := newLinkedStakeBuyer(t, "base-split", []int64{100_00000, 100_00000, 100_00000})

	rowOne := one.fulfill(t)
	rowSplit := split.fulfill(t)

	if rowOne.Status != CashbackQueued || rowSplit.Status != CashbackQueued {
		t.Fatalf("statuses = %q / %q (skip reasons %q / %q), want both queued",
			rowOne.Status, rowSplit.Status, rowOne.SkipReason, rowSplit.SkipReason)
	}
	if rowOne.StakeLuna != rowSplit.StakeLuna {
		t.Fatalf("stake_luna = %d for one wallet vs %d for three: the split stake was not summed to the same total",
			rowOne.StakeLuna, rowSplit.StakeLuna)
	}
	if want := int64(300_00000); rowSplit.StakeLuna != want {
		t.Fatalf("stake_luna = %d, want %d (100 NIM in each of three wallets)", rowSplit.StakeLuna, want)
	}
	if rowOne.Bps != rowSplit.Bps {
		t.Fatalf("bps = %d for one wallet but %d for three wallets holding the same total: the base is not counted once",
			rowOne.Bps, rowSplit.Bps)
	}
	if rowOne.AmountLuna != rowSplit.AmountLuna {
		t.Fatalf("amount = %d vs %d for the same order and the same total stake: splitting a stake must not change the payout",
			rowOne.AmountLuna, rowSplit.AmountLuna)
	}
	if rowOne.AmountLuna <= 0 {
		t.Fatalf("amount = %d, want a real staker payout", rowOne.AmountLuna)
	}
	// Both pay to their own account wallets.
	if canonicalStakeAddr(rowSplit.Recipient) != canonicalStakeAddr(split.account) {
		t.Fatalf("recipient = %q, want the account wallet %q", rowSplit.Recipient, split.account)
	}
}

// TestLedgerBoostIsSummedAcrossLinkedWallets proves the other half of the rule:
// stakes and the ledger books they earned ADD UP (each capped by its own
// eligible spend), so a buyer with credit in two wallets earns more than the
// same buyer with credit in one — while the flat base stays single.
func TestLedgerBoostIsSummedAcrossLinkedWallets(t *testing.T) {
	// Same two wallets, same stakes; only the number of funded ledger books
	// differs, so any difference in the payout is the summed boost.
	single := newLinkedStakeBuyer(t, "boost-single", []int64{200_00000, 200_00000})
	both := newLinkedStakeBuyer(t, "boost-both", []int64{200_00000, 200_00000})

	fund := func(b *linkedStakeBuyer, wallets []string) {
		for _, wallet := range wallets {
			if err := b.store.ObserveStakeLedger(wallet, 200_00000, b.now); err != nil {
				t.Fatalf("observe ledger: %v", err)
			}
			if _, err := b.store.ApplyProfitSnapshot(wallet, b.now.Format("2006-01"), 8_000_000, 0.012, 400, 0.5, 200_00000, b.now); err != nil {
				t.Fatalf("profit snapshot: %v", err)
			}
		}
	}
	fund(single, single.wallets[:1])
	fund(both, both.wallets)

	rowSingle := single.fulfill(t)
	rowBoth := both.fulfill(t)

	if rowSingle.BoostLuna <= 0 {
		t.Fatalf("boost_luna = %d for the funded wallet, want > 0", rowSingle.BoostLuna)
	}
	if rowBoth.BoostLuna <= rowSingle.BoostLuna {
		t.Fatalf("boost_luna = %d with two funded wallets vs %d with one: the books were not summed",
			rowBoth.BoostLuna, rowSingle.BoostLuna)
	}
	if rowBoth.AmountLuna <= rowSingle.AmountLuna {
		t.Fatalf("amount = %d vs %d: a second funded wallet must add to the payout",
			rowBoth.AmountLuna, rowSingle.AmountLuna)
	}
	// The base part of the rate is identical — only the boost grew.
	if rowBoth.Bps-rowSingle.Bps <= 0 {
		t.Fatalf("bps = %d vs %d, want the second wallet's book to raise the total rate", rowBoth.Bps, rowSingle.Bps)
	}
	// Each book was debited, so neither wallet can pay the same credit twice.
	for _, wallet := range both.wallets {
		l, ok, err := both.store.ReadStakeLedger(wallet)
		if err != nil || !ok {
			t.Fatalf("ledger for %s: ok=%v err=%v", wallet, ok, err)
		}
		if l.A < 0 {
			t.Fatalf("ledger for %s went negative (A=%v): the debit overshot the book", wallet, l.A)
		}
	}
	if canonicalStakeAddr(rowBoth.Recipient) != canonicalStakeAddr(both.account) {
		t.Fatalf("recipient = %q, want the account wallet %q", rowBoth.Recipient, both.account)
	}
}

// TestStakeRecheckWatchesEveryLinkedWallet is the regression test for a silent
// money loss the link feature would otherwise introduce: the post-fulfillment
// re-ask recorded ONE address, and the repair path resolved the account FROM
// that address. For a linked wallet there is no account at that address, so
// GetUserByAddress returned ErrNotFound, the repair never ran, the row stayed
// skipped at zero rate and the buyer was never paid — with no error anywhere to
// notice. With several wallets the re-ask must also watch all of them: the stake
// that was missing at delivery can appear under any wallet the buyer owns.
func TestStakeRecheckWatchesEveryLinkedWallet(t *testing.T) {
	b := newLinkedStakeBuyer(t, "recheck", []int64{200_00000, 200_00000})
	indexed := map[string]bool{}
	b.store.SetStakerLookupDetailed(func(_ context.Context, address string) (StakerStake, error) {
		key := canonicalStakeAddr(address)
		if indexed[key] {
			return StakerStake{StakeLuna: 200_00000, Staked: true, BaseBps: 50}, nil
		}
		return StakerStake{}, nil
	})

	// Delivered while the pool has indexed neither wallet.
	row := b.fulfill(t)
	recs, err := b.store.ListStakeRechecks()
	if err != nil || len(recs) != 1 {
		t.Fatalf("want one scheduled re-check, got %d (%v)", len(recs), err)
	}
	if recs[0].UserID != b.user.ID {
		t.Fatalf("re-check user = %q, want %q (the user-scoped repair needs it)", recs[0].UserID, b.user.ID)
	}
	watched := map[string]bool{}
	for _, addr := range recs[0].EarningAddresses() {
		watched[canonicalStakeAddr(addr)] = true
	}
	for _, wallet := range append([]string{b.account}, b.wallets...) {
		if !watched[canonicalStakeAddr(wallet)] {
			t.Fatalf("the re-check does not watch %q (watching %v)", wallet, recs[0].EarningAddresses())
		}
	}
	// The legacy single field stays the sign-in address, so even the no-user
	// fallback resolves an account instead of a wallet nobody owns.
	if canonicalStakeAddr(recs[0].Address) != canonicalStakeAddr(b.account) {
		t.Fatalf("re-check Address = %q, want the sign-in wallet %q", recs[0].Address, b.account)
	}

	// Only the SECOND wallet shows up in the pool's index. One positive answer
	// is enough: the row must be repaired, and the total must include it.
	indexed[canonicalStakeAddr(b.wallets[1])] = true
	resolved, _ := b.store.RunStakeRechecks(b.now.Add(time.Minute))
	if resolved != 1 {
		t.Fatalf("re-check resolved = %d, want 1 — the repair could not find the account behind the linked wallets", resolved)
	}

	rows, _ := b.store.ListCashbacksByUser(b.user.ID, 0)
	if len(rows) != 1 {
		t.Fatalf("want one cashback row, got %d", len(rows))
	}
	got := rows[0]
	if got.Status != CashbackQueued {
		t.Errorf("status = %q, want queued (skip reason %q)", got.Status, got.SkipReason)
	}
	if got.Bps <= 0 {
		t.Errorf("bps = %d, want the staker rate after the repair", got.Bps)
	}
	if got.AmountLuna < row.AmountLuna {
		t.Errorf("amount = %d, want at least the pre-repair %d", got.AmountLuna, row.AmountLuna)
	}
	if got.AmountLuna <= 0 {
		t.Errorf("amount = %d, want > 0", got.AmountLuna)
	}
	// Revived AND payable — to the account wallet, not a linked one.
	if canonicalStakeAddr(got.Recipient) != canonicalStakeAddr(b.account) {
		t.Errorf("recipient = %q, want the account wallet %q", got.Recipient, b.account)
	}
	if remaining, _ := b.store.ListStakeRechecks(); len(remaining) != 0 {
		t.Errorf("the resolved re-check was not cleared: %d left", len(remaining))
	}
}
