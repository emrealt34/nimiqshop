package cryptorefills

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ProblemLightningOrderLimit is the (undocumented) problem code Cryptorefills
// answers on /v5/orders/validations when a Lightning order breaks one of the
// ceilings attached to the delivery email's KYC level. Observed live on
// 2026-10-20 for Amazon.fr FR €200:
//
//	{"problem":"LIGHTNING_ORDER_LIMIT_EXCEEDED","moreDetails":{
//	  "kyc_level":"NOT_REGISTERED",
//	  "limits":{"per_order":{"amount":100,"currency":"EUR"},"daily":{...100},"monthly":{...300}},
//	  "next_level_limits":{"per_order":{...100},"daily":{...250},"monthly":{...1000}},
//	  "order_total":{"amount":208.07,"currency":"EUR"},
//	  "spent_today":{...0},"spent_this_month":{...0},
//	  "usd":{...same block in USD...}}}
const ProblemLightningOrderLimit = "LIGHTNING_ORDER_LIMIT_EXCEEDED"

// Money is one {"amount","currency"} pair of that payload.
type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// LightningLimitDetails is the structured half of the refusal.
type LightningLimitDetails struct {
	KYCLevel string `json:"kyc_level"`
	Limits   struct {
		PerOrder Money `json:"per_order"`
		Daily    Money `json:"daily"`
		Monthly  Money `json:"monthly"`
	} `json:"limits"`
	OrderTotal     Money `json:"order_total"`
	SpentToday     Money `json:"spent_today"`
	SpentThisMonth Money `json:"spent_this_month"`
}

// Which ceiling was actually crossed: "per_order", "daily", "monthly" or ""
// when the payload does not say.
func (d *LightningLimitDetails) Exceeded() string {
	if d == nil || d.OrderTotal.Amount <= 0 {
		return ""
	}
	switch {
	case d.Limits.PerOrder.Amount > 0 && d.OrderTotal.Amount > d.Limits.PerOrder.Amount:
		return "per_order"
	case d.Limits.Daily.Amount > 0 && d.SpentToday.Amount+d.OrderTotal.Amount > d.Limits.Daily.Amount:
		return "daily"
	case d.Limits.Monthly.Amount > 0 && d.SpentThisMonth.Amount+d.OrderTotal.Amount > d.Limits.Monthly.Amount:
		return "monthly"
	}
	return ""
}

// ParseLightningLimit decodes the moreDetails of a LIGHTNING_ORDER_LIMIT_EXCEEDED
// problem. ok is false for any other problem or an unreadable payload.
func ParseLightningLimit(p Problem) (d LightningLimitDetails, ok bool) {
	if !strings.EqualFold(strings.TrimSpace(p.Code), ProblemLightningOrderLimit) || p.Details == nil {
		return d, false
	}
	raw, err := json.Marshal(p.Details)
	if err != nil {
		return d, false
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, false
	}
	return d, d.Limits.PerOrder.Amount > 0 || d.Limits.Daily.Amount > 0 || d.Limits.Monthly.Amount > 0
}

// ProblemDetailMessage is ProblemMessage plus the concrete numbers when the
// supplier sent them, so a log line or a batch "which item broke the cart"
// answer names the real ceiling instead of a generic sentence.
func ProblemDetailMessage(p Problem) string {
	base := ProblemMessage(p.Code)
	d, ok := ParseLightningLimit(p)
	if !ok {
		return base
	}
	parts := make([]string, 0, 4)
	if d.OrderTotal.Amount > 0 {
		parts = append(parts, "order total "+moneyString(d.OrderTotal))
	}
	if d.Limits.PerOrder.Amount > 0 {
		parts = append(parts, "per-order limit "+moneyString(d.Limits.PerOrder))
	}
	if d.Limits.Daily.Amount > 0 {
		parts = append(parts, "daily limit "+moneyString(d.Limits.Daily))
	}
	if d.Limits.Monthly.Amount > 0 {
		parts = append(parts, "monthly limit "+moneyString(d.Limits.Monthly))
	}
	if len(parts) == 0 {
		return base
	}
	return base + " (" + strings.Join(parts, ", ") + ")"
}

func moneyString(m Money) string {
	s := strconv.FormatFloat(m.Amount, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i-1 > 2 {
		s = strconv.FormatFloat(m.Amount, 'f', 2, 64)
	}
	if m.Currency != "" {
		s += " " + strings.ToUpper(m.Currency)
	}
	return s
}
