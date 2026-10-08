package settlement

import (
	"sync/atomic"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/safe"
)

// paidNotifyFnVal holds the payment-confirmed hook (wallet memo to the buyer).
var paidNotifyFnVal atomic.Value // func(db.Quote)

// SetPaidNotifyFn wires the payment-confirmed memo. Safe to call at any time.
func SetPaidNotifyFn(fn func(q db.Quote)) { paidNotifyFnVal.Store(fn) }

func paidNotifyFn() func(q db.Quote) {
	fn, _ := paidNotifyFnVal.Load().(func(q db.Quote))
	return fn
}

// paidStatus reports whether a supplier-side quote status means the customer's
// payment has been confirmed. A quote can jump straight from awaiting_payment
// to delivering or fulfilled when the supplier is fast, and payment is still
// complete at that point, so every paid-or-later status counts.
func paidStatus(s string) bool {
	switch s {
	case cryptorefills.QuotePaidReceived, cryptorefills.QuoteDelivering, cryptorefills.QuoteFulfilled:
		return true
	}
	return false
}

// shouldNotifyPaid is the whole trigger: the transition really happened
// (changed) and the supplier status is paid-or-later.
func shouldNotifyPaid(changed bool, supplierStatus string) bool {
	return changed && paidStatus(cryptorefills.MapToQuoteStatus(supplierStatus))
}

// ApplySupplierOrderNotify is Store.ApplySupplierOrder plus the payment-
// confirmed hook. The hook fires only on the transition that first reaches a
// paid status (changed == true), so a repeated poll or webhook cannot re-send
// it; the notifier's own per-order reference makes it at-most-once as well.
func ApplySupplierOrderNotify(s *db.Store, id string, order *cryptorefills.Order) (bool, error) {
	changed, err := s.ApplySupplierOrder(id, order)
	if err != nil || !changed {
		return changed, err
	}
	if !shouldNotifyPaid(changed, order.Status) {
		return changed, nil
	}
	if fn := paidNotifyFn(); fn != nil {
		if q, e := s.GetQuote(id); e == nil {
			qq := q
			safe.Go("settlement:paid-notify", func() { fn(qq) })
		}
	}
	return changed, nil
}
