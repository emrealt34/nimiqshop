package main

// Demo + regression guard for the operator's question: can the cashback
// engine pay a TEST order end to end, and can that be abused?
//
//   listed test wallet → real quote (real supplier order, real invoice)
//   → "simulate payment" → the REAL cashback engine books the row with
//   TestMode=true → the REAL worker pays it with a TESTTX- hash, with NO
//   seed and NO RPC.
//
// The other half of the claim is the abuse guard: the same seedless worker
// leaves a REAL row queued (a real payout needs a funded, configured wallet),
// the simulated row never reaches the public feed or the public leaderboard,
// and only an admin can list a wallet in the first place.
//
// Note on prices: the test stack deliberately runs without an oracle, so an
// HTTP-created quote has no NIM snapshot and its cashback row is booked as
// "skipped: product NIM price unknown" (that is itself the engine's
// fail-closed guard, see the first section). The payout section therefore
// writes the same TestMode row the engine writes on a priced order — the
// shape the admin test center produces on a live deployment — with an
// explicit price, so the worker half is deterministic.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cbworker "nimiqshop/internal/cashback/worker"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

func TestTestWalletCashbackIsPaidThroughTheSimulatedPayout(t *testing.T) {
	s := bootStack(t, stackOptions{}) // live setting: global TEST_MODE is OFF
	admin := s.adminHeaders()
	listed, listedAddress := s.signIn()
	other, otherAddress := s.signIn()
	buyer, err := s.h.Store.GetUserByAddress(nimiq.NormalizeAddress(listedAddress))
	if err != nil {
		t.Fatal(err)
	}

	// Operator lists ONE wallet and sets the base rate to 1%. Nobody else
	// can do either, and an unlisted wallet is unaffected.
	if add := s.do(http.MethodPost, "/api/admin/test-accounts",
		map[string]any{"address": listedAddress, "label": "cashback demo"}, admin); add.status != 201 {
		t.Fatalf("add test account: %d %s", add.status, truncate(add.body))
	}
	if set := s.do(http.MethodPost, "/api/admin/settings/cashback",
		map[string]any{"cashback_bps": 100}, admin); set.status != 200 {
		t.Fatalf("cashback rate: %d %s", set.status, truncate(set.body))
	}

	// ---- 1. The test wallet checks out like any customer ------------------
	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-airbnb", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 25,
		"email":                "cashback-demo@example.com",
		"cashback_destination": "cashback", // pay the NIM to my own wallet
	}, listed)
	if q.status != 201 {
		t.Fatalf("listed wallet quote: %d %s", q.status, truncate(q.body))
	}
	if q.json(t)["simulated_payment"] == true {
		t.Fatal("a listed test wallet must still get a REAL order/invoice; only the pay button is simulated")
	}
	quoteID := quoteIDFrom(t, q)

	// ---- 2. It presses "simulate payment" --------------------------------
	pay := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{}, listed)
	if pay.status != 200 {
		t.Fatalf("test-pay: %d %s", pay.status, truncate(pay.body))
	}
	if st := quoteStatus(pay.json(t)); st != "fulfilled" {
		t.Fatalf("simulated order should fulfil, got %q", st)
	}

	// ---- 3. The REAL cashback engine booked a row for it -----------------
	httpCB, err := s.h.Store.GetCashbackByQuote(quoteID)
	if err != nil {
		t.Fatalf("a fulfilled test order must enqueue cashback: %v", err)
	}
	if !httpCB.TestMode {
		t.Fatalf("cashback for a test order must carry TestMode: %+v", httpCB)
	}
	if httpCB.UserID != buyer.ID || httpCB.CashbackDestination != db.CashbackDestWallet {
		t.Fatalf("cashback must belong to the buyer and their wallet: %+v", httpCB)
	}
	// With no oracle in the test stack the price is unknown, and the engine
	// refuses to guess: the row is skipped, never paid with an invented
	// amount. On a live deployment (warm rate snapshot) the same row is
	// queued and paid in luna exactly like a real purchase.
	if httpCB.Status == db.CashbackSkipped {
		if !strings.Contains(httpCB.SkipReason, "NIM price unknown") || httpCB.TxHash != "" || httpCB.AmountLuna != 0 {
			t.Fatalf("a price-less row must be skipped cleanly: %+v", httpCB)
		}
	} else if httpCB.Status != db.CashbackQueued || httpCB.AmountLuna <= 0 {
		t.Fatalf("unexpected cashback state for the test order: %+v", httpCB)
	}

	// ---- 4. The simulated payout: TestMode row → TESTTX- hash ------------
	// Same row the engine writes for a priced test order (the admin test
	// center's shape), written directly so the test does not need an oracle.
	demoQ := db.Quote{
		ID: "demo-test-cashback", UserID: buyer.ID, ProductID: "test-airbnb",
		ProductCountry: "US", ProductValue: 25, ProductCurrency: "USD", Quantity: 1,
		Status: "awaiting_payment",
		// 1 USD order with a 0.01 USD/NIM snapshot: 100 NIM of product.
		ProductUSD: 1_000_000, NimUsdRate: 0.01, EstimatedNIM: 100,
		CashbackDestination: db.CashbackDestWallet,
		TestMode:            true,
		CreatedAt:           time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.h.Store.CreateQuoteWithDailyLimits(demoQ, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Store.CompleteQuoteWithFulfillment(demoQ.ID, nil); err != nil {
		t.Fatal(err)
	}
	cb, err := s.h.Store.GetCashbackByQuote(demoQ.ID)
	if err != nil {
		t.Fatalf("fulfilled test quote must enqueue cashback: %v", err)
	}
	if !cb.TestMode || cb.Status != db.CashbackQueued || cb.TxHash != "" {
		t.Fatalf("row should be queued, unpaid and marked as a test: %+v", cb)
	}
	// 1% of 100 NIM = 1 NIM = 100000 luna.
	if cb.Bps != 100 || cb.AmountLuna != 100_000 {
		t.Fatalf("rate/amount wrong: bps=%d amount=%d", cb.Bps, cb.AmountLuna)
	}
	if cb.UserID != buyer.ID || cb.Recipient != nimiq.NormalizeAddress(listedAddress) {
		t.Fatalf("cashback must go to the buyer's own wallet: user=%q recipient=%q", cb.UserID, cb.Recipient)
	}

	// The worker runs with NO seed and NO RPC: a simulated payout can never
	// touch the chain. This is what makes test orders safe to repeat.
	w := &cbworker.Worker{Store: cbworker.WithQuizPayouts(s.h.Store), Interval: 30 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Run(ctx)

	var paid db.Cashback
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if row, gerr := s.h.Store.GetCashbackByQuote(demoQ.ID); gerr == nil {
			paid = row
			if row.Status == db.CashbackPaid {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if paid.Status != db.CashbackPaid {
		t.Fatalf("seedless worker did not pay the simulated row: %+v", paid)
	}
	if !strings.HasPrefix(paid.TxHash, "TESTTX-") {
		t.Fatalf("simulated payout must carry a TESTTX- hash, got %q", paid.TxHash)
	}
	if paid.PaidAt == nil {
		t.Fatal("paid row must record paid_at")
	}
	if paid.SignedTxHex != "" {
		t.Fatal("a simulated payout must never sign transaction bytes")
	}

	// ---- 5. The buyer really sees it in their cashback page --------------
	me := s.do(http.MethodGet, "/api/cashback/me", nil, listed)
	if me.status != 200 {
		t.Fatalf("cashback/me: %d %s", me.status, truncate(me.body))
	}
	if body := string(me.body); !strings.Contains(body, demoQ.ID) || !strings.Contains(body, paid.TxHash) {
		t.Fatalf("buyer should see the (simulated) payout: %s", truncate(me.body))
	}

	// ---- 6. …but the public surfaces stay clean --------------------------
	if act := s.do(http.MethodGet, "/api/activity", nil, nil); strings.Contains(string(act.body), quoteID) {
		t.Fatalf("test order leaked to the public feed: %s", truncate(act.body))
	}
	lb := s.do(http.MethodGet, "/api/cashback/leaderboard", nil, nil)
	if lb.status != 200 {
		t.Fatalf("leaderboard: %d %s", lb.status, truncate(lb.body))
	}
	if totals, _ := lb.json(t)["totals"].(map[string]any); totals["orders"] != float64(0) {
		t.Fatalf("simulated cashback must not count publicly: %v", totals)
	}

	// ---- 7. Abuse guard: a REAL row is never paid by this worker ---------
	// Same running worker, still seedless: a genuine (unlisted buyer) row
	// stays queued. That is why the simulated payout is not a hole — it only
	// ever fires for rows the operator's test list created.
	otherUser, err := s.h.Store.GetUserByAddress(nimiq.NormalizeAddress(otherAddress))
	if err != nil {
		t.Fatal(err)
	}
	realQ := db.Quote{
		ID: "demo-real-cashback", UserID: otherUser.ID, ProductID: "Test product",
		Status: "awaiting_payment", EstimatedNIM: 100, NimUsdRate: 0.01, ProductUSD: 1_000_000,
	}
	if err := s.h.Store.CreateQuoteWithDailyLimits(realQ, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Store.CompleteQuoteWithFulfillment(realQ.ID, nil); err != nil {
		t.Fatal(err)
	}
	realCB, err := s.h.Store.GetCashbackByQuote(realQ.ID)
	if err != nil || realCB.TestMode {
		t.Fatalf("precondition: a real row must be TestMode=false: %+v %v", realCB, err)
	}
	time.Sleep(300 * time.Millisecond) // several worker ticks
	if row, _ := s.h.Store.GetCashbackByQuote(realQ.ID); row.Status != db.CashbackQueued || row.TxHash != "" {
		t.Fatalf("a real payout must wait for a funded wallet, got %q tx=%q", row.Status, row.TxHash)
	}
	_ = other
}
