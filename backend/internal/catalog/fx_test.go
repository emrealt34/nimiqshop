package catalog

import (
	"math"
	"testing"

	"nimiqshop/internal/cryptorefills"
)

func TestToUSDCurrencies(t *testing.T) {
	cases := []struct {
		amount float64
		code   string
		want   float64
	}{
		{150000, "IDR", 150000 * 0.000062},
		{1000000, "VND", 1000000 * 0.0000395},
		{20, "EUR", 21.6},
		{100, "USD", 100},
		{300, "TRY", 300 * 0.029},
		{5000, "IQD", 5000 * 0.00076},
	}
	for _, c := range cases {
		if got := ToUSD(c.amount, c.code); math.Abs(got-c.want) > 0.0001 {
			t.Errorf("ToUSD(%v, %s) = %v, want %v", c.amount, c.code, got, c.want)
		}
	}
	if got := ToUSD(123, "ZZZ"); got != 123 {
		t.Errorf("unknown code must fall back 1:1, got %v", got)
	}
}

// TestFXAwarePriceCap: the admin's $20 cap must be applied against each
// country's USD-equivalent — the old raw comparison ("150.000 > 20")
// hid entire high-denomination countries.
func TestFXAwarePriceCap(t *testing.T) {
	rules := &Rules{MaxFaceValueUSD: 20}
	fams := []cryptorefills.Family{
		{Family: "cheap-id", Products: []cryptorefills.Product{{IsDynamic: true, Range: &cryptorefills.ValueRange{Min: 10000, Max: 100000, Currency: "IDR", StepSize: 1000}}}},
		{Family: "pricey-id", Products: []cryptorefills.Product{{IsDynamic: true, Range: &cryptorefills.ValueRange{Min: 500000, Max: 5000000, Currency: "IDR", StepSize: 100000}}}},
		{Family: "fixed-ok", Products: []cryptorefills.Product{{Denomination: "150.000 IDR"}}},
		{Family: "fixed-big", Products: []cryptorefills.Product{{Denomination: "1.000.000 IDR"}}},
	}
	kept := FilterFamilies(rules, fams)
	got := map[string]bool{}
	for _, f := range kept {
		got[f.Family] = true
		if f.Family == "cheap-id" && f.Products[0].Range.Max != 100000 {
			t.Errorf("cheap-id range must survive untouched, got %+v", f.Products[0].Range)
		}
	}
	if !got["cheap-id"] || !got["fixed-ok"] {
		t.Fatalf("under-cap families must stay: %v", got)
	}
	if got["pricey-id"] || got["fixed-big"] {
		t.Fatalf("over-cap families must be hidden: %v", got)
	}

	// Brand list: min label converted to USD.
	br := cryptorefills.BrandCategory{Kind: "giftcard", Category: "games", Brands: []cryptorefills.Brand{
		{Family: "brand-ok", Min: "150.000 IDR", Max: "300.000 IDR"},
		{Family: "brand-big", Min: "5.000.000 IDR", Max: "9.000.000 IDR"},
	}}
	out := FilterBrands(rules, &cryptorefills.BrandsResponse{Categories: []cryptorefills.BrandCategory{br}})
	if len(out.Categories) != 1 || len(out.Categories[0].Brands) != 1 || out.Categories[0].Brands[0].Family != "brand-ok" {
		t.Fatalf("brand cap filter wrong: %+v", out)
	}
}
