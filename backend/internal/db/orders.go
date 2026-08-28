package db

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/money"
)

// CreateOrder replaces the INSERT INTO orders in PlaceProductOrder,
// including `idempotency_key TEXT NOT NULL UNIQUE` — enforced here by
// writing the guard key in the same transaction, so a duplicate HTTP retry
// still gets ErrConflict rather than a second order.
func (s *Store) CreateOrder(o Order) error {
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	o.UpdatedAt = o.CreatedAt
	if o.Status == "" {
		o.Status = "pending" // was the column DEFAULT
	}
	if o.Quantity < 1 {
		o.Quantity = 1
	}

	return s.Update(func(txn *badger.Txn) error {
		idemKey := orderIdempotencyIndexKey(o.IdempotencyKey)
		if o.IdempotencyKey != "" {
			if _, err := txn.Get(idemKey); err == nil {
				return ErrConflict
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
		}

		blob, err := marshal(o)
		if err != nil {
			return err
		}
		if err := txn.Set(orderKey(o.ID), blob); err != nil {
			return err
		}
		if o.IdempotencyKey != "" {
			if err := txn.Set(idemKey, []byte(o.ID)); err != nil {
				return err
			}
		}
		if err := txn.Set(orderUserIndexKey(o.UserID, o.CreatedAt.UnixNano(), o.ID), []byte(o.ID)); err != nil {
			return err
		}
		return txn.Set(orderStatusIndexKey(o.Status, o.ID), []byte(o.ID))
	})
}

// GetOrder replaces SELECT ... FROM orders WHERE id = $1.
func (s *Store) GetOrder(id string) (Order, error) {
	var o Order
	err := s.View(func(txn *badger.Txn) error {
		return getJSON(txn, orderKey(id), &o)
	})
	return o, err
}

// GetOrderForUser fetches an order and verifies it belongs to the given userID.
func (s *Store) GetOrderForUser(id, userID string) (Order, error) {
	var o Order
	err := s.View(func(txn *badger.Txn) error {
		if err := getJSON(txn, orderKey(id), &o); err != nil {
			return err
		}
		if o.UserID != userID {
			return ErrNotFound
		}
		return nil
	})
	return o, err
}

// GetOrderBySupplierID replaces the webhook's
// `WHERE supplier_order_id = $1` lookups, which had no index in SQL and
// now resolve through ix:o:cr:<id>.
func (s *Store) GetOrderBySupplierID(supplierOrderID string) (Order, error) {
	var o Order
	err := s.View(func(txn *badger.Txn) error {
		id, err := getString(txn, orderSupplierIndexKey(supplierOrderID))
		if err != nil {
			return err
		}
		return getJSON(txn, orderKey(id), &o)
	})
	return o, err
}

// SetOrderStatus replaces:
//
//	UPDATE orders SET status = 'failed', updated_at = now() WHERE id = $1
func (s *Store) SetOrderStatus(orderID, status string) error {
	return s.Update(func(txn *badger.Txn) error {
		var o Order
		if err := getJSON(txn, orderKey(orderID), &o); err != nil {
			return err
		}
		return writeOrderStatus(txn, &o, status)
	})
}

// UpdateOrderFulfillment updates status and fulfillment JSON for an order.
func (s *Store) UpdateOrderFulfillment(orderID, status string, fulfillment json.RawMessage) error {
	return s.Update(func(txn *badger.Txn) error {
		var o Order
		if err := getJSON(txn, orderKey(orderID), &o); err != nil {
			return err
		}
		if fulfillment != nil {
			o.Fulfillment = fulfillment
		}
		if status == "" {
			status = o.Status
		}
		return writeOrderStatus(txn, &o, status)
	})
}

// AttachSupplierIDs replaces the post-invoice update:
//
//	UPDATE orders SET supplier_order_id = $1, supplier_invoice_id = $2,
//	                  status = $3, updated_at = now() WHERE id = $4
//
// It also maintains the ix:o:cr index the webhook path depends on.
func (s *Store) AttachSupplierIDs(orderID, supplierOrderID, invoiceID, status string) error {
	return s.Update(func(txn *badger.Txn) error {
		var o Order
		if err := getJSON(txn, orderKey(orderID), &o); err != nil {
			return err
		}

		if supplierOrderID != "" {
			o.SupplierOrderID = &supplierOrderID
			if err := txn.Set(orderSupplierIndexKey(supplierOrderID), []byte(o.ID)); err != nil {
				return err
			}
		}
		if invoiceID != "" {
			o.SupplierInvoiceID = &invoiceID
		}
		if status == "" {
			status = o.Status
		}
		return writeOrderStatus(txn, &o, status)
	})
}

// UpdateOrderFromWebhook replaces the webhook's conditional update:
//
//	UPDATE orders SET status = $1, fulfillment = $2, updated_at = now()
//	WHERE supplier_order_id = $3 AND status IS DISTINCT FROM $1
//
// The `IS DISTINCT FROM` clause made repeat deliveries no-ops, and
// RowsAffected() == 0 signalled "nothing changed". That is reproduced by
// the changed return value, so the caller still only refunds on a real
// transition to 'failed'.
func (s *Store) UpdateOrderFromWebhook(supplierOrderID, status string, fulfillment json.RawMessage) (o Order, changed bool, err error) {
	err = s.Update(func(txn *badger.Txn) error {
		changed = false

		id, err := getString(txn, orderSupplierIndexKey(supplierOrderID))
		if errors.Is(err, ErrNotFound) {
			return nil // unknown order id — safe to skip, as before
		}
		if err != nil {
			return err
		}

		var found Order
		if err := getJSON(txn, orderKey(id), &found); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if found.Status == status {
			o = found
			return nil // already up to date
		}

		if fulfillment != nil {
			found.Fulfillment = fulfillment
		}
		if err := writeOrderStatus(txn, &found, status); err != nil {
			return err
		}

		o = found
		changed = true
		return nil
	})
	return o, changed, err
}

// ListOrders replaces the user's order list query, newest-first.
func (s *Store) ListOrders(userID string, limit int) ([]Order, error) {
	var out []Order
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, orderUserIndexPrefix(userID), limit, func(id string) error {
			var o Order
			if err := getJSON(txn, orderKey(id), &o); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, o)
			return nil
		})
	})
	return out, err
}

// writeOrderStatus persists an order with a new status, moving it between
// status index buckets and bumping updated_at.
func writeOrderStatus(txn *badger.Txn, o *Order, status string) error {
	prev := o.Status
	o.Status = status
	o.UpdatedAt = time.Now().UTC()

	blob, err := marshal(*o)
	if err != nil {
		return err
	}
	if err := txn.Set(orderKey(o.ID), blob); err != nil {
		return err
	}
	if prev != status {
		if err := txn.Delete(orderStatusIndexKey(prev, o.ID)); err != nil {
			return err
		}
		if err := txn.Set(orderStatusIndexKey(status, o.ID), []byte(o.ID)); err != nil {
			return err
		}
		// A transition into a delivered OR refunded state publishes the order to
		// the public activity feed. Refunds are public too — everyone can verify.
		feedNow := (isDeliveredStatus(status) && !isDeliveredStatus(prev)) || (status == "refunded" && prev != "refunded")
		if feedNow {
			if err := txn.Set(feedOrderIndexKey(o.UpdatedAt.UnixNano(), o.ID), []byte(o.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// isDeliveredStatus reports whether a status represents a completed, fulfilled
// purchase (the only orders eligible for the public feed and for rating).
func isDeliveredStatus(s string) bool {
	switch s {
	case "delivered", "complete", "fulfilled":
		return true
	}
	return false
}

// unused keeps money imported for callers of this file's types.
var _ = money.Micros(0)

// GetOrderByIdempotencyKey returns the order previously created by this user
// for a client supplied idempotency key. The user comparison prevents one
// authenticated account from probing another account's order records.
func (s *Store) GetOrderByIdempotencyKey(userID, key string) (Order, error) {
	var o Order
	err := s.View(func(txn *badger.Txn) error {
		id, err := getString(txn, orderIdempotencyIndexKey(key))
		if err != nil {
			return err
		}
		if err := getJSON(txn, orderKey(id), &o); err != nil {
			return err
		}
		if o.UserID != userID {
			return ErrNotFound
		}
		return nil
	})
	return o, err
}

/* ---------------- Public activity feed + ratings ---------------- */

// SetOrderRating records a 1-5 star rating on a delivered order owned by the
// caller, and updates the global RatingAggregate in the SAME transaction so
// the public average can never diverge from the individual ratings. It is
// idempotent: re-submitting the same value is a no-op; changing a value
// adjusts the aggregate by the delta. Returns the updated aggregate.
func (s *Store) SetOrderRating(orderID, userID string, rating int) (Order, RatingAggregate, error) {
	var o Order
	var agg RatingAggregate
	if rating < 1 || rating > 5 {
		return o, agg, ErrConflict
	}
	err := s.Update(func(txn *badger.Txn) error {
		if e := getJSON(txn, orderKey(orderID), &o); e != nil {
			return e
		}
		if o.UserID != userID {
			return ErrNotFound
		}
		if !isDeliveredStatus(o.Status) {
			return ErrConflict // not delivered yet — not rateable
		}

		agg = loadAggregate(txn)
		old := o.Rating
		if old == rating {
			return nil // idempotent no-op
		}

		now := time.Now().UTC()
		o.Rating = rating
		o.RatedAt = &now

		blob, e := marshal(o)
		if e != nil {
			return e
		}
		if e := txn.Set(orderKey(o.ID), blob); e != nil {
			return e
		}

		if old == 0 {
			// first rating for this order
			agg.Count++
			agg.Sum += rating
			agg.Dist[rating]++
		} else {
			// rating changed: adjust sum and distribution, count unchanged
			agg.Sum += rating - old
			agg.Dist[old]--
			agg.Dist[rating]++
		}
		return saveAggregate(txn, agg)
	})
	return o, agg, err
}

// ListFeedOrders returns the most recently delivered orders (newest-first) for
// the public activity feed, scanned through the ix:feed:o index.
func (s *Store) ListFeedOrders(limit int) ([]Order, error) {
	var out []Order
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, feedOrderIndexPrefix(), limit, func(id string) error {
			var o Order
			if e := getJSON(txn, orderKey(id), &o); e != nil {
				if errors.Is(e, ErrNotFound) {
					return nil
				}
				return e
			}
			out = append(out, o)
			return nil
		})
	})
	return out, err
}

// GetRatingAggregate returns the global star-rating summary (zero value if
// nobody has rated yet).
func (s *Store) GetRatingAggregate() (RatingAggregate, error) {
	var agg RatingAggregate
	err := s.View(func(txn *badger.Txn) error {
		agg = loadAggregate(txn)
		return nil
	})
	return agg, err
}

// loadAggregate reads the meta:rating_aggregate record, returning the zero
// value when it does not exist yet.
func loadAggregate(txn *badger.Txn) RatingAggregate {
	var agg RatingAggregate
	item, err := txn.Get([]byte(metaRatingAggregateKey))
	if err != nil {
		return agg // ErrKeyNotFound -> zero value
	}
	_ = item.Value(func(v []byte) error { return unmarshal(v, &agg) })
	return agg
}

// saveAggregate writes the meta:rating_aggregate record.
func saveAggregate(txn *badger.Txn, agg RatingAggregate) error {
	blob, err := marshal(agg)
	if err != nil {
		return err
	}
	return txn.Set([]byte(metaRatingAggregateKey), blob)
}

// ListOrdersByStatus returns orders in a given status bucket. It is used by
// the admin/manual-review views; supplier status transitions themselves are
// driven by supplier webhooks.
func (s *Store) ListOrdersByStatus(status string, limit int) ([]Order, error) {
	var out []Order
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, orderStatusIndexPrefix(status), limit, func(id string) error {
			var o Order
			if err := getJSON(txn, orderKey(id), &o); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, o)
			return nil
		})
	})
	return out, err
}

// AttachOrderRefund records the supplier-side refund details and moves the
// order to 'refunded'. The supplier (CryptoRefills, merchant of record)
// returns the paid amount; this record is the auditable proof of where the
// money went.
func (s *Store) AttachOrderRefund(orderID string, refund json.RawMessage) error {
	return s.Update(func(txn *badger.Txn) error {
		var o Order
		if err := getJSON(txn, orderKey(orderID), &o); err != nil {
			return err
		}
		if o.Status == "delivered" || o.Status == "fulfilled" || o.Status == "complete" {
			return ErrConflict // never "refund" a delivered order
		}
		if refund != nil {
			o.Refund = refund
		}
		return writeOrderStatus(txn, &o, "refunded")
	})
}
