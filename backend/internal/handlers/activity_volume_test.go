package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
	"nimiqshop/internal/money"
)

func TestActivityPublishesPurchaseTimeProductNIMWithoutAnonymousPaymentDetails(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	q := db.Quote{ID: "volume", UserID: "buyer", ProductID: "Steam", ProductUSD: money.FromFloat(6), Status: "awaiting_payment", Anonymous: true,
		NimUsdRate: 0.002, EstimatedNIM: 3500, Coin: "USDT", CoinAmount: "7", WalletAddress: "private-wallet", LightningPaymentHash: "private-hash"}
	if err := store.CreateQuoteWithDailyLimits(q, 0, 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Store: store}
	var ctx fasthttp.RequestCtx
	ctx.Request.SetRequestURI("/api/activity?limit=50")
	h.ListActivity(&ctx)
	var body struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected one fulfilled quote, got %s", ctx.Response.Body())
	}
	item := body.Items[0]
	if item["nim"] != "3000.00000" {
		t.Fatalf("volume must use product value at its historical rate, not the fee-inclusive invoice: %#v", item)
	}
	for _, key := range []string{"address", "paid", "tx", "wallet_address"} {
		if _, exists := item[key]; exists {
			t.Fatalf("anonymous payment detail leaked: %s", key)
		}
	}
}
