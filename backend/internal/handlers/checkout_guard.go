package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"nimiqshop/internal/settlement"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

// Bounded process-local optimization. The database's read/write lock and
// compare-and-set dispatch claim are the durable authority, not this mutex.
var checkoutLocks [256]sync.Mutex

func lockCreateQuote(userID string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	m := &checkoutLocks[h.Sum32()%256]
	m.Lock()
	return m.Unlock
}
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// The buyer's answer to the checkout gate ("continue anyway" / "open a new
// order beside the live one") is NOT part of what is being bought, so it is
// in neither fingerprint. It used to be in both: an order opened through the
// acknowledgement carried the flag in its fingerprints, so the SAME cart
// coming back later (no flag) never matched it — the buyer hit the hard
// ACTIVE_CHECKOUT wall instead of the "go to the open order or open a new
// one" question, and a retry of that order's own key without the flag was an
// IDEMPOTENCY_MISMATCH (found by the full-stack browser run, 2026-10-18).
func singleFingerprints(req createQuoteRequest) (string, string) {
	req.AckActiveCheckout = false
	request := fingerprint(req)
	req.CashbackCode = ""
	req.GiftChannel = ""
	req.GiftMessage = ""
	req.GifterIdenticon = ""
	return request, fingerprint(req)
}
func batchFingerprints(req batchQuoteRequest) (string, string) {
	req.AckActiveCheckout = false
	req.Items = append([]batchQuoteItem(nil), req.Items...)
	sort.Slice(req.Items, func(i, j int) bool { return fingerprint(req.Items[i]) < fingerprint(req.Items[j]) })
	request := fingerprint(req)
	// Cashback code is a display/promo tweak and can change on retry without
	// invalidating the purchase intent. Payment method / coin / network /
	// cashback destination ARE part of the purchase and stay in the
	// purchase fingerprint.
	req.CashbackCode = ""
	return request, fingerprint(req)
}

// How many lapsed invoices a single checkout attempt may ask the supplier
// about. Asking is a GET on the buyer's critical path, and it exists only to
// catch a payment that landed at the very edge of its window — which is at
// most the one or two orders the buyer was just looking at. Anything beyond
// that is the settlement tracker's job (it keeps polling a dead order for the
// whole late-money reconcile window).
const maxLapsedInvoiceAsks = 3

func checkoutKey(ctx *fasthttp.RequestCtx) (string, bool) {
	key := strings.TrimSpace(string(ctx.Request.Header.Peek("Idempotency-Key")))
	if len(key) < 16 || len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		writeError(ctx, 400, "Idempotency-Key is required (16–128 printable characters); reuse it for retries")
		return "", false
	}
	return key, true
}

// Runs BEFORE catalog, price, promo or supplier validation. Retries must work
// even when the catalog changes, a promo code was consumed, or the API is down.
func (h *Handlers) existingCheckout(ctx *fasthttp.RequestCtx, user, key, requestFP, purchaseFP string, ack bool) bool {
	now := time.Now().UTC()
	q, fp, err := h.Store.GetQuoteByIdempotencyRequest(user, key)
	switch {
	case err == nil && db.CheckoutSettledAt(q, now):
		// The key's order is finished (paid and delivered, refunded, or
		// dead without money). The storefront's key is derived from the
		// cart, so this is the buyer purchasing the SAME thing again — a
		// new checkout, not a retry. Replaying the finished order here is
		// exactly how a buyer ended up on a "completed" order page without
		// ever seeing a pay screen (live bug, 2026-10-18). Fall through:
		// the blocking check and the atomic gate treat the key as free and
		// move it onto the new quote.
	case err == nil:
		if fp == "" || fp != requestFP {
			writeJSON(ctx, 409, map[string]any{"error": "idempotency key belongs to a different request", "code": "IDEMPOTENCY_MISMATCH", "quote_id": q.ID, "detail": "Open the original order; do not pay a different cart using this key."})
		} else {
			// A replay is never a creation, and the storefront needs to know
			// the difference: when the handed-back order is a LIVE, unpaid
			// invoice of the same cart, it asks the buyer whether to return
			// to that order or open a new one beside it (ack_active_checkout)
			// instead of silently reopening the old pay screen. Every
			// handback path says `reused` for that reason — see
			// reuseCheckout and quoteReservationConflict.
			h.writeQuoteCreatedExtra(ctx, q, map[string]any{"reused": true})
		}
		return true
	case !errors.Is(err, db.ErrNotFound):
		writeError(ctx, 503, "could not verify checkout idempotency; no order was dispatched")
		return true
	}
	// (1) The buyer's LAPSED, unpaid invoices are not blocking any more —
	// but before the shop hands out a brand-new order it asks the supplier
	// about them. A payment pushed through at the very edge of the window is
	// money the supplier can see and the local record cannot: two orders for
	// one cart may never be paid for twice. The question is a GET, and the
	// ONLY thing the shop ever does with the answer is apply it (money seen
	// → the ordinary settlement path) or ignore it (unpaid or unreachable →
	// the buyer is simply free). Nothing here cancels an order: an abandoned
	// order lapses at the supplier on its own, and withdrawing one is a
	// decision an operator makes per order, from the admin console.
	if lapsed, err := h.Store.LapsedUnpaidQuotesForUser(user, now, 0); err == nil {
		for i, q := range lapsed {
			if i >= maxLapsedInvoiceAsks {
				break // bounded: this is a question on the checkout path, not a sweep
			}
			verified := h.askSupplierAboutALapsedInvoice(ctx, q, now)
			if verified == nil {
				continue
			}
			// Money was found, so the order is live again and it DOES block.
			if db.BlocksNewPurchaseAt(*verified, now) {
				h.activeCheckoutError(ctx, *verified)
				return true
			}
		}
	}
	// (2) Only the quotes that can actually block a new purchase are read.
	// The previous ListQuotesForUser(user, 0) loaded and decoded the buyer's
	// ENTIRE history on every checkout attempt, which turned "abandon a lot
	// of carts" into a way of making one's own (and the server's) checkout
	// path arbitrarily expensive. See internal/db/quote_index.go.
	quotes, err := h.Store.BlockingQuotesForUser(user)
	if err != nil {
		writeError(ctx, 503, "could not verify outstanding checkouts; no order was dispatched")
		return true
	}
	for _, q := range quotes {
		// The release rule lives in db.BlocksNewPurchaseAt, NOT here. It used
		// to be a `continue` in this loop, which made it dead code: the atomic
		// gate inside CreateQuote re-asks the same question through the same
		// index and answered "blocked" for a lapsed invoice forever, so the
		// buyer got ACTIVE_CHECKOUT with no way out (live bug, 2026-10-04).
		// One predicate, one answer for both callers — and it is EVIDENCE,
		// never a clock: money seen, a payment settling, or an operator hold.
		// A lapsed, unpaid invoice is not decided by a timer any more (and it
		// never reaches this loop: it is not blocking, so the index above it
		// is where the supplier is asked about it — see step 1).
		if !db.BlocksNewPurchaseAt(q, now) {
			continue
		}
		if q.PurchaseFingerprint != "" && q.PurchaseFingerprint == purchaseFP && !ack {
			// A lapsed unpaid invoice is not a checkout to return to: the
			// buyer pressed "create a fresh invoice", and handing back the
			// dead quote loops them onto a pay screen with no live invoice
			// (live bug, 2026-10-04). Let creation proceed instead.
			if !db.LapsedUnpaidInvoice(q, now) {
				h.reuseCheckout(ctx, q, key, requestFP)
				return true
			}
			continue
		}
		if !ack {
			h.activeCheckoutError(ctx, q)
			return true
		}
		// Acked ("continue anyway"): this unresolved cart must not block a
		// new purchase — check the rest, then let creation proceed.
		continue
	}
	return false
}

// askSupplierAboutALapsedInvoice replaces the old grace buffer with an ANSWER
// — and it CANCELS NOTHING.
//
// A lapsed invoice used to keep blocking the buyer for a few more minutes in
// case a payment was still in flight; the v6 partner API can settle that
// question directly, so the shop asks it:
//
//   - money observed (or any paid-or-beyond state) → the answer is applied
//     here and the quote keeps blocking. The purchase is settling and the
//     buyer must not be handed a second order to pay for it.
//
//   - no money, or the supplier cannot be asked → the shop does NOTHING to
//     the order. No cancellation, no cleanup, no "releasing" it. The buyer is
//     then free to buy something else, and the abandoned order simply lapses
//     on its own at the supplier, which is the supplier's own business.
//
// Owner (2026-10-19): "sipariş iptali öldürme yapılmaz." Withdrawing an
// order is a one-way door, and the one who decides to walk through it is a
// human looking at that order in the admin console (AdminCancelOrder — which
// re-reads the supplier first and refuses outright when money is involved).
// A pre-check that runs because somebody opened the shop is not that.
//
// It returns the updated quote (nil when nothing changed).
func (h *Handlers) askSupplierAboutALapsedInvoice(ctx *fasthttp.RequestCtx, q db.Quote, now time.Time) *db.Quote {
	if h.CR == nil || !h.CR.PartnerMode() || q.SimulatedSupplier() || q.TestMode {
		return nil
	}
	if cryptorefills.IsTerminal(q.SupplierStatus) && !q.PaymentObserved {
		return nil // already dead upstream and unpaid; nothing left to settle
	}
	sctx := cryptorefills.WithEndUserIP(h.supplierContext(ctx), q.EndUserIP)
	sctx = cryptorefills.WithEndUserAgent(sctx, q.EndUserAgent)
	fresh, err := h.CR.GetOrderFresh(sctx, q.SupplierOrderID)
	if err != nil {
		return nil // could not ask: the ordinary rule decides
	}
	if !cryptorefills.MoneyObserved(fresh) && !cryptorefills.IsPaidOrBeyond(fresh.Status) {
		// Nothing was paid AND the shop still does nothing about it: the
		// order is the supplier's to keep or lapse, not ours to withdraw.
		return nil
	}
	// Money is (or may be) on its way: settle it here, never beside it.
	if _, aerr := settlement.ApplySupplierOrderNotify(h.Store, q.ID, fresh); aerr != nil {
		return nil
	}
	latest, gerr := h.Store.GetQuote(q.ID)
	if gerr != nil {
		return nil
	}
	return &latest
}

func (h *Handlers) activeCheckoutError(ctx *fasthttp.RequestCtx, q db.Quote) {
	writeJSON(ctx, 409, map[string]any{"error": "an unresolved checkout already exists", "code": "ACTIVE_CHECKOUT", "quote_id": q.ID, "status": q.Status, "detail": "Check the existing order before starting another payment. Changing or splitting the cart cannot bypass this safety hold."})
}
func (h *Handlers) reuseCheckout(ctx *fasthttp.RequestCtx, q db.Quote, key, fp string) {
	if err := h.Store.BindQuoteIdempotency(q.UserID, key, fp, q.ID); err != nil {
		writeError(ctx, 503, "could not durably link this retry; no new order was dispatched")
		return
	}
	latest, err := h.Store.GetQuote(q.ID)
	if err != nil {
		writeError(ctx, 503, "could not read existing checkout")
		return
	}
	h.writeQuoteCreatedExtra(ctx, latest, map[string]any{"reused": true})
}

// quoteGateError reports whether err is one of the atomic gate's POLICY
// refusals and, if so, writes the buyer-facing response. It is checked before
// quoteReservationConflict because a limit refusal must never be mistaken for
// an idempotency conflict — the two look identical to errors.Is(err,
// db.ErrConflict) callers and mean completely different things to a shopper.
func (h *Handlers) quoteGateError(ctx *fasthttp.RequestCtx, err error) bool {
	var codeLimit *db.CodeLimitDetail
	if errors.As(err, &codeLimit) {
		h.codeLimitError(ctx, codeLimit)
		return true
	}
	if errors.Is(err, db.ErrQuoteAttemptLimit) {
		limit := h.Cfg.DailyQuoteAttemptLimit
		writeJSON(ctx, fasthttp.StatusTooManyRequests, map[string]any{
			"error":                     "you have opened too many checkouts in the last 24 hours — please finish or abandon fewer carts and try again later",
			"code":                      "QUOTE_ATTEMPT_LIMIT",
			"daily_quote_attempt_limit": limit,
		})
		return true
	}
	return false
}

func (h *Handlers) quoteReservationConflict(ctx *fasthttp.RequestCtx, err error, user, key, requestFP string) bool {
	var live *db.ErrLiveDuplicate
	if errors.As(err, &live) {
		h.reuseCheckout(ctx, live.Quote, key, requestFP)
		return true
	}
	var active *db.ErrActiveCheckout
	if errors.As(err, &active) {
		h.activeCheckoutError(ctx, active.Quote)
		return true
	}
	if errors.Is(err, db.ErrConflict) {
		// The gate refused the key because it is bound to an UNRESOLVED
		// quote (a settled one would have let creation through and moved
		// the key — see db.CheckoutSettledAt). Replay that quote; it is
		// the retry contract. Anything else is a genuine conflict.
		q, fp, e := h.Store.GetQuoteByIdempotencyRequest(user, key)
		if e == nil && fp == requestFP && !db.CheckoutSettled(q) {
			h.writeQuoteCreatedExtra(ctx, q, map[string]any{"reused": true})
		} else {
			writeError(ctx, 409, "conflicting checkout key or single-use cashback code")
		}
		return true
	}
	return false
}

func (h *Handlers) claimSupplierDispatch(ctx *fasthttp.RequestCtx, q db.Quote) bool {
	if err := h.Store.MarkSupplierRequestStarted(q.ID); err != nil {
		// Another worker may have WON the one-shot claim. Do not mutate its
		// in-flight state or make a second POST.
		if errors.Is(err, db.ErrConflict) {
			if latest, e := h.Store.GetQuote(q.ID); e == nil {
				h.writeQuoteCreated(ctx, latest)
				return false
			}
		}
		_ = h.Store.HoldQuote(q.ID, "supplier dispatch claim could not be persisted")
		writeError(ctx, 503, "could not safely claim supplier dispatch; no request was sent by this handler")
		return false
	}
	return true
}

func (h *Handlers) supplierCreateFailed(ctx *fasthttp.RequestCtx, q db.Quote, err error) {
	if cryptorefills.CreateOutcomeAmbiguous(err) {
		if latest, e := h.Store.GetQuote(q.ID); e == nil {
			extra := map[string]any{"code": "ORDER_OUTCOME_UNKNOWN", "detail": "Supplier acceptance is unconfirmed. Keep this order and check its status; do not start another payment."}
			// Retain diagnostics without replacing the payment-safety hold
			// with a business-error retry instruction.
			var pe *cryptorefills.ProblemError
			var rl *cryptorefills.RateLimitError
			var se *cryptorefills.SupplierError
			if errors.As(err, &pe) {
				extra["supplier_problems"] = pe.Problems
			}
			if errors.As(err, &rl) {
				extra["supplier_problems"] = rl.Problems
				extra["supplier_retry_at"] = rl.ResetAt
			}
			if errors.As(err, &se) {
				extra["supplier_error_code"] = se.Code
			}
			h.writeQuoteCreatedExtra(ctx, latest, extra)
		} else {
			writeError(ctx, 503, "order outcome unknown; check Orders, do not create another checkout")
		}
		return
	}
	_ = h.Store.MarkSupplierFailure(q.ID, "supplier rejected creation: "+err.Error())
	h.supplierError(ctx, err, "supplier refused this order")
}

func (h *Handlers) finishSupplierCreation(ctx *fasthttp.RequestCtx, q db.Quote, order *cryptorefills.Order, extra map[string]any) {
	if err := h.Store.RecordSupplierOrder(q.ID, order); err != nil {
		_ = h.Store.HoldQuote(q.ID, "supplier accepted an order, but its linkage could not be persisted")
		writeError(ctx, 503, "supplier order linkage needs review; do not create another checkout")
		return
	}
	now := time.Now().UTC()
	_, guardErr := cryptorefills.ValidatePayableOrder(order, now)

	if guardErr != nil {
		if err := h.Store.HoldQuote(q.ID, guardErr.Error()); err != nil {
			writeError(ctx, 503, "could not persist invoice safety hold")
			return
		}
		// A creation response can already be a delivery/failure state; observe
		// it, but NEVER expose an unvalidated invoice as a way to pay again.
		if cryptorefills.MapToQuoteStatus(order.Status) != "awaiting_payment" {
			_, _ = settlement.ApplySupplierOrderNotify(h.Store, q.ID, order)
		}
		if latest, err := h.Store.GetQuote(q.ID); err == nil {
			h.writeQuoteCreatedExtra(ctx, latest, map[string]any{"code": "INVOICE_NOT_PAYABLE", "detail": "This order is not available for payment. Check its status or contact support; do not pay a replacement invoice."})
		} else {
			writeError(ctx, 503, "could not read invoice safety hold")
		}
		return
	}
	if order.Network == "" {
		// Default network based on the payment method stored on the quote.
		if IsStablecoinMethod(q.PaymentMethod) {
			order.Network = PaymentNetworkStable
		} else {
			order.Network = PaymentNetworkNIM
		}
	}
	if err := h.Store.AttachQuotePayment(q.ID, order.ID, order.WalletAddress, order.Coin, order.CoinAmount, order.Network, cryptorefills.PaymentExpiryFor(order, now)); err != nil {
		_ = h.Store.HoldQuote(q.ID, "supplier accepted order; invoice attachment needs review")
		writeError(ctx, 503, "order exists but invoice attachment needs review; do not start a new payment")
		return
	}
	// Final NIM shop estimate for Lightning. USDT already locks its NIM
	// cashback equivalent atomically inside AttachQuotePayment.
	if !IsStablecoinMethod(q.PaymentMethod) {
		if est := h.nimEstimateForBTC(order.CoinAmount); est > 0 {
			_ = h.Store.SetQuoteNIMSnapshot(q.ID, currentRates().nimUSD, est)
		}
	}
	latest, err := h.Store.GetQuote(q.ID)
	if err != nil {
		writeError(ctx, 503, "could not read created order")
		return
	}
	h.writeQuoteCreatedExtra(ctx, latest, extra)
}

// quoteGateOptions builds the per-call options the atomic quote gate applies.
// It is the single place where "what does this checkout cost the shop beyond
// its own price?" is answered, so the single-item, batch and admin-test paths
// can never drift apart on it.
//
// The promo-code payout estimate is deliberately CONSERVATIVE: it charges the
// code's total budget with the maximum the order could pay (rate × order USD),
// computed from the supplier's own live amount. Over-reserving by a few cents
// is harmless — the reservation is replaced by the exact figure at
// fulfillment — while under-reserving would let a burst of concurrent
// checkouts collectively blow through a budget the operator set.
func (h *Handlers) quoteGateOptions(q *db.Quote, view *quoteCashbackView, ack bool) db.QuoteOptions {
	opts := db.QuoteOptions{MaxAttemptsPerDay: h.Cfg.DailyQuoteAttemptLimit, AckActiveCheckout: ack}
	if q == nil {
		return opts
	}
	// The admin test center runs on a synthetic identity; exempting it from
	// the attempt ceiling is what makes it usable as a test surface at all.
	if q.TestMode && q.UserID == testCenterUserID {
		opts.MaxAttemptsPerDay = 0
	}
	if view == nil || view.Code == "" || q.CashbackCodeBps <= 0 {
		return opts
	}
	orderUSD := float64(q.ProductUSD) / 1_000_000
	// A promo cap means only the capped portion earns the promo rate; the
	// remainder earns the buyer's base rate, which is not this code's budget.
	if view.PromoCapUSD > 0 && view.PromoCapUSD < orderUSD {
		orderUSD = view.PromoCapUSD
	}
	if orderUSD <= 0 || view.Bps <= 0 {
		opts.CashbackCode = view.Code
		opts.CashbackCodeLimits = view.Limits
		return opts
	}
	estimate := orderUSD * float64(view.Bps) / 10_000
	opts.CashbackCode = view.Code
	opts.CashbackCodeLimits = view.Limits
	opts.CashbackEstimateUSD = money.FromFloat(estimate)
	return opts
}
