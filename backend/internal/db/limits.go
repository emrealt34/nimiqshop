package db

import (
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/money"
)

// countsAgainstDailyBudget reports whether a quote still occupies the buyer's
// rolling budget. It is the exact predicate CreateQuoteWithDailyLimits uses
// inside its transaction, kept in one place so the gate and any explanation of
// a refusal can never disagree.
func countsAgainstDailyBudget(q Quote, since time.Time) bool {
	if q.TestMode || q.CreatedAt.Before(since) {
		return false
	}
	switch q.Status {
	case "refunded", "failed", "expired", "manual_review", "order_creating", "awaiting_payment", "payment_started":
		// A quote that was opened but never paid is not a purchase and must
		// not consume the rolling order or spend limit.
		return false
	}
	return true
}

// DailyUsage summarizes a user's non-failed purchases within a rolling window,
// used to enforce the per-account daily order + spend limits.
type DailyUsage struct {
	OrderCount int
	SpendUSD   money.Micros
	OldestAt   time.Time // creation time of the oldest in-window purchase (zero if none)
}

// GetUserDailyUsage uses the exact same records and exclusions as the gate.
// Legacy orders are not written by checkout and must not be counted twice.
func (s *Store) GetUserDailyUsage(userID string, window time.Duration) (DailyUsage, error) {
	return s.GetUserDailyBudget(userID, time.Now().UTC().Add(-window))
}

// PurchaseMonthStart is shared by the atomic gate and all usage displays.
func PurchaseMonthStart(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// GetUserMonthlyUsage returns current UTC-calendar-month purchase spend.
// It is a purchase budget, NOT the staking cashback eligibility cap.
func (s *Store) GetUserMonthlyUsage(userID string, now time.Time) (DailyUsage, error) {
	return s.GetUserDailyBudget(userID, PurchaseMonthStart(now))
}

// GetUserDailyBudget reports the counters the purchase gate enforces: every
// quote inside the window that still occupies budget, plus the creation time of
// the oldest one (that is when the window next slides). Handlers use it to
// explain a refusal with the buyer's own numbers — used/left/reset — instead of
// a bare "limit reached".
//
// It deliberately counts QUOTES only, matching CreateQuoteWithDailyLimits.
// The daily/monthly profile counters use this same untruncated scan.
func (s *Store) GetUserDailyBudget(userID string, since time.Time) (DailyUsage, error) {
	var u DailyUsage
	// Bounded scan: the user index is ordered newest-first, so the iterator
	// seeks to the window edge and stops there instead of reading the
	// buyer's whole history. This runs on every checkout AND on every
	// profile/limits render, so the difference between O(window) and
	// O(history) is the difference between a flat latency curve and one that
	// grows with how long a customer has been shopping.
	err := s.View(func(txn *badger.Txn) error {
		return scanQuotesSince(txn, userID, since, func(q Quote) error {
			if !countsAgainstDailyBudget(q, since) {
				return nil
			}
			u.OrderCount++
			u.SpendUSD += q.ProductUSD
			if u.OldestAt.IsZero() || q.CreatedAt.Before(u.OldestAt) {
				u.OldestAt = q.CreatedAt
			}
			return nil
		})
	})
	return u, err
}

// DailyBudget is GetUserDailyBudget plus the configured ceilings and what is
// still available — the shape an error response needs, computed once.
type DailyBudget struct {
	DailyUsage
	// MaxOrders/MaxSpendUSD are 0 when that ceiling is disabled.
	MaxOrders   int
	MaxSpendUSD money.Micros
	// OrderLimitHit is true when the ORDER COUNT ceiling is what refused the
	// purchase; otherwise the SPEND ceiling did.
	OrderLimitHit bool
	// Remaining is what the buyer may still spend in this window (never
	// negative). RemainingOrders is -1 when the count ceiling is disabled.
	Remaining       money.Micros
	RemainingOrders int
	// ResetsAt is when the oldest in-window purchase drops off. With nothing
	// purchased it is a full window away, so a countdown always reads sensibly.
	ResetsAt time.Time
}

// DailyBudgetFor returns the buyer's live budget position: how much of the
// rolling 24h window is used, what is left, which ceiling is binding and when
// the window next slides.
func (s *Store) DailyBudgetFor(userID string, maxOrders int, maxSpendUSD money.Micros, now time.Time) (DailyBudget, error) {
	usage, err := s.GetUserDailyBudget(userID, now.Add(-24*time.Hour))
	b := DailyBudget{
		DailyUsage:      usage,
		MaxOrders:       maxOrders,
		MaxSpendUSD:     maxSpendUSD,
		RemainingOrders: -1,
		ResetsAt:        now.Add(24 * time.Hour),
	}
	if !usage.OldestAt.IsZero() {
		b.ResetsAt = usage.OldestAt.Add(24 * time.Hour)
	}
	if maxOrders > 0 {
		b.RemainingOrders = maxOrders - usage.OrderCount
		if b.RemainingOrders < 0 {
			b.RemainingOrders = 0
		}
	}
	if maxSpendUSD > 0 {
		b.Remaining = maxSpendUSD - usage.SpendUSD
		if b.Remaining < 0 {
			b.Remaining = 0
		}
	}
	b.OrderLimitHit = maxOrders > 0 && usage.OrderCount >= maxOrders
	return b, err
}
