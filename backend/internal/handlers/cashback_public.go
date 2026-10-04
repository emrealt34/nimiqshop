package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

// liveCashbackBps is the operator's UNIVERSAL base (what a non-staker
// earns; 0 by default). Anything that knows WHICH buyer is asking must use
// buyerBaseBps instead, so a staker gets the admin-panel staker base.
func (h *Handlers) liveCashbackBps() int {
	s, err := h.Store.GetAdminSettings(0)
	if err != nil {
		return adminmodel.DefaultCashbackBps
	}
	return s.EffectiveCashbackBps()
}

// poolStakerBaseBps is the shop's staker base (what ANY staked address
// earns). It is owned by the admin panel — default 1% — and never read from
// the pool. The pool only answers "is this wallet staked?".
func (h *Handlers) poolStakerBaseBps(_ context.Context) int {
	s, err := h.Store.GetAdminSettings(0)
	if err != nil {
		return adminmodel.DefaultStakerCashbackBps
	}
	return s.EffectiveStakerCashbackBps()
}

// buyerBaseBps is THE base-rate answer for one buyer: the admin-panel staker
// base if they are staked (any amount), else the operator's universal base.
func (h *Handlers) buyerBaseBps(ctx context.Context, userID string) (bps int, stake db.StakerStake) {
	operator := h.liveCashbackBps()
	if userID == "" {
		return operator, db.StakerStake{}
	}
	stake = h.Store.UserStakerStake(ctx, userID)
	if stake.Staked {
		stake.BaseBps = h.poolStakerBaseBps(ctx)
	}
	return stake.EffectiveBaseBps(operator), stake
}

// PublicSite is the live shop hostname so the static frontend can brand
// itself from the single backend SITE_HOST env.
func (h *Handlers) PublicSite(ctx *fasthttp.RequestCtx) {
	host := h.Cfg.SiteName()
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"host": host,
		"name": host,
		"url":  h.Cfg.SiteURL(),
	})
}

// PublicCashbackRate moved to poolstake_public.go: the storefront rate now
// travels together with the pool-staker ladder it can be upgraded by.

func (h *Handlers) writeQuoteCreated(ctx *fasthttp.RequestCtx, q db.Quote) {
	h.writeQuoteCreatedExtra(ctx, q, nil)
}

func (h *Handlers) writeQuoteCreatedExtra(ctx *fasthttp.RequestCtx, q db.Quote, extra map[string]interface{}) {
	status := fasthttp.StatusAccepted
	if q.CanPay(time.Now().UTC()) {
		status = fasthttp.StatusCreated
	}
	resp := map[string]interface{}{
		"test_mode":          h.Cfg.TestMode,
		"quote_id":           q.ID,
		"status":             q.Status,
		"payment_handoff_at": q.PaymentHandoffAt,
		"can_pay":            q.CanPay(time.Now().UTC()),
		"supplier_status":    q.SupplierStatus,
		"supplier_order_id":  q.SupplierOrderID,
		"customer_email":     q.CustomerEmail, "phone_number": q.PhoneNumber, "beneficiary_account": q.BeneficiaryAccount,
		"gift_channel": q.GiftChannel, "gift_message": q.GiftMessage,
		"coin": q.Coin, "coin_amount": q.CoinAmount, "network": q.Network,
		"payment_expires_at": q.PaymentExpiry,
		"payment_observed":   q.PaymentObserved, "payment_blocked": q.PaymentBlocked,
		"payment_method":   paymentMethodOf(q),
		"powered_by":       "cryptorefills",
		"product_id":       q.ProductID,
		"country":          q.ProductCountry,
		"product_value":    q.ProductValue,
		"product_currency": q.ProductCurrency,
		"denomination":     q.Denomination,
		"quantity":         q.Quantity,
		"expires_at":       q.ExpiresAt,
	}
	if q.IsBatch {
		resp["is_batch"] = true
		resp["batch_items"] = q.BatchItems
		resp["face_value_totals"] = q.FaceValueTotals
	}
	if q.CanPay(time.Now().UTC()) {
		resp["wallet_address"] = q.WalletAddress
		resp["coin"] = q.Coin
		resp["coin_amount"] = q.CoinAmount
		resp["network"] = q.Network
		resp["payment_expires_at"] = q.PaymentExpiry
		if cryptorefills.IsBOLT11(q.WalletAddress) {
			resp["lightning_invoice"] = q.WalletAddress
			resp["payment_uri"] = cryptorefills.LightningURI(q.WalletAddress)
		}
	}
	if q.EstimatedNIM > 0 {
		resp["estimated_nim"] = q.EstimatedNIM
	}
	h.attachQuoteCashbackEstimate(resp, q, q.EstimatedNIM)
	for k, v := range extra {
		resp[k] = v
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	writeJSON(ctx, status, resp)
}

// paymentMethodOf returns the public payment_method string for a quote.
// Rows saved before the PaymentMethod column existed infer the rail from the
// coin/network fields.
func paymentMethodOf(q db.Quote) string {
	if q.PaymentMethod != "" {
		return q.PaymentMethod
	}
	if IsStablecoinCoin(q.Coin) || strings.Contains(strings.ToLower(q.Network), "polygon") {
		return PaymentMethodUSDT
	}
	return PaymentMethodNIM
}
