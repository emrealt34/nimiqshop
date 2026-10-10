package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

// Test accounts are wallets the operator lists in the admin console. A listed
// wallet's own checkouts run the simulated path: real validation and pricing,
// no supplier order, no real payment, and a "simulate payment" button. Every
// other wallet gets the normal flow. Listing is per wallet, so the global
// TEST_MODE switch is never needed for this.

// isTestAccountUser reports whether the signed-in user's wallet is listed.
func (h *Handlers) isTestAccountUser(userID string) bool {
	if userID == "" {
		return false
	}
	u, err := h.Store.GetUser(userID)
	if err != nil || u.NimiqAddress == "" {
		return false
	}
	return h.Store.IsTestAccount(nimiq.NormalizeAddress(u.NimiqAddress))
}

// canTestPay reports whether the simulated pay button (and its endpoint) apply
// to this quote: a simulated order owned by a listed test wallet. The global
// TEST_MODE switch does not extend this, so nobody else can simulate a payment.
func (h *Handlers) canTestPay(q db.Quote) bool {
	return q.TestMode && strings.HasPrefix(q.SupplierOrderID, "TESTSIM-") && h.isTestAccountUser(q.UserID)
}

// simulatedPaymentFor is the single gate for the simulated checkout: the
// global TEST_MODE switch (dev/staging) or a listed test wallet (live).
func (h *Handlers) simulatedPaymentFor(userID string) bool {
	return h.Cfg.TestMode || h.isTestAccountUser(userID)
}

type testAccountView struct {
	Address string    `json:"address"`
	Label   string    `json:"label"`
	AddedAt time.Time `json:"added_at"`
}

func testAccountViews(list []db.TestAccount) []testAccountView {
	out := make([]testAccountView, 0, len(list))
	for _, a := range list {
		out = append(out, testAccountView{Address: a.Address, Label: a.Label, AddedAt: a.AddedAt})
	}
	return out
}

// GET /api/admin/test-accounts
func (h *Handlers) AdminListTestAccounts(ctx *fasthttp.RequestCtx) {
	list, err := h.Store.ListTestAccounts()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load test accounts")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"accounts": testAccountViews(list)})
}

// POST /api/admin/test-accounts  { "address": "NQ..", "label": "" }
func (h *Handlers) AdminAddTestAccount(ctx *fasthttp.RequestCtx) {
	var req struct {
		Address string `json:"address"`
		Label   string `json:"label"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	addr := nimiq.NormalizeAddress(req.Address)
	if err := nimiq.ValidateAddress(addr); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "not a valid Nimiq address")
		return
	}
	label := strings.TrimSpace(req.Label)
	if len([]rune(label)) > 60 {
		writeError(ctx, fasthttp.StatusBadRequest, "label is too long (max 60 characters)")
		return
	}
	list, err := h.Store.AddTestAccount(db.TestAccount{Address: addr, Label: label, AddedAt: time.Now().UTC()})
	if errors.Is(err, db.ErrTestAccountExists) {
		writeError(ctx, fasthttp.StatusConflict, "this wallet is already a test account")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not save test account")
		return
	}
	writeJSON(ctx, fasthttp.StatusCreated, map[string]interface{}{"accounts": testAccountViews(list)})
}

// DELETE /api/admin/test-accounts/{address}
func (h *Handlers) AdminRemoveTestAccount(ctx *fasthttp.RequestCtx) {
	addr, _ := ctx.UserValue("address").(string)
	addr = nimiq.NormalizeAddress(addr)
	list, found, err := h.Store.RemoveTestAccount(addr)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not remove test account")
		return
	}
	if !found {
		writeError(ctx, fasthttp.StatusNotFound, "test account not found")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{"accounts": testAccountViews(list)})
}
