package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
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
func singleFingerprints(req createQuoteRequest) (string, string) {
	request := fingerprint(req)
	req.CashbackCode = ""
	req.GiftChannel = ""
	req.GiftMessage = ""
	req.GifterIdenticon = ""
	return request, fingerprint(req)
}
func batchFingerprints(req batchQuoteRequest) (string, string) {
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
func (h *Handlers) existingCheckout(ctx *fasthttp.RequestCtx, user, key, requestFP, purchaseFP string) bool {
	q, fp, err := h.Store.GetQuoteByIdempotencyRequest(user, key)
	if err == nil {
		if fp == "" || fp != requestFP {
			writeJSON(ctx, 409, map[string]any{"error": "idempotency key belongs to a different request", "code": "IDEMPOTENCY_MISMATCH", "quote_id": q.ID, "detail": "Open the original order; do not pay a different cart using this key."})
		} else {
			h.writeQuoteCreated(ctx, q)
		}
		return true
	}
	if !errors.Is(err, db.ErrNotFound) {
		writeError(ctx, 503, "could not verify checkout idempotency; no order was dispatched")
		return true
	}
	// Only the quotes that can actually block a new purchase are read. The
	// previous ListQuotesForUser(user, 0) loaded and decoded the buyer's
	// ENTIRE history on every checkout attempt, which turned "abandon a lot
	// of carts" into a way of making one's own (and the server's) checkout
	// path arbitrarily expensive. See internal/db/quote_index.go.
	quotes, err := h.Store.BlockingQuotesForUser(user)
	if err != nil {
		writeError(ctx, 503, "could not verify outstanding checkouts; no order was dispatched")
		return true
	}
	for _, q := range quotes {
		// A still-"awaiting payment" order that the buyer can no longer pay
		// (its window lapsed, no money seen, no hold) must not hold the
		// checkout hostage. Release it so a fresh purchase may proceed; the
		// settlement tracker independently sweeps it to "expired" for display.
		if q.Status == "awaiting_payment" && !q.PaymentObserved && !q.PaymentBlocked &&
			!q.PaymentExpiry.IsZero() && q.PaymentExpiry.Before(time.Now().UTC()) {
			continue
		}
		if !db.BlocksNewPurchase(q) {
			continue
		}
		if q.PurchaseFingerprint != "" && q.PurchaseFingerprint == purchaseFP {
			h.reuseCheckout(ctx, q, key, requestFP)
		} else {
			h.activeCheckoutError(ctx, q)
		}
		return true
	}
	return false
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
		q, fp, e := h.Store.GetQuoteByIdempotencyRequest(user, key)
		if e == nil && fp == requestFP {
			h.writeQuoteCreated(ctx, q)
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
			_, _ = h.Store.ApplySupplierOrder(q.ID, order)
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
func (h *Handlers) quoteGateOptions(q *db.Quote, view *quoteCashbackView) db.QuoteOptions {
	opts := db.QuoteOptions{MaxAttemptsPerDay: h.Cfg.DailyQuoteAttemptLimit}
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
