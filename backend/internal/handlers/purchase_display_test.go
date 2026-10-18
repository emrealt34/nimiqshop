package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
)

func TestRecordedAverageIncludesInstantAnonymousPurchasesAndExcludesFakeAndUnknownTimes(t *testing.T) {
	paid := time.Now().UTC().Add(-time.Hour)
	end := paid.Add(time.Second / 2)
	base := db.Quote{ID: "real", Status: "fulfilled", Anonymous: true, CreatedAt: paid.Add(-time.Minute), PaidAt: &paid, DeliveredAt: &end}
	instant := base
	instant.ID = "instant"
	instant.DeliveredAt = &paid
	fake := base
	fake.ID = "29760a02-9a43-40a2-8d5c-71277624df94"
	sim := base
	sim.ID = "simulation"
	sim.TestMode = true
	unknown := base
	unknown.ID = "unknown"
	unknown.PaidAt = nil
	unknown.PaymentHandoffAt = paid
	legacy := db.Order{ID: "legacy", Status: "delivered", CreatedAt: base.CreatedAt, PaidAt: &paid, DeliveredAt: &end}
	got := recordedDeliverySummary([]db.Order{legacy}, []db.Quote{base, instant, fake, sim, unknown}, false)
	if got["delivered_count"] != 3 || got["avg_delivery_seconds"] != 1.0/3.0 {
		t.Fatalf("wrong summary: %+v", got)
	}
	// With test orders shown publicly the simulation counts too; the legacy
	// exclusion list and unknown times never do.
	shown := recordedDeliverySummary([]db.Order{legacy}, []db.Quote{base, instant, fake, sim, unknown}, true)
	if shown["delivered_count"] != 4 || shown["avg_delivery_seconds"] != 1.5/4.0 {
		t.Fatalf("wrong summary with test orders shown: %+v", shown)
	}
}

func TestPublicActivityAndTrackUseRecordedTimesAndLegacyPurchasedUnits(t *testing.T) {
	s, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	paid := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	q := db.Quote{ID: "carrefour-timing", UserID: "buyer", ProductID: "Carrefour", ProductValue: 10, ProductCurrency: "EUR", ProductCountry: "FR", Status: "awaiting_payment", PaidAt: &paid, CreatedAt: paid.Add(-time.Minute), SupplierUpdatedAt: paid}
	if err := s.CreateQuoteWithDailyLimits(q, 0, 0, q.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteQuoteWithFulfillment(q.ID, nil); err != nil {
		t.Fatal(err)
	}
	// Emulate a historical fulfilled row: read projection prefers its saved supplier time.
	q, _ = s.GetQuote(q.ID)
	q.DeliveredAt = nil
	stages := quoteStages(q)
	if stages[1]["timestamp"] != paid.Format(time.RFC3339Nano) || stages[3]["timestamp"] != paid.Format(time.RFC3339Nano) {
		t.Fatal(stages)
	}
	if got := recordedDeliverySummary(nil, []db.Quote{q}, false); got["avg_delivery_seconds"] != float64(0) || got["delivered_count"] != 1 {
		t.Fatal(got)
	}
	o := db.Order{ID: "legacy-unit", UserID: "buyer", Payload: json.RawMessage(`{"product_name":"Carrefour","value":10,"currency":"EUR","country":"FR"}`)}
	if err := s.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrderStatus(o.ID, "delivered"); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Store: s}
	var ctx fasthttp.RequestCtx
	ctx.SetUserValue("id", o.ID)
	h.TrackStatus(&ctx)
	var body map[string]interface{}
	if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if body["face_label"] != "10 €" || body["delivered_at"] == nil {
		t.Fatal(body)
	}
	ctx.Request.SetRequestURI("/api/activity?limit=50")
	h.ListActivity(&ctx)
	var feed struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &feed); err != nil {
		t.Fatal(err)
	}
	for _, item := range feed.Items {
		if item["face_label"] != "10 €" {
			t.Fatal(item)
		}
	}
}
