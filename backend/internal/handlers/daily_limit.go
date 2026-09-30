package handlers

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

// CodeDailyLimit is the machine-readable reason on the 429 a buyer gets when
// the rolling 24-hour purchase budget refuses an order.
//
// A purchase budget and a request-rate limit are different problems with
// different fixes: "wait a few seconds and retry" is wrong advice for "you have
// already spent today's $50". The storefront keys off this code to render the
// daily-limit screen (used / left / when the window slides) instead of a
// generic rate-limit message, and to stop the checkout loop from re-attempting
// the remaining cart items that the same budget would refuse anyway.
const CodeDailyLimit = "DAILY_LIMIT_EXCEEDED"

// writeDailyLimitError answers a refused purchase with the buyer's own numbers
// — what today's window holds, how much is used, what is left, what this order
// would have cost and when the oldest purchase drops off.
//
// subject names the thing that was refused ("price" for a single item, "cart"
// for a batch) so the same sentence reads correctly on both paths. The HTTP
// status stays 429: the purchase is refused for now and a later retry can
// succeed once the window slides.
func (h *Handlers) writeDailyLimitError(ctx *fasthttp.RequestCtx, userID string, attempted money.Micros, subject string) {
	now := time.Now().UTC()
	maxSpend := money.FromFloat(h.Cfg.DailySpendLimitUSD)
	b, err := h.Store.DailyBudgetFor(userID, h.Cfg.DailyOrderLimit, maxSpend, now)
	if err != nil {
		// The gate already refused this purchase, so a failed lookup must not
		// turn the answer into a 500 — explain with the ceilings alone.
		log.Printf("daily limit: budget lookup failed for user %s: %v", userID, err)
	}

	ceiling := fmt.Sprintf("%d orders / $%s per 24h", h.Cfg.DailyOrderLimit, usdShort(maxSpend))
	msg := fmt.Sprintf(
		"this order exceeds your daily purchase limit (%s) — the %s is above what is left today ($%s); try a smaller amount or wait for your oldest purchase to drop off",
		ceiling, subject, usdShort(b.Remaining))
	if b.OrderLimitHit {
		msg = fmt.Sprintf(
			"this order exceeds your daily purchase limit (%s) — all %d of today's purchases are used; wait for your oldest one to drop off",
			ceiling, h.Cfg.DailyOrderLimit)
	}
	if !b.ResetsAt.IsZero() && b.ResetsAt.After(now) {
		msg += fmt.Sprintf(" (about %s from now)", humanWindow(b.ResetsAt.Sub(now)))
	}
	msg += ". Nothing was charged."

	writeJSON(ctx, fasthttp.StatusTooManyRequests, map[string]interface{}{
		"error":            msg,
		"detail":           msg,
		"code":             CodeDailyLimit,
		"limit_reason":     limitReason(b),
		"attempted_usd":    attempted.String(),
		"used_orders":      b.OrderCount,
		"max_orders":       b.MaxOrders,
		"remaining_orders": b.RemainingOrders,
		"used_usd":         b.SpendUSD.String(),
		"max_usd":          h.Cfg.DailySpendLimitUSD,
		"remaining_usd":    b.Remaining.String(),
		"resets_at":        b.ResetsAt,
		"server_now":       now,
	})
}

// limitReason names the binding ceiling so the storefront can pick copy that
// matches the buyer's actual situation ("all 3 purchases used" vs "$4 of $50
// left").
func limitReason(b db.DailyBudget) string {
	if b.OrderLimitHit {
		return "orders"
	}
	return "spend"
}

// usdShort renders micro-USD for a human sentence: "50", "12.34", "0".
// API fields keep the exact 6-decimal money string; only copy is shortened.
func usdShort(m money.Micros) string {
	s := strconv.FormatFloat(m.Float(), 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// humanWindow renders a duration the way the storefront's countdown does, so
// the sentence and the on-screen "resets in 6h 12m" agree.
func humanWindow(d time.Duration) string {
	if d <= 0 {
		return "under a minute"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return "under a minute"
	}
}
