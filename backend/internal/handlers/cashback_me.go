package handlers

import (
	"fmt"
	"strings"
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

// purchaseTxOfQuote is the Lightning payment hash of the purchase behind a
// cashback row — the owner wants the product's tx on his own summary too
// (2026-10-05). Empty when the quote is gone or was paid off-chain.
// CashbackMe is the signed-in buyer's cashback ledger: the paid-vs-pending
// totals plus the per-order rows the Cashback & staking page renders.
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

	sum, err := h.Store.GetUserCashbackSummary(userID)
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
		ID                  string     `json:"id"`
		QuoteID             string     `json:"quote_id"`
		ProductID           string     `json:"product_id"`
		AmountNIM           float64    `json:"amount_nim"`
		Bps                 int        `json:"bps"`
		Status              string     `json:"status"`
		StatusLabel         string     `json:"status_label"`
		Boosted             bool       `json:"boosted"`
		CashbackSource      string     `json:"cashback_source,omitempty"`
		CashbackCode        string     `json:"cashback_code,omitempty"`
		CashbackDestination string     `json:"cashback_destination,omitempty"`
		PaidBaseNIM         float64    `json:"paid_base_nim,omitempty"`
		BaseSource          string     `json:"base_source,omitempty"`
		TxHash              string     `json:"tx_hash,omitempty"`
		PurchaseTx          string     `json:"purchase_tx,omitempty"`
		Country             string     `json:"country,omitempty"`
		PaidAt              *time.Time `json:"paid_at,omitempty"`
		CreatedAt           time.Time  `json:"created_at"`
	}

	out := make([]cashbackRow, 0, len(rows))
	for _, cb := range rows {
		dest := cb.CashbackDestination
		if dest == "" {
			dest = db.CashbackDestWallet
		}
		// The thumb template is shared with every other page (REQ-62 class
		// fix): it needs the quote's product country to pick the real
		// catalog logo instead of falling back to the US default.
		purchaseTx := ""
		country := ""
		if cb.QuoteID != "" {
			if q, qerr := h.Store.GetQuote(cb.QuoteID); qerr == nil {
				purchaseTx = q.LightningPaymentHash
				country = q.ProductCountry
			}
		}
		out = append(out, cashbackRow{
			ID:                  cb.ID,
			QuoteID:             cb.QuoteID,
			ProductID:           cb.ProductID,
			AmountNIM:           float64(cb.AmountLuna) / lunaPerNIM,
			Bps:                 cb.Bps,
			Status:              cb.Status,
			StatusLabel:         cashbackStatusLabel(cb.Status),
			Boosted:             cb.Boosted,
			CashbackSource:      cb.CashbackSource,
			CashbackCode:        cb.CashbackCode,
			CashbackDestination: dest,
			PaidBaseNIM:         cb.PaidBaseNIM,
			BaseSource:          cb.BaseSource,
			TxHash:              cb.TxHash,
			PurchaseTx:          purchaseTx,
			Country:             country,
			PaidAt:              cb.PaidAt,
			CreatedAt:           cb.CreatedAt,
		})
	}

	paidNIM := float64(sum.PaidLuna) / lunaPerNIM
	pendingNIM := float64(sum.PendingLuna) / lunaPerNIM
	burnedNIM := float64(sum.BurnedLuna) / lunaPerNIM
	walletNIM := float64(sum.WalletLuna) / lunaPerNIM

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"totals": map[string]any{
			"paid_nim":      paidNIM,
			"paid_count":    sum.PaidCount,
			"pending_nim":   pendingNIM,
			"pending_count": sum.PendingCount,
			"earned_nim":    paidNIM + pendingNIM,
			"burned_nim":    burnedNIM,
			"burned_count":  sum.BurnedCount,
			"wallet_nim":    walletNIM,
			"wallet_count":  sum.WalletCount,
			"orders":        sum.PaidCount + sum.PendingCount,
			"preference":    sum.LastDest,
		},
		"cashbacks":              out,
		"ledger":                 h.ledgerView(user.NimiqAddress),
		"staker_program_enabled": h.stakeProgramEnabled(),
		"stake_cashback":         h.stakeCashbackView(),
	})
}

// CashbackLeaderboard returns the global cashback totals plus the public
// leaderboard for ?bucket=week|month|all (default all).
func (h *Handlers) CashbackLeaderboard(ctx *fasthttp.RequestCtx) {
	bucket := strings.ToLower(strings.TrimSpace(string(ctx.QueryArgs().Peek("bucket"))))
	switch bucket {
	case "", "all":
		bucket = "all"
	case "week":
		y, w := time.Now().UTC().ISOWeek()
		bucket = fmt.Sprintf("week:%04dW%02d", y, w)
	case "month":
		bucket = "month:" + time.Now().UTC().Format("2006-01")
	default:
		if !strings.HasPrefix(bucket, "week:") && !strings.HasPrefix(bucket, "month:") {
			bucket = "all"
		}
	}
	totals, leaderboard, err := h.Store.CashbackLeaderboardAndTotals(bucket, 50)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load cashback leaderboard")
		return
	}
	// REQ-63: burn is gone from every user-facing surface — the public
	// leaderboard no longer advertises a burn wallet address either.
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"bucket":      bucket,
		"totals":      totals,
		"leaderboard": leaderboard,
	})
}
