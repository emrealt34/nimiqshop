package db

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestProfitSnapshotNeverCreditsRegressionsOrReplayedMonths(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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
	l, ok, err := s.ReadStakeLedger("NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD")
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
	l, _, err := s.ReadStakeLedger(address)
	if err != nil || math.Abs(l.A-8) > 1e-9 {
		t.Fatalf("duplicate credit %v %v", l.A, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, e := s.ApplyProfitSnapshot(address, "2026-10", 1000000, .01, 0, 1, 100000000, now); e != nil || got != 0 {
		t.Fatalf("restart replay %v %v", got, e)
	}
}

func TestNoRealizedFeeMeansNoBoostCredit(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	addr := "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"
	got, err := s.ApplyProfitSnapshot(addr, "2026-10", 0, .01, 1825, 1, 100000000, now)
	if err != nil || got != 0 {
		t.Fatalf("zero fee %v %v", got, err)
	}
	l, _, _ := s.ReadStakeLedger(addr)
	if l.A != 0 {
		t.Fatalf("unearned boost balance %v", l.A)
	}
	for _, multiplier := range []float64{math.NaN(), math.Inf(1), -1, 2} {
		if _, e := s.ApplyProfitSnapshot(addr, "2026-10", 1000000, .01, 1825, multiplier, 100000000, now); e == nil {
			t.Fatalf("accepted multiplier %v", multiplier)
		}
	}
}
