package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

// Test accounts are wallets the operator lists in the admin console.
//
// A listed test wallet runs the REAL checkout: the same validation, the same
// pricing, the same catalog rules, a REAL supplier order and a REAL, payable
// invoice. The ONLY simulated thing left is the "simulate payment" button:
// pressing it settles the order through the real state machine without any
// money moving, which is what makes the button a test tool rather than a
// purchase. Before this, a listed wallet was handed a fabricated invoice
// (checksum-valid but unpayable, always priced at 0.00001 BTC) and no supplier
// order at all — so nothing about the payment rail could be tested for real.
//
// Everything else stays simulated in the bookkeeping sense (Quote.TestMode):
// test orders are hidden from the public feed, badged in the admin console,
// paid out with a TESTTX- cashback hash, and kept out of the daily budget.
//
// The global TEST_MODE switch (dev/staging) is a different thing: there the
// supplier itself is simulated, so quotes get the fabricated attach.

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

// testModeQuoteFor reports whether a quote created by this user is a TEST
// order for bookkeeping: hidden from public surfaces, simulated cashback
// payout, no daily-budget consumption. A listed test wallet's quote is one;
// the global TEST_MODE switch makes every quote one.
func (h *Handlers) testModeQuoteFor(userID string) bool {
	return h.Cfg.TestMode || h.isTestAccountUser(userID)
}

// simulatedPaymentFor is the single gate for the SIMULATED supplier: when it
// is true the quote gets a fabricated invoice instead of a real supplier
// order. That is now the global TEST_MODE switch only (dev/staging, where no
// real supplier order should be created at all). A listed test wallet on the
// live site is NOT simulated here — it pays a real invoice, or fakes the
// payment with its test-pay button.
func (h *Handlers) simulatedPaymentFor(userID string) bool {
	return h.Cfg.TestMode
}

// canTestPay reports whether the simulated pay button (and its endpoint) apply
// to this quote: an unsettled order owned by a listed test wallet. The wallet
// — never the global switch — is what grants it, so nobody else can settle an
// order without paying. A quote whose payment target was fabricated by the
// admin sandbox (TESTSIM-) can still be driven when the global TEST_MODE
// switch is on; on the live site that shape is the operator's test center and
// belongs to the admin test-pay endpoint, not to a customer's button.
func (h *Handlers) canTestPay(q db.Quote) bool {
	if !h.isTestAccountUser(q.UserID) {
		return false
	}
	if h.Cfg.TestMode {
		return !settledForTestPay(q)
	}
	return !q.SimulatedSupplier() && !settledForTestPay(q)
}

// settledForTestPay reports whether this quote is already paid or otherwise
// finished, so the simulated pay button has nothing left to do.
func settledForTestPay(q db.Quote) bool {
	switch q.Status {
	case cryptorefills.QuoteFulfilled, cryptorefills.QuoteRefunded,
		cryptorefills.QuoteExpired, cryptorefills.QuoteFailed:
		return true
	default:
		return false
	}
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
