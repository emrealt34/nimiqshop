package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/money"
	"nimiqshop/internal/stakeledger"
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
	// A local timer is not evidence of payment failure. Reconcile late money.
	"expired":       {"payment_started": true, "payment_received": true, "delivering": true, "fulfilled": true, "refunded": true, "manual_review": true, "failed": true},
	"failed":        {"payment_started": true, "payment_received": true, "delivering": true, "fulfilled": true, "manual_review": true, "refunded": true},
	"manual_review": {"awaiting_payment": true, "payment_started": true, "payment_received": true, "delivering": true, "fulfilled": true, "expired": true, "failed": true, "refunded": true},
	"fulfilled":     {"refunded": true},
	"refunded":      {},
}

func canQuoteTransition(from, to string) bool {
	return quoteTransitions[from][to]
}

// ErrLiveDuplicate is returned when CreateQuoteWithDailyLimits finds an
// already-live quote for the same cart. The handler MUST return that quote
// instead of calling CreateOrder again — a second supplier order would be
// a double purchase.
type ErrLiveDuplicate struct{ Quote Quote }

func (e *ErrLiveDuplicate) Error() string { return "live duplicate quote" }

// CartEqual reports whether two quotes are the same purchase (product +
// face + quantity + delivery target). Used to collapse double-clicks and
// crash retries into one supplier order.
func CartEqual(a, b Quote) bool {
	if a.PurchaseFingerprint != "" && b.PurchaseFingerprint != "" {
		return a.PurchaseFingerprint == b.PurchaseFingerprint
	}
	if a.ProductID != b.ProductID || a.ProductCountry != b.ProductCountry {
		return false
	}
	if a.Denomination != b.Denomination || a.ProductValue != b.ProductValue || a.Quantity != b.Quantity {
		return false
	}
	if a.CashbackCode != b.CashbackCode {
		return false
	}
	return cartBeneficiary(a) == cartBeneficiary(b)
}

func cartBeneficiary(q Quote) string {
	if q.BeneficiaryAccount != "" {
		return q.BeneficiaryAccount
	}
	if q.PhoneNumber != "" {
		return q.PhoneNumber
	}
	return q.CustomerEmail
}

// CreateQuoteWithDailyLimits inserts the write-ahead quote intent with the
// user's daily order/spend check inside the SAME transaction. The previous
// read-then-write check was racy under concurrent requests.
func (s *Store) CreateQuoteWithDailyLimits(q Quote, maxOrders int, maxSpend money.Micros, since time.Time) error {
	now := since.Add(24 * time.Hour)
	return s.createQuoteWithPurchaseLimits(q, maxOrders, maxSpend, 0, since, PurchaseMonthStart(now), now, QuoteOptions{})
}

// QuoteOptions carries the optional, per-call extras the atomic quote gate
// applies. It is variadic so every existing call site (and every test) keeps
// compiling with exactly its previous behaviour.
type QuoteOptions struct {
	// CashbackCode is the promo code redeemed on this quote. When non-empty
	// the gate reserves one redemption slot in the SAME transaction as the
	// quote, so a crash can never leave the two disagreeing. See
	// internal/db/cashback_codes.go.
	CashbackCode string
	// CashbackCodeLimits is the operator's policy for that code. Zero fields
	// mean unlimited, which is what every pre-existing configuration is.
	CashbackCodeLimits CodeLimits
	// CashbackEstimateUSD is charged against the code's total payout budget
	// at reserve time, so a burst of concurrent checkouts cannot collectively
	// overshoot a ceiling that is otherwise only measured at fulfillment.
	CashbackEstimateUSD money.Micros
	// AckActiveCheckout is the buyer's explicit "continue anyway" against the
	// unresolved-checkout hold: the gate still surfaces a live duplicate of
	// the SAME cart (that path just hands back the existing quote) but no
	// longer refuses a DIFFERENT unresolved cart. Operator decision
	// (2026-10-04): a buyer with an active payment must be able to start a
	// new purchase; the button that sets this lives on the hold screen.
	AckActiveCheckout bool
	// MaxAttemptsPerDay bounds how many quotes (PAID OR ABANDONED) one
	// account may open in a rolling 24h. 0 disables.
	//
	// The other half of the abandoned-checkout defence: the blocking index
	// made reading a buyer's history cheap, but an attacker could still
	// CREATE records without limit, and each one costs a supplier dry-run, a
	// Badger write, an fsync and a row the settlement tracker must sweep.
	// The default ceiling (200/day — one every seven minutes around the
	// clock) is unreachable by a human and stops the loop.
	MaxAttemptsPerDay int
}

// ErrQuoteAttemptLimit is returned when the rolling quote-attempt ceiling is
// reached. Distinct from ErrLimit (which is about MONEY) so the handler can
// say "slow down" rather than "you have no budget left".
var ErrQuoteAttemptLimit = errors.New("quote attempt limit reached")

// CreateQuoteWithPurchaseLimits checks rolling daily and UTC calendar monthly
// budgets atomically, preserving the same per-user lock and replay guards.
func (s *Store) CreateQuoteWithPurchaseLimits(q Quote, maxOrders int, maxDaily, maxMonthly money.Micros, now time.Time, opts ...QuoteOptions) error {
	var o QuoteOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return s.createQuoteWithPurchaseLimits(q, maxOrders, maxDaily, maxMonthly, now.Add(-24*time.Hour), PurchaseMonthStart(now), now, o)
}

func (s *Store) createQuoteWithPurchaseLimits(q Quote, maxOrders int, maxSpend, maxMonthly money.Micros, since, monthStart, now time.Time, opts QuoteOptions) error {
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
		// Same effective window as the payment path: documented 30-minute
		// window minus the 5-minute safety buffer (see cryptorefills).
		q.ExpiresAt = q.CreatedAt.Add(cryptorefills.PaymentWindow - cryptorefills.PaymentSafetyBuffer)
	}
	return s.Update(func(tx *badger.Txn) error {
		// Force an optimistic-concurrency conflict between simultaneous
		// requests for the same user (Badger has no predicate locks).
		if _, err := tx.Get(quoteUserLimitLockKey(q.UserID)); err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		if err := tx.Set(quoteUserLimitLockKey(q.UserID), []byte("1")); err != nil {
			return err
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
		// Checkout-safety question, answered through the per-user blocking
		// index (see quote_index.go). This used to scan the buyer's ENTIRE
		// quote history with a JSON decode per record, inside this write
		// transaction — which made "abandon a lot of carts" into a
		// self-amplifying denial of service. The index holds only the quotes
		// that can actually block, which is a handful at most.
		blocking, berr := s.blockingQuotesFor(tx, q.UserID)
		if berr != nil {
			return berr
		}
		// The first blocking quote decides. Either it IS this cart (a live
		// duplicate, surfaced as such so the buyer is sent back to it), or
		// it is a DIFFERENT unresolved cart, which is a hard refusal exactly
		// as the full scan did: changing quantities, recipients or the
		// cashback code must not evade a possibly-paid purchase.
		if len(blocking) > 0 {
			existing := blocking[0]
			sameCart := CartEqual(existing, q)
			renewing := sameCart && LapsedUnpaidInvoice(existing, now)
			if sameCart && !renewing && !opts.AckActiveCheckout {
				return &ErrLiveDuplicate{Quote: existing}
			}
			// Same cart with a lapsed, never-paid invoice: this request IS
			// the renewal the pay screen promises. Neither the live-duplicate
			// bounce nor the active-checkout refusal may fire — both would
			// hand the buyer back the dead quote and strand them on a screen
			// with no way to start (live bug, 2026-10-04). The lapsed quote
			// keeps its own settlement path; if money ever shows up on it the
			// observed flags remove it from this predicate and it blocks
			// again like any unresolved checkout.
			if !renewing && !opts.AckActiveCheckout {
				return &ErrActiveCheckout{Quote: existing}
			}
			// Acked or renewing: fall through and create the fresh quote
			// beside the unresolved one.
		}
		count := 0
		attempts := 0
		spend := money.Micros(0)
		monthlySpend := money.Micros(0)
		// Bounded by the OLDER of the two windows: monthStart is always at
		// least as old as the rolling 24h window, so seeking there reads
		// exactly the records both predicates care about and never the
		// buyer's whole history.
		windowStart := monthStart
		if since.Before(windowStart) {
			windowStart = since
		}
		if err := scanQuotesSince(tx, q.UserID, windowStart, func(existing Quote) error {
			// The attempt ceiling counts EVERY quote opened in the rolling
			// 24h window regardless of outcome — that is the point: an
			// abandoned checkout costs the shop as much as a paid one.
			//
			// The CreatedAt guard is load-bearing, not decorative. This scan
			// starts at min(since, monthStart), so at the top of a month it
			// reaches back far further than 24 hours in order to serve the
			// MONTHLY budget predicate from the same pass. Counting every
			// record it visits would charge a returning shopper for last
			// month's orders and lock them out of buying anything — the exact
			// "hardening must never touch a normal user" failure this whole
			// pass exists to prevent.
			if existing.CreatedAt.After(since) {
				attempts++
			}
			// Test-center purchases are not real orders: they neither count
			// against nor consume a buyer's daily order/spend budget.
			if existing.TestMode {
				return nil
			}
			if countsAgainstDailyBudget(existing, monthStart) {
				monthlySpend += existing.ProductUSD
			}
			if !countsAgainstDailyBudget(existing, since) {
				return nil
			}
			count++
			spend += existing.ProductUSD
			return nil
		}); err != nil {
			return err
		}
		if opts.MaxAttemptsPerDay > 0 && attempts >= opts.MaxAttemptsPerDay {
			return ErrQuoteAttemptLimit
		}
		if maxOrders > 0 && count >= maxOrders {
			return ErrLimit
		}
		if maxSpend > 0 && spend+q.ProductUSD > maxSpend {
			return ErrLimit
		}
		if maxMonthly > 0 && monthlySpend+q.ProductUSD > maxMonthly {
			return ErrMonthlyLimit
		}
		// Promo-code redemption slot, reserved in the same transaction as the
		// quote so the accounting and the order can never disagree.
		if err := ReserveCodeRedemption(tx, opts.CashbackCode, q.UserID, q.ID, opts.CashbackCodeLimits, opts.CashbackEstimateUSD, now); err != nil {
			return err
		}
		if _, err := tx.Get(quoteKey(q.ID)); err == nil {
			return ErrConflict
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		err := putQuoteTx(tx, &q)
		if err != nil {
			return err
		}
		if q.RequestFingerprint != "" && q.IdempotencyKey != "" {
			if err = tx.Set(quoteRequestFingerprintKey(q.UserID, q.IdempotencyKey), []byte(q.RequestFingerprint)); err != nil {
				return err
			}
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
		if q.SupplierOrderID != "" && q.SupplierOrderID != supplierOrderID {
			return ErrConflict
		}
		if q.WalletAddress != "" && (q.WalletAddress != walletAddress || q.Coin != coin || q.CoinAmount != coinAmount || q.Network != network) {
			return ErrConflict
		}
		if err := bindUniqueIndex(tx, quoteSupplierOrderIndexKey(supplierOrderID), id); err != nil {
			return err
		}
		if info, err := cryptorefills.DecodeInvoice(walletAddress); err == nil {
			if err := bindUniqueIndex(tx, quotePaymentHashIndexKey(info.PaymentHash), id); err != nil {
				return err
			}
			q.LightningPaymentHash = info.PaymentHash
		}
		q.SupplierOrderID = supplierOrderID
		q.WalletAddress = walletAddress
		q.Coin = coin
		q.CoinAmount = coinAmount
		q.Network = network
		// Lock the NIM cashback equivalent of the USDT invoice shown by the
		// shop, atomically with attachment. Never use a later received amount,
		// wallet gas charge or a denomination/product-name inference.
		if strings.EqualFold(coin, "USDT") && q.NimUsdRate > 0 {
			if amount := positiveCoinUnits(coinAmount); amount > 0 {
				q.EstimatedNIM = amount / q.NimUsdRate
			}
		}
		if !q.PaymentExpiry.IsZero() && (paymentExpiry.IsZero() || paymentExpiry.After(q.PaymentExpiry)) {
			paymentExpiry = q.PaymentExpiry
		}
		if !paymentExpiry.IsZero() {
			q.PaymentExpiry = paymentExpiry
			q.ExpiresAt = paymentExpiry
		}
		return nil
	})
}

// transitionQuote applies a conditional state transition: it loads the
// quote, checks canQuoteTransition, applies mutate (with the open
// transaction for extra index writes), and rewrites the record + status
// index atomically.
func (s *Store) transitionQuote(id, to string, mutate func(*Quote, *badger.Txn) error) error {
	// Resolve the buyer's pool stake BEFORE the transaction opens. The
	// lookup is a network call and Update() replays the closure on every
	// optimistic-concurrency conflict, so asking inside would hold a write
	// transaction across a round trip and re-hit the pool on each retry.
	var stake StakerStake
	var ledgerParams stakeledger.Params
	var buyerAddr string
	if to == "fulfilled" {
		stake = s.resolveStakerStake(id)
		ledgerParams = s.StakeLedgerParamsForTx()
		buyerAddr = s.quoteBuyerAddress(id)
	}

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
		err := putQuoteTx(tx, &q)
		if err != nil {
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
		// Promo-code accounting follows the quote's lifecycle. A redemption
		// is RESERVED when the quote is created, COMMITTED here (with the
		// cashback the engine actually computed) and RELEASED if the quote
		// ends without paying — otherwise abandoned carts would permanently
		// eat a promotion's budget. Both moves happen inside this same
		// transaction, so the counter and the order can never disagree.
		if q.CashbackCode != "" {
			switch to {
			case "fulfilled":
				if err = CommitCodeRedemption(tx, q.CashbackCode, q.UserID, q.ID, cashbackUSDForQuote(tx, &q), time.Now().UTC()); err != nil {
					return err
				}
			case "expired", "failed", "refunded":
				if err = ReleaseCodeRedemption(tx, q.CashbackCode, q.UserID, q.ID, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
		if to == "fulfilled" {
			if err = publishToFeed(tx, &q); err != nil {
				return err
			}
			if err = enqueueCashbackOnFulfill(tx, &q, stake, ledgerParams, s.cashbackEnrich()); err != nil {
				return err
			}
			// The buyer was not VERIFIABLY staked at delivery time (no
			// stake, a wallet stake the pool has not indexed yet, or the
			// pool was unreachable). Schedule the trust-free re-ask: the
			// shop keeps querying its own pool about this buyer for an
			// hour and upgrades the cashback the moment the pool says
			// "staked". Written in the SAME transaction as the delivery
			// itself, so a crash can never lose it.
			if !stake.Staked && buyerAddr != "" {
				now := time.Now().UTC()
				if err = recordStakeRecheckTx(tx, StakeRecheck{
					QuoteID:   q.ID,
					UserID:    q.UserID,
					Address:   buyerAddr,
					CreatedAt: now,
					Deadline:  now.Add(StakeRecheckWindow),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return err
}

// quoteBuyerAddress loads the wallet address behind a quote's buyer (used to
// schedule the post-fulfillment stake re-ask).
func (s *Store) quoteBuyerAddress(quoteID string) string {
	var addr string
	_ = s.View(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(quoteID), &q); err != nil || q.UserID == "" {
			return err
		}
		var u User
		if err := getJSON(tx, userKey(q.UserID), &u); err != nil {
			return err
		}
		addr = u.NimiqAddress
		return nil
	})
	return addr
}

// stakerLookupTimeout bounds the pool round trip on the fulfillment path.
// The pool answers from its own database, so this is generous — it exists so
// a hung pool can never stall delivery.
const stakerLookupTimeout = 5 * time.Second

// resolveStakerStake reads the quote's buyer address and asks the operator's
// pool how much NIM that address has delegated there. The pool is the SINGLE
// source of stakeness: every failure mode degrades to "not staked" — a pool
// outage must delay nothing and must never be upgraded into a cashback boost
// (the last durable POSITIVE pool observation is the only circuit breaker,
// and the post-fulfillment stake recheck re-asks the pool every 30 seconds for
// an hour so an index-lagged staker is upgraded within minutes, trust-free).
func (s *Store) resolveStakerStake(quoteID string) StakerStake {
	if !s.HasStakerLookup() {
		return StakerStake{}
	}

	var address string
	err := s.View(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(quoteID), &q); err != nil {
			return err
		}
		if q.UserID == "" {
			return ErrNotFound
		}
		var u User
		if err := getJSON(tx, userKey(q.UserID), &u); err != nil {
			return err
		}
		address = u.NimiqAddress
		return nil
	})
	if err != nil || address == "" {
		return StakerStake{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), stakerLookupTimeout)
	defer cancel()
	now := time.Now().UTC()
	st, _, lerr := s.lookupStake(ctx, address)
	stakeLuna, staked := st.StakeLuna, st.Staked
	if lerr != nil {
		// Pool unreachable: leave every clock exactly as it is. Use the last
		// positive local POOL observation as a read-only circuit
		// breaker. An upstream outage must not silently turn a known staker
		// into a non-staker (which would lose both the staker rate and loyalty
		// continuity). We never invent a new stake: only a previously stored
		// positive observation is eligible, and the normal ledger caps still
		// apply. A later authoritative zero is what can clear it.
		if cached := s.CachedStakerStake(address, now); cached.Staked {
			_, _ = s.ReconcileStakerCashback(address, cached, now)
			return cached
		}
		return StakerStake{}
	}
	if !staked || stakeLuna <= 0 {
		// A DEFINITIVE "not staked": record it. observeStake clears the v1
		// clock only once the zero state has been continuous for the grace
		// window; ObserveStakeLedger applies r = 0 → A = 0 (full withdrawal
		// is a full reset).
		_, _ = s.observeStake(address, 0, now)
		_ = s.ObserveStakeLedger(address, 0, now)
		return StakerStake{}
	}
	// Record the observation so the loyalty clock advances, and read back how
	// long this stake has stayed positive for the locked-time bonus.
	watch, err := s.observeStake(address, stakeLuna, now)
	lockedDays := 0
	if err == nil {
		lockedDays = watch.stakeLockedDays(now)
	}
	// The single-ledger book keeps its own stake-weighted clock: an
	// increase dilutes loyalty age, a decrease shrinks the book pro rata.
	_ = s.ObserveStakeLedger(address, stakeLuna, now)
	resolved := StakerStake{StakeLuna: stakeLuna, Staked: true, LockedDays: lockedDays, BaseBps: st.BaseBps}
	// If an earlier fulfillment happened while the pool was unreachable,
	// upgrade its still-unpaid base row now. This is idempotent and survives a
	// process restart because the row itself is the durable recovery queue.
	_, _ = s.ReconcileStakerCashback(address, resolved, now)
	return resolved
}

// UserStakerStake resolves a buyer's pool standing directly from their user
// id (no quote involved): the same lookup + loyalty observation the fulfill
// path uses, best-effort. Every failure degrades to "not staked" so a pool
// outage can never block quote creation.
func (s *Store) UserStakerStake(ctx context.Context, userID string) StakerStake {
	if !s.HasStakerLookup() || userID == "" {
		return StakerStake{}
	}

	var address string
	err := s.View(func(tx *badger.Txn) error {
		var u User
		if err := getJSON(tx, userKey(userID), &u); err != nil {
			return err
		}
		address = u.NimiqAddress
		return nil
	})
	if err != nil || address == "" {
		return StakerStake{}
	}

	cctx, cancel := context.WithTimeout(ctx, stakerLookupTimeout)
	defer cancel()
	st, _, lerr := s.lookupStake(cctx, address)
	stakeLuna, staked := st.StakeLuna, st.Staked
	now := time.Now().UTC()
	if lerr != nil {
		// Profile reads are allowed to be stale during a pool outage. Prefer
		// the last positive observation to showing a known staker as zero;
		// this is display/promise preservation only, not a new entitlement.
		if cached := s.CachedStakerStake(address, now); cached.Staked {
			_, _ = s.ReconcileStakerCashback(address, cached, now)
			return cached
		}
		return StakerStake{}
	}

	if !staked || stakeLuna <= 0 {
		_, _ = s.observeStake(address, 0, now)
		_ = s.ObserveStakeLedger(address, 0, now)
		return StakerStake{}
	}
	watch, werr := s.observeStake(address, stakeLuna, now)
	lockedDays := 0
	if werr == nil {
		lockedDays = watch.stakeLockedDays(now)
	}
	_ = s.ObserveStakeLedger(address, stakeLuna, now)
	resolved := StakerStake{StakeLuna: stakeLuna, Staked: true, LockedDays: lockedDays, BaseBps: st.BaseBps}
	_, _ = s.ReconcileStakerCashback(address, resolved, now)
	return resolved
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
		if q.Status != "order_creating" || !q.SupplierRequestAt.IsZero() || q.SupplierOrderID != "" {
			return ErrConflict
		}
		now := time.Now().UTC()
		q.SupplierRequestAt = now
		q.OrderAttempts++
		q.UpdatedAt = now
		return putQuoteTx(tx, &q)
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
		return putQuoteTx(tx, &q)
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
			// Stop offering payment at ExpiresAt, but keep observing the supplier.
			if !now.After(q.ExpiresAt) {
				continue
			}
			// Supplier-linked quotes are only expired locally when they are
			// overdue by the safety buffer AND no money was observed AND the
			// supplier itself has not reported any payment-beyond state. A
			// lapsed single-use Lightning invoice can no longer be paid, so this
			// can never manufacture a duplicate charge; it simply lets the buyer
			// move on from an abandoned order. Quotes where money was seen stay
			// awaiting so fulfillment polling NEVER stops.
			if q.SupplierOrderID != "" {
				if q.PaymentObserved || cryptorefills.IsPaidOrBeyond(q.SupplierStatus) || now.Before(q.ExpiresAt.Add(quoteExpiryBuffer)) {
					continue
				}
			}
			if err := s.transitionQuote(q.ID, "expired", nil); err == nil {
				expired++
			}
		}
	}
	return expired, nil
}

// PurgeTestQuotes hard-deletes every quote stamped TestMode — the admin
// sandbox purchases and TEST_MODE quotes (owner, 2026-10-05: the fake test
// orders leave the shop for good). Real orders carry TestMode=false and are
// never touched. The idempotency index entry goes with its quote so the same
// buyer key can never point at a deleted record.
func (s *Store) PurgeTestQuotes() (int, error) {
	type victim struct {
		id     string
		userID string
		idem   string
	}
	var victims []victim
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixQuote), func(item *badger.Item) error {
			var q Quote
			if err := item.Value(func(value []byte) error { return unmarshal(value, &q) }); err != nil {
				return err
			}
			if q.TestMode {
				victims = append(victims, victim{id: q.ID, userID: q.UserID, idem: q.IdempotencyKey})
			}
			return nil
		})
	})
	if err != nil || len(victims) == 0 {
		return 0, err
	}
	err = s.Update(func(txn *badger.Txn) error {
		for _, v := range victims {
			if err := txn.Delete(quoteKey(v.id)); err != nil {
				return err
			}
			if v.userID != "" && v.idem != "" {
				if err := txn.Delete(quoteIdempotencyIndexKey(v.userID, v.idem)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(victims), nil
}

// PurgeQuotesAllExceptUnipin is the owner's "wipe the order DB, keep ONLY the
// real UniPin purchase" button (2026-10-05). Keep-rule: a non-simulated quote
// whose product id names UniPin. Safety: when no such quote exists NOTHING is
// deleted (an empty keep-set means the rule misfired, not that the shop is
// empty). Supplier order rows whose quote vanishes go with it.
func (s *Store) PurgeQuotesAllExceptUnipin() (int, int, error) {
	type victim struct {
		id     string
		userID string
		idem   string
	}
	var victims []victim
	keep := 0
	err := s.View(func(txn *badger.Txn) error {
		return scanJSONPrefix(txn, []byte(prefixQuote), func(item *badger.Item) error {
			var q Quote
			if err := item.Value(func(value []byte) error { return unmarshal(value, &q) }); err != nil {
				return err
			}
			if !q.TestMode && strings.Contains(strings.ToLower(q.ProductID), "unipin") {
				keep++
				return nil
			}
			victims = append(victims, victim{id: q.ID, userID: q.UserID, idem: q.IdempotencyKey})
			return nil
		})
	})
	if err != nil {
		return 0, 0, err
	}
	if keep == 0 {
		return 0, 0, errors.New("no real UniPin order found — refusing to delete everything")
	}
	err = s.Update(func(txn *badger.Txn) error {
		for _, v := range victims {
			if err := txn.Delete(quoteKey(v.id)); err != nil {
				return err
			}
			if v.userID != "" && v.idem != "" {
				if err := txn.Delete(quoteIdempotencyIndexKey(v.userID, v.idem)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	// The legacy o: order rows carry no quote link and no test stamp; the
	// buyer-facing "orders" everywhere in the shop ARE the quotes, so the
	// quote wipe is the wipe the owner sees.
	return len(victims), 0, nil
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

		if e := putQuoteTx(tx, &q); e != nil {
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
// (the gift email — the one channel). The marker is independent of the supplier state so it
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
		return putQuoteTx(tx, &q)
	})
}

// publishToFeed adds a fulfilled quote to the public feed index (idempotent:
// the index key is deterministic, so replays are no-ops). Anonymous purchases
// are indexed too; the public handler publishes the product/status summary but
// omits the buyer identity and payment transaction details.
func publishToFeed(tx *badger.Txn, q *Quote) error {
	if q.Status != "fulfilled" {
		return nil
	}
	return tx.Set(feedQuoteIndexKey(q.UpdatedAt.UnixNano(), q.ID), []byte(q.ID))
}

// SetQuoteNIMSnapshot stores the NIM/USD rate and estimated NIM cost used
// later to lock the cashback amount. Safe to call on any non-terminal quote.
func (s *Store) SetQuoteNIMSnapshot(id string, nimUsdRate, estimatedNIM float64) error {
	if nimUsdRate <= 0 && estimatedNIM <= 0 {
		return nil
	}
	return s.Update(func(tx *badger.Txn) error {
		var q Quote
		if err := getJSON(tx, quoteKey(id), &q); err != nil {
			return err
		}
		if nimUsdRate > 0 {
			q.NimUsdRate = nimUsdRate
		}
		if estimatedNIM > 0 {
			q.EstimatedNIM = estimatedNIM
		}
		q.UpdatedAt = time.Now().UTC()
		return putQuoteTx(tx, &q)
	})
}
