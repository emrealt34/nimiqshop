// Package stakeledger is the pure computation engine of the "single ledger"
// (Tek Defter) staker cashback model. It has no I/O and no settings access:
// every decision is a deterministic function of (ledger state, pool-fee
// batch, NIM price, params, now), so the whole money model is testable in
// isolation and the persistence layer cannot drift from the math.
//
// Model in one sentence: the stake's realized pool fee credits k*g(d) of
// itself to a per-user NIM ledger (A); the buyer's cashback boost is always
// derived from A, and every boost payment is subtracted from A — so the
// shop can never pay out more than it has actually earned.
//
// Invariant (the "we never lose" proof, per user):
//
//	sum(paid boost NIM)  ≤  sum(credited NIM)  ≤  k * sum(pool-fee NIM)
//
// credited ≤ k*fee because Accrue multiplies by k ≤ 1;
// paid ≤ credited because Purchase can only debit A, and A only ever grows
// via Accrue (stake decreases shrink A, the month-end haircut shrinks A).
// The ledger is denominated in NIM, so the bound holds regardless of the
// NIM/USD price at purchase time.
package stakeledger

import (
	"math"
	"time"
)

// Params are the operator-tunable programme parameters with safe defaults.
// Zero/absent values are replaced by Defaults via Normalize — an admin
// typo can loosen the programme but never disable its safety rails.
type Params struct {
	// K is the fraction of each realized pool-fee NIM credited to the
	// ledger (0, 1]. The remainder is the shop's structural margin.
	K float64 `json:"k"`
	// Q is the fraction of a month's UNACCOUNTED-AS-USED accrual that
	// carries over at month end ([0, 1]). (1-Q) of the unused portion is
	// hair-cut back to the reserve.
	Q float64 `json:"q"`
	// G0 is the loyalty multiplier at d=0 (fresh stake), [0, 1].
	G0 float64 `json:"g0"`
	// TDays is how many days of loyalty age reach the full g=1.
	TDays int `json:"t_days"`
	// MinStakeNIM is the minimum active stake (NIM) for accrual at all.
	MinStakeNIM float64 `json:"min_stake_nim"`
	// MaxBoostBps hard-caps the boost rate (basis points; 1000 = 10%).
	MaxBoostBps int `json:"max_boost_bps"`
	// AMaxUSD is the ledger cap in USD terms; converted to NIM at the live
	// price on every accrual (A_MAX = AMaxUSD / P).
	AMaxUSD float64 `json:"a_max_usd"`
	// DailyCapUSD / MonthlyCapUSD bound how much of one day / one calendar
	// month of a buyer's spend is "cashback-eligible" for the boost.
	DailyCapUSD   float64 `json:"daily_cap_usd"`
	MonthlyCapUSD float64 `json:"monthly_cap_usd"`
	// DisplayBasisUSD is the rate display basis ("the next $100"). The
	// boost rate is min(MaxBoostBps, A*P/DisplayBasisUSD*10000); a
	// purchase pays that rate on its eligible amount only.
	DisplayBasisUSD float64 `json:"display_basis_usd"`
}

// Defaults is the published programme: 80% of the realized fee is credited,
// 10% of the unused month-end balance carries over, loyalty ramps from x0.5
// to x1.0 over 1825 days (5 years), entry at 100 NIM, boost capped at 10% of the next
// $100, ledger capped at $10, spend caps $500/day and $1000/month
	// (owner, 2026-10-05: "günlük harcama limiti 500 dolar, aylık 1000").
var Defaults = Params{
	K:               0.8,
	Q:               0.1,
	G0:              0.5,
	TDays:           1825,
	MinStakeNIM:     100,
	MaxBoostBps:     1000,
	AMaxUSD:         10,
	DailyCapUSD:     500,
	MonthlyCapUSD:   1000,
	DisplayBasisUSD: 100,
}

// Normalize clamps an operator-supplied parameter set into the safe
// envelope. It never fails: an unusable input falls back to the default
// for that field, and the safety rails (K ≤ 1, caps positive, boost cap
// bounded) can never be switched off.
func Normalize(p Params) Params {
	if p.K <= 0 {
		p.K = Defaults.K
	}
	if p.K > 1 {
		p.K = 1
	}
	// Month-end carry is a fixed programme rule, not an admin-tunable
	// loophole: exactly 10% of the remaining balance survives the boundary.
	p.Q = Defaults.Q
	if p.G0 < 0 {
		p.G0 = 0
	}
	if p.G0 > 1 {
		p.G0 = 1
	}
	if p.TDays < 1 {
		p.TDays = Defaults.TDays
	}
	if p.TDays > 3650 {
		p.TDays = 3650
	}
	if p.MinStakeNIM <= 0 {
		p.MinStakeNIM = Defaults.MinStakeNIM
	}
	if p.MaxBoostBps <= 0 {
		p.MaxBoostBps = Defaults.MaxBoostBps
	}
	// Even a fat-fingered admin can promise at most 20%: more would turn
	// the boost into a payment rail nobody has funded.
	// The staker boost is a hard maximum of 10%. Admin settings may lower
	// it, but can never advertise or pay more than the published ceiling.
	if p.MaxBoostBps > 1000 {
		p.MaxBoostBps = 1000
	}
	if p.AMaxUSD <= 0 {
		p.AMaxUSD = Defaults.AMaxUSD
	}
	if p.DailyCapUSD <= 0 {
		p.DailyCapUSD = Defaults.DailyCapUSD
	}
	if p.MonthlyCapUSD <= 0 {
		p.MonthlyCapUSD = Defaults.MonthlyCapUSD
	}
	// The day cap can never exceed the month cap: a day is inside a month.
	if p.DailyCapUSD > p.MonthlyCapUSD {
		p.DailyCapUSD = p.MonthlyCapUSD
	}
	if p.DisplayBasisUSD <= 0 {
		p.DisplayBasisUSD = Defaults.DisplayBasisUSD
	}
	if p.DisplayBasisUSD > 1000 {
		p.DisplayBasisUSD = 1000
	}
	return p
}

// G is the loyalty multiplier: g(d) = min(1, g0 + (1-g0) * d / T).
// d=0 → G0, d≥T → 1, linear in between. Negative d (clock skew) is 0-aged.
func G(d float64, p Params) float64 {
	if d <= 0 {
		return p.G0
	}
	if d >= float64(p.TDays) {
		return 1
	}
	return p.G0 + (1-p.G0)*d/float64(p.TDays)
}

// AMaxNIM converts the USD ledger cap to NIM at nimUsd. Returns 0 when the
// price is unusable — callers then keep the previous cap (never uncapped by
// accident of a bad price).
func AMaxNIM(nimUsd float64, p Params) float64 {
	if nimUsd <= 0 {
		return 0
	}
	return p.AMaxUSD / nimUsd
}

// BoostBps is the current boost rate in basis points on the display basis:
// min(MaxBoostBps, A * P / basis * 10000). Note the price cancels in the
// actual payout (rate * x / P = A * x / basis), so a manipulated price
// cannot inflate the NIM paid — it only moves the display.
func BoostBps(l *Ledger, nimUsd float64, p Params) int {
	if l == nil || l.A <= 0 || nimUsd <= 0 {
		return 0
	}
	bps := l.A * nimUsd / p.DisplayBasisUSD * 10000
	max := float64(p.MaxBoostBps)
	if bps > max {
		bps = max
	}
	if bps < 0.5 {
		return 0
	}
	return int(math.Round(bps))
}

// RemainingCaps reports the buyer's remaining boost-eligible spend
// (USD) for today and this calendar month as of now, after applying any
// pending day/month boundary.
func RemainingCaps(l *Ledger, now time.Time, p Params) (dayUSD, monthUSD float64) {
	dk, mk := dayKeyOf(now), monthKeyOf(now)
	if l.DayKey != dk {
		dayUSD = p.DailyCapUSD
	} else {
		dayUSD = p.DailyCapUSD - float64(l.HDay)/100
	}
	if l.MonthKey != mk {
		monthUSD = p.MonthlyCapUSD
	} else {
		monthUSD = p.MonthlyCapUSD - float64(l.HMonth)/100
	}
	if dayUSD < 0 {
		dayUSD = 0
	}
	if monthUSD < 0 {
		monthUSD = 0
	}
	return
}
