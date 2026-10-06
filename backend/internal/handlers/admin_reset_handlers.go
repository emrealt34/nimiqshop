package handlers

/*
 * admin_reset_handlers.go — the operator console's "start over" endpoints.
 *
 * These are the only destructive endpoints in the API, so they are built to be
 * awkward to fire by accident and impossible to fire quietly:
 *
 *   GET  /api/admin/reset/preview   what would be deleted, in counts
 *   POST /api/admin/reset           deletes it, given the exact phrase
 *
 * The phrase ("RESET ALL DATA") is required in the request body: a stray
 * request, a replayed one, or a curl with a typo can never delete a shop. The
 * request is admin-authenticated like every other console endpoint, and the
 * deletion is written to the service log with the operator's id and address.
 *
 * Admin accounts, site settings and catalog snapshots are NOT touched — see
 * db.ResetShopData for the exact namespace list and why those three are kept.
 */

import (
	"log"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
	"nimiqshop/internal/db"
)

// resetPhrase is the confirmation the client must send. Deliberately a phrase
// rather than a boolean: it cannot be produced by a UI framework re-sending a
// saved form, and it reads as what it does in the service log.
const resetPhrase = "RESET ALL DATA"

func resetNamespaceView(counts map[string]int) []map[string]any {
	out := make([]map[string]any, 0, len(counts))
	for _, pair := range db.WipeNamespaces() {
		prefix, label := pair[0], pair[1]
		n := counts[prefix]
		if n == 0 {
			continue
		}
		out = append(out, map[string]any{"label": label, "count": n})
	}
	return out
}

// AdminResetPreview answers what a reset would remove right now. Read-only, so
// the console can show it every time the maintenance card loads.
func (h *Handlers) AdminResetPreview(ctx *fasthttp.RequestCtx) {
	counts, err := h.Store.ResetPreview()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"namespaces": resetNamespaceView(counts),
		"total":      total,
		"phrase":     resetPhrase,
		"kept": []string{
			"Operator accounts and sessions",
			"Site settings and meta records",
			"Catalog snapshots",
		},
		"note": "Deleting customer data cannot be undone. The operator login you are using now is kept.",
	})
}

// AdminReset deletes every customer-owned record. Requires the confirmation
// phrase; answers with what was removed, so the console can show a receipt
// instead of a shrug.
func (h *Handlers) AdminReset(ctx *fasthttp.RequestCtx) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(strings.ToUpper(req.Confirm)) != resetPhrase {
		writeError(ctx, fasthttp.StatusBadRequest,
			"confirmation phrase missing — send {\"confirm\":\""+resetPhrase+"\"} to delete customer data")
		return
	}
	started := time.Now()
	counts, err := h.Store.ResetShopData()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	// The audit line: who deleted what, from where, when. This is the record
	// that answers "where did the orders go?" six weeks from now.
	info := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy())
	who := adminIdentity(ctx).User.Username
	log.Printf("ADMIN RESET: %d records across %d namespaces deleted by operator=%q from ip=%q — operator accounts, settings and catalog snapshots kept",
		total, len(counts), who, info.IP)

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"deleted":     resetNamespaceView(counts),
		"total":       total,
		"kept":        []string{"Operator accounts and sessions", "Site settings and meta records", "Catalog snapshots"},
		"duration_ms": time.Since(started).Milliseconds(),
		"at":          time.Now().UTC(),
	})
}
