package handlers

import (
	"fmt"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

const CodeMonthlyLimit = "MONTHLY_LIMIT_EXCEEDED"

// A calendar purchase budget is not request throttling and not a cashback cap.
// Refuse before supplier creation, with the same fields the checkout UI uses.
func (h *Handlers) writeMonthlyLimitError(ctx *fasthttp.RequestCtx, userID string, attempted money.Micros, subject string) {
	now := time.Now().UTC()
	usage, err := h.Store.GetUserMonthlyUsage(userID, now)
	remaining := money.Micros(0)
	if err == nil {
		remaining = money.FromFloat(h.Cfg.MonthlySpendLimitUSD) - usage.SpendUSD
	}
	if remaining < 0 {
		remaining = 0
	}
	msg := fmt.Sprintf("This order exceeds your monthly purchase limit ($%s per UTC calendar month) — the %s is above what is left this month ($%s). Choose an amount within both daily and monthly limits, or wait until the next month starts at 00:00 UTC. Nothing was charged.", usdShort(money.FromFloat(h.Cfg.MonthlySpendLimitUSD)), subject, usdShort(remaining))
	writeJSON(ctx, fasthttp.StatusTooManyRequests, map[string]interface{}{
		"error": msg, "detail": msg, "code": CodeMonthlyLimit, "limit_reason": "spend", "limit_period": "monthly",
		"attempted_usd": attempted.String(), "used_usd": usage.SpendUSD.String(), "max_usd": h.Cfg.MonthlySpendLimitUSD,
		"remaining_usd": remaining.String(), "max_orders": 0, "remaining_orders": -1,
		"resets_at": db.PurchaseMonthStart(now).AddDate(0, 1, 0), "server_now": now,
	})
}
