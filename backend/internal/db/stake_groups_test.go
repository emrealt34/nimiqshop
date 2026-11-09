package db

import (
	"errors"
	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/stakeledger"
	"sync"
	"testing"
	"time"
)

func assertStakeMembers(t *testing.T, s *Store, address string, want ...string) User {
	t.Helper()
	u, e := s.GetUserByAddress(address)
	if e != nil {
		t.Fatal(e)
	}
	found := map[string]bool{}
	for _, a := range u.EarningStakeAddresses() {
		found[canonicalStakeAddr(a)] = true
	}
	if len(found) != len(want) {
		t.Fatalf("%s got group %v, want %v", address, found, want)
	}
	for _, a := range want {
		if !found[canonicalStakeAddr(a)] {
			t.Fatalf("%s missing %s from %v", address, a, found)
		}
	}
	if canonicalStakeAddr(u.NimiqAddress) != canonicalStakeAddr(address) {
		t.Fatal("payout identity changed")
	}
	return u
}

func TestStakeGroupTransitiveAndDetach(t *testing.T) {
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	a, b, c := stakeTestWallet(t, "group-a"), stakeTestWallet(t, "group-b"), stakeTestWallet(t, "group-c")
	ua, _ := s.FindOrCreateUserByAddress(a)
	ub, _ := s.FindOrCreateUserByAddress(b)
	now := time.Now().UTC()
	if _, e = s.AddStakeAddress(ua.ID, b, now); e != nil {
		t.Fatal(e)
	}
	assertStakeMembers(t, s, a, a, b)
	assertStakeMembers(t, s, b, a, b)
	if _, e = s.AddStakeAddress(ub.ID, c, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	for _, addr := range []string{a, b, c} {
		assertStakeMembers(t, s, addr, a, b, c)
	}
	// Login after linking returns the existing third account with the group.
	uc, e := s.FindOrCreateUserByAddress(c)
	if e != nil {
		t.Fatal(e)
	}
	if len(uc.EarningStakeAddresses()) != 3 {
		t.Fatal("login lost the group")
	}
	if _, e = s.RemoveStakeAddress(ua.ID, b); e != nil {
		t.Fatal(e)
	}
	assertStakeMembers(t, s, a, a, c)
	assertStakeMembers(t, s, c, a, c)
	assertStakeMembers(t, s, b, b)
	if _, e = s.AddStakeAddress(ub.ID, c, now.Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	for _, addr := range []string{a, b, c} {
		assertStakeMembers(t, s, addr, a, b, c)
	}
	if _, e = s.ClearStakeAddresses(ub.ID); e != nil {
		t.Fatal(e)
	}
	assertStakeMembers(t, s, b, b)
	assertStakeMembers(t, s, a, a, c)
	assertStakeMembers(t, s, c, a, c)
}

func TestStakeGroupBudgetMergeSplitAndRejoin(t *testing.T) {
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	now := time.Now().UTC()
	a, b := stakeTestWallet(t, "budget-group-a"), stakeTestWallet(t, "budget-group-b")
	ua, _ := s.FindOrCreateUserByAddress(a)
	ub, _ := s.FindOrCreateUserByAddress(b)
	charge := func(addr string, amount float64) {
		t.Helper()
		if e := s.Update(func(tx *badger.Txn) error {
			ss, e := sharedSpendInTx(tx, addr, now)
			if e != nil {
				return e
			}
			ss.Add(amount, now)
			return persistSharedSpendInTx(tx, addr, ss)
		}); e != nil {
			t.Fatal(e)
		}
	}
	check := func(addr string, cents int64) {
		t.Helper()
		ss, e := s.ReadSharedSpend(addr, now)
		if e != nil || ss.HDay != cents || ss.HMonth != cents {
			t.Fatalf("%s spend=%+v err=%v want=%d", addr, ss, e, cents)
		}
	}
	charge(a, 100)
	charge(b, 200)
	if _, e = s.AddStakeAddress(ua.ID, b, now); e != nil {
		t.Fatal(e)
	}
	check(a, 30000)
	check(b, 30000)
	charge(b, 50)
	check(a, 35000)
	check(b, 35000)
	if _, e = s.RemoveStakeAddress(ua.ID, b); e != nil {
		t.Fatal(e)
	}
	check(a, 35000)
	check(b, 35000)
	charge(a, 10)
	charge(b, 20)
	check(a, 36000)
	check(b, 37000)
	if _, e = s.AddStakeAddress(ub.ID, a, now); e != nil {
		t.Fatal(e)
	}
	check(a, 38000)
	check(b, 38000) // historical $350 counted once, not twice
	// UTC rollover is shared too.
	ss, e := s.ReadSharedSpend(a, now.Add(24*time.Hour))
	if e != nil || ss.HDay != 0 {
		t.Fatalf("rollover: %+v %v", ss, e)
	}
}

func TestStakeGroupLimitAndConcurrentLinks(t *testing.T) {
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	now := time.Now().UTC()
	var wallets []string
	var users []User
	for i := 0; i < 7; i++ {
		a := stakeTestWallet(t, "concurrent-"+string(rune('a'+i)))
		wallets = append(wallets, a)
		u, e := s.FindOrCreateUserByAddress(a)
		if e != nil {
			t.Fatal(e)
		}
		users = append(users, u)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, e := s.AddStakeAddress(users[0].ID, wallets[i], now); errs <- e }(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 3; i < 6; i++ {
		if _, e = s.AddStakeAddress(users[i-1].ID, wallets[i], now); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.AddStakeAddress(users[0].ID, wallets[6], now); !errors.Is(e, ErrTooManyStakeAddresses) {
		t.Fatalf("expected cap: %v", e)
	}
	for i := 0; i < 6; i++ {
		assertStakeMembers(t, s, wallets[i], wallets[:6]...)
	}
	assertStakeMembers(t, s, wallets[6], wallets[6])
}

func TestStakeGroupLegacyMigrationAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	a, b, c := stakeTestWallet(t, "migration-a"), stakeTestWallet(t, "migration-b"), stakeTestWallet(t, "migration-c")
	ua, _ := s.FindOrCreateUserByAddress(a)
	ub, _ := s.FindOrCreateUserByAddress(b)
	now := time.Now().UTC()
	old := stakeledger.NewSharedSpend(now)
	old.Add(90, now)
	e = s.Update(func(tx *badger.Txn) error {
		ua.StakeAddresses = []StakeAddressLink{{Address: b, LinkedAt: now.Add(-time.Hour)}}
		ub.StakeAddresses = []StakeAddressLink{{Address: c, LinkedAt: now}}
		for _, u := range []User{ua, ub} {
			raw, e := marshal(u)
			if e != nil {
				return e
			}
			if e = tx.Set(userKey(u.ID), raw); e != nil {
				return e
			}
		}
		raw, e := marshal(old)
		if e != nil {
			return e
		}
		if e = tx.Set(sharedSpendKey(a), raw); e != nil {
			return e
		}
		return tx.Delete([]byte(stakeGroupMigrationKey))
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = New(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, addr := range []string{a, b, c} {
		assertStakeMembers(t, s, addr, a, b, c)
		ss, e := s.ReadSharedSpend(addr, now)
		if e != nil || ss.HDay != 9000 {
			t.Fatalf("legacy budget %+v %v", ss, e)
		}
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	for _, addr := range []string{a, b, c} {
		assertStakeMembers(t, s, addr, a, b, c)
		ss, e := s.ReadSharedSpend(addr, now)
		if e != nil || ss.HDay != 9000 {
			t.Fatalf("restart budget %+v %v", ss, e)
		}
	}
}

func TestStakeGroupProofCannotBeReplayedAfterUnlink(t *testing.T) {
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	now := time.Now().UTC()
	a, b := stakeTestWallet(t, "proof-a"), stakeTestWallet(t, "proof-b")
	ua, _ := s.FindOrCreateUserByAddress(a)
	if _, e = s.AddStakeAddressWithProof(ua.ID, b, now, "proof-unique"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RemoveStakeAddress(ua.ID, b); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AddStakeAddressWithProof(ua.ID, b, now, "proof-unique"); !errors.Is(e, ErrStakeLinkProofUsed) {
		t.Fatalf("replayed proof accepted: %v", e)
	}
	assertStakeMembers(t, s, a, a)
	assertStakeMembers(t, s, b, b)
	if _, e = s.AddStakeAddressWithProof(ua.ID, b, now, "proof-new"); e != nil {
		t.Fatal(e)
	}
	assertStakeMembers(t, s, a, a, b)
}

func TestStakeGroupRejectsOverLimitGroupMergeWithoutPartialChanges(t *testing.T) {
	s, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	now := time.Now().UTC()
	wallets := make([]string, 7)
	users := make([]User, 7)
	for i := range wallets {
		wallets[i] = stakeTestWallet(t, "merge-cap-"+string(rune('a'+i)))
		users[i], e = s.FindOrCreateUserByAddress(wallets[i])
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, i := range []int{1, 2} {
		if _, e = s.AddStakeAddress(users[0].ID, wallets[i], now); e != nil {
			t.Fatal(e)
		}
	}
	for _, i := range []int{4, 5, 6} {
		if _, e = s.AddStakeAddress(users[3].ID, wallets[i], now); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.AddStakeAddress(users[0].ID, wallets[3], now); !errors.Is(e, ErrTooManyStakeAddresses) {
		t.Fatalf("merge must fail: %v", e)
	}
	for _, i := range []int{0, 1, 2} {
		assertStakeMembers(t, s, wallets[i], wallets[0:3]...)
	}
	for _, i := range []int{3, 4, 5, 6} {
		assertStakeMembers(t, s, wallets[i], wallets[3:7]...)
	}
}
