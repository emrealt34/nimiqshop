package db

import (
	"time"

	"nimiqshop/internal/money"
)

// DailyUsage summarizes a user's non-failed purchases within a rolling window,
// used to enforce the per-account daily order + spend limits.
type DailyUsage struct {
	OrderCount int
	SpendUSD   money.Micros
	OldestAt   time.Time // creation time of the oldest in-window purchase (zero if none)
}

// GetUserDailyUsage sums the user's purchases (orders + direct-NIM quotes) that
// fall inside `window` and are not failed/refunded/expired. Failed purchases do
// not count against the limit. OldestAt drives the "resets in …" countdown.
func (s *Store) GetUserDailyUsage(userID string, window time.Duration) (DailyUsage, error) {
	since := time.Now().Add(-window)
	var u DailyUsage

	if orders, err := s.ListOrders(userID, 200); err == nil {
		for _, o := range orders {
			if o.CreatedAt.Before(since) || o.Status == "failed" || o.Status == "refunded" {
				continue
			}
			u.OrderCount++
			u.SpendUSD += o.PriceUSD
			if u.OldestAt.IsZero() || o.CreatedAt.Before(u.OldestAt) {
				u.OldestAt = o.CreatedAt
			}
		}
	}

	if quotes, err := s.ListQuotesForUser(userID, 200); err == nil {
		for _, q := range quotes {
			if q.CreatedAt.Before(since) || q.Status == "failed" || q.Status == "expired" {
				continue
			}
			u.OrderCount++
			u.SpendUSD += q.ProductUSD
			if u.OldestAt.IsZero() || q.CreatedAt.Before(u.OldestAt) {
				u.OldestAt = q.CreatedAt
			}
		}
	}

	return u, nil
}
