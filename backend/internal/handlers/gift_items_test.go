package handlers

import (
	"fmt"
	"strings"
	"testing"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/mailtrap"
)

func TestBrandIndexKeepsACompleteEntry(t *testing.T) {
	cats := []cryptorefills.BrandCategory{
		{Brands: []cryptorefills.Brand{{Family: "Steam", LogoURL: "[https://cdn.example.com/steam.png]", BgColor: "#1b2838"}}},
		// A later duplicate with no logo must not blank the complete entry.
		{Brands: []cryptorefills.Brand{{Family: "steam"}}},
	}
	idx := brandIndexFrom(cats)
	ref, ok := idx["steam"]
	if !ok || ref.logo == "" || ref.bg != "#1b2838" {
		t.Fatalf("steam entry = %+v", ref)
	}
}

func TestGiftItemsFromMapsLinesToBrandAndValue(t *testing.T) {
	idx := map[string]brandRef{"steam": {logo: "https://cdn.example.com/steam.png", bg: "#1b2838"}}
	lines := []db.QuoteLine{
		{ProductID: "Steam", FaceLabel: "50 USD", Quantity: 2},
		{ProductID: "Amazon", ProductValue: 25, ProductCurrency: "USD", Quantity: 0},
	}
	items, fields := giftItemsFrom(lines, idx)
	if len(items) != 2 || len(fields) != 2 {
		t.Fatalf("items=%d fields=%d", len(items), len(fields))
	}
	if items[0].Name != "Steam" || items[0].Detail != "50 USD" || items[0].Qty != 2 || items[0].BgColor != "#1b2838" {
		t.Fatalf("steam item = %+v", items[0])
	}
	if fields[0] != "https://cdn.example.com/steam.png" {
		t.Fatalf("steam logo field = %q", fields[0])
	}
	// No catalog entry: no logo field, no background, quantity defaults to 1.
	if items[1].Qty != 1 || items[1].BgColor != "" || fields[1] != "" || items[1].Detail != "25 USD" {
		t.Fatalf("amazon item = %+v field=%q", items[1], fields[1])
	}
}

func TestGiftItemsFetchLogosForAtMostTheShownRows(t *testing.T) {
	idx := map[string]brandRef{"steam": {logo: "https://cdn.example.com/steam.png"}}
	var lines []db.QuoteLine
	for i := 0; i < giftItemLogoLimit+4; i++ {
		lines = append(lines, db.QuoteLine{ProductID: "Steam", FaceLabel: fmt.Sprintf("%d USD", i+1)})
	}
	items, fields := giftItemsFrom(lines, idx)
	if len(items) != len(lines) {
		t.Fatalf("every line must stay in the list: %d of %d", len(items), len(lines))
	}
	fetched := 0
	for _, f := range fields {
		if f != "" {
			fetched++
		}
	}
	if fetched != giftItemLogoLimit {
		t.Fatalf("logos requested for %d lines, want %d", fetched, giftItemLogoLimit)
	}
}

func TestSingleProductQuoteBecomesOneLine(t *testing.T) {
	q := db.Quote{ProductID: "Netflix", ProductValue: 30, Denomination: "30 USD", ProductCountry: "US"}
	lines := giftLinesFor(q)
	if len(lines) != 1 || lines[0].ProductID != "Netflix" {
		t.Fatalf("lines = %+v", lines)
	}
	if got := giftFaceDetail(lines[0]); got != "30 USD" {
		t.Fatalf("detail = %q", got)
	}
}

func TestFaceDetailSkipsRangeLabels(t *testing.T) {
	if got := giftFaceDetail(db.QuoteLine{Denomination: "range", ProductValue: 12, ProductCurrency: "USD"}); got != "12 USD" {
		t.Fatalf("range detail = %q", got)
	}
}

func TestHistoricalFaceLabelUsesItsOwnCurrency(t *testing.T) {
	for _, tc := range []struct {
		line db.QuoteLine
		want string
	}{
		{db.QuoteLine{FaceLabel: "10", ProductValue: 10, ProductCurrency: "EUR", Country: "FR"}, "10 €"},
		{db.QuoteLine{FaceLabel: "10", ProductValue: 10, Denomination: "10 USD", Country: "FR"}, "10 USD"},
		{db.QuoteLine{FaceLabel: "50 GB 90 days", ProductValue: 16.16, ProductCurrency: "USD"}, "50 GB 90 days"},
	} {
		if got := giftFaceDetail(tc.line); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestPurchaseAndGiftEmailIncludeUnitsInBothParts(t *testing.T) {
	h := &Handlers{}
	for _, channel := range []string{"", "email"} {
		for _, lang := range []string{"en", "tr", "fr", "de", "es", "pt"} {
			q := db.Quote{ProductID: "Carrefour", ProductValue: 10, ProductCurrency: "EUR", ProductCountry: "FR",
				Denomination: "range", Quantity: 2, CustomerEmail: "buyer@example.test", Anonymous: true, GiftChannel: channel}
			note := h.BuildGiftNoteFromQuote(q, lang)
			if note.ProductLabel != "Carrefour · 10 €" || note.Items[0].Detail != "10 €" || note.Items[0].Qty != 2 {
				t.Fatalf("incorrect receipt: %+v", note)
			}
			msg, err := note.Build(mailtrap.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg.HTML, "10 €") || !strings.Contains(msg.Text, "10 €") {
				t.Fatalf("missing unit in %s/%s receipt", channel, lang)
			}
		}
	}
}

func TestNewAndPublicLinesShowUnitsWithoutChangingSupplierSelection(t *testing.T) {
	req := createQuoteRequest{ProductID: "Carrefour", Country: "FR", Denomination: "range", ProductValue: 10, Quantity: 1}
	line := singleQuoteLine(req, familyMeta{Currency: "EUR"}, "range", "buyer@example.test")
	if line.FaceLabel != "10 €" || line.Denomination != "range" || line.ProductValue != 10 || req.Denomination != "range" {
		t.Fatalf("display changed checkout identity: %+v", line)
	}
	old := line
	old.FaceLabel = "10"
	pub := publicLines(db.Quote{Lines: []db.QuoteLine{old}})
	if pub[0]["face_label"] != "10 €" {
		t.Fatal(pub)
	}
	if _, leaked := pub[0]["delivery_target"]; leaked {
		t.Fatal("public lines leaked delivery target")
	}
	if old.FaceLabel != "10" {
		t.Fatal("display mutated a stored historical line")
	}
}
