package db

import (
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

/* email_budget.go — the supplier's per-email Lightning budget, as far as
 * this shop can see it.
 *
 * The supplier counts every Lightning order an unregistered buyer places
 * against the DELIVERY EMAIL: a per-order ceiling, a daily budget and a
 * monthly budget on the order total, paid or not (live 2026-10-20: 100 EUR
 * per order and per day, 300 EUR per month; an unpaid 104 USD Walmart card
 * used up the day, and the next attempt with the same email was refused).
 *
 * The shop cannot keep that ledger itself — it does not see orders placed
 * elsewhere — but it knows the buyer's OWN orders. When the supplier
 * refuses, those are what turn "could not approve" into "your unpaid
 * Walmart order from 09:41 is holding today's budget: pay it instead of
 * opening another, or use another email".
 */

// EmailBudgetOrder is one of the buyer's own supplier orders that counts
// against the delivery email's budget.
type EmailBudgetOrder struct {
	QuoteID         string    `json:"quote_id"`
	ProductID       string    `json:"product_id"`
	Country         string    `json:"country,omitempty"`
	Denomination    string    `json:"denomination,omitempty"`
	ProductValue    float64   `json:"product_value,omitempty"`
	ProductCurrency string    `json:"product_currency,omitempty"`
	Quantity        int       `json:"quantity"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	// PaymentExpiry is when an unpaid invoice stops being payable; the
	// supplier keeps counting the order after that.
	PaymentExpiry time.Time `json:"payment_expiry,omitempty"`
	// TotalUSD is the supplier's order total (fee included) in USD, the
	// same number the supplier compares with its budgets.
	TotalUSD float64 `json:"total_usd"`
	// Pending is true while the invoice can still be paid: paying it is
	// the honest way to use the budget this order is already holding.
	Pending bool `json:"pending"`
}

// EmailBudgetOrders lists the buyer's own orders for one delivery email that
// the supplier counts against that email's budget since `since`: every
// quote that reached the supplier (it has a supplier order id), newest
// first. Test-centre purchases never reach the budget and are skipped.
func (s *Store) EmailBudgetOrders(userID, email string, since, now time.Time) ([]EmailBudgetOrder, error) {
	email = strings.TrimSpace(email)
	if userID == "" || email == "" {
		return nil, nil
	}
	var out []EmailBudgetOrder
	err := s.View(func(tx *badger.Txn) error {
		return scanQuotesSince(tx, userID, since, func(q Quote) error {
			if q.TestMode || q.SupplierOrderID == "" || !strings.EqualFold(strings.TrimSpace(q.CustomerEmail), email) {
				return nil
			}
			out = append(out, EmailBudgetOrder{
				QuoteID: q.ID, ProductID: q.ProductID, Country: q.ProductCountry,
				Denomination: q.Denomination, ProductValue: q.ProductValue, ProductCurrency: q.ProductCurrency,
				Quantity: q.Quantity, Status: q.Status, CreatedAt: q.CreatedAt, PaymentExpiry: q.PaymentExpiry,
				TotalUSD: q.ProductUSD.Float(), Pending: q.CanPay(now),
			})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	// scanQuotesSince visits newest first — the order that just used up the
	// budget is the one the buyer is looking for.
	return out, nil
}
