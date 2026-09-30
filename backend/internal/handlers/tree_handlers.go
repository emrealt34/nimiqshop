package handlers

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

// PublicSiteInfo returns the public shop config (feature flags) so the
// frontend knows what payment rails / donation features are enabled.
// Augments the existing /api/site response (kept in site handler); this
// is a dedicated /api/site-config endpoint focused on client feature flags.
func (h *Handlers) SiteConfig(ctx *fasthttp.RequestCtx) {
	writeJSON(ctx, 200, map[string]interface{}{
		"site_host":                h.Cfg.SiteHost,
		"site_name":                h.Cfg.SiteName(),
		"enable_usdt":              true,
		"usdt_coin":                h.Cfg.USDTCoin,
		"usdt_network":             PaymentNetworkStable,
		"usdt_cashback_multiplier": h.Cfg.USDTCashbackMultiplier,
		"tree_planting_enabled":    h.Cfg.TreePlantingEnabled,
		// Public transparency data: this is a donation destination, never a secret.
		"tree_donation_nim_address": h.Cfg.TreePlantingNimAddress,
		"trees_per_usd":             h.Cfg.TreesPerUSD,
		"polygon_scan_base_url":     h.Cfg.PolygonScanBaseURL,
		"polygon_chain_id":          h.Cfg.PolygonChainID,
	})
}

type treeDonationBalanceSnapshot struct {
	BalanceLuna int64
	CachedAt    time.Time
}

const treeDonationBalanceTTL = 60 * time.Second

// TreeDonationBalance returns the public donation wallet balance. The chain
// read is cached briefly in the shared in-memory cache and a stale value is
// preferred over hiding a previously verified balance during an RPC outage.
func (h *Handlers) TreeDonationBalance(ctx *fasthttp.RequestCtx) {
	address := strings.TrimSpace(h.Cfg.TreePlantingNimAddress)
	if !h.Cfg.TreePlantingEnabled || address == "" {
		writeJSON(ctx, 200, map[string]interface{}{"enabled": false, "available": false})
		return
	}

	key := "tree:donation-balance:" + address
	writeSnapshot := func(s treeDonationBalanceSnapshot, cached, stale bool, ageSeconds int64) {
		writeJSON(ctx, 200, map[string]interface{}{
			"enabled":           true,
			"available":         true,
			"address":           address,
			"balance_luna":      s.BalanceLuna,
			"balance_nim":       float64(s.BalanceLuna) / 100000.0,
			"cached":            cached,
			"stale":             stale,
			"cached_at":         s.CachedAt.UTC(),
			"age_seconds":       ageSeconds,
			"cache_ttl_seconds": int64(treeDonationBalanceTTL / time.Second),
		})
	}

	if h.cache != nil {
		// peekStale deliberately keeps an expired snapshot available: if the
		// RPC is down below, the last known balance is still useful to readers.
		if value, age, ok := h.cache.peekStale(key); ok && age < treeDonationBalanceTTL {
			if snapshot, ok := value.(treeDonationBalanceSnapshot); ok {
				writeSnapshot(snapshot, true, false, int64(age.Seconds()))
				return
			}
		}
	}

	if h.NIMRPC == nil {
		writeJSON(ctx, 200, map[string]interface{}{"enabled": true, "available": false, "error": "NIM RPC unavailable"})
		return
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	balanceLuna, err := h.NIMRPC.GetAccountBalance(readCtx, address)
	cancel()
	if err != nil {
		if h.cache != nil {
			if value, age, ok := h.cache.peekStale(key); ok {
				if snapshot, ok := value.(treeDonationBalanceSnapshot); ok {
					writeSnapshot(snapshot, true, true, int64(age.Seconds()))
					return
				}
			}
		}
		writeJSON(ctx, 200, map[string]interface{}{"enabled": true, "available": false, "error": "NIM balance temporarily unavailable"})
		return
	}

	snapshot := treeDonationBalanceSnapshot{BalanceLuna: balanceLuna, CachedAt: time.Now().UTC()}
	if h.cache != nil {
		h.cache.setTTL(key, snapshot, treeDonationBalanceTTL)
	}
	writeSnapshot(snapshot, false, false, 0)
}

// ---------------- Tree planting public API ----------------

// TreePublic returns the public leaderboard and totals.
// ?bucket=week|month|all  (default all)
func (h *Handlers) TreePublic(ctx *fasthttp.RequestCtx) {
	if !h.Cfg.TreePlantingEnabled {
		writeJSON(ctx, 200, map[string]interface{}{"enabled": false})
		return
	}
	bucket := strings.ToLower(strings.TrimSpace(string(ctx.QueryArgs().Peek("bucket"))))
	switch bucket {
	case "", "all":
		bucket = "all"
	case "week":
		// Current ISO week
		y, w := time.Now().UTC().ISOWeek()
		bucket = "week:" + formatISOBucket(y, w)
	case "month":
		bucket = "month:" + time.Now().UTC().Format("2006-01")
	default:
		// Allow explicit "week:YYYYWww" / "month:YYYY-MM"
		if !strings.HasPrefix(bucket, "week:") && !strings.HasPrefix(bucket, "month:") {
			bucket = "all"
		}
	}
	totals, err := h.Store.TreeTotals()
	if err != nil {
		writeError(ctx, 500, "could not load tree totals")
		return
	}
	leaderboard, err := h.Store.TreeLeaderboard(bucket, 50)
	if err != nil {
		writeError(ctx, 500, "could not load leaderboard")
		return
	}
	settlements, err := h.Store.ListTreeSettlements(24)
	if err != nil {
		settlements = nil
	}
	// Hide user ids on the public leaderboard: hash them / use short codes.
	// For simplicity keep first 8 chars of user id (already a uuid, not a
	// wallet address) so users can identify themselves.
	lb := make([]map[string]interface{}, 0, len(leaderboard))
	for i, row := range leaderboard {
		uid := row.UserID
		if len(uid) > 8 {
			uid = uid[:8]
		}
		lb = append(lb, map[string]interface{}{
			"rank":    i + 1,
			"user":    uid,
			"trees":   row.Trees,
			"planted": row.Planted,
			"orders":  row.Count,
		})
	}
	// Build settlement public view: include polygonscan link when tx hash present.
	settView := make([]map[string]interface{}, 0, len(settlements))
	for _, s := range settlements {
		row := map[string]interface{}{
			"id":            s.ID,
			"month_bucket":  s.MonthBucket,
			"amount_usdt":   s.AmountUSDT,
			"amount_label":  s.AmountLabel,
			"amount_value":  s.AmountValue,
			"amount_nim":    s.AmountNIM,
			"wallet_nim":    s.WalletNIM,
			"status":        s.Status,
			"trees_planted": s.TreesPlanted,
			"created_at":    s.CreatedAt,
			"note":          s.Note,
		}
		if s.TxHash != "" {
			row["tx_hash"] = s.TxHash
		}
		if s.TransactionURL != "" {
			row["polygonscan_url"] = s.TransactionURL
		} else if s.TxHash != "" {
			row["polygonscan_url"] = h.Cfg.PolygonScanBaseURL + "/tx/" + s.TxHash
		}
		if s.FromAddress != "" {
			row["from_address"] = s.FromAddress
		}
		if s.ToAddress != "" {
			row["to_address"] = s.ToAddress
		}
		if len(s.ProofImages) > 0 {
			row["proof_images"] = s.ProofImages
		}
		settView = append(settView, row)
	}
	writeJSON(ctx, 200, map[string]interface{}{
		"enabled":              true,
		"bucket":               bucket,
		"totals":               totals,
		"leaderboard":          lb,
		"settlements":          settView,
		"trees_per_usd":        h.Cfg.TreesPerUSD,
		"one_tree_planted_url": "https://onetreeplanted.org/",
	})
}

func formatISOBucket(year, week int) string {
	// "2026W37"
	return itoa(year) + "W" + twoDigit(week)
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
func twoDigit(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// TreeMe returns the authenticated user's planting stats + prefs.
func (h *Handlers) TreeMe(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	if userID == "" {
		writeError(ctx, 401, "auth required")
		return
	}
	if !h.Cfg.TreePlantingEnabled {
		writeJSON(ctx, 200, map[string]interface{}{"enabled": false})
		return
	}
	prefs, _ := h.Store.GetTreeUserPrefs(userID)
	t, _ := h.Store.UserTreeTotal(userID)
	writeJSON(ctx, 200, map[string]interface{}{
		"enabled":            true,
		"preference":         prefs.Destination,
		"user_trees":         t.Funded,
		"user_trees_planted": t.Planted,
		"user_trees_pending": t.Pending,
		"user_orders":        t.Orders,
	})
}

// TreeSetPrefs updates the buyer's cashback destination.
func (h *Handlers) TreeSetPrefs(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)
	if userID == "" {
		writeError(ctx, 401, "auth required")
		return
	}
	if !h.Cfg.TreePlantingEnabled {
		writeError(ctx, 400, "tree planting is not enabled")
		return
	}
	var req struct {
		Destination string `json:"destination"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, 400, "invalid request")
		return
	}
	dest := strings.ToLower(strings.TrimSpace(req.Destination))
	if dest != db.TreeDestTrees && dest != db.TreeDestCashback {
		writeError(ctx, 400, "destination must be 'cashback' or 'trees'")
		return
	}
	prefs, err := h.Store.SetTreeUserPrefs(userID, dest)
	if err != nil {
		writeError(ctx, 500, "could not save preference")
		return
	}
	writeJSON(ctx, 200, map[string]interface{}{
		"ok":         true,
		"preference": prefs.Destination,
	})
}

// AdminRecordTreeSettlement is the month-end manual disclosure. Admin-only.
func (h *Handlers) AdminRecordTreeSettlement(ctx *fasthttp.RequestCtx) {
	var req struct {
		Month          string              `json:"month_bucket"`
		AmountUSDT     float64             `json:"amount_usdt"`
		AmountLabel    string              `json:"amount_label"`
		AmountValue    string              `json:"amount_value"`
		TreesPlanted   float64             `json:"trees_planted"`
		TxHash         string              `json:"tx_hash"`
		TransactionURL string              `json:"transaction_url"`
		FromAddress    string              `json:"from_address"`
		ToAddress      string              `json:"to_address"`
		Note           string              `json:"note"`
		Status         string              `json:"status"`
		AmountNIM      float64             `json:"amount_nim"`
		WalletNIM      float64             `json:"wallet_nim"`
		ProofImages    []db.TreeProofImage `json:"proof_images"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, 400, "invalid request")
		return
	}
	req.TxHash = strings.TrimSpace(req.TxHash)
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	if req.Status == "" {
		req.Status = "paid"
	}
	if req.Status != "paid" && req.Status != "skipped" {
		writeError(ctx, 400, "status must be paid or skipped")
		return
	}
	if req.Status == "paid" && (strings.TrimSpace(req.AmountLabel) == "" || strings.TrimSpace(req.AmountValue) == "" || req.TreesPlanted < 0 || (req.TxHash == "" && strings.TrimSpace(req.TransactionURL) == "")) {
		writeError(ctx, 400, "paid month requires a manual amount label, amount value, tree count, and transaction hash or direct link")
		return
	}
	if req.Status == "skipped" && (req.AmountNIM < 0 || req.WalletNIM < 0) {
		writeError(ctx, 400, "skipped month NIM values cannot be negative")
		return
	}
	const prefix = "data:image/png;base64,"
	for i := range req.ProofImages {
		req.ProofImages[i].Caption = strings.TrimSpace(req.ProofImages[i].Caption)
		data := req.ProofImages[i].Data
		if !strings.HasPrefix(data, prefix) || len(data) > 2*1024*1024 || func() bool {
			_, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, prefix))
			return e != nil
		}() {
			writeError(ctx, 400, "each proof image must be a PNG data URI smaller than 2 MB")
			return
		}
	}
	st, err := h.Store.RecordTreeSettlementWithMeta(req.Month, req.TxHash, strings.TrimSpace(req.TransactionURL), req.FromAddress, req.ToAddress, req.AmountUSDT, strings.TrimSpace(req.AmountLabel), strings.TrimSpace(req.AmountValue), req.TreesPlanted, req.AmountNIM, req.WalletNIM, req.ProofImages, req.Status, req.Note)
	if err != nil {
		writeError(ctx, 500, "could not record settlement: "+err.Error())
		return
	}
	writeJSON(ctx, 200, st)
}

// AdminUpdateTreeSettlement edits every public/manual field, including all proof PNGs.
func (h *Handlers) AdminUpdateTreeSettlement(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	var req struct {
		Month          string              `json:"month_bucket"`
		AmountUSDT     float64             `json:"amount_usdt"`
		AmountLabel    string              `json:"amount_label"`
		AmountValue    string              `json:"amount_value"`
		TreesPlanted   float64             `json:"trees_planted"`
		TxHash         string              `json:"tx_hash"`
		TransactionURL string              `json:"transaction_url"`
		FromAddress    string              `json:"from_address"`
		ToAddress      string              `json:"to_address"`
		Note           string              `json:"note"`
		Status         string              `json:"status"`
		AmountNIM      float64             `json:"amount_nim"`
		WalletNIM      float64             `json:"wallet_nim"`
		ProofImages    []db.TreeProofImage `json:"proof_images"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, 400, "invalid request")
		return
	}
	if req.Status == "" {
		req.Status = "paid"
	}
	if req.Status == "paid" && (req.AmountLabel == "" || req.AmountValue == "" || req.TreesPlanted < 0 || (req.TxHash == "" && req.TransactionURL == "")) {
		writeError(ctx, 400, "paid payout requires amount, trees, and transaction link")
		return
	}
	for _, p := range req.ProofImages {
		if !strings.HasPrefix(p.Data, "data:image/png;base64,") || len(p.Data) > 2*1024*1024 {
			writeError(ctx, 400, "invalid proof PNG")
			return
		}
	}
	st := db.TreeSettlement{ID: id, MonthBucket: req.Month, AmountUSDT: req.AmountUSDT, AmountLabel: req.AmountLabel, AmountValue: req.AmountValue, TreesPlanted: req.TreesPlanted, TxHash: req.TxHash, TransactionURL: req.TransactionURL, FromAddress: req.FromAddress, ToAddress: req.ToAddress, Note: req.Note, Status: req.Status, AmountNIM: req.AmountNIM, WalletNIM: req.WalletNIM, ProofImages: req.ProofImages}
	if err := h.Store.UpdateTreeSettlement(id, st); err != nil {
		writeError(ctx, 500, "could not update payout")
		return
	}
	writeJSON(ctx, 200, st)
}

// AdminListTreeSettlements returns every settlement recorded (newest first).
func (h *Handlers) AdminListTreeSettlements(ctx *fasthttp.RequestCtx) {
	list, err := h.Store.ListTreeSettlements(100)
	if err != nil {
		writeError(ctx, 500, "could not list settlements")
		return
	}
	writeJSON(ctx, 200, list)
}
