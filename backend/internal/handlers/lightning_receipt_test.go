package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
)

func TestPublicLightningReceiptIsMetadataNotAnUnpaidInvoiceProof(t *testing.T) {
	q := db.Quote{Coin: "BTC", Network: "Lightning", LightningPaymentHash: strings.Repeat("AB", 32), CoinAmount: "0.00012", Status: "awaiting_payment"}
	if got := publicLightningReceipt(q); got["confirmed"] != false || got["payment_hash"] != strings.Repeat("ab", 32) {
		t.Fatalf("unpaid invoice metadata: %#v", got)
	}
	q.Status = "fulfilled"
	got := publicLightningReceipt(q)
	if got["confirmed"] != true || got["confirmed_at"] != nil {
		t.Fatalf("legacy fulfillment must not invent a paid timestamp: %#v", got)
	}
	paid := time.Now().UTC()
	q.PaidAt = &paid
	if publicLightningReceipt(q)["confirmed_at"] != q.PaidAt {
		t.Fatal("paid observation timestamp lost")
	}
	for _, mutate := range []func(*db.Quote){
		func(q *db.Quote) { q.Anonymous = true }, func(q *db.Quote) { q.TestMode = true },
		func(q *db.Quote) { q.Network = "Polygon" }, func(q *db.Quote) { q.Coin = "USDT" },
		func(q *db.Quote) { q.LightningPaymentHash = "private-non-hash" },
	} {
		copy := q
		mutate(&copy)
		if publicLightningReceipt(copy) != nil {
			t.Fatal("private, simulated, malformed or non-Lightning receipt published")
		}
	}
}

func TestActivityAndTrackPublishOnlySafeLightningMetadata(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}
	for _, anonymous := range []bool{false, true} {
		id := "public-lightning"
		if anonymous {
			id = "anonymous-lightning"
		}
		q := db.Quote{ID: id, UserID: "buyer", ProductID: "Carrefour", Status: "awaiting_payment", Coin: "BTC", Network: "Lightning", CoinAmount: "0.00012", Anonymous: anonymous,
			LightningPaymentHash: strings.Repeat("ab", 32), WalletAddress: "private-invoice", SupplierOrderID: "private-supplier-id", CustomerEmail: "private@example.test", LightningSwapID: "wallet-swap-private"}
		if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteQuoteWithFulfillment(id, nil); err != nil {
			t.Fatal(err)
		}
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/api/track/" + id)
		ctx.SetUserValue("id", id)
		h.TrackStatus(&ctx)
		var item map[string]any
		if err := json.Unmarshal(ctx.Response.Body(), &item); err != nil {
			t.Fatal(err)
		}
		if item["tx"] != nil {
			t.Fatal("invoice hash mislabeled as chain transaction")
		}

		if anonymous {
			if item["lightning_receipt"] != nil {
				t.Fatal("anonymous track leaked receipt")
			}
		} else if item["lightning_receipt"] == nil {
			t.Fatalf("public track missing receipt: %s", ctx.Response.Body())
		}
		for _, secret := range []string{"private-invoice", "private-supplier-id", "private@example.test", "wallet-swap-private"} {
			if strings.Contains(string(ctx.Response.Body()), secret) {
				t.Fatalf("track leaked %s", secret)
			}
		}
	}
	var ctx fasthttp.RequestCtx
	ctx.Request.SetRequestURI("/api/activity?limit=50")
	h.ListActivity(&ctx)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("missing real purchases: %s", ctx.Response.Body())
	}
	if strings.Contains(string(ctx.Response.Body()), "wallet-swap-private") {
		t.Fatal("activity leaked swap id")
	}
	for _, item := range body.Items {
		if item["tx"] != nil {
			t.Fatal("invoice hash mislabeled as feed transaction")
		}
		if (item["lightning_receipt"] != nil) != (item["id"] == "public-lightning") {
			t.Fatalf("receipt privacy: %#v", item)
		}
	}
}

func TestPublicChainProofNeverLeaksAnonymousOrSimulatedSavedReceipts(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}
	for _, mode := range []string{"anonymous", "test"} {
		q := db.Quote{ID: mode, UserID: "buyer", Coin: "BTC", Network: "Lightning", Anonymous: mode == "anonymous", TestMode: mode == "test", LightningChainProof: &db.LightningChainProof{FundingTx: strings.Repeat("a", 64), SettlementTx: strings.Repeat("b", 64), Preimage: strings.Repeat("0", 64)}}
		if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/api/track/" + mode + "/lightning-proof")
		ctx.SetUserValue("id", mode)
		h.LightningProof(&ctx)
		if ctx.Response.StatusCode() != 404 || strings.Contains(string(ctx.Response.Body()), strings.Repeat("b", 64)) {
			t.Fatal("private saved chain receipt disclosed")
		}
	}
}

func TestPublicLightningReceiptSubmissionIsNotSettlement(t *testing.T) {
	for _, hash := range []string{strings.Repeat("ab", 32), "0x" + strings.Repeat("cd", 32)} {
		q := db.Quote{Coin: "BTC", Network: "Lightning", LightningPaymentHash: strings.Repeat("ef", 32), LightningFundingHint: hash}
		got := publicLightningReceipt(q)
		if got["submitted_tx"] != hash || got["confirmed"] != false || got["chain_proof"] != nil {
			t.Fatalf("submission misrepresented: %#v", got)
		}
	}
}

func TestLightningSwapOnlyOwnerDetailAndSubmissionWrite(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := &Handlers{Store: store}
	q := db.Quote{ID: "swap-private", UserID: "buyer", Coin: "BTC", Network: "Lightning", LightningPaymentHash: strings.Repeat("ab", 32), Status: "awaiting_payment"}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	var post fasthttp.RequestCtx
	post.SetUserValue("id", q.ID)
	post.SetUserValue("user_id", q.UserID)
	post.Request.Header.SetMethod("POST")
	post.Request.SetBodyString(`{"swap_id":"wallet-swap-private"}`)
	h.LightningProof(&post)
	if post.Response.StatusCode() != 200 {
		t.Fatalf("swap-only submission rejected: %s", post.Response.Body())
	}
	saved, err := store.GetQuote(q.ID)
	if err != nil || saved.LightningSwapID != "wallet-swap-private" || saved.PaidAt != nil || saved.PaymentObserved {
		t.Fatalf("submission: %#v %v", saved, err)
	}
	for _, user := range []string{"buyer", "other", ""} {
		var ctx fasthttp.RequestCtx
		ctx.SetUserValue("id", q.ID)
		ctx.SetUserValue("user_id", user)
		h.GetUserQuote(&ctx)
		exposed := strings.Contains(string(ctx.Response.Body()), "wallet-swap-private")
		if exposed != (user == "buyer") {
			t.Fatalf("owner visibility for %q: %s", user, ctx.Response.Body())
		}
	}
	var listed fasthttp.RequestCtx
	listed.SetUserValue("user_id", q.UserID)
	h.ListUserQuotes(&listed)
	if strings.Contains(string(listed.Response.Body()), "wallet-swap-private") {
		t.Fatal("swap reference escaped private detail")
	}
	post.Request.SetBodyString(`{"swap_id":"replacement-swap"}`)
	h.LightningProof(&post)
	if post.Response.StatusCode() != 409 {
		t.Fatalf("conflicting reference accepted: %s", post.Response.Body())
	}
}
