package handlers

import (
	"github.com/valyala/fasthttp"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/settlement"
	"strings"
	"time"
)

// PaymentLaunch authorizes one explicit wallet/clipboard/QR handoff after a
// fresh supplier GET. This endpoint NEVER creates an order or sends money.
func (h *Handlers) PaymentLaunch(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	user := middleware.UserID(ctx)
	q, err := h.Store.GetQuoteForUser(id, user)
	if err != nil {
		writeError(ctx, 404, "quote not found")
		return
	}
	if q.TestMode {
		writeJSON(ctx, 409, map[string]any{"code": "TEST_ORDER", "quote_id": q.ID, "detail": "This order's payment is simulated (test mode) — pay it with the simulated Pay button, not a wallet handoff."})
		return
	}
	if !q.CanPay(time.Now()) {
		writeJSON(ctx, 409, map[string]any{"code": "PAYMENT_NOT_PAYABLE", "detail": "This order is not safely payable. Check its status; do not send another payment.", "quote_id": q.ID})
		return
	}
	order, err := h.CR.GetOrderFresh(h.supplierContext(ctx), q.SupplierOrderID)
	if err != nil {
		h.supplierError(ctx, err, "could not verify current payment state; wallet not opened")
		return
	}
	changed, err := settlement.ApplySupplierOrderNotify(h.Store, q.ID, order)
	if err != nil {
		writeError(ctx, 503, "supplier update could not be saved; wallet not opened")
		return
	}
	if changed && cryptorefills.MapToQuoteStatus(order.Status) == "fulfilled" {
		if latest, e := h.Store.GetQuote(q.ID); e == nil {
			settlement.NotifyFulfilled(latest)
		}
	}
	// A supplier response may not silently replace the original invoice/amount.
	if !strings.EqualFold(order.WalletAddress, q.WalletAddress) || !cryptorefills.SamePositiveAmount(order.CoinAmount, q.CoinAmount) || !strings.EqualFold(order.Coin, q.Coin) {
		_ = h.Store.HoldQuote(q.ID, "supplier payment details changed after invoice was attached")
		writeError(ctx, 409, "payment details changed; order held for review")
		return
	}
	if _, err := cryptorefills.ValidatePayableOrder(order, time.Now().UTC()); err != nil {
		writeJSON(ctx, 409, map[string]any{"code": "PAYMENT_NOT_PAYABLE", "detail": "The supplier no longer reports a payable order. Check its status; do not pay again."})
		return
	}
	// Stamp that the buyer's wallet was handed this single-use invoice (audit
	// only). A Lightning invoice can only ever be paid once, so re-opening/paying
	// the same invoice is always allowed — there is no "already opened" refusal.
	_ = h.Store.ClaimPaymentHandoff(q.ID, user)
	h.GetUserQuote(ctx)
}

// RefreshQuote performs an explicit live status GET, not another order create.
func (h *Handlers) RefreshQuote(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	user := middleware.UserID(ctx)
	q, err := h.Store.GetQuoteForUser(id, user)
	if err != nil {
		writeError(ctx, 404, "quote not found")
		return
	}
	if q.SupplierOrderID == "" {
		writeJSON(ctx, 409, map[string]any{"code": "ORDER_OUTCOME_UNKNOWN", "quote_id": q.ID, "detail": "Supplier acceptance is unconfirmed and no order id is known. No new order will be sent; contact support."})
		return
	}
	// Simulated (TEST_MODE) quotes never existed at the supplier: their
	// TESTSIM- order advances through the settlement tracker, so a refresh
	// is just a re-read of local state instead of a doomed supplier GET.
	if q.TestMode || strings.HasPrefix(q.SupplierOrderID, "TESTSIM-") {
		h.GetUserQuote(ctx)
		return
	}
	order, err := h.CR.GetOrderFresh(h.supplierContext(ctx), q.SupplierOrderID)
	if err != nil {
		h.supplierError(ctx, err, "live supplier status unavailable")
		return
	}
	changed, err := settlement.ApplySupplierOrderNotify(h.Store, q.ID, order)
	if err != nil {
		writeError(ctx, 503, "supplier state could not be saved")
		return
	}
	if changed && cryptorefills.MapToQuoteStatus(order.Status) == "fulfilled" {
		if latest, e := h.Store.GetQuote(q.ID); e == nil {
			settlement.NotifyFulfilled(latest)
		}
	}
	h.GetUserQuote(ctx)
}
