// Package settlement owns the background fulfillment worker for the
// Cryptorefills rail.
//
// There is no customer wallet, treasury or refund signer on this server:
// money moves directly between the customer and the supplier. The worker's
// two jobs are therefore pure observation:
//
//  1. Poll each non-terminal supplier order (GET /v5/orders/{id}) and apply
//     the conditional local transition — the webhook (when configured) is
//     an acceleration, polling is the guarantee.
//
//  2. Resolve order-creation crash windows using the durable request-start
//     marker (Quote.SupplierRequestAt, committed before the CreateOrder
//     call):
//
//     marker zero  -> no supplier request was ever dispatched, so no
//     supplier order exists; the intent is re-dispatched
//     (bounded) and the purchase completes.
//     marker set   -> the request left (or left and lost its response);
//     the supplier has no order-listing endpoint, so the
//     id cannot be re-attached. Past the stale bound the
//     quote is flagged manual_review — visible, never
//     silent. The unpaid supplier order simply expires;
//     no money was ever at risk because the customer
//     never received the wallet address.
//
// POLLING DISCIPLINE (the anti-hammer design):
//
// The tracker never polls every order on every tick. Each quote has its own
// due time computed from its state and age:
//
//	awaiting_payment  0-2min old      -> every 15s   (customer on the page)
//	awaiting_payment  2-5min old      -> every 30s
//	awaiting_payment  5-15min old     -> every 60s
//	awaiting_payment  15min+ old      -> every 120s  (about to expire anyway)
//	payment_started / received        -> every 10s   (delivery is imminent)
//	delivering                        -> every 8s
//
// On top of that: at most MaxPollsPerTick supplier calls per pass, a
// per-quote exponential backoff (30s -> 30min, jittered) after transient
// errors, a global breaker when failures cluster (supplier-wide budget
// exhaustion), and a pass-level skip while the endpoint cooldown says the
// next admission would be rejected anyway. Transient failures NEVER flag
// orders for manual review — only definitive supplier 4xx answers do.
package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"sync"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/safe"
)

const (
	// MaxPollsPerTick bounds supplier order-polls per tracker pass. With a
	// 4s tick this is ≤2 polls/s upstream worst case, regardless of how
	// many orders are open — one buyer can no longer stampede the partner
	// account by opening many tabs.
	MaxPollsPerTick = 8

	// breakerWindow is the rolling window in which breakerThreshold
	// transient failures trip the global breaker.
	breakerWindow = 60 * time.Second
	// breakerThreshold: 8 failures/min means the endpoint is effectively
	// down for us (supplier 429 window or network) — pause everything.
	breakerThreshold = 8
	// breakerBaseOpen is the first pause duration; each consecutive trip
	// doubles it up to breakerMaxOpen.
	breakerBaseOpen = 2 * time.Minute
	breakerMaxOpen  = 15 * time.Minute

	// errLogDedupWindow: the same backoff message for the same quote is
	// logged at most once per window. This alone kills the log flood from
	// the old behavior (one line per order per 4s tick).
	errLogDedupWindow = 10 * time.Minute

	// cooldownSkipLogEvery: while a pass is skipped due to endpoint
	// cooldown, log that fact at most once per window.
	cooldownSkipLogEvery = 60 * time.Second
)

type OrderTracker struct {
	Store      *db.Store
	CR         *cryptorefills.Client
	Interval   time.Duration
	StaleAfter time.Duration
	MaxOrders  int

	// Per-quote poll state (process-local by design: a restart resets the
	// backoff clock, which is the right thing — restarting is the
	// operator's signal that the upstream budget had time to recover).
	pollMu       sync.Mutex
	nextPollAt   map[string]time.Time // quoteID -> earliest next poll
	consecErrors map[string]int       // quoteID -> consecutive transient errors
	lastErrLog   map[string]errLog    // quoteID -> last logged error (dedup)

	// Global circuit breaker. Per-quote backoff handles one bad order;
	// the breaker handles a bad supplier or a thundering-herd stampede.
	// breakerThreshold failures in any rolling breakerWindow open the
	// breaker; every poll pass is paused while open. Each consecutive
	// trip doubles the pause up to breakerMaxOpen; any success resets.
	breakerMu        sync.Mutex
	breakerWindowAt  time.Time
	breakerWindowErr int
	breakerOpenedAt  time.Time
	breakerReopensAt time.Time
	breakerStreak    int

	// skipLogAt dedups the pass-skip log lines.
	skipLogAt time.Time
}

type errLog struct {
	msg string
	at  time.Time
}

// Run is the tracker loop. Each iteration is panic-guarded: the worst
// outcome of any panic is one skipped poll pass, never a process outage.
func (w *OrderTracker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Second
	}
	if w.StaleAfter <= 0 {
		w.StaleAfter = 300 * time.Second
	}
	if w.MaxOrders <= 0 {
		w.MaxOrders = 100
	}
	w.pollMu.Lock()
	w.nextPollAt = make(map[string]time.Time)
	w.consecErrors = make(map[string]int)
	w.lastErrLog = make(map[string]errLog)
	w.pollMu.Unlock()

	safe.Go("settlement:tracker", func() {
		t := time.NewTicker(w.Interval)
		defer t.Stop()
		for {
			func() {
				defer safe.Guard("settlement:tracker:tick", func(panicked bool) {
					if panicked {
						select {
						case <-time.After(2 * time.Second):
						case <-ctx.Done():
						}
					}
				})
				w.tick(ctx)
			}()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
}

func (w *OrderTracker) tick(ctx context.Context) {
	// 1) Local expiry sweep: releases daily-limit slots for unpaid quotes.
	if n, err := w.Store.SweepExpiredQuotes(time.Now().UTC(), 200); err == nil && n > 0 {
		log.Printf("tracker: expired %d unpaid quotes locally", n)
	}

	// 2) Crash-window resolution for order-creation intents (local DB work
	// only — no supplier budget is consumed here).
	creating, err := w.Store.ListQuotesByStatus(cryptorefills.QuoteOrderCreating, 100)
	if err == nil {
		for _, q := range creating {
			w.resolveCreating(ctx, q)
		}
	}

	// 3) Global breaker check: while open, no order polls at all.
	if until, open := w.breakerOpen(); open {
		w.logSkipOnce(fmt.Sprintf("tracker: order polling paused by circuit breaker until %s", until.UTC().Format(time.RFC3339)))
		return
	}

	// 4) Endpoint cooldown check: if the scheduler says the next order
	// poll would be rejected anyway (supplier 429 window / remote budget),
	// skip the entire pass. One supplier-state question costs nothing;
	// burning one rejection per order per tick is what used to flood the
	// log and deepen the budget hole.
	if wait := w.CR.OrderPollWait(); wait > 2*time.Second {
		w.logSkipOnce(fmt.Sprintf("tracker: order polling paused %s (order endpoint cooling down; supplier budget recovering)", wait.Round(time.Second)))
		return
	}

	// 5) Bulk budget gate — refuse this entire pass if our own supplier
	// queue is already loaded. Prevents a thundering-herd burst from
	// every tracker tick re-driving the same 429 window.
	stats := w.CR.QueueStats()
	if stats.Queued >= w.MaxOrders {
		w.logSkipOnce(fmt.Sprintf("tracker: supplier queue full (%d/%d), skipping this pass", stats.Queued, w.MaxOrders))
		return
	}

	// 6) Poll only DUE quotes, highest priority first, capped per pass.
	tracked, err := w.Store.ListQuotesByStatuses([]string{
		cryptorefills.QuoteAwaitingPay,
		cryptorefills.QuotePaidStarted,
		cryptorefills.QuotePaidReceived,
		cryptorefills.QuoteDelivering,
	}, w.MaxOrders)
	if err != nil {
		log.Printf("tracker: list tracked quotes: %v", err)
		return
	}
	now := time.Now()
	due := make([]db.Quote, 0, len(tracked))
	for _, q := range tracked {
		if ctx.Err() != nil {
			return
		}
		if w.markDue(q, now) {
			due = append(due, q)
		}
	}
	if len(due) == 0 {
		return
	}
	// Priority: delivering > payment_received > payment_started >
	// awaiting_payment. Within a class, keep DB order (newest first).
	sortQuotesByPriority(due)
	if len(due) > MaxPollsPerTick {
		due = due[:MaxPollsPerTick]
	}
	for _, q := range due {
		if ctx.Err() != nil {
			return
		}
		w.trackOne(ctx, q)
	}
}

// logSkipOnce logs a skip reason at most once per cooldownSkipLogEvery so
// a stuck supplier can never flood the log again.
func (w *OrderTracker) logSkipOnce(msg string) {
	w.breakerMu.Lock()
	defer w.breakerMu.Unlock()
	if time.Since(w.skipLogAt) < cooldownSkipLogEvery {
		return
	}
	w.skipLogAt = time.Now()
	log.Print(msg)
}

// markDue reports whether this quote should be polled now, and (re)arms
// its due time. First sight of a quote is staggered pseudo-randomly across
// its natural interval so a restart does not poll-burst every order at
// once.
func (w *OrderTracker) markDue(q db.Quote, now time.Time) bool {
	interval := pollIntervalFor(q, now)
	w.pollMu.Lock()
	defer w.pollMu.Unlock()
	next, ok := w.nextPollAt[q.ID]
	if !ok {
		// Stagger: deterministic hash of the quote id spread over the
		// interval. No quotes are due instantly after a restart.
		h := fnv.New32a()
		_, _ = h.Write([]byte(q.ID))
		offset := time.Duration(h.Sum32()%uint32(interval)) //nolint:gosec // hash mod is fine
		w.nextPollAt[q.ID] = now.Add(offset)
		return false
	}
	if now.Before(next) {
		return false
	}
	w.nextPollAt[q.ID] = now.Add(interval)
	// Opportunistic pruning: if the maps grew large (long-lived process,
	// many churned quotes), drop entries that are no longer tracked.
	if len(w.nextPollAt) > 4096 {
		for id, t := range w.nextPollAt {
			if t.Before(now.Add(-2 * time.Hour)) {
				delete(w.nextPollAt, id)
				delete(w.consecErrors, id)
				delete(w.lastErrLog, id)
			}
		}
	}
	return true
}

// pollIntervalFor is the adaptive schedule. Fresh awaiting_payment orders
// poll fast (the customer is actively on the payment page and the webhook
// may not be configured), then decay as the order ages. Paid orders poll
// fast because delivery typically completes within seconds.
func pollIntervalFor(q db.Quote, now time.Time) time.Duration {
	base := q.UpdatedAt
	if base.IsZero() {
		base = q.CreatedAt
	}
	age := now.Sub(base)
	if age < 0 {
		age = 0
	}
	switch q.Status {
	case cryptorefills.QuoteDelivering:
		return 8 * time.Second
	case cryptorefills.QuotePaidReceived, cryptorefills.QuotePaidStarted:
		return 10 * time.Second
	default: // awaiting_payment and anything else still tracked
		switch {
		case age < 2*time.Minute:
			return 15 * time.Second
		case age < 5*time.Minute:
			return 30 * time.Second
		case age < 15*time.Minute:
			return 60 * time.Second
		default:
			return 120 * time.Second
		}
	}
}

// sortQuotesByPriority orders due quotes so delivery-adjacent states are
// always polled before waiting-for-money states within the capped batch.
func sortQuotesByPriority(qs []db.Quote) {
	rank := func(s string) int {
		switch s {
		case cryptorefills.QuoteDelivering:
			return 0
		case cryptorefills.QuotePaidReceived:
			return 1
		case cryptorefills.QuotePaidStarted:
			return 2
		default:
			return 3
		}
	}
	// Insertion sort: the slice is tiny (≤ MaxOrders).
	for i := 1; i < len(qs); i++ {
		for j := i; j > 0 && rank(qs[j].Status) < rank(qs[j-1].Status); j-- {
			qs[j], qs[j-1] = qs[j-1], qs[j]
		}
	}
}

// creationRecoveryGrace bounds how long a LIVE process may hold an
// order_creating intent without the request-start marker (between the
// write-ahead intent row and the marker commit — microseconds in practice).
// Anything older is a crash remnant, not in-flight work.
const creationRecoveryGrace = 15 * time.Second

// maxCreationAttempts bounds total supplier order-creation attempts for one
// intent (the live call + one tracker re-dispatch). The supplier has no
// idempotency key, so the bound is the duplicate-order guard for the
// recovery path.
const maxCreationAttempts = 2

// resolveCreating owns the order_creating crash window. The durable
// request-start marker (Quote.SupplierRequestAt, committed immediately
// before the CreateOrder call) splits it into two safe halves:
//
//   - marker ZERO: the process died between the write-ahead intent and the
//     marker commit; no request was dispatched, so no supplier order can
//     exist. The intent is re-dispatched (bounded by OrderAttempts) and the
//     purchase completes; a failed re-dispatch flips the quote to failed.
//   - marker SET: the request left (or left and lost its response in the
//     crash). The supplier has no order-listing endpoint, so the order id
//     cannot be re-attached; past the stale bound the quote becomes
//     manual_review — visible, never silent.
//
// The stale clock runs from the marker, NOT from the quote write: the local
// supplier queue may legitimately hold the call for a while, and the
// in-flight round trip is bounded by the supplier call timeout, which the
// config validation requires StaleAfter to exceed.
func (w *OrderTracker) resolveCreating(ctx context.Context, q db.Quote) {
	if ctx.Err() != nil {
		return
	}
	if q.SupplierRequestAt.IsZero() {
		if time.Since(q.UpdatedAt) < creationRecoveryGrace {
			return // still inside the live-process window; act next tick
		}
		if q.OrderAttempts >= maxCreationAttempts {
			// A re-dispatch was already spent (or the bound is exhausted):
			// stop and surface it instead of looping supplier calls.
			if err := w.Store.MarkQuoteManualReview(q.ID, "order-creation recovery bound exhausted: supplier request start was never durable; operator must inspect and re-create the quote"); err == nil {
				log.Printf("CRITICAL tracker: quote %s creation-recovery bound exhausted, flagged manual review", q.ID)
			}
			return
		}
		w.redeliverCreation(ctx, q)
		return
	}
	if time.Since(q.SupplierRequestAt) > w.StaleAfter {
		if err := w.Store.MarkQuoteManualReview(q.ID, "order creation crash window: supplier order may exist but is unattributable (no supplier listing endpoint); the unpaid order expires on the supplier side"); err == nil {
			log.Printf("CRITICAL tracker: quote %s stuck in order_creating since %s, flagged manual review", q.ID, q.SupplierRequestAt.UTC().Format(time.RFC3339))
		}
	}
}

// redeliverCreation re-sends the supplier order for an intent that provably
// never reached the supplier (crash between the write-ahead row and the
// request-start marker). The request is rebuilt from the durable quote
// fields and the outcome is attached through the same conditional
// transition as the live path.
func (w *OrderTracker) redeliverCreation(ctx context.Context, q db.Quote) {
	log.Printf("tracker: quote %s crashed before supplier dispatch; re-dispatching supplier order (attempt %d)", q.ID, q.OrderAttempts+1)
	// Persist the marker FIRST (re-arms the stale clock and spends one
	// attempt) so a second crash cannot be misread as "never sent".
	if err := w.Store.MarkSupplierRequestStarted(q.ID); err != nil {
		return // row moved or DB is sick: the next tick re-evaluates
	}
	// The beneficiary is fixed at quote creation (email for gift cards and
	// eSIMs, normalized E.164 phone for top-ups) and must be re-sent
	// EXACTLY — re-deriving it here (e.g. "phone if present") would deliver
	// a gift card to a phone number and fail with INVALID_BENEFICIARY_ACCOUNT.
	beneficiary := q.BeneficiaryAccount
	if beneficiary == "" {
		// Legacy row created before BeneficiaryAccount existed: preserve the
		// pre-fix recovery behavior (such rows expire within their 30-minute
		// payment window).
		if q.PhoneNumber != "" {
			beneficiary = q.PhoneNumber
		} else {
			beneficiary = q.CustomerEmail
		}
	}
	delivery := cryptorefills.Delivery{
		BrandName:          q.ProductID,
		CountryCode:        q.ProductCountry,
		Denomination:       q.Denomination,
		BeneficiaryAccount: beneficiary,
	}
	if q.ProductValue > 0 {
		v := q.ProductValue
		delivery.Deliverable.ProductValue = &v
	}
	// Email goes to the supplier ONLY when the buyer actually provided one.
	// Top-ups are delivered to the phone number (delivery is by phone), so a
	// top-up quote without a buyer email carries no email at all.
	req := &cryptorefills.CreateOrderRequest{
		Deliveries:  make([]cryptorefills.Delivery, 0, q.Quantity),
		Payment:     cryptorefills.OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: q.Coin, Network: q.Network},
		Lang:        "en",
		Acquisition: &cryptorefills.Acquisition{UTMSource: "nimshop"},
	}
	if q.CustomerEmail != "" {
		req.Email = q.CustomerEmail
		req.User = &cryptorefills.OrderUser{Email: q.CustomerEmail}
	}
	for i := 0; i < q.Quantity; i++ {
		req.Deliveries = append(req.Deliveries, delivery)
	}
	order, err := w.CR.CreateOrder(safeActor(ctx, q.UserID), req)
	if err != nil {
		if _, lerr := w.Store.FailOrderAttempt(q.ID); lerr == nil {
			_ = w.Store.MarkSupplierFailure(q.ID, "crash recovery: supplier order creation failed: "+err.Error())
		}
		log.Printf("tracker: quote %s crash recovery failed: %v", q.ID, err)
		return
	}
	if order.WalletAddress == "" || order.CoinAmount == "" {
		_ = w.Store.MarkQuoteManualReview(q.ID, "crash recovery: supplier created order without a payable wallet address")
		log.Printf("CRITICAL tracker: quote %s recovery created an unpayable supplier order %s", q.ID, order.ID)
		return
	}
	expiry := time.Now().UTC().Add(30 * time.Minute)
	if err := w.Store.AttachQuotePayment(q.ID, order.ID, order.WalletAddress, order.Coin, order.CoinAmount, order.Network, expiry); err != nil {
		_ = w.Store.MarkQuoteManualReview(q.ID, "crash recovery: order created but local attach failed: "+err.Error())
		log.Printf("CRITICAL tracker: quote %s recovery attach failed: %v", q.ID, err)
		return
	}
	log.Printf("tracker: quote %s crash recovery succeeded (supplier order %s, status awaiting_payment)", q.ID, order.ID)
}

// backoffSchedule returns the wait before the next poll attempt after a
// TRANSIENT supplier call failed: 30s, 90s, 4m30s, 13m30s, 30min cap, with
// ±20% jitter so many paused orders do not re-synchronize into a stampede.
func backoffSchedule(consecutive int) time.Duration {
	var d time.Duration
	switch {
	case consecutive <= 1:
		d = 30 * time.Second
	case consecutive == 2:
		d = 90 * time.Second
	case consecutive == 3:
		d = 4*time.Minute + 30*time.Second
	case consecutive == 4:
		d = 13*time.Minute + 30*time.Second
	default:
		d = 30 * time.Minute
	}
	return jitter(d)
}

// jitter widens d by ±20% so many paused orders do not re-synchronize
// into a synchronized stampede when the cooldown lifts.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%d", time.Now().UnixNano()>>20))) // ~ms resolution
	spread := d / 5                                                     // 20%
	return d - spread + time.Duration(h.Sum32()%uint32(2*spread+1)) //nolint:gosec // jitter only
}

// classifyPollError decides whether a GetOrder failure is TRANSIENT
// (back off, retry forever, never bother the operator) or PERMANENT
// (the supplier definitively answered that this order cannot be polled —
// flag manual review so a stuck order is visible).
func classifyPollError(err error) (permanent bool, reason string) {
	var rl *cryptorefills.RateLimitError
	if errors.As(err, &rl) {
		return false, "supplier 429"
	}
	if errors.Is(err, cryptorefills.ErrBudgetWait) || errors.Is(err, cryptorefills.ErrQueueFull) {
		return false, "local supplier queue budget"
	}
	var se *cryptorefills.SupplierError
	if errors.As(err, &se) {
		switch se.Status {
		case http400, http401, http403, http404:
			// The supplier definitively refused: order unknown, partner
			// key problem, or a request the supplier will never accept.
			// Retrying forever cannot fix a 404; surface it.
			return true, fmt.Sprintf("supplier %d", se.Status)
		default:
			// 429 handled above; 5xx and anything else is transient.
			return false, fmt.Sprintf("supplier HTTP %d", se.Status)
		}
	}
	// Timeouts, connection refused/ reset, TLS errors, DNS... all transient.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false, "timed out"
	}
	if _, ok := err.(net.Error); ok {
		return false, "network error"
	}
	return false, "transient error"
}

// Convenience aliases so classifyPollError reads cleanly without importing
// net/http here.
const (
	http400 = 400
	http401 = 401
	http403 = 403
	http404 = 404
)

// noteTransientFailure feeds the global breaker. Returns true when the
// breaker just OPENED (for a single loud log line).
func (w *OrderTracker) noteTransientFailure(now time.Time) bool {
	w.breakerMu.Lock()
	defer w.breakerMu.Unlock()
	if w.breakerWindowAt.IsZero() || now.Sub(w.breakerWindowAt) > breakerWindow {
		w.breakerWindowAt = now
		w.breakerWindowErr = 0
	}
	w.breakerWindowErr++
	if w.breakerWindowErr < breakerThreshold {
		return false
	}
	if !w.breakerReopensAt.After(now) { // not already open
		streak := w.breakerStreak + 1
		open := breakerBaseOpen << (streak - 1) //nolint:gosec // bounded by max below
		if open > breakerMaxOpen || open <= 0 {
			open = breakerMaxOpen
		}
		w.breakerOpenedAt = now
		w.breakerReopensAt = now.Add(open)
		w.breakerStreak = streak
		w.breakerWindowErr = 0
		return true
	}
	return false
}

// breakerOpen returns (reopensAt, true) while the breaker is open.
func (w *OrderTracker) breakerOpen() (time.Time, bool) {
	now := time.Now()
	w.breakerMu.Lock()
	defer w.breakerMu.Unlock()
	if w.breakerReopensAt.After(now) {
		return w.breakerReopensAt, true
	}
	return time.Time{}, false
}

// notePollSuccess resets the breaker streak and clears per-quote state.
func (w *OrderTracker) notePollSuccess(q db.Quote) {
	w.pollMu.Lock()
	delete(w.nextPollAt, q.ID)
	delete(w.consecErrors, q.ID)
	delete(w.lastErrLog, q.ID)
	w.pollMu.Unlock()
	w.breakerMu.Lock()
	w.breakerStreak = 0
	w.breakerMu.Unlock()
}

// trackOne polls one supplier order and applies the conditional transition.
// Transient errors back the quote off exponentially and NEVER escalate;
// permanent supplier refusals flag manual review exactly once.
func (w *OrderTracker) trackOne(ctx context.Context, q db.Quote) {
	order, err := w.CR.GetOrder(safeActor(ctx, q.UserID), q.SupplierOrderID)
	if err != nil {
		permanent, class := classifyPollError(err)
		if permanent {
			// A definitive supplier refusal will not heal by polling.
			// Flag once; manual_review quotes are not polled again.
			if q.Status != cryptorefills.QuoteManualReview {
				if merr := w.Store.MarkQuoteManualReview(q.ID, "supplier refuses to serve this order ("+class+"): "+err.Error()+"; operator must inspect"); merr == nil {
					log.Printf("CRITICAL tracker: quote %s flagged manual review (%s)", q.ID, class)
				}
			}
			w.pollMu.Lock()
			delete(w.nextPollAt, q.ID)
			delete(w.consecErrors, q.ID)
			w.pollMu.Unlock()
			return
		}
		w.pollMu.Lock()
		w.consecErrors[q.ID]++
		consec := w.consecErrors[q.ID]
		backoff := backoffSchedule(consec)
		w.nextPollAt[q.ID] = time.Now().Add(backoff)
		var last errLog
		if l, ok := w.lastErrLog[q.ID]; ok {
			last = l
		}
		w.pollMu.Unlock()
		opened := w.noteTransientFailure(time.Now())
		if opened {
			log.Printf("tracker: TRANSIENT failures clustered (supplier budget storm) — pausing all order polls via circuit breaker")
		}
		if last.msg != class || time.Since(last.at) > errLogDedupWindow {
			w.pollMu.Lock()
			w.lastErrLog[q.ID] = errLog{msg: class, at: time.Now()}
			w.pollMu.Unlock()
			log.Printf("tracker: order %s poll paused %s (attempt %d, %s): %v",
				q.SupplierOrderID, backoff.Round(time.Second), consec, class, err)
		}
		return
	}
	w.notePollSuccess(q)
	if order.ID == "" {
		log.Printf("tracker: supplier returned empty order for %s", q.SupplierOrderID)
		return
	}
	supplierStatus := order.Status
	if supplierStatus == "" {
		log.Printf("tracker: supplier order %s has no status field", q.SupplierOrderID)
		return
	}
	local := cryptorefills.MapToQuoteStatus(supplierStatus)
	switch local {
	case cryptorefills.QuoteFulfilled:
		payload := cryptorefills.FulfillmentPayload(order)
		if err := w.Store.CompleteQuoteWithFulfillment(q.ID, payload); err == nil {
			log.Printf("tracker: quote %s fulfilled (supplier %s)", q.ID, supplierStatus)
			w.notifyFulfilled(q)
		} else if q.Status != local {
			log.Printf("tracker: quote %s fulfillment transition: %v", q.ID, err)
		}
	case cryptorefills.QuoteRefunded:
		var refund []byte
		if order.Refund != nil {
			refund, _ = json.Marshal(order.Refund)
		}
		if err := w.Store.MarkQuoteRefunded(q.ID, refund, "supplier refund: "+supplierStatus); err == nil {
			log.Printf("tracker: quote %s refunded by supplier", q.ID)
		}
	default:
		if local == cryptorefills.QuoteFailed {
			if err := w.Store.MarkSupplierFailure(q.ID, "supplier "+supplierStatus+" for order "+q.SupplierOrderID); err == nil && q.Status != local {
				log.Printf("tracker: quote %s failed at supplier (%s)", q.ID, supplierStatus)
			}
		} else if local == cryptorefills.QuoteManualReview {
			if err := w.Store.MarkQuoteManualReview(q.ID, "supplier manual action required: "+supplierStatus); err == nil && q.Status != local {
				log.Printf("tracker: quote %s needs manual action (%s)", q.ID, supplierStatus)
			}
		} else if local != q.Status {
			if _, err := w.Store.SetSupplierStatus(q.ID, supplierStatus, local); err == nil {
				log.Printf("tracker: quote %s -> %s (supplier %s)", q.ID, local, supplierStatus)
			}
		} else if supplierStatus != q.SupplierStatus {
			_, _ = w.Store.SetSupplierStatus(q.ID, supplierStatus, local)
		}
	}
}

// safeActor gives the tracker a stable, non-user supplier identity so the
// fulfillment budget is one fair actor, not N user actors.
func safeActor(ctx context.Context, _ string) context.Context {
	return cryptorefills.WithActor(ctx, "system:tracker")
}

// Notify is an optional hook (notification fan-out) invoked after a
// fulfillment lands. It runs in its own goroutine by the assignee.
var NotifyFn func(q db.Quote)

// GiftNotifyFn is the seam for the gift channel notifier; main wires it.
// It runs after a quote reaches the fulfilled state and the quote carries
// the buyer-supplied GiftChannel/GiftMessage/GiftRecipientPhone. When this
// hook returns, the notifier has already persisted GiftNotifiedAt so a
// subsequent retry will be a no-op.
var GiftNotifyFn func(q db.Quote)

// GiftHasChannel is a tiny helper so the tracker can decide whether to fire
// the gift notifier without importing the db package's full field set here.
func GiftHasChannel(q db.Quote) bool { return q.GiftChannel != "" }

// Notify is the seam for the notification package; main wires it.
func (w *OrderTracker) notifyFulfilled(q db.Quote) {
	if NotifyFn != nil {
		qq := q
		safe.Go("settlement:notify", func() { NotifyFn(qq) })
	}
	if GiftNotifyFn != nil && GiftHasChannel(q) {
		qq := q
		safe.Go("settlement:gift-notify", func() { GiftNotifyFn(qq) })
	}
}
