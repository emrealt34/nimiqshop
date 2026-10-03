package catalog

import (
	"errors"
	"math"
	"testing"
	"time"
)

// liveRates builds a payload big enough to pass the minimum-size guard.
func liveRates(pairs map[string]float64) map[string]float64 {
	out := map[string]float64{"USD": 1}
	// Fill with plausible filler so the >=50 guard is satisfied.
	filler := []string{
		"AED", "AFN", "ALL", "AMD", "ANG", "AOA", "ARS", "AUD", "AWG", "AZN",
		"BAM", "BBD", "BDT", "BGN", "BHD", "BIF", "BMD", "BND", "BOB", "BRL",
		"BSD", "BTN", "BWP", "BYN", "BZD", "CAD", "CDF", "CHF", "CLP", "CNY",
		"COP", "CRC", "CUP", "CVE", "CZK", "DJF", "DKK", "DOP", "DZD", "EGP",
		"ERN", "ETB", "EUR", "FJD", "FKP", "GBP", "GEL", "GHS", "GIP", "GMD",
		"GNF", "GTQ", "GYD", "HKD", "HNL",
	}
	for _, c := range filler {
		if _, ok := pairs[c]; !ok {
			out[c] = 1.5
		}
	}
	for k, v := range pairs {
		out[k] = v
	}
	return out
}

func resetLiveFX(t *testing.T) {
	t.Helper()
	prev := fxLive.Load()
	t.Cleanup(func() { fxLive.Store(prev) })
}

func TestSetLiveFXShadowsBaseline(t *testing.T) {
	resetLiveFX(t)

	// Baseline is the embedded table: TRY is whatever fx.go ships.
	baseline, ok := UsdPerUnit("TRY")
	if !ok {
		t.Fatal("TRY missing from the embedded baseline")
	}

	// A live snapshot with a wildly different TRY (as happened in Oct 2026:
	// the curated 0.029 vs the real 0.02035) must take over every reader.
	live := 0.02035214
	if n := SetLiveFX(liveRates(map[string]float64{"TRY": live}), "test", time.Now().UTC()); n < 50 {
		t.Fatalf("expected the snapshot to be accepted, got %d currencies", n)
	}
	got, ok := UsdPerUnit("TRY")
	if !ok || math.Abs(got-live) > 1e-12 {
		t.Fatalf("UsdPerUnit(TRY) = %v (ok=%v), want %v", got, ok, live)
	}
	if v := ToUSD(100, "TRY"); math.Abs(v-100*live) > 1e-9 {
		t.Fatalf("ToUSD(100, TRY) = %v, want %v", v, 100*live)
	}
	if got := FXTable()["TRY"]; math.Abs(got-live) > 1e-12 {
		t.Fatalf("FXTable()[TRY] = %v, want %v (live must win over embedded)", got, live)
	}
	if got := FXTable()["EUR"]; math.Abs(got-1.5) > 1e-12 {
		t.Fatalf("FXTable()[EUR] = %v, want the live value 1.5", got)
	}
	// A currency only the baseline knows must survive the merge.
	if _, ok := FXTable()["JPY"]; !ok {
		t.Fatal("baseline-only currency dropped from the merged table")
	}
	_ = baseline
}

func TestSetLiveFXRejectsGarbage(t *testing.T) {
	resetLiveFX(t)

	// Establish a good snapshot first: rejections must never clear it.
	good := liveRates(map[string]float64{"TRY": 0.02})
	if n := SetLiveFX(good, "test", time.Now().UTC()); n == 0 {
		t.Fatal("setup snapshot rejected")
	}

	bad := []struct {
		name  string
		rates map[string]float64
	}{
		{"missing USD", map[string]float64{"TRY": 0.02}},
		{"USD not 1", map[string]float64{"USD": 7, "TRY": 0.02}},
		{"too few currencies", map[string]float64{"USD": 1, "TRY": 0.02}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if n := SetLiveFX(tc.rates, "test", time.Now().UTC()); n != 0 {
				t.Fatalf("payload was accepted (%d currencies); want rejection", n)
			}
			if v, _ := UsdPerUnit("TRY"); math.Abs(v-0.02) > 1e-12 {
				t.Fatalf("rejection wiped the good snapshot: TRY = %v", v)
			}
		})
	}
}

func TestSetLiveFXDropsBadEntries(t *testing.T) {
	resetLiveFX(t)
	good := liveRates(map[string]float64{"TRY": 0.02})
	if n := SetLiveFX(good, "test", time.Now().UTC()); n == 0 {
		t.Fatal("setup snapshot rejected")
	}
	for _, bad := range []struct {
		name string
		v    float64
	}{
		{"negative", -1},
		{"NaN", math.NaN()},
		{"absurdly large", 1e9},
		{"sub-atomic", 1e-30},
	} {
		if n := SetLiveFX(liveRates(map[string]float64{"TRY": bad.v}), "test", time.Now().UTC()); n == 0 {
			t.Fatalf("%s: payload with one bad entry must still be accepted", bad.name)
		}
		// The bad TRY is dropped, so the embedded baseline TRY shows again —
		// crucially the snapshot itself was not wiped.
		if at, _, count := LiveFXStatus(); at.IsZero() || count == 0 {
			t.Fatalf("%s: snapshot was cleared", bad.name)
		}
	}
	if err := func() error {
		_, ok := UsdPerUnit("TRY")
		if !ok {
			return errNoTRY
		}
		return nil
	}(); err != nil {
		t.Fatal(err)
	}
}

var errNoTRY = errors.New("TRY vanished from both live and baseline tables")

func TestSetLiveFXDropsOnlyBadEntries(t *testing.T) {
	resetLiveFX(t)

	rates := liveRates(map[string]float64{"TRY": 0.02})
	rates["BADCODE"] = 1.0     // not a 3-letter code
	rates["XXX"] = 0           // zero
	rates["YYY"] = math.Inf(1) // infinite
	n := SetLiveFX(rates, "test", time.Now().UTC())
	if n == 0 {
		t.Fatal("payload with a few bad entries must still be accepted")
	}
	if _, ok := UsdPerUnit("XXX"); ok {
		t.Fatal("zero rate survived the sanity filter")
	}
	if _, ok := UsdPerUnit("BADCODE"); ok {
		t.Fatal("non-3-letter code survived the sanity filter")
	}
	if _, ok := UsdPerUnit("YYY"); ok {
		t.Fatal("infinite rate survived the sanity filter")
	}
}

func TestLiveFXStatusReportsBaselineBeforeFirstFetch(t *testing.T) {
	resetLiveFX(t)
	fxLive.Store(nil)

	at, source, count := LiveFXStatus()
	if !at.IsZero() || source != "embedded" || count == 0 {
		t.Fatalf("empty state = (%v, %q, %d); want zero time, \"embedded\", >0", at, source, count)
	}
}
