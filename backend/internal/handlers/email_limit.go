package handlers

import (
	"errors"
	"strings"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

/* The supplier's per-email Lightning rule, explained with the buyer's own
 * orders.
 *
 * Live, 2026-10-20: a 104 USD Walmart card (97.70 EUR with the fee) was
 * created, left unpaid, and the very next attempt with the same delivery
 * email came back LIGHTNING_DAILY_LIMIT_EXCEEDED — a code the shop did not
 * know, so the buyer read "Cryptorefills could not approve this request.
 * Review the order and contact support". The rule is simple once stated:
 * per delivery email, 100 EUR per order, 100 EUR per day, 300 EUR per
 * month, fee included, and every order counts whether it was paid or not.
 *
 * The shop cannot keep the supplier's ledger (orders placed elsewhere are
 * invisible to it), but it does know THIS buyer's orders. A refusal on one
 * of the three ceilings therefore leaves here with a "shop" block inside
 * the problem's details: the buyer's orders for that email that count,
 * which of them can still be paid, and what they add up to — enough for
 * the storefront to say "your unpaid Walmart order is holding today's
 * budget: pay it instead of opening another, or use another email".
 */

// Period names the supplier's three Lightning ceilings.
const (
	lightningLimitPerOrder = "per_order"
	lightningLimitDaily    = "daily"
	lightningLimitMonthly  = "monthly"
)

// lightningLimitPeriod maps a supplier problem code to the ceiling it
// reports; "" for every other code.
func lightningLimitPeriod(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "LIGHTNING_ORDER_LIMIT_EXCEEDED":
		return lightningLimitPerOrder
	case "LIGHTNING_DAILY_LIMIT_EXCEEDED":
		return lightningLimitDaily
	case "LIGHTNING_MONTHLY_LIMIT_EXCEEDED":
		return lightningLimitMonthly
	}
	return ""
}

// emailBudgetView is the "shop" block attached to a per-email refusal.
type emailBudgetView struct {
	Email string `json:"email"`
	// Period is the ceiling the supplier named (per_order, daily, monthly).
	Period string `json:"period"`
	// Orders are the buyer's own supplier orders for this email in the
	// period (today / this month, UTC), newest first.
	Orders []db.EmailBudgetOrder `json:"orders"`
	// PendingCount is how many of them can still be paid.
	PendingCount int `json:"pending_count"`
	// CountedTotalUSD is what those orders add up to (fee included).
	CountedTotalUSD float64 `json:"counted_total_usd"`
}

// withEmailLimitContext returns err unchanged unless it is a supplier
// refusal on one of the per-email Lightning ceilings; then the matching
// problems carry the buyer's own orders for that email under
// moreDetails.shop. Nothing is fetched from the supplier.
func (h *Handlers) withEmailLimitContext(err error, userID, email string, now time.Time) error {
	if err == nil || h.Store == nil || strings.TrimSpace(email) == "" {
		return err
	}
	var pe *cryptorefills.ProblemError
	if errors.As(err, &pe) {
		for i := range pe.Problems {
			period := lightningLimitPeriod(pe.Problems[i].Code)
			if period == "" {
				continue
			}
			pe.Problems[i].Details = withShopBlock(pe.Problems[i].Details, h.emailBudget(userID, email, period, now))
		}
		return err
	}
	var se *cryptorefills.SupplierError
	if errors.As(err, &se) {
		if period := lightningLimitPeriod(se.Code); period != "" {
			se.MoreDetails = withShopBlock(se.MoreDetails, h.emailBudget(userID, email, period, now))
		}
	}
	return err
}

// emailBudget collects the buyer's orders for the email in the supplier's
// window: the calendar day for the daily ceiling, the calendar month for
// the monthly one (both UTC — the supplier's clock, not the buyer's), and
// today for the per-order ceiling, where the list only shows what else
// is open.
func (h *Handlers) emailBudget(userID, email, period string, now time.Time) *emailBudgetView {
	now = now.UTC()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if period == lightningLimitMonthly {
		since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	orders, err := h.Store.EmailBudgetOrders(userID, email, since, now)
	if err != nil {
		return nil
	}
	view := &emailBudgetView{Email: strings.TrimSpace(email), Period: period, Orders: orders}
	if view.Orders == nil {
		view.Orders = []db.EmailBudgetOrder{}
	}
	for _, o := range orders {
		view.CountedTotalUSD += o.TotalUSD
		if o.Pending {
			view.PendingCount++
		}
	}
	return view
}

// withShopBlock adds the shop's view to a problem's moreDetails without
// disturbing the supplier's own fields.
func withShopBlock(details any, view *emailBudgetView) any {
	if view == nil {
		return details
	}
	switch d := details.(type) {
	case map[string]any:
		out := make(map[string]any, len(d)+1)
		for k, v := range d {
			out[k] = v
		}
		out["shop"] = view
		return out
	case nil:
		return map[string]any{"shop": view}
	default:
		return map[string]any{"supplier": d, "shop": view}
	}
}
