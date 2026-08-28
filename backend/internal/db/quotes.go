package db

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/money"
)

/* Quote state machine (Cryptorefills rail)
 *
 * States:
 *	order_creating     WAL intent, persisted BEFORE the supplier CreateOrder.
 *	                   SupplierRequestAt splits this state into "request not
 *	                   yet dispatched" (re-dispatchable after a crash) and
 *	                   "request in flight / awaiting supplier order id"
 *	                   (stale => manual_review; see the settlement tracker).
 *	awaiting_payment   wallet address + coin amount issued to the customer
 *	payment_started    customer tx broadcast / partial payment
 *	payment_received   payment confirmed on-chain
 *	delivering         supplier delivery in progress
 *	fulfilled          delivered (terminal)
 *	expired            payment window elapsed (terminal; late verified
 *	                   deliveries can still land — see
 *	                   CompleteQuoteWithFulfillment)
 *	failed             payment setup/attempt failed (terminal)
 *	refunded           supplier refunded the customer (terminal)
 *	manual_review      supplier needs manual action, or an unrecoverable
 *	                   crash window (operator-visible, never silent)
 *
 * Every transition is a CONDITIONAL update: it only fires from an allowed
 * previous state, so two concurrent workers (or a restart mid-flight) can
 * never apply the same external effect twice. The supplier order id is
 * persisted BEFORE it is ever shown to the customer, so a crash can never
 * lose the link between a paid order and its local quote.
 */

var quoteTransitions = map[string]map[string]bool{
	"order_creating":   {"awaiting_payment": true, "manual_review": true, "failed": true},
	"awaiting_payment": {"payment_started": true, "payment_received": true, "delivering": true, "fulfilled": true, "expired": true, "failed": true, "manual_review": true, "refunded": true},
	"payment_started":  {"payment_received": true, "delivering": true, "fulfilled": true, "expired": true, "failed": true, "manual_review": true, "refunded": true},
	"payment_received": {"delivering": true, "fulfilled": true, "failed": true, "manual_review": true, "refunded": true},
	"delivering":       {"fulfilled": true, "failed": true, "manual_review": true, "refunded": true},
	// A locally-expired quote can still be completed when the supplier's
	// verified webhook/poll says the customer paid: the payment window on
	// the supplier side is authoritative for money, the local timer only
	// releases the daily-limit slot.
	"expired":       {"fulfilled": true, "refunded": true},
	"failed":        {"manual_review": true},
	"manual_review": {},
	"fulfilled":     {},
	"refunded":      {},
}

func canQuoteTransition(from, to string) bool {
	return quoteTransitions[from][to]
}

// CreateQuoteWithDailyLimits inserts the write-ahead quote intent with the
// user's daily order/spend check inside the SAME transaction. The previous
// read-then-write check was racy under concurrent requests.
func (s *Store) CreateQuoteWithDailyLimits(q Quote, maxOrders int, maxSpend money.Micros, since time.Time) error {
	// ProductUSD == 0 is a valid LABEL-ONLY quote ("Java & Bedrock Ed" —
	// fixed products the supplier prices by the exact denomination label).
	// The old `<= 0 → ErrConflict` invariant silently rejected every such
	// purchase with a misleading 409 "idempotency key already used".
	// Negative amounts remain impossible (Micros of a non-negative float).
	if q.ID == "" || q.UserID == "" || q.ProductUSD < 0 {
		return ErrConflict
	}
	if q.CreatedAt.IsZero() {
		q.CreatedAt = time.Now().UTC()
	}
	if q.Status == "" {
		q.Status = "order_creating"
	}
	if q.ExpiresAt.IsZero() {
		q.ExpiresAt = q.PaymentExpiry
	}
	if q.ExpiresAt.IsZero() {
		q.ExpiresAt = q.CreatedAt.Add(30 * time.Minute)
	}
	return s.Update(func(tx *badger.Txn) error {
		// Force an optimistic-concurrency conflict between simultaneous
		// requests for the same user (Badger has no predicate locks).
		if err := tx.Set(quoteUserLimitLockKey(q.UserID), []byte("1")); err != nil {
			return err
		}
		count := 0
		spend := money.Micros(0)
		if err := scanIndex(tx, quoteUserIndexPrefix(q.UserID), 0, func(id string) error {
			var existing Quote
			if err := getJSON(tx, quoteKey(id), &existing); err != nil {
				return err
			}
			if existing.CreatedAt.Before(since) || existing.Status == "failed" || existing.Status == "expired" || existing.Status == "manual_review" {
				return nil
			}
			count++
			spend += existing.ProductUSD
			return nil
		}); err != nil {
			return err
		}
		if maxOrders > 0 && count >= maxOrders {
			return ErrLimit
		}
		if maxSpend > 0 && spend+q.ProductUSD > maxSpend {
			return ErrLimit
		}
		// Idempotency: the same client key can never create a second
		// supplier order (retries return the first quote).
		if q.IdempotencyKey != "" {
			if _, err := tx.Get(quoteIdempotencyIndexKey(q.UserID, q.IdempotencyKey)); err == nil {
				return ErrConflict
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
		}
		raw, err := marshal(q)
		if err != nil {
			return err
		}
		if err = tx.Set(quoteKey(q.ID), raw); err != nil {
			return err
		}
		if q.IdempotencyKey != "" {
			if err = tx.Set(quoteIdempotencyIndexKey(q.UserID, q.IdempotencyKey), []byte(q.ID)); err != nil {
				return err
			}
		}
		if err = tx.Set(quoteUserIndexKey(q.UserID, q.CreatedAt.UnixNano(), q.ID), []byte(q.ID)); err != nil {
			return err
		}
		return tx.Set(quoteStatusIndexKey(q.Status, q.ID), []byte(q.ID))
	})
}

// AttachQuotePayment completes the WAL step after the supplier created the
// order: order id + one-time wallet address + exact coin amount are
// persisted BEFORE anything is shown to the customer.
func (s *Store) AttachQuotePayment(id, supplierOrderID, walletAddress, coin, coinAmount, network string, paymentExpiry time.Time) error {
	if supplierOrderID == "" || walletAddress == "" || coinAmount == "" {
		return ErrConflict
	}
	return s.transitionQuote(id, "awaiting_payment", func(q *Quote, tx *badger.Txn) error {
		q.SupplierOrderID = supplierOrderID
		q.WalletAddress = walletAddress
		q.Coin = coin
		q.CoinAmount = coinAmount
		q.Network = network
		if !paymentExpiry.IsZero() {
			q.PaymentExpiry = paymentExpiry
			q.ExpiresAt = paymentExpiry
		}
		q.OrderAttempts++
		return tx.Set(quoteSupplierOrderIndexKey(supplierOrderID), []byte(id))
	})
}

// transitionQuote applies a conditional state transition: it loads the
// quote, checks canQuoteTransition, applies mutate (with the open
// transaction for extra index writes), and rewrites the record + status
// index atomically.
func (s *Store) transitionQuote(id, to string, mutate func(*Quote, *badger.Txn) error) error {
	var q Quote
	var oldStatus string
	err := s.Update(func(tx *badger.Txn) error {
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		oldStatus = q.Status
		if to == oldStatus {
			// Re-entrant calls are only allowed as status-metadata updates
			// (mutate != nil) on non-terminal states. Terminal states are
			// never re-entered: a duplicate "Done" must not overwrite the
			// stored delivery (handlers treat the conflict as an
			// idempotent no-op after re-checking the status).
			if mutate == nil || oldStatus == "fulfilled" || oldStatus == "refunded" {
				return ErrConflict
			}
		} else if !canQuoteTransition(oldStatus, to) {
			return ErrConflict
		}
		if mutate != nil {
			if err := mutate(&q, tx); err != nil {
				return err
			}
		}
		q.Status = to
		q.UpdatedAt = time.Now().UTC()
		raw, err := marshal(q)
		if err != nil {
			return err
		}
		if err = tx.Set(quoteKey(q.ID), raw); err != nil {
			return err
		}
		if to != oldStatus {
			if err = tx.Delete(quoteStatusIndexKey(oldStatus, id)); err != nil {
				return err
			}
			if err = tx.Set(quoteStatusIndexKey(to, id), []byte(id)); err != nil {
				return err
			}
		}
		if to == "fulfilled" {
			if err = publishToFeed(tx, &q); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// GetQuote fetches one quote by id.
func (s *Store) GetQuote(id string) (Quote, error) {
	var q Quote
	err := s.View(func(tx *badger.Txn) error { return getJSON(tx, quoteKey(id), &q) })
	return q, err
}

// GetQuoteForUser is the ownership-checked fetch for customer endpoints.
func (s *Store) GetQuoteForUser(id, userID string) (Quote, error) {
	q, err := s.GetQuote(id)
	if err != nil {
		return q, err
	}
	if q.UserID != userID {
		return Quote{}, ErrNotFound
	}
	return q, nil
}

// GetQuoteByIdempotencyKey finds the quote a user started with a given
// idempotency key (client retries return the same quote, never a second
// supplier order).
func (s *Store) GetQuoteByIdempotencyKey(userID, key string) (Quote, error) {
	var q Quote
	err := s.View(func(tx *badger.Txn) error {
		idItem, err := tx.Get(quoteIdempotencyIndexKey(userID, key))
		if err != nil {
			return err
		}
		var id string
		if err := idItem.Value(func(v []byte) error { id = string(v); return nil }); err != nil {
			return err
		}
		return getJSON(tx, quoteKey(id), &q)
	})
	return q, err
}

// GetQuoteBySupplierOrderID maps a supplier order id to the local quote.
// Webhook and polling fulfillment use this index so delivery never
// requires a full scan.
func (s *Store) GetQuoteBySupplierOrderID(supplierOrderID string) (Quote, error) {
	var q Quote
	err := s.View(func(tx *badger.Txn) error {
		idItem, err := tx.Get(quoteSupplierOrderIndexKey(supplierOrderID))
		if err != nil {
			return err
		}
		var id string
		if err := idItem.Value(func(v []byte) error { id = string(v); return nil }); err != nil {
			return err
		}
		return getJSON(tx, quoteKey(id), &q)
	})
	return q, err
}

// ListQuotesByStatus returns quotes in one status (oldest first — workers
// want the longest-waiting ones).
func (s *Store) ListQuotesByStatus(status string, limit int) ([]Quote, error) {
	return s.ListQuotesByStatuses([]string{status}, limit)
}

// ListQuotesByStatuses returns quotes in any of the given statuses.
func (s *Store) ListQuotesByStatuses(statuses []string, limit int) ([]Quote, error) {
	var out []Quote
	err := s.View(func(tx *badger.Txn) error {
		for _, st := range statuses {
			if err := scanIndex(tx, quoteStatusIndexKey(st, ""), limit, func(id string) error {
				var q Quote
				if err := getJSON(tx, quoteKey(id), &q); err != nil {
					if errors.Is(err, ErrNotFound) {
						return nil
					}
					return err
				}
				out = append(out, q)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	// Newest-waiting first: sort by UpdatedAt ascending.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].UpdatedAt.Before(out[i].UpdatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, err
}

// SetSupplierStatus records a polled supplier state on the quote. It maps
// the supplier state to the local transition; no-op when already there.
// Returns the new local status.
func (s *Store) SetSupplierStatus(id, supplierStatus, mapLocalTo string) (string, error) {
	var q Quote
	err := s.View(func(tx *badger.Txn) error { return getJSON(tx, quoteKey(id), &q) })
	if err != nil {
		return "", err
	}
	if mapLocalTo == "" || mapLocalTo == q.Status {
		if supplierStatus != "" && supplierStatus != q.SupplierStatus {
			_ = s.transitionQuote(id, q.Status, func(q *Quote, _ *badger.Txn) error {
				q.SupplierStatus = supplierStatus
				return nil
			})
		}
		return q.Status, nil
	}
	if err := s.transitionQuote(id, mapLocalTo, func(q *Quote, _ *badger.Txn) error {
		if supplierStatus != "" {
			q.SupplierStatus = supplierStatus
		}
		return nil
	}); err != nil {
		return q.Status, err
	}
	return mapLocalTo, nil
}

// CompleteQuoteWithFulfillment stores the verified delivery payload. Source
// states: any paid state, plus "expired" (late verified payment — the
// supplier re-fetch already happened in the webhook/poller; acking without
// saving would be a paid-order data loss).
func (s *Store) CompleteQuoteWithFulfillment(id string, fulfillment json.RawMessage) error {
	return s.transitionQuote(id, "fulfilled", func(q *Quote, _ *badger.Txn) error {
		if len(fulfillment) > 0 && string(fulfillment) != "null" {
			q.Fulfillment = fulfillment
		}
		return nil
	})
}

// MarkSupplierFailure records a terminal supplier failure (payment failed,
// setup failed, invoice expired...) with the reason for the audit trail.
func (s *Store) MarkSupplierFailure(id, reason string) error {
	return s.transitionQuote(id, "failed", func(q *Quote, _ *badger.Txn) error {
		q.RefundReason = reason
		return nil
	})
}

// MarkQuoteManualReview flags a quote for operator action (supplier manual
// action, or an unrecoverable order-creation crash window).
func (s *Store) MarkQuoteManualReview(id, reason string) error {
	return s.transitionQuote(id, "manual_review", func(q *Quote, _ *badger.Txn) error {
		if reason != "" {
			q.RefundReason = reason
		}
		return nil
	})
}

// MarkQuoteRefunded records a supplier-side refund (Cryptorefills is
// merchant of record; no local refund transaction exists).
func (s *Store) MarkQuoteRefunded(id string, refundInfo json.RawMessage, reason string) error {
	return s.transitionQuote(id, "refunded", func(q *Quote, _ *badger.Txn) error {
		if len(refundInfo) > 0 && string(refundInfo) != "null" {
			q.Refund = refundInfo
		}
		if reason != "" {
			q.RefundReason = reason
		}
		return nil
	})
}

// MarkSupplierRequestStarted persists the "supplier request started /
// supplier order id awaited" marker right before the CreateOrder call. It is
// the durable half of the order-creation crash window: once this commit
// returns, a crash can never be mistaken for "the request was never sent" —
// the settlement tracker treats such an intent as possibly accepted upstream
// and resolves it to manual_review once stale, instead of re-sending it
// (which could create a duplicate supplier order). It also counts the
// attempt, which bounds how many times the tracker may re-dispatch an
// intent that provably never left.
//
// Only legal from order_creating: the marker belongs to the creation phase
// and is meaningless once a supplier order id is attached.
func (s *Store) MarkSupplierRequestStarted(id string) error {
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		if q.Status != "order_creating" {
			return ErrConflict
		}
		now := time.Now().UTC()
		q.SupplierRequestAt = now
		q.OrderAttempts++
		q.UpdatedAt = now
		raw, err := marshal(q)
		if err != nil {
			return err
		}
		return tx.Set(quoteKey(q.ID), raw)
	})
}

// FailOrderAttempt increments the order-creation attempt counter and
// returns the new count (bounded retries for a transient API error).
func (s *Store) FailOrderAttempt(id string) (int, error) {
	var q Quote
	err := s.Update(func(tx *badger.Txn) error {
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		q.OrderAttempts++
		q.UpdatedAt = time.Now().UTC()
		raw, err := marshal(q)
		if err != nil {
			return err
		}
		return tx.Set(quoteKey(q.ID), raw)
	})
	return q.OrderAttempts, err
}

// SetQuoteStatus is an unconditional operator/status setter (admin use).
func (s *Store) SetQuoteStatus(id, status string) error {
	return s.transitionQuote(id, status, nil)
}

// SweepExpiredQuotes expires locally-waiting quotes whose payment window
// has passed. Only order-creating / awaiting-payment quotes are swept:
// once the supplier has seen money, only the supplier's own state can end
// the quote. Expiry releases the daily-limit slot (failed/expired/manual
// quotes do not count against it).
func (s *Store) SweepExpiredQuotes(now time.Time, limit int) (int, error) {
	var expired int
	// order_creating is NOT swept here: a stuck creation is a crash window
	// the tracker resolves to manual_review; marking it expired would hide
	// the incident and could race a late supplier acceptance.
	for _, st := range []string{"awaiting_payment"} {
		qs, err := s.ListQuotesByStatus(st, limit)
		if err != nil {
			return expired, err
		}
		for _, q := range qs {
			if now.After(q.ExpiresAt) {
				if err := s.transitionQuote(q.ID, "expired", nil); err == nil {
					expired++
				}
			}
		}
	}
	return expired, nil
}

/* ------------------------- activity feed + ratings ------------------------ */

// ListFeedQuotes returns the most recently fulfilled purchases (newest
// first) for the public activity feed.
func (s *Store) ListFeedQuotes(limit int) ([]Quote, error) {
	var out []Quote
	err := s.View(func(tx *badger.Txn) error {
		return scanIndex(tx, feedQuoteIndexPrefix(), limit, func(id string) error {
			var q Quote
			if err := getJSON(tx, quoteKey(id), &q); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, q)
			return nil
		})
	})
	return out, err
}

// SetQuoteRating records a 1-5 star rating on a fulfilled quote and keeps
// the public aggregate in the same transaction.
func (s *Store) SetQuoteRating(quoteID, userID string, rating int) (Quote, RatingAggregate, error) {
	var q Quote
	var agg RatingAggregate
	if rating < 1 || rating > 5 {
		return q, agg, ErrConflict
	}
	err := s.Update(func(tx *badger.Txn) error {
		if e := getJSON(tx, quoteKey(quoteID), &q); e != nil {
			return e
		}
		if q.UserID != userID {
			return ErrNotFound
		}
		if q.Status != "fulfilled" {
			return ErrConflict // not fulfilled yet — not rateable
		}

		agg = loadAggregate(tx)
		old := q.Rating
		if old == rating {
			return nil
		}

		now := time.Now().UTC()
		q.Rating = rating
		q.RatedAt = &now

		blob, e := marshal(q)
		if e != nil {
			return e
		}
		if e := tx.Set(quoteKey(q.ID), blob); e != nil {
			return e
		}

		if old == 0 {
			agg.Count++
			agg.Sum += rating
			agg.Dist[rating]++
		} else {
			agg.Sum += rating - old
			agg.Dist[old]--
			agg.Dist[rating]++
		}
		return saveAggregate(tx, agg)
	})
	return q, agg, err
}

// MarkGiftNotified records that a gift notification has been dispatched
// (email and/or SMS). The marker is independent of the supplier state so it
// survives any later state transition; the notifier uses it to skip already-
// delivered recipients on retry, regardless of how the supplier finished.
//
// Safe to call on any quote state. A no-op when already set (idempotent).
func (s *Store) MarkGiftNotified(id string) error {
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		if !q.GiftNotifiedAt.IsZero() {
			return nil // already notified, idempotent
		}
		q.GiftNotifiedAt = time.Now().UTC()
		q.UpdatedAt = q.GiftNotifiedAt
		raw, err := marshal(q)
		if err != nil {
			return err
		}
		return tx.Set(quoteKey(q.ID), raw)
	})
}

// publishToFeed adds a fulfilled quote to the public feed index (idempotent:
// the index key is deterministic, so replays are no-ops).
func publishToFeed(tx *badger.Txn, q *Quote) error {
	if q.Status != "fulfilled" {
		return nil
	}
	return tx.Set(feedQuoteIndexKey(q.UpdatedAt.UnixNano(), q.ID), []byte(q.ID))
}
