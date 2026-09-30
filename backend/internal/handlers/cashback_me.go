package handlers

import (
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

// cashbackStatusLabel turns a stored cashback status into the buyer-facing
// word the Cashback & staking page shows. The raw status stays in "status";
// this label never leaks internal skip reasons or worker state.
func cashbackStatusLabel(status string) string {
	switch status {
	case db.CashbackQueued:
		return "queued"
	case db.CashbackSending:
		return "sending"
	case db.CashbackBroadcast:
		return "confirming"
	case db.CashbackPaid:
		return "paid"
	case db.CashbackSkipped:
		return "skipped"
	default:
		return status
	}
}

// CashbackMe is the signed-in buyer's cashback ledger: the paid-vs-pending
// totals plus the per-order rows the Cashback & staking page renders.
//
// It was registered in cmd/server/main.go as GET /api/cashback/me but the
// handler itself was missing, so the endpoint 404'd and the page never
// showed a buyer's payout history. The store already exposed
// UserCashbackTotals and ListCashbacksByUser, so this only wires them up.
func (h *Handlers) CashbackMe(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	if userID == "" {
		writeError(ctx, fasthttp.StatusUnauthorized, "session expired")
		return
	}
	user, err := h.Store.GetUser(userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "session expired")
		return
	}

	// This response displays the ledger, therefore this is one of the few
	// useful moments to refresh the signed-in wallet's cumulative pool profit.
	h.syncProfitOnDemand(ctx, user.NimiqAddress)

	paidLuna, pendingLuna, paidCount, pendingCount, err := h.Store.UserCashbackTotals(userID)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load cashback")
		return
	}

	rows, err := h.Store.ListCashbacksByUser(userID, 50)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load cashback")
		return
	}

	type cashbackRow struct {
		ID             string  `json:"id"`
		QuoteID        string  `json:"quote_id"`
		ProductID      string  `json:"product_id"`
		AmountNIM      float64 `json:"amount_nim"`
		Bps            int     `json:"bps"`
		Status         string  `json:"status"`
		StatusLabel    string  `json:"status_label"`
		Boosted        bool    `json:"boosted"`
		CashbackSource string  `json:"cashback_source,omitempty"`
		CashbackCode   string  `json:"cashback_code,omitempty"`
		// Paid-invoice cashback audit: the NIM base the rate was applied to
		// and whether it followed the PAID amount ("paid") or the shop's
		// priced base ("priced"). ProductNIM comparison lives in the admin
		// view; here the buyer sees what their cashback was measured on.
		PaidBaseNIM float64    `json:"paid_base_nim,omitempty"`
		BaseSource  string     `json:"base_source,omitempty"`
		TxHash      string     `json:"tx_hash,omitempty"`
		PaidAt      *time.Time `json:"paid_at,omitempty"`
		CreatedAt   time.Time  `json:"created_at"`
	}

	out := make([]cashbackRow, 0, len(rows))
	for _, cb := range rows {
		out = append(out, cashbackRow{
			ID:             cb.ID,
			QuoteID:        cb.QuoteID,
			ProductID:      cb.ProductID,
			AmountNIM:      float64(cb.AmountLuna) / lunaPerNIM,
			Bps:            cb.Bps,
			Status:         cb.Status,
			StatusLabel:    cashbackStatusLabel(cb.Status),
			Boosted:        cb.Boosted,
			CashbackSource: cb.CashbackSource,
			CashbackCode:   cb.CashbackCode,
			PaidBaseNIM:    cb.PaidBaseNIM,
			BaseSource:     cb.BaseSource,
			TxHash:         cb.TxHash,
			PaidAt:         cb.PaidAt,
			CreatedAt:      cb.CreatedAt,
		})
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"totals": map[string]any{
			"paid_nim":      float64(paidLuna) / lunaPerNIM,
			"paid_count":    paidCount,
			"pending_nim":   float64(pendingLuna) / lunaPerNIM,
			"pending_count": pendingCount,
		},
		"cashbacks": out,
		// The single-ledger staker book for this wallet (available $, boost
		// rate, loyalty age, remaining caps) — the "Cashback & staking"
		// page renders it as one clean card, no levels.
		"ledger":                 h.ledgerView(user.NimiqAddress),
		"staker_program_enabled": h.stakeProgramEnabled(),
		"stake_cashback":         h.stakeCashbackView(),
	})
}
