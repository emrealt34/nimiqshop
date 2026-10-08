package handlers

import (
	"fmt"
	"testing"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
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
