package db

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/stakeledger"
)

func TestProfitSnapshotNeverCreditsRegressionsOrReplayedMonths(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	apply := func(month string, fee int64) float64 {
		t.Helper()
		got, err := s.ApplyProfitSnapshot("NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD", month, fee, .01, 0, 1, 100000000, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := apply("2026-09", 1000000); math.Abs(got-8) > 1e-9 {
		t.Fatalf("first credit %v", got)
	}
	for _, value := range []int64{1000000, 500000, 1000000} {
		if got := apply("2026-09", value); got != 0 {
			t.Fatalf("replayed fee credited %v", got)
		}
	}
	if got := apply("2026-10", 1000000); math.Abs(got-8) > 1e-9 {
		t.Fatalf("new month %v", got)
	}
	if got := apply("2026-09", 1000000); got != 0 {
		t.Fatalf("old month replay %v", got)
	}
	if got := apply("2026-09", 1100000); math.Abs(got-.8) > 1e-9 {
		t.Fatalf("late fee delta %v", got)
	}
	rows, listErr := s.ListStakeLedgers(100)
	if listErr != nil || len(rows) != 1 {
		t.Fatalf("profit watermarks polluted ledger listing: %v %v", len(rows), listErr)
	}
	l, ok, err := s.ReadStakeLedger("NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD", now)
	if err != nil || !ok || l.ProfitMonth != "2026-10" {
		t.Fatalf("latest snapshot changed: %+v %v", l, err)
	}
}

func TestProfitSnapshotsAreIdempotentAcrossRacesAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	address := "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.ApplyProfitSnapshot(address, "2026-10", 1000000, .01, 0, 1, 100000000, now)
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	// Read the STORED ledger, not ReadStakeLedger: that accessor deliberately
	// applies the pending month-end carry rule against the WALL CLOCK
	// (l.A *= Q, i.e. the 10% that survives into the next month) without
	// persisting it. The snapshots above are dated 2026-10, so the moment the
	// real calendar moved into November every read of this fixture came back
	// with A = 8 * 0.1 = 0.8 and this assertion failed — the shop's on-disk
	// balance was correct all along. Pinning the assertion to the raw record
	// keeps it about the thing under test (no duplicate credit) instead of
	// about what month the CI machine happens to be in.
	l := readStoredLedger(t, s, address)
	if math.Abs(l.A-8) > 1e-9 {
		t.Fatalf("duplicate credit %v", l.A)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	if got, e := s.ApplyProfitSnapshot(address, "2026-10", 1000000, .01, 0, 1, 100000000, now); e != nil || got != 0 {
		t.Fatalf("restart replay %v %v", got, e)
	}
}

func TestNoRealizedFeeMeansNoBoostCredit(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	addr := "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"
	got, err := s.ApplyProfitSnapshot(addr, "2026-10", 0, .01, 1825, 1, 100000000, now)
	if err != nil || got != 0 {
		t.Fatalf("zero fee %v %v", got, err)
	}
	l, _, _ := s.ReadStakeLedger(addr, now)
	if l.A != 0 {
		t.Fatalf("unearned boost balance %v", l.A)
	}
	for _, multiplier := range []float64{math.NaN(), math.Inf(1), -1, 2} {
		if _, e := s.ApplyProfitSnapshot(addr, "2026-10", 1000000, .01, 1825, multiplier, 100000000, now); e == nil {
			t.Fatalf("accepted multiplier %v", multiplier)
		}
	}
}

// readStoredLedger returns the persisted ledger exactly as written, with no
// wall-clock rollover applied — the state the next writer will build on.
func readStoredLedger(t *testing.T, s *Store, address string) stakeledger.Ledger {
	t.Helper()
	var l stakeledger.Ledger
	if err := s.View(func(tx *badger.Txn) error {
		return getJSON(tx, stakeLedgerKey(canonicalStakeAddr(address)), &l)
	}); err != nil {
		t.Fatalf("read stored ledger: %v", err)
	}
	return l
}
