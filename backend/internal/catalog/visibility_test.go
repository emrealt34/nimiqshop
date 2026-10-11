package catalog

import (
	"testing"

	"nimiqshop/internal/cryptorefills"
)

func TestEMoneyCannotBeListedOrTestPurchased(t *testing.T) {
	rules := Rules{}
	for _, category := range []string{"e-money", " E_MONEY ", "e money", "emoney"} {
		if !rules.IsCategoryBanned(category) {
			t.Fatalf("category %q must be excluded even with empty admin rules", category)
		}
		if GateQuote(&rules, "Financial Card", "retail", "giftcard", "TR", []string{category}, 5, 10) == nil {
			t.Fatalf("additional category %q must block purchase and admin tests", category)
		}
	}
	if err := GateQuote(&rules, "Steam", "gaming", "giftcard", "TR", nil, 5, 10); err != nil {
		t.Fatalf("ordinary gift cards must remain available: %v", err)
	}
	in := &cryptorefills.BrandsResponse{CountryCode: "TR", Categories: []cryptorefills.BrandCategory{
		{Kind: "giftcard", Category: "retail", Brands: []cryptorefills.Brand{{Family: "Mixed Card"}, {Family: "Steam"}}},
		{Kind: "giftcard", Category: "E_MONEY", Brands: []cryptorefills.Brand{{Family: "mixed card"}}},
	}}
	out := FilterBrands(&rules, in)
	if len(out.Categories) != 1 || len(out.Categories[0].Brands) != 1 || out.Categories[0].Brands[0].Family != "Steam" {
		t.Fatalf("an e-money copy must exclude the entire family: %+v", out)
	}
	if len(in.Categories[0].Brands) != 2 {
		t.Fatal("filter mutated the supplier cache")
	}
	families := FilterFamilies(&rules, []cryptorefills.Family{{Family: "Mixed Card", Category: "retail", AdditionalCats: []string{"e-money"}, Products: []cryptorefills.Product{{ProductID: "blocked"}}}})
	if len(families) != 0 {
		t.Fatal("direct product pages must exclude additional e-money categories")
	}
}
