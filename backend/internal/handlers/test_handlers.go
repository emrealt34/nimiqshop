package handlers

import (
	"strings"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/middleware"
	"nimiqshop/internal/phone"
)

/* test_handlers.go — a DEV-ONLY purchase path from the old mock stack.
 * Superseded for operator testing by the two sandbox surfaces:
 *   - TEST_MODE=true: normal customers, normal flow, simulated payments
 *     (see createQuoteInner's branch and user_test_pay.go);
 *   - the admin Test Center (admin_test_center.go).
 * This route still exists for the local crash/mock stack (loopback supplier
 * + test- families only); a test- prefix never makes a production order free.
 */

type testBuyRequest struct {
	ProductID    string  `json:"product_id"`
	Country      string  `json:"country"`
	Denomination string  `json:"denomination,omitempty"`
	ProductValue float64 `json:"product_value,omitempty"`
	Quantity     int     `json:"quantity,omitempty"`
	Email        string  `json:"email"`
	PhoneNumber  string  `json:"phone_number,omitempty"`
	Coin         string  `json:"coin,omitempty"`
}

func (h *Handlers) TestBuy(ctx *fasthttp.RequestCtx) {
	if !h.Cfg.TestMode || !h.Cfg.IsLocalSupplierMock() {
		writeError(ctx, fasthttp.StatusNotFound, "test mode is disabled")
		return
	}
	userID := middleware.UserID(ctx)
	_, _ = h.Store.GetOrCreateUserByID(userID, "TEST-"+userID)

	var req testBuyRequest
	if err := readJSON(ctx, &req); err != nil || req.ProductID == "" || req.Country == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "product_id and country are required")
		return
	}
	req.ProductID = strings.TrimSpace(req.ProductID)
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	if !strings.HasPrefix(req.ProductID, "test-") {
		writeError(ctx, fasthttp.StatusBadRequest, "only test products (family starts with 'test-') can be used here")
		return
	}
	if len(req.Country) != 2 {
		writeError(ctx, fasthttp.StatusBadRequest, "country must be a 2-letter code")
		return
	}
	// Email is validated inside createQuoteInner (required for gift cards /
	// eSIMs, optional for mobile top-ups which deliver to the phone number).
	if req.Quantity < 1 {
		req.Quantity = 1
	}
	req.Coin = PaymentCoinNIM
	_ = PaymentNetworkNIM
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	// Same normalization + strict E.164 as the production quote path: the
	// supplier delivers test top-ups to the normalized number too.
	if req.PhoneNumber != "" {
		norm, err := phone.Normalize(req.PhoneNumber, req.Country)
		if err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		req.PhoneNumber = norm
	}

	meta := h.lookupFamilyMeta(ctx, req.ProductID, req.Country)
	if isTopUpProduct(meta) {
		if req.PhoneNumber == "" {
			writeError(ctx, fasthttp.StatusBadRequest, "phone_number is required for mobile top-ups (E.164)")
			return
		}
		if err := phone.Validate(req.PhoneNumber); err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
	}

	// Delegate to the quote path: it performs validation, the write-ahead
	// intent, the supplier order and the attach. The response is the same
	// payment payload a real quote returns.
	h.createTestQuote(ctx, userID, req)
}

// createTestQuote mirrors CreateQuote for the test-buy route (same flow,
// test-only guards).
func (h *Handlers) createTestQuote(ctx *fasthttp.RequestCtx, userID string, req testBuyRequest) {
	inner := createQuoteRequest{
		ProductID: req.ProductID, Country: req.Country,
		Denomination: req.Denomination, ProductValue: req.ProductValue,
		Quantity: req.Quantity, Email: req.Email, PhoneNumber: req.PhoneNumber,
		Coin: req.Coin,
	}
	if inner.Denomination == "" {
		if inner.ProductValue > 0 {
			inner.Denomination = "range"
		} else {
			writeError(ctx, 400, "select an actual test denomination or a positive range value")
			return
		}
	}
	// Reuse the exact production flow.
	h.createQuoteInner(ctx, userID, inner)
}
