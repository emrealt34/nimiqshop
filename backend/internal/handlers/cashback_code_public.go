package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/cashback"
	"nimiqshop/internal/cashbackcode"
	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

type quoteCashbackView struct {
	Bps         int
	Source      string
	Code        string
	Exclusive   bool
	MaxOrderUSD float64
	PromoCapUSD float64
	OrderUSD    float64
	// Limits is the operator's redemption policy for this code, resolved
	// once at quote time and handed to the atomic gate so the reservation
	// and the quote commit together. Zero fields mean unlimited, which is
	// what an env-configured (CASHBACK_CODES) table always is — the env form
	// carries only a rate, so its lifecycle is unchanged.
	Limits db.CodeLimits
	// UsesLeft is the remaining total redemption budget, for the public
	// "how many are left" surface. -1 means unlimited/unknown.
	UsesLeft int
}

func envCashbackCodeRules(tableRaw string) ([]adminmodel.CashbackCodeRule, error) {
	rewards, err := cashbackcode.Sorted(tableRaw)
	if err != nil {
		return nil, err
	}
	out := make([]adminmodel.CashbackCodeRule, 0, len(rewards))
	for _, reward := range rewards {
		out = append(out, adminmodel.CashbackCodeRule{Code: reward.Code, CashbackBps: reward.Bps})
	}
	return out, nil
}

func (h *Handlers) effectiveCashbackCodeRules() ([]adminmodel.CashbackCodeRule, string, error) {
	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		return nil, "", err
	}
	if settings.CashbackCodeRules != nil {
		return settings.EffectiveCashbackCodeRules(), "admin", nil
	}
	rules, err := envCashbackCodeRules(h.Cfg.CashbackCodes)
	if err != nil {
		return nil, "", err
	}
	return rules, "env", nil
}

func (h *Handlers) cashbackCodeEnabled() bool {
	rules, _, err := h.effectiveCashbackCodeRules()
	return err == nil && len(rules) > 0
}

func (h *Handlers) lookupCashbackCodeRule(codeRaw string) (adminmodel.CashbackCodeRule, bool, error) {
	code := cashbackcode.Normalize(codeRaw)
	if code == "" {
		return adminmodel.CashbackCodeRule{}, false, nil
	}
	if err := cashbackcode.ValidateCode(code); err != nil {
		return adminmodel.CashbackCodeRule{}, false, err
	}
	rules, _, err := h.effectiveCashbackCodeRules()
	if err != nil {
		return adminmodel.CashbackCodeRule{}, false, err
	}
	for _, rule := range rules {
		if rule.Code == code {
			return rule, true, nil
		}
	}
	return adminmodel.CashbackCodeRule{Code: code}, false, nil
}

// codeLimitsFor translates the operator's rule into the store's accounting
// policy. Kept next to the resolver so the two can never drift: a rule that
// the storefront says is valid is exactly a rule the gate will enforce.
func codeLimitsFor(rule adminmodel.CashbackCodeRule) db.CodeLimits {
	return db.CodeLimits{
		MaxUsesTotal:        rule.MaxUsesTotal,
		MaxUsesPerUser:      rule.MaxUsesPerUser,
		MaxTotalCashbackUSD: money.FromFloat(rule.MaxTotalCashbackUSD),
		ExpiresAt:           rule.ExpiresAt,
	}
}

// codeLimitError renders a redemption-limit refusal in the buyer's language.
// The store returns a typed error; the response must never leak the code's
// remaining budget as a number an attacker could use to time a retry, so only
// the reason is echoed.
func (h *Handlers) codeLimitError(ctx *fasthttp.RequestCtx, detail *db.CodeLimitDetail) {
	writeJSON(ctx, fasthttp.StatusConflict, map[string]any{
		"error":         detail.Reason,
		"code":          "CASHBACK_CODE_LIMIT",
		"limit":         detail.Limit,
		"cashback_code": detail.Code,
	})
}

func (h *Handlers) resolveQuoteCashback(code string, userID string) (quoteCashbackView, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		bps, stake := h.buyerBaseBps(context.Background(), userID)
		src := "base"
		if stake.Staked && bps > 0 {
			src = "staker"
		}
		return quoteCashbackView{Bps: bps, Source: src}, nil
	}
	rule, ok, err := h.lookupCashbackCodeRule(code)
	if err != nil {
		return quoteCashbackView{}, err
	}
	if !ok {
		return quoteCashbackView{}, fmt.Errorf("cashback code %q is invalid or expired", rule.Code)
	}
	// Expiry is checked here, before any supplier work: a finished promotion
	// must be refused cheaply and with a clear message rather than after a
	// dry-run has already spent partner budget.
	if !rule.ExpiresAt.IsZero() && time.Now().UTC().After(rule.ExpiresAt) {
		return quoteCashbackView{}, fmt.Errorf("cashback code %q has expired", rule.Code)
	}
	view := quoteCashbackView{
		Bps: rule.CashbackBps, Source: "code", Code: rule.Code, Exclusive: true,
		MaxOrderUSD: rule.MaxOrderUSD, Limits: codeLimitsFor(rule), UsesLeft: -1,
	}
	if rule.MaxUsesTotal > 0 {
		if usage, uerr := h.Store.CodeUsageFor(rule.Code); uerr == nil {
			left := rule.MaxUsesTotal - usage.InFlight
			if left < 0 {
				left = 0
			}
			view.UsesLeft = left
			if left <= 0 {
				return quoteCashbackView{}, fmt.Errorf("cashback code %q has reached its redemption limit", rule.Code)
			}
		}
	}
	if rule.MaxUsesPerUser > 0 && userID != "" {
		if n, uerr := h.Store.CodeUserUsesFor(rule.Code, userID); uerr == nil && n >= rule.MaxUsesPerUser {
			return quoteCashbackView{}, fmt.Errorf("you have already used cashback code %q", rule.Code)
		}
	}
	if rule.MaxTotalCashbackUSD > 0 {
		if usage, uerr := h.Store.CodeUsageFor(rule.Code); uerr == nil &&
			usage.PaidUSD >= money.FromFloat(rule.MaxTotalCashbackUSD) {
			return quoteCashbackView{}, fmt.Errorf("cashback code %q has reached its total cashback budget", rule.Code)
		}
	}
	return view, nil
}

func validatePromoOrderCap(view *quoteCashbackView, orderUSD float64) {
	view.OrderUSD = orderUSD
	if view.Code == "" || view.MaxOrderUSD <= 0 || orderUSD <= 0 {
		view.PromoCapUSD = orderUSD
		return
	}
	if orderUSD <= view.MaxOrderUSD+0.000001 {
		view.PromoCapUSD = orderUSD
		return
	}
	view.PromoCapUSD = view.MaxOrderUSD
}

func applyQuoteCashbackFields(resp map[string]interface{}, q db.Quote, bps int) {
	if q.CashbackCodeBps > 0 {
		bps = q.CashbackCodeBps
	}
	resp["cashback_bps"] = bps
	resp["cashback_percent"] = float64(bps) / 100.0
	if q.CashbackCodeBps > 0 {
		resp["cashback_source"] = "code"
		resp["cashback_code"] = q.CashbackCode
		resp["cashback_code_bps"] = q.CashbackCodeBps
		resp["cashback_code_percent"] = float64(q.CashbackCodeBps) / 100.0
		resp["cashback_exclusive"] = true
		resp["cashback_note"] = fmt.Sprintf("Promo code %s is active and replaces any loyalty or staker cashback on this order.", q.CashbackCode)
		return
	}
	resp["cashback_source"] = "base"
}

func (h *Handlers) PublicCashbackCode(ctx *fasthttp.RequestCtx) {
	code := strings.TrimSpace(string(ctx.QueryArgs().Peek("code")))
	if code == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "code is required")
		return
	}
	rule, ok, err := h.lookupCashbackCodeRule(code)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if !ok {
		writeError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("cashback code %q is invalid or expired", rule.Code))
		return
	}
	orderUSD, _ := strconv.ParseFloat(strings.TrimSpace(string(ctx.QueryArgs().Peek("order_usd"))), 64)
	view := quoteCashbackView{Code: rule.Code, Bps: rule.CashbackBps, MaxOrderUSD: rule.MaxOrderUSD}
	validatePromoOrderCap(&view, orderUSD)
	partial := rule.MaxOrderUSD > 0 && orderUSD > rule.MaxOrderUSD+0.000001
	msg := fmt.Sprintf("Promo code %s is active and replaces any loyalty or staker cashback on this order.", rule.Code)
	if partial {
		msg = fmt.Sprintf("Promo code %s applies to the first $%.2f of your cart only — the remaining $%.2f earns your base cashback rate.", rule.Code, view.PromoCapUSD, orderUSD-view.PromoCapUSD)
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"valid": true, "code": rule.Code, "max_order_usd": rule.MaxOrderUSD,
		"cashback_bps": rule.CashbackBps, "cashback_percent": float64(rule.CashbackBps) / 100.0,
		"cashback_source": "code", "cashback_exclusive": true, "overrides_staker": true,
		"promo_cap_usd": view.PromoCapUSD, "promo_cap_partial": partial,
		"promo_remainder_usd": orderUSD - view.PromoCapUSD, "message": msg,
	})
}

func (h *Handlers) attachQuoteCashbackEstimate(resp map[string]interface{}, q db.Quote, productNIM float64) {
	// Nimiq Pay ONLY: if another method is used, zero out cashback
	if q.PaymentMethod != "" && q.PaymentMethod != "nimiq_pay" {
		return
	}
	view, err := h.resolveQuoteCashback(q.CashbackCode, q.UserID)
	if err != nil {
		bps, _ := h.buyerBaseBps(context.Background(), q.UserID)
		view = quoteCashbackView{Bps: bps, Source: "base"}
	}
	if q.CashbackCodeBps > 0 {
		view.Bps = q.CashbackCodeBps
		view.Source = "code"
		view.Code = q.CashbackCode
		view.Exclusive = true
	}
	applyQuoteCashbackFields(resp, q, view.Bps)
	if q.CashbackCodeBps <= 0 && view.Source == "staker" {
		resp["cashback_source"] = "staker"
	}
	if productNIM > 0 && view.Bps > 0 {
		if luna := cashback.AmountLuna(productNIM, view.Bps); luna > 0 {
			resp["estimated_cashback_nim"] = cashback.NIMFromLuna(luna)
			resp["estimated_cashback_luna"] = luna
		}
	}
}
