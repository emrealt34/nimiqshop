package handlers

import (
	"errors"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/middleware"
)

/* user_test_pay.go — the TEST_MODE customer's "pay" button.
 *
 * When the shop runs with TEST_MODE=true, the customer experiences the
 * COMPLETE normal flow — wallet login, catalog, delivery step, checkout,
 * invoice — and pays through this endpoint instead of a real wallet handoff:
 * POST /api/quotes/{id}/test-pay walks the order through the REAL state
 * machine (broadcast → confirm → deliver) exactly like an on-chain payment
 * landing, so the pay screen's live polling, the delivery code, the gift
 * email, the cashback engine and the orders page all behave like production.
 *
 * Only the owner's own test quote (TestMode + TESTSIM- supplier id) can be
 * driven, and only while TEST_MODE is on.
 */

// UserTestPay is the customer-facing simulated payment.
//
//	POST /api/quotes/{id}/test-pay  { "action":"auto" }   (body optional)
func (h *Handlers) UserTestPay(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)
	if !h.isTestAccountUser(userID) {
		writeError(ctx, fasthttp.StatusNotFound, "test payment is not available for this account")
		return
	}
	q, err := h.Store.GetQuoteForUser(id, userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusNotFound, "quote not found")
		return
	}
	if !h.canTestPay(q) {
		writeError(ctx, fasthttp.StatusConflict, "this order cannot be test-paid")
		return
	}
	var req struct {
		Action string `json:"action,omitempty"`
	}
	_ = readJSON(ctx, &req) // an empty body means "auto"

	latest, applied, derr := h.driveTestQuote(q, req.Action)
	if derr != nil {
		if errors.Is(derr, errBadTestAction) {
			writeError(ctx, fasthttp.StatusBadRequest, derr.Error())
			return
		}
		writeError(ctx, fasthttp.StatusConflict, "this order cannot be paid right now (status: "+q.Status+")")
		return
	}
	resp := testQuoteView(h.Store, latest, applied)
	resp["simulated_payment"] = true
	writeJSON(ctx, fasthttp.StatusOK, resp)
}
