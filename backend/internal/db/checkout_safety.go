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

// PaymentGrace is how long after the supplier's payment window the shop keeps
// treating a lapsed invoice as possibly paid. A payment submitted right at the
// edge of the window may not be visible to the supplier yet, so a timer running
// out is NOT proof that nothing was charged. Until this buffer has passed the
// quote keeps blocking a new purchase and the buyer is told the shop is still
// verifying — never that the payment failed. The frontend mirrors the same
// constant in src/lib/pay.ts; the two must not drift.
const PaymentGrace = quoteExpiryBuffer

func quoteRequestFingerprintKey(user, key string) []byte {
	return []byte("ix:q:request:" + user + ":" + key)
}
func quotePaymentHashIndexKey(hash string) []byte { return []byte("ix:q:payment-hash:" + hash) }

type ErrActiveCheckout struct{ Quote Quote }

func (e *ErrActiveCheckout) Error() string {
	return "an unresolved checkout already exists for this buyer"
}

// PaymentDeadline is the moment the supplier's single-use invoice stops being
// payable: the supplier's own window when we have it, otherwise the local quote
// expiry that mirrors the same window. A zero value means UNKNOWN, and an
// unknown deadline must keep blocking — it can never authorise a second
// payment.
func (q Quote) PaymentDeadline() time.Time {
	if !q.PaymentExpiry.IsZero() {
		return q.PaymentExpiry
	}
	return q.ExpiresAt
}

// hasPaymentEvidence reports whether any of the three sources saw money or
// requested a hold. This is the ONLY thing that may end the "unresolved
// payment" state — never a clock.
func (q Quote) hasPaymentEvidence() bool {
	return q.PaymentObserved || q.PaymentBlocked || cryptorefills.IsPaidOrBeyond(q.SupplierStatus)
}

// PaymentVerifiedUnpaid reports whether the shop can PROVE nothing was charged:
// the supplier was able to see any last-second payment (the deadline plus the
// grace buffer is in the past) and no payment evidence exists in the shop or
// upstream. Only this authorises telling a buyer "nothing was charged" and
// offering a fresh invoice.
func (q Quote) PaymentVerifiedUnpaid(now time.Time) bool {
	if q.hasPaymentEvidence() {
		return false
	}
	deadline := q.PaymentDeadline()
	if deadline.IsZero() {
		return false
	}
	return !now.Before(deadline.Add(PaymentGrace))
}

// Deliberately conservative: one unresolved checkout per buyer, including an
// uncertain batch. Changing quantities/recipients/cashback codes or switching
// from batch to per-item must not evade a possibly-paid purchase.
//
// Time-aware on purpose. The old version returned true for every
// "awaiting_payment" quote forever, which is how a single lapsed invoice — one
// the buyer can never pay again — held the whole shop shut for them and the
// checkout answered ACTIVE_CHECKOUT with no way out. Now such a quote only
// blocks while money could still be in play (see PaymentVerifiedUnpaid); the
// re-check inside blockingQuotesFor drops it from the index on the next read,
// so both the pre-check and the atomic gate release at the same instant.
func BlocksNewPurchaseAt(q Quote, now time.Time) bool {
	switch q.Status {
	case "order_creating", "payment_started", "payment_received", "delivering", "manual_review":
		return true
	case "awaiting_payment":
		return !q.PaymentVerifiedUnpaid(now)
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

// LapsedUnpaidInvoice reports that the single-use invoice can no longer be
// paid AND no money was ever seen on it. Such a quote must never be handed
// back as a "live duplicate" or reused by the idempotency pre-check: doing so
// strands the buyer on a dead pay screen whose own copy promises a fresh
// invoice "right here" while no button or backend path can create one (live
// bug, 2026-10-04). Renewing the same cart is therefore a CREATION, not a
// reuse. Money safety is untouched: the supplier invoice died with the
// window, so a late payment on it reconciles through the existing observed
// path, and any quote with observed/blocked money is not lapsed by this
// predicate and keeps blocking exactly as before.
func LapsedUnpaidInvoice(q Quote, now time.Time) bool {
	switch q.Status {
	case "payment_received", "delivering", "manual_review", "fulfilled", "refunded":
		return false
	}
	if q.PaymentObserved || q.PaymentBlocked {
		return false
	}
	dl := q.PaymentExpiry
	if dl.IsZero() {
		dl = q.ExpiresAt
	}
	return !dl.IsZero() && dl.Before(now)
}

// BlocksNewPurchase is the time.Now() convenience wrapper used by every call
// site that does not carry a shared clock (the index build, the exported
// pre-check). Callers that already have `now` must use BlocksNewPurchaseAt so
// one request cannot decide on two different clocks.
func BlocksNewPurchase(q Quote) bool { return BlocksNewPurchaseAt(q, time.Now().UTC()) }

// NeedsSupplierPoll answers a DIFFERENT question from BlocksNewPurchase and
// must not be derived from it: "is there any supplier state left to
// reconcile?" A lapsed-but-unpaid quote no longer blocks a new purchase, yet
// the tracker must still drive it to `expired` (display, admin timeline, and
// the release of its daily-limit slot), and a quote where money was seen must
// be polled forever. Deleting a poll is how a real payment ends up forgotten.
func (q Quote) NeedsSupplierPoll() bool {
	if q.SupplierOrderID == "" || q.Status == "fulfilled" || q.Status == "refunded" {
		return false
	}
	switch q.Status {
	case "order_creating", "awaiting_payment", "payment_started", "payment_received", "delivering", "manual_review":
		return true
	case "expired":
		return q.PaymentObserved
	case "failed":
		return q.PaymentObserved || !strings.EqualFold(q.SupplierStatus, cryptorefills.StatusPaymentSetupFailed)
	}
	return false
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
	if q.RatingCommentHidden {
		q.RatingComment = "" // an admin-hidden comment is withheld everywhere
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
