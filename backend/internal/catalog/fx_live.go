// Live FX rates — the automatic half of the curated table in fx.go.
//
// The embedded usdPerUnit table is only the BASELINE (a safe fallback for a
// cold start with no network). At runtime the authoritative source is the
// live snapshot installed by handlers.StartFXRefresher, which pulls the
// public, key-less open.er-api.com feed and swaps the whole map atomically.
//
// Why this exists: a curated table silently rots. By 2026-10-03 21 of its
// 155 entries had drifted >=15% from the market (TRY by 42%), which skewed
// the admin's USD price cap and the pre-quote display estimate. Nobody
// should have to edit Go code to keep a price cap honest.
//
// Charging is NOT affected by either table: real orders are priced from the
// supplier's own validated coin amount and the live oracle BTC rate (see
// quotedUSD in internal/handlers/quote_money.go). This only feeds the cap
// and the display estimate.
package catalog

import (
	"math"
	"strings"
	"sync/atomic"
	"time"
)

// fxSnapshot is one live FX payload. It is replaced wholesale, never
// mutated in place, so readers never observe a half-updated map.
type fxSnapshot struct {
	rates  map[string]float64
	source string
	at     time.Time
}

var fxLive atomic.Pointer[fxSnapshot]

// fxSanityReturn is the hard bound on an individual returned rate. Anything
// outside means the feed is broken or the currency redenominated; the entry
// is dropped rather than trusted.
const (
	fxRateMin = 1e-10
	fxRateMax = 100000.0
)

// SetLiveFX installs a live rate snapshot and returns how many currencies
// were accepted. It is deliberately conservative: malformed or absurd
// entries are dropped, USD is forced to exactly 1 (the feed's base), and a
// payload that yields no usable currency is rejected outright so a broken
// response can never blank out the table. The previous snapshot is left
// untouched on rejection.
func SetLiveFX(rates map[string]float64, source string, at time.Time) int {
	clean := make(map[string]float64, len(rates))
	for code, v := range rates {
		code = strings.ToUpper(strings.TrimSpace(code))
		if len(code) != 3 {
			continue
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < fxRateMin || v > fxRateMax {
			continue
		}
		clean[code] = v
	}
	// The feed is USD-based: a USD rate other than 1 is a broken payload.
	if usd, ok := clean["USD"]; !ok || math.Abs(usd-1) > 1e-9 {
		return 0
	}
	// A partial response (a handful of currencies) is a failure, not a
	// refresh — refuse it and keep serving the previous set.
	if len(clean) < 50 {
		return 0
	}
	fxLive.Store(&fxSnapshot{rates: clean, source: source, at: at})
	return len(clean)
}

// LiveFXStatus reports the current snapshot: when it was observed, where it
// came from, and how many currencies it carries. Zero time = still on the
// embedded baseline.
func LiveFXStatus() (at time.Time, source string, currencies int) {
	s := fxLive.Load()
	if s == nil {
		return time.Time{}, "embedded", len(usdPerUnit)
	}
	return s.at, s.source, len(s.rates)
}

// fxRate resolves a currency code against the live snapshot first and the
// embedded baseline second.
func fxRate(code string) (float64, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if s := fxLive.Load(); s != nil {
		if v, ok := s.rates[code]; ok {
			return v, true
		}
	}
	v, ok := usdPerUnit[code]
	return v, ok
}

// FXTable returns the effective USD-per-unit table (live snapshot merged
// over the embedded baseline) as a fresh copy — callers can never mutate
// the live set through it.
func FXTable() map[string]float64 {
	out := make(map[string]float64, len(usdPerUnit)+16)
	for k, v := range usdPerUnit {
		out[k] = v
	}
	if s := fxLive.Load(); s != nil {
		for k, v := range s.rates {
			out[k] = v
		}
	}
	return out
}
