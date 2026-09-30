package handlers

// User-facing controls for the 1-Luna wallet-memo channel.
//
//	GET  /api/account/notifications        -> { wallet_memo: {enabled, ...} }
//	PUT  /api/account/notifications        -> { enabled: bool }
//
// The opt-out exists because this channel writes into a wallet's permanent
// history. A user who wants it to stop must be able to stop it in one tap,
// without contacting support, and it must stop everything — there is no
// "important updates only" trickle that keeps costing them ledger lines.

import (
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/middleware"
	"nimiqshop/internal/notification"
)

// GetNotificationPrefs returns the caller's wallet-notification settings and
// the policy that governs them, so the UI can explain the rules honestly
// instead of hiding them in a tooltip.
func (h *Handlers) GetNotificationPrefs(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	enabled, err := h.Store.WalletNotifyEnabled(userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not read your notification settings")
		return
	}
	reasons := make([]map[string]any, 0, 8)
	for _, r := range notification.Reasons() {
		reasons = append(reasons, map[string]any{
			"reason":      string(r),
			"description": notification.Describe(r),
			"budgeted":    notification.CountsToBudget(r),
		})
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"wallet_memo": map[string]any{
			"enabled":   enabled,
			"available": h.WalletNotifier != nil && h.WalletNotifier.Enabled(),
			// Surfaced so the UI can say "at most 2 shop-initiated messages
			// per month" rather than asking users to trust us.
			"monthly_budget": notification.MonthlyBudget,
			"reasons":        reasons,
		},
	})
}

// SetNotificationPrefs updates the caller's opt-in/opt-out.
func (h *Handlers) SetNotificationPrefs(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(ctx, &req); err != nil || req.Enabled == nil {
		writeError(ctx, fasthttp.StatusBadRequest, "enabled (true/false) is required")
		return
	}
	if err := h.Store.SetWalletNotifyEnabled(userID, *req.Enabled); err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not save your notification settings")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"enabled": *req.Enabled})
}
