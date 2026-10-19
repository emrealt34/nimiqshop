package db

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"nimiqshop/internal/cryptorefills"
)

// There is deliberately NO grace buffer any more.
//
// The shop used to make a lapsed invoice keep blocking a new purchase for a
// few more minutes, on the theory that a payment submitted at the very edge of
// the window might not have reached the supplier yet — a timer standing in for
// knowledge nobody had. On the v6 PARTNER API that guess is unnecessary: the
// shop can ask whether anything was paid and, when nothing was, cancel the
// order itself (DELETE /v6/partner/orders/{id}). So the release rule is now a
// FACT from the supplier, checked in handlers.verifyLapsedWithSupplier, and
// the moment the window closes the buyer is either released or told why not.
// The frontend mirrors this in src/lib/pay.ts (PAYMENT_VERIFY_GRACE_MS = 0).

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
	return !now.Before(deadline)
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
	case "order_creating", "payment_started", "payment_received", "delivering":
		return true
	case "manual_review":
		// A review only blocks when the SUPPLIER may be holding something:
		// an order exists whose outcome is unknown, or money was seen. A
		// quote flagged before any order was created (a crashed creation,
		// an intent the tracker gave up on) has neither — blocking on it
		// would lock the buyer out of the shop for an incident that is not
		// theirs. An unresolved order is still the operator's to resolve.
		return q.SupplierOrderID != "" || q.hasPaymentEvidence()
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

// CheckoutSettledAt reports that the checkout behind q is OVER: the invoice
// can no longer be paid and no money is — or could still be — in play on it
// (paid and delivered, refunded, lapsed unpaid past the supplier's grace,
// or failed before any invoice existed).
//
// Why this exists (live bug, 2026-10-18, owner: "unipin 5 TL aldım, sonra
// tekrar aynı ürünü almaya çalışınca beni tamamlanmış order sayfasına attı"):
// the storefront derives its Idempotency-Key from the CART CONTENTS so that a
// reload or a double tap can never open two supplier orders. The flip side is
// that buying the very same product a second time arrives under the key of
// the first, already-fulfilled order — and a key was honoured forever, so the
// shop "created" the finished order again and the buyer landed on a completed
// order page without ever seeing a pay screen.
//
// An idempotency key protects ONE unresolved purchase. Once that purchase is
// settled there is nothing left for the key to protect, so a request arriving
// under it is a NEW purchase of the same cart, not a retry of the old one.
// This predicate is the single place that decides it, and every reader of the
// key index (the handler pre-check, the atomic creation gate and the retry
// alias binding) must consult it so they can never disagree.
//
// The money-safety half is untouched by construction: anything that still
// blocks a new purchase (payment seen, hold, in-flight settlement, inside the
// grace buffer) is NOT settled and keeps being replayed exactly as before.
func CheckoutSettledAt(q Quote, now time.Time) bool {
	return !q.CanPay(now) && !BlocksNewPurchaseAt(q, now)
}

// CheckoutSettled is the time.Now() wrapper of CheckoutSettledAt.
func CheckoutSettled(q Quote) bool { return CheckoutSettledAt(q, time.Now().UTC()) }

// LateMoneyReconcileWindow is how long a quote the shop expired ON ITS OWN
// CLOCK keeps being offered to the supplier poller.
//
// A local expiry is not evidence that nothing was charged (see PaymentGrace
// and the quoteTransitions comment: "A local timer is not evidence of payment
// failure. Reconcile late money."). Yet NeedsSupplierPoll used to answer
// `false` for exactly that quote — `expired` was polled only when money had
// ALREADY been observed, and money can only be observed by polling. So the
// sweep and the poll gate combined into a one-way door:
//
//	payment lands → poller was asleep / backed off / breaker-open / starved
//	              → window lapses → SweepExpiredQuotes marks it expired
//	              → NeedsSupplierPoll() == false → the supplier is NEVER asked
//	              → a paid order is forgotten forever.
//
// Railway's idle-sleep and the tracker's own circuit breaker (up to 15 min)
// both make that reachable in production.
//
// THREE DAYS, not one (owner, 2026-10-17: "expired olsa bile 6 saatte bir
// bak expired olanlara 3 gün boyunca; durum ödenire çevrilirse"). A supplier
// can take a long time to move an order out of its own waiting state — a
// manual-action queue, a weekend, a refund review — and the shop has no way
// to predict that. Cost is not the reason to be stingy here: at the 6-hour
// reconcile cadence a quote is asked about ~12 times over the whole window,
// so even a thousand abandoned carts a day stay far below the per-tick poll
// cap and never compete with a buyer who is on the pay screen (see
// pollIntervalFor / pollRank / capPollPass).
const LateMoneyReconcileWindow = 72 * time.Hour

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
		return q.PaymentObserved || q.withinLateMoneyReconcile(time.Now().UTC())
	case "failed":
		// A supplier-side PaymentSetupFailed never produced a payable
		// invoice, so no late money can land on it: nothing to reconcile.
		return q.PaymentObserved || !strings.EqualFold(q.SupplierStatus, cryptorefills.StatusPaymentSetupFailed)
	}
	return false
}

// withinLateMoneyReconcile reports whether a locally-settled quote is still
// inside its late-money window. A quote with no known deadline at all cannot
// be windowed, so it is not re-polled — it never had a payment window to miss.
func (q Quote) withinLateMoneyReconcile(now time.Time) bool {
	deadline := q.PaymentDeadline()
	if deadline.IsZero() {
		return false
	}
	return now.Before(deadline.Add(LateMoneyReconcileWindow))
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
	q.LightningSwapID = ""
	if delivered := q.DeliveryTime(); !delivered.IsZero() {
		q.DeliveredAt = &delivered
	}
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

// idempotencyKeySettled answers, inside the caller's transaction, whether an
// Idempotency-Key is free to start a NEW quote: it is unbound, its binding
// dangles (the quote record is gone), or the quote it is bound to is settled
// (CheckoutSettledAt). A key bound to an unresolved quote is NOT free — that
// request is a retry and must replay the existing quote.
func idempotencyKeySettled(tx *badger.Txn, user, key string, now time.Time) (bool, error) {
	boundID, err := getString(tx, quoteIdempotencyIndexKey(user, key))
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var bound Quote
	switch err := getJSON(tx, quoteKey(boundID), &bound); {
	case errors.Is(err, ErrNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	return CheckoutSettledAt(bound, now), nil
}

// An alias is durable for as long as the quote is unresolved. Without this, a
// second tab's new idempotency key could create a new order while the first
// one is still being paid. Once the bound quote is SETTLED the alias is spent
// (CheckoutSettledAt) and the key may move on to a new quote — the same rule
// the atomic creation gate applies, so a stale key can never strand a buyer
// on a finished order.
func (s *Store) BindQuoteIdempotency(user, key, fingerprint, quoteID string) error {
	now := time.Now().UTC()
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(quoteID), &q); err != nil {
			return err
		}
		if q.UserID != user {
			return ErrConflict
		}
		indexKey := quoteIdempotencyIndexKey(user, key)
		existing, err := getString(tx, indexKey)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		rebinding := err == nil && existing != quoteID
		if rebinding {
			settled, serr := idempotencyKeySettled(tx, user, key, now)
			if serr != nil {
				return serr
			}
			if !settled {
				return ErrConflict
			}
		}
		if err := tx.Set(indexKey, []byte(quoteID)); err != nil {
			return err
		}
		if !rebinding {
			previous, err := getString(tx, quoteRequestFingerprintKey(user, key))
			if err == nil && previous != fingerprint {
				return ErrConflict
			}
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
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
		// The partner API reports what this order earns the shop. Stored once
		// with the order id (both live creates and crash recoveries land
		// here) so the earning is auditable per order; it is never read back
		// into a price. A later observation without a rate never erases one
		// that was already reported.
		if rate, ok := order.CommissionRate(); ok && q.SupplierCommissionRate == 0 {
			q.SupplierCommissionRate = rate
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
			if q.DeliveredAt == nil && !observedAt.IsZero() {
				t := observedAt
				q.DeliveredAt = &t
			}
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
