package stakeledger

import (
	"math"
	"time"
)

/*
SharedSpend is ONE account's boost-eligible spend budget, shared across every
wallet whose book earns for it.

The books (Ledger) used to carry these counters per wallet, which meant a
buyer with three earning wallets had three separate $500/day + $1000/month
budgets — the account's total eligible spend scaled with the number of
wallets it linked. That was the multi-wallet loophole: link five wallets,
spend $2,500 a day at boost, "limits included".

The spend budget is therefore an ACCOUNT-level fact, keyed by the account's
canonical address (db layer), rolled over on the same UTC calendar boundaries
as the books, and consumed exactly once per eligible order — however many
books pay on it. It carries no money: only the calendar keys and the two
USD-cent counters. The books keep incrementing their own HDay/HMonth by the
same amount purely so per-book views (the admin console, the wallet row) keep
telling the truth about what was spent against them; those counters are
display, never enforcement.
*/
type SharedSpend struct {
	DayKey   string `json:"day_key"`
	MonthKey string `json:"month_key"`
	HDay     int64  `json:"h_day_usd_cents"`
	HMonth   int64  `json:"h_month_usd_cents"`
}

// NewSharedSpend starts a fresh budget at the current UTC day/month.
func NewSharedSpend(now time.Time) SharedSpend {
	now = now.UTC()
	return SharedSpend{DayKey: dayKeyOf(now), MonthKey: monthKeyOf(now)}
}

// Rollover applies any pending UTC day/month boundary in place and reports
// whether a counter was reset. Lazy, like the books: there is no cron race —
// every read/write path rolls over before looking at the counters.
func (s *SharedSpend) Rollover(now time.Time) bool {
	now = now.UTC()
	changed := false
	if s.MonthKey != monthKeyOf(now) {
		s.MonthKey = monthKeyOf(now)
		s.HMonth = 0
		changed = true
	}
	if s.DayKey != dayKeyOf(now) {
		s.DayKey = dayKeyOf(now)
		s.HDay = 0
		changed = true
	}
	return changed
}

// SharedRemaining reports the account's remaining boost-eligible spend for
// today and this calendar month, in USD, after applying any pending
// boundary. Negative leftovers are clamped to zero.
func SharedRemaining(s SharedSpend, now time.Time, p Params) (dayUSD, monthUSD float64) {
	s.Rollover(now)
	dayUSD = p.DailyCapUSD - float64(s.HDay)/100
	monthUSD = p.MonthlyCapUSD - float64(s.HMonth)/100
	if dayUSD < 0 {
		dayUSD = 0
	}
	if monthUSD < 0 {
		monthUSD = 0
	}
	return dayUSD, monthUSD
}

// SharedEligible is the single number the whole purchase flow spends against:
// how much of this order is boost-eligible given what the account already
// spent today and this month. Zero means the budget is exhausted — no book
// may pay a boost on this order, however empty its own counters are.
func SharedEligible(s SharedSpend, orderUSD float64, now time.Time, p Params) float64 {
	if orderUSD <= 0 || math.IsNaN(orderUSD) || math.IsInf(orderUSD, 0) {
		return 0
	}
	dayRem, monthRem := SharedRemaining(s, now, p)
	return math.Min(orderUSD, math.Min(dayRem, monthRem))
}

// Add charges the account's budget. Call once per eligible order that at
// least one book actually earned on — never once per book.
func (s *SharedSpend) Add(eligibleUSD float64, now time.Time) {
	if eligibleUSD <= 0 {
		return
	}
	s.Rollover(now)
	cents := int64(math.Round(eligibleUSD * 100))
	s.HDay += cents
	s.HMonth += cents
}

// PurchasePlan is the outcome of running one shared purchase across a set of
// books: what each book paid (NIM, float), what each book's bps contributed,
// and what happened to the account's shared budget. It is the ONLY purchase
// engine for multi-book (and single-book) earning sets; the fulfillment path
// writes it back inside the transaction, the preview path discards it, so
// what the buyer sees and what pays out cannot drift.
type PurchasePlan struct {
	// EligibleUSD is the amount each participating book paid on (the account's
	// remaining-budget bound), already debited from the account's budget when
	// SharedSpendChanged is true.
	EligibleUSD float64
	// PaidNIM and Bps are aligned with the books passed in.
	PaidNIM []float64
	Bps     []int
	// LunaSum is the whole-Luna total actually payable, after per-book dust
	// correction so the books never over-pay by rounding.
	LunaSum int64
	// BpsSum is the total the buyer sees (and that is capped by MaxBoostBps
	// across the set).
	BpsSum int
	// ChargedSharedBudget reports whether the account's shared budget was
	// consumed (at least one book earned) — meters and displays key off this.
	ChargedSharedBudget bool
	// SharedSpend is the account's budget AFTER rollover and after the charge.
	// Persist it whenever SharedSpendChanged is true (a boundary may have been
	// applied, an order may have charged it, or both).
	SharedSpend        SharedSpend
	SharedSpendChanged bool
}

// PlanPurchaseDebits computes (and applies, in memory) one order's boost
// across every earning book. It mutates the books in place: A is debited,
// HDay/HMonth are incremented by the shared eligible amount (display only),
// UpdatedAt is bumped. The caller writes the books back inside the
// transaction, or discards them for a read-only preview.
//
// Rules, in the order they bind:
//
//  1. the order's boost-eligible spend is ONE shared account budget
//     (SharedEligible): $500/day, $1000/month for the account, however many
//     wallets it linked;
//  2. per book, the rate is min(MaxBoostBps-attempted, A·P/basis), but the
//     MaxBoostBps pool is shared across the set too — the first books consume
//     the 1000 bps headroom, later books get what is left;
//  3. no book pays more than its own A holds.
func PlanPurchaseDebits(books []Ledger, spend SharedSpend, orderUSD, nimUsd float64, p Params, now time.Time) PurchasePlan {
	norm := Normalize(p)
	now = now.UTC()
	out := PurchasePlan{
		PaidNIM: make([]float64, len(books)),
		Bps:     make([]int, len(books)),
	}
	spendChanged := spend.Rollover(now)
	// Merge with the books' own counters before looking at the budget. Books
	// accumulated their own HDay/HMonth under the pre-upgrade per-book caps;
	// the account's true spend is the MERGE (max), so a buyer who already
	// spent against the boost today neither gets a silent refill nor double
	// charged. Books only ever increment by the same eligible amount under the
	// shared rule, so the merge is stable and idempotent on later calls.
	for i := range books {
		books[i].Rollover(norm, now)
		if books[i].HDay > spend.HDay {
			spend.HDay = books[i].HDay
			spendChanged = true
		}
		if books[i].HMonth > spend.HMonth {
			spend.HMonth = books[i].HMonth
			spendChanged = true
		}
	}
	out.SharedSpend = spend
	if orderUSD <= 0 || math.IsNaN(orderUSD) || math.IsInf(orderUSD, 0) || nimUsd <= 0 || math.IsNaN(nimUsd) || math.IsInf(nimUsd, 0) {
		out.SharedSpendChanged = spendChanged
		return out
	}
	eligible := SharedEligible(spend, orderUSD, now, norm)
	out.EligibleUSD = eligible
	if eligible <= 0 {
		out.SharedSpendChanged = spendChanged
		return out
	}
	remBps := norm.MaxBoostBps
	charged := false
	for i := range books {
		if remBps <= 0 {
			break
		}
		l := &books[i]
		if l.A <= 0 {
			continue
		}
		rate := l.A * nimUsd / norm.DisplayBasisUSD
		capFrac := float64(remBps) / 10000
		if rate > capFrac {
			rate = capFrac
		}
		bps := int(math.Round(rate * 10000))
		if bps <= 0 {
			continue
		}
		cbNIM := rate * eligible / nimUsd
		if cbNIM > l.A {
			cbNIM = l.A // belt-and-braces: the math above already forbids it
		}
		if cbNIM < 0 {
			cbNIM = 0
		}
		luna := int64(cbNIM*100_000.0 + 0.5)
		if luna < 1 {
			continue // the book was not charged; the buyer keeps the dust
		}
		// Round the debit to what is actually paid — under-pay by dust, never over.
		cpaid := float64(luna) / 100_000.0
		l.A -= cpaid
		if l.A < 0 {
			l.A = 0
		}
		l.HDay += int64(math.Round(eligible * 100))
		l.HMonth += int64(math.Round(eligible * 100))
		l.UpdatedAt = now
		out.PaidNIM[i] = cpaid
		out.Bps[i] = bps
		out.LunaSum += luna
		out.BpsSum += bps
		remBps -= bps
		charged = true
	}
	out.ChargedSharedBudget = charged
	if charged {
		out.SharedSpend.Add(eligible, now)
		spendChanged = true
	}
	out.SharedSpendChanged = spendChanged
	return out
}
