package stakeledger

import (
	"math"
	"time"
)

// dayKey / monthKey format: the ledger's spend counters reset on UTC
// calendar boundaries (a crash over midnight is handled lazily: whenever a
// mutation sees a stale key it resets the counter first — no cron race, no
// double reset, and a month boundary can never be missed because every
// write path and the periodic sweep both call the same rollover).
const (
	dayKeyFmt   = "2006-01-02"
	monthKeyFmt = "2006-01"
)

func dayKeyOf(t time.Time) string   { return t.UTC().Format(dayKeyFmt) }
func monthKeyOf(t time.Time) string { return t.UTC().Format(monthKeyFmt) }

// Ledger is one buyer's single cashback book. It is deliberately flat and
// JSON-friendly: the persistence layer stores exactly this struct.
//
//	S         current observed stake (NIM) — the input of the accrual rules
//	A         cashback ledger balance (NIM) — the ONLY thing boost pays from
//	D         stake-weighted loyalty age (days)
//	AccM      NIM credited to A during the current calendar month (haircut)
//	HDay/HMonth  USD cents of cashback-eligible spend today / this month
//	LastBatch last pool-fee batch applied to this ledger (idempotency)
type Ledger struct {
	Address   string  `json:"address"`
	S         float64 `json:"s_nim"`
	A         float64 `json:"a_nim"`
	D         float64 `json:"d_days"`
	AccM      float64 `json:"acc_m_nim"`
	HDay      int64   `json:"h_day_usd_cents"`
	DayKey    string  `json:"day_key"`
	HMonth    int64   `json:"h_month_usd_cents"`
	MonthKey  string  `json:"month_key"`
	LastBatch int64   `json:"last_batch,omitempty"` // legacy feed migration only
	// ProfitMonth/ProfitLuna are the last cumulative pool-profit snapshot
	// consumed for this address. The pool returns a cumulative calendar total;
	// crediting only the positive delta makes on-demand refreshes idempotent.
	ProfitMonth string `json:"profit_month,omitempty"`
	ProfitLuna  int64  `json:"profit_luna,omitempty"`
	// Loyalty is authoritative metadata returned by NimiqBase. D is retained
	// as the wire/storage field but is never advanced or derived by the shop.
	LoyaltyMultiplier float64   `json:"loyalty_multiplier,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// New creates a fresh ledger for address with an initial stake observation.
// A starts empty: no fee has been realized yet, so no boost exists yet.
func New(address string, stakeNIM float64, now time.Time) *Ledger {
	if stakeNIM < 0 {
		stakeNIM = 0
	}
	now = now.UTC()
	return &Ledger{
		Address:   address,
		S:         stakeNIM,
		D:         0,
		DayKey:    dayKeyOf(now),
		MonthKey:  monthKeyOf(now),
		UpdatedAt: now,
	}
}

// advance moves the loyalty clock forward by the wall time since the last
// mutation. A backwards clock (NTP step) never ages the ledger negatively.
func (l *Ledger) advance(now time.Time) {
	// Loyalty is authoritative in NimiqBase and arrives with the profit
	// response. The shop must never age it from its own wall clock.
	_ = now
}

// Rollover applies any pending UTC day/month boundary (month end also does
// the haircut on this month's unused accrual). Returns true when a counter
// actually changed. Safe to call on every mutation and from the sweep.
func (l *Ledger) Rollover(p Params, now time.Time) bool {
	l.advance(now)
	changed := false
	if l.MonthKey != monthKeyOf(now) {
		monthEnd(l, p)
		l.MonthKey = monthKeyOf(now)
		changed = true
	}
	if l.DayKey != dayKeyOf(now) {
		l.HDay = 0
		l.DayKey = dayKeyOf(now)
		changed = true
	}
	l.UpdatedAt = now
	return changed
}

// monthEnd applies the fixed carry rule to what is still unused at the UTC
// month boundary. Purchases have already debited A, therefore A itself is the
// remaining/unused balance: exactly q (published as 10%) survives and 90%
// returns to the reserve. Spend history cannot increase the carry percentage.
func monthEnd(l *Ledger, p Params) {
	l.A *= p.Q
	if l.A < 0 {
		l.A = 0
	}
	l.HMonth = 0
	l.AccM = 0
}

// Accrue is spec item 5, fed by REALIZED pool fees instead of an APY guess:
// feeNIM is the shop's pool-fee share attributed to this stake for one
// settled batch. The ledger gains k * g(d) * feeNIM, capped at A_MAX(P).
// Returns the NIM actually written (0 below the program minimum or when the
// cap is already saturated). now advances the loyalty clock.
func (l *Ledger) Accrue(feeNIM, nimUsd float64, p Params, now time.Time) float64 {
	l.Rollover(p, now)
	if feeNIM <= 0 || l.S < p.MinStakeNIM {
		return 0
	}
	credited := p.K * G(l.D, p) * feeNIM
	if credited <= 0 {
		l.UpdatedAt = now
		return 0
	}
	written := credited
	if cap := AMaxNIM(nimUsd, p); cap > 0 && l.A >= cap {
		return 0 // already saturated at the cap — nothing to write
	} else if cap > 0 && l.A+credited > cap {
		written = cap - l.A
	}
	l.A += written
	l.AccM += written
	l.UpdatedAt = now
	return written
}

// AccrueAuthoritative credits a realized fee using the loyalty multiplier
// supplied by NimiqBase. The shop deliberately does not derive stake age.
func (l *Ledger) AccrueAuthoritative(feeNIM, nimUsd, multiplier float64, p Params, now time.Time) float64 {
	l.Rollover(p, now)
	if feeNIM <= 0 || multiplier <= 0 {
		return 0
	}
	if multiplier > 1 {
		multiplier = 1
	}
	credited := p.K * multiplier * feeNIM
	written := credited
	if cap := AMaxNIM(nimUsd, p); cap > 0 && l.A >= cap {
		return 0
	} else if cap > 0 && l.A+written > cap {
		written = cap - l.A
	}
	if written <= 0 {
		return 0
	}
	l.A += written
	l.AccM += written
	l.UpdatedAt = now
	return written
}

// OnStakeChange is spec items 6-7 for an observed stake change:
//
//	increase:  d = S_old*d / S_new   (new NIM is age 0 — loyalty dilutes)
//	decrease:  A = A * S_new/S_old   (the book shrinks pro rata; full
//	            withdrawal is r = 0 → A = 0, i.e. a complete reset that
//	            falls out of the same formula, no special case)
//	unchanged: nothing
//
// It never credits: observing stake moves the clock, not the balance.
func (l *Ledger) OnStakeChange(newStakeNIM float64, p Params, now time.Time) {
	l.Rollover(p, now)
	if newStakeNIM < 0 {
		newStakeNIM = 0
	}
	if l.S > 0 {
		switch {
		case newStakeNIM > l.S:
			l.D = l.S * l.D / newStakeNIM
		case newStakeNIM < l.S:
			l.A *= newStakeNIM / l.S
		}
	}
	l.S = newStakeNIM
	l.UpdatedAt = now
}

// Purchase is spec item 8: the boost for a purchase of xUSD.
//
//	eligible = min(x, dayRemaining, monthRemaining)
//	rate     = min(MaxBoostBps, A * P / basis)
//	cbNIM    = min(A, rate * eligible / P)
//
// and debits exactly cbNIM from A plus eligible from the day/month
// counters. Because rate is priced on the display basis (100 USD) while
// eligible is capped below it (day cap 50 USD), cbNIM ≤ A * eligible/100 <
// A always holds even before the explicit clamp — the ledger cannot go
// negative, and spending can never lower the ratio for the NEXT purchase:
// A/(remaining) is invariant under a purchase (debt and debt-base shrink
// by the same factor).
//
// Returns the eligible USD and the NIM debited (the exact payout in NIM).
func (l *Ledger) Purchase(xUSD, nimUsd float64, p Params, now time.Time) (eligible, cbNIM float64) {
	l.Rollover(p, now)
	if xUSD <= 0 || nimUsd <= 0 || l.A <= 0 {
		return 0, 0
	}
	dayRem := p.DailyCapUSD - float64(l.HDay)/100
	monthRem := p.MonthlyCapUSD - float64(l.HMonth)/100
	if dayRem < 0 {
		dayRem = 0
	}
	if monthRem < 0 {
		monthRem = 0
	}
	eligible = math.Min(xUSD, math.Min(dayRem, monthRem))
	if eligible <= 0 {
		l.UpdatedAt = now
		return 0, 0
	}
	rate := l.A * nimUsd / p.DisplayBasisUSD // fraction, before the cap
	capFrac := float64(p.MaxBoostBps) / 10000
	if rate > capFrac {
		rate = capFrac
	}
	cbNIM = rate * eligible / nimUsd
	if cbNIM > l.A {
		cbNIM = l.A // belt-and-braces: the math above already forbids it
	}
	if cbNIM < 0 {
		cbNIM = 0
	}
	l.A -= cbNIM
	l.HDay += int64(math.Round(eligible * 100))
	l.HMonth += int64(math.Round(eligible * 100))
	l.UpdatedAt = now
	return eligible, cbNIM
}

// Reset is the operator's "start over" for a buyer (fraud, chargeback,
// goodwill): the balance, the loyalty age and the month's accrual are
// wiped. The observed stake S and the spend counters survive — the buyer
// simply earns from zero again, like a fresh wallet.
func (l *Ledger) Reset(p Params, now time.Time) {
	l.Rollover(p, now)
	l.A = 0
	l.D = 0
	l.AccM = 0
	l.UpdatedAt = now
}
