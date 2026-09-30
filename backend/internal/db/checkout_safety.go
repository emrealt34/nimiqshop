package db

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/cryptorefills"
)

// quoteExpiryBuffer is how long a supplier-linked quote must sit past its
// payment window (unpaid, no money observed) before the tracker may expire it
// locally. A lapsed single-use Lightning invoice can no longer be paid, so this
// buffer only guards against a payment that landed right at the window edge —
// it never ends fulfillment polling for a quote where money was actually seen.
const quoteExpiryBuffer = 5 * time.Minute

func quoteRequestFingerprintKey(user, key string) []byte {
	return []byte("ix:q:request:" + user + ":" + key)
}
func quotePaymentHashIndexKey(hash string) []byte { return []byte("ix:q:payment-hash:" + hash) }

type ErrActiveCheckout struct{ Quote Quote }

func (e *ErrActiveCheckout) Error() string {
	return "an unresolved checkout already exists for this buyer"
}

// Deliberately conservative: one unresolved checkout per buyer, including an
// uncertain batch. Changing quantities/recipients/cashback codes or switching
// from batch to per-item must not evade a possibly-paid purchase.
func BlocksNewPurchase(q Quote) bool {
	switch q.Status {
	case "order_creating", "awaiting_payment", "payment_started", "payment_received", "delivering", "manual_review":
		return true
	case "expired":
		// An expired quote only blocks a new purchase when real money was
		// observed (it must then be reconciled). Once the window has lapsed
		// unpaid, the single-use invoice can no longer be paid, so it must not
		// hold the buyer's checkout hostage.
		return q.SupplierOrderID != "" && q.PaymentObserved
	case "failed":
		return q.SupplierOrderID != "" && (q.PaymentObserved || !strings.EqualFold(q.SupplierStatus, cryptorefills.StatusPaymentSetupFailed))
	}
	return false
}

func (q Quote) NeedsSupplierPoll() bool {
	if q.SupplierOrderID == "" || q.Status == "fulfilled" || q.Status == "refunded" {
		return false
	}
	return BlocksNewPurchase(q)
}

func (q Quote) CanPay(now time.Time) bool {
	return q.Status == "awaiting_payment" && !q.PaymentBlocked && !q.PaymentObserved &&
		q.WalletAddress != "" && q.CoinAmount != "" && q.SupplierOrderID != "" &&
		q.PaymentExpiry.After(now) &&
		(q.SupplierStatus == "" || strings.EqualFold(q.SupplierStatus, cryptorefills.StatusCreated) || strings.EqualFold(q.SupplierStatus, cryptorefills.StatusWaitingForPayment))
}

// PublicQuote omits the internal outbox and attribution. Historical invoices are
// also removed when not payable, so old links cannot accidentally launch them.
func (q Quote) PublicQuote(now time.Time) Quote {
	q.SupplierRequest = nil
	q.EndUserIP = ""
	q.EndUserAgent = ""
	q.RequestFingerprint = ""
	q.PurchaseFingerprint = ""
	q.IdempotencyKey = ""
	if !q.CanPay(now) {
		q.WalletAddress = ""
	}
	return q
}

func bindUniqueIndex(tx *badger.Txn, key []byte, id string) error {
	existing, err := getString(tx, key)
	if err == nil && existing != id {
		return ErrConflict
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return tx.Set(key, []byte(id))
}

func (s *Store) GetQuoteByIdempotencyRequest(user, key string) (Quote, string, error) {
	var q Quote
	var fp string
	err := s.View(func(tx *badger.Txn) error {
		id, err := getString(tx, quoteIdempotencyIndexKey(user, key))
		if err != nil {
			return err
		}
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		if q.UserID != user {
			return ErrConflict
		}
		fp, err = getString(tx, quoteRequestFingerprintKey(user, key))
		if errors.Is(err, ErrNotFound) {
			fp = q.RequestFingerprint
			return nil
		}
		return err
	})
	return q, fp, err
}

// An alias is durable even after the quote finishes. Without this, a second
// tab's new idempotency key could create a new order after fulfillment.
func (s *Store) BindQuoteIdempotency(user, key, fingerprint, quoteID string) error {
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(quoteID), &q); err != nil {
			return err
		}
		if q.UserID != user {
			return ErrConflict
		}
		if err := bindUniqueIndex(tx, quoteIdempotencyIndexKey(user, key), quoteID); err != nil {
			return err
		}
		previous, err := getString(tx, quoteRequestFingerprintKey(user, key))
		if err == nil && previous != fingerprint {
			return ErrConflict
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return tx.Set(quoteRequestFingerprintKey(user, key), []byte(fingerprint))
	})
}

// Record the supplier link BEFORE price/invoice validation: an invalid invoice
// still represents an upstream order that must be reconciled, not forgotten.
func (s *Store) RecordSupplierOrder(id string, order *cryptorefills.Order) error {
	if order == nil || order.ID == "" {
		return ErrConflict
	}
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		if q.SupplierOrderID != "" && q.SupplierOrderID != order.ID {
			return ErrConflict
		}
		if err := bindUniqueIndex(tx, quoteSupplierOrderIndexKey(order.ID), id); err != nil {
			return err
		}
		q.SupplierOrderID = order.ID
		if q.SupplierStatus == "" {
			q.SupplierStatus = order.Status
		}
		q.UpdatedAt = time.Now().UTC()
		return putQuoteTx(tx, &q)
	})
}

func (s *Store) HoldQuote(id, reason string) error {
	return s.transitionQuote(id, "manual_review", func(q *Quote, _ *badger.Txn) error {
		q.PaymentBlocked = true
		q.RefundReason = reason
		return nil
	})
}

var errStaleSupplierObservation = errors.New("stale supplier observation")

// ApplySupplierOrder is shared by polling and the optional webhook. It verifies
// the supplier ID, stores the RAW state on every accepted transition and commits
// fulfillment/cashback atomically. Duplicate/out-of-order observations are no-ops;
// actual persistence errors are returned so webhooks never acknowledge data loss.
func (s *Store) ApplySupplierOrder(id string, order *cryptorefills.Order) (bool, error) {
	if order == nil || order.ID == "" || order.Status == "" {
		return false, ErrConflict
	}
	local := cryptorefills.MapToQuoteStatus(order.Status)
	observedAt := order.UpdatedTime()
	err := s.transitionQuote(id, local, func(q *Quote, _ *badger.Txn) error {
		if q.SupplierOrderID != order.ID {
			return ErrConflict
		}
		if !observedAt.IsZero() && !q.SupplierUpdatedAt.IsZero() && observedAt.Before(q.SupplierUpdatedAt) {
			return errStaleSupplierObservation
		}
		newMoney := cryptorefills.MoneyObserved(order)
		if local == q.Status && q.SupplierStatus == order.Status && (!newMoney || q.PaymentObserved) {
			return errStaleSupplierObservation
		}
		// Never re-open a pay button after ANY evidence of money or a local hold.
		if local == "awaiting_payment" && (q.PaymentObserved || q.PaymentBlocked) {
			return errStaleSupplierObservation
		}
		q.SupplierStatus = order.Status
		if !observedAt.IsZero() {
			q.SupplierUpdatedAt = observedAt
		}
		if newMoney {
			q.PaymentObserved = true
			if q.PaidAt == nil {
				t := observedAt
				if t.IsZero() {
					t = time.Now()
				}
				q.PaidAt = &t
			}
		}
		// The amount the supplier reports actually receiving, in the rail's
		// own coin. Persisted the moment it is seen so the fulfillment-time
		// cashback can follow the PAID value (within the +100% cap) instead
		// of only the shop's priced base. Never overwritten by a smaller
		// later observation: money that landed once has landed.
		if v := strings.TrimSpace(order.SentCoinAmount); v != "" && v != q.SentCoinAmount {
			if q.SentCoinAmount == "" || positiveCoinUnits(v) >= positiveCoinUnits(q.SentCoinAmount) {
				q.SentCoinAmount = v
			}
		}
		switch local {
		case "fulfilled":
			q.Fulfillment = cryptorefills.FulfillmentPayload(order)
		case "refunded":
			if order.Refund != nil {
				q.Refund, _ = json.Marshal(order.Refund)
			}
			q.RefundReason = "supplier confirmed refund"
		case "failed", "manual_review":
			q.RefundReason = "supplier state: " + order.Status
		}
		return nil
	})
	if errors.Is(err, errStaleSupplierObservation) {
		return false, nil
	}
	if errors.Is(err, ErrConflict) {
		q, e := s.GetQuote(id)
		if e != nil {
			return false, e
		}
		if q.SupplierOrderID != order.ID {
			return false, ErrConflict
		}
		// Conditional transition rejected a duplicate or a state regression.
		if q.Status == local || !canQuoteTransition(q.Status, local) {
			return false, nil
		}
	}
	return err == nil, err
}

// ClaimPaymentHandoff records that the buyer's wallet has been handed this
// invoice (a single supplier invoice that can only be paid once). It never
// refuses re-opening the same invoice: Lightning is single-pay, so paying it
// again is always safe and never a duplicate charge. Audit-only; never blocks.
func (s *Store) ClaimPaymentHandoff(id, user string) error {
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		now := time.Now().UTC()
		if q.UserID != user || !q.CanPay(now) {
			return ErrConflict
		}
		q.PaymentHandoffAt = now
		return putQuoteTx(tx, &q)
	})
}
