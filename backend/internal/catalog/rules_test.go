package catalog

import (
	"testing"
)

func TestNormalizeCanonicalizes(t *testing.T) {
	r := Rules{
		HiddenFamilies:   []string{"  Amazon.COM ", "", "amazon.com", "Steam"},
		BannedCategories: []string{" E-Money "},
		BannedKinds:      []string{"GiftCard"},
		HiddenCountries:  []string{" ru ", "RU"},
		VisibleCountries: []string{},
		MaxFaceValueUSD:  -5,
	}
	r.Normalize()
	if len(r.HiddenFamilies) != 2 || r.HiddenFamilies[0] != "amazon.com" || r.HiddenFamilies[1] != "steam" {
		t.Fatalf("hidden families not deduped/cased: %#v", r.HiddenFamilies)
	}
	if len(r.BannedCategories) != 1 || r.BannedCategories[0] != "e-money" {
		t.Fatalf("banned categories wrong: %#v", r.BannedCategories)
	}
	if len(r.HiddenCountries) != 1 || r.HiddenCountries[0] != "RU" {
		t.Fatalf("hidden countries wrong: %#v", r.HiddenCountries)
	}
	if r.MaxFaceValueUSD != 0 {
		t.Fatalf("negative cap should clamp to 0, got %v", r.MaxFaceValueUSD)
	}
	if r.OutOfStockPolicy != "show" {
		t.Fatalf("default oos policy should be show, got %q", r.OutOfStockPolicy)
	}
}

func TestCountryVisible(t *testing.T) {
	r := Rules{HiddenCountries: []string{"RU"}}
	if r.CountryVisible("ru") {
		t.Fatal("hidden country must be invisible (case-insensitive)")
	}
	if !r.CountryVisible("US") {
		t.Fatal("US should be visible")
	}
	if !r.CountryVisible("") {
		t.Fatal("global query must stay visible")
	}

	allow := Rules{VisibleCountries: []string{"US", "DE"}}
	if allow.CountryVisible("TR") {
		t.Fatal("allowlist must hide TR")
	}
	if !allow.CountryVisible("de") {
		t.Fatal("allowlist must show DE (case-insensitive)")
	}

	// Hidden wins over visible.
	both := Rules{VisibleCountries: []string{"US"}, HiddenCountries: []string{"US"}}
	if both.CountryVisible("US") {
		t.Fatal("hidden must take precedence over allowlist")
	}
}

func TestFamilyVisibility(t *testing.T) {
	r := Rules{
		HiddenFamilies:   []string{"amazon.com"},
		BannedCategories: []string{"e-money"},
		BannedKinds:      []string{"mobile_recharge"},
		MaxFaceValueUSD:  10000,
	}
	cases := []struct {
		name              string
		family, cat, kind string
		min, max          float64
		want              bool
	}{
		{"plain visible", "Steam", "gaming", "giftcard", 5, 100, true},
		{"hidden family", "Amazon.com", "e-commerce", "giftcard", 5, 500, false},
		{"banned category", "PokerSite", "e-money", "giftcard", 5, 100, false},
		{"banned kind", "T-Mobile", "top-up", "mobile_recharge", 5, 50, false},
		{"min above cap", "YachtCard", "luxury", "giftcard", 12000, 50000, false},
		{"min below cap, max above", "MidCard", "luxury", "giftcard", 100, 20000, true},
		{"unknown price visible", "OddCard", "misc", "giftcard", 0, 0, true},
	}
	for _, c := range cases {
		if got := r.FamilyVisible(c.family, c.cat, c.kind, c.min, c.max); got != c.want {
			t.Errorf("%s: FamilyVisible=%v want %v", c.name, got, c.want)
		}
	}
}

func TestClampRange(t *testing.T) {
	r := Rules{MaxFaceValueUSD: 100}
	m, vis := r.ClampRange(5, 500)
	if !vis || m != 100 {
		t.Fatalf("range should clamp max to cap: vis=%v max=%v", vis, m)
	}
	_, vis = r.ClampRange(150, 500)
	if vis {
		t.Fatal("range fully above cap must be hidden")
	}
	m, vis = r.ClampRange(1, 50)
	if !vis || m != 50 {
		t.Fatalf("range below cap must stay: vis=%v max=%v", vis, m)
	}
	off := Rules{}
	m, vis = off.ClampRange(1, 1000000)
	if !vis || m != 1000000 {
		t.Fatal("cap off must keep range")
	}
}

func TestParseFaceValue(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"$5", 5},
		{"$5.50", 5.5},
		{"€10", 10},
		{"100 USD", 100},
		{"5-500 USD", 5}, // min of a range
		{"$1,000", 1},    // comma stops the number — min 1 (fine: range)
		{"1 GB 3 days", 1},
		{"Any", 0},
		{"", 0},
		{"$0.0001", 0.0001},
		{"25.50 USD", 25.5},
		{"€0.50", 0.5},
	}
	for _, c := range cases {
		if got := ParseFaceValue(c.in); got != c.want {
			t.Errorf("ParseFaceValue(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestParseFaceValueAny(t *testing.T) {
	min, max := ParseFaceValueAny("$5", "$500")
	if min != 5 || max != 500 {
		t.Fatalf("got %v/%v", min, max)
	}
	min, max = ParseFaceValueAny("Any", "1 GB 3 days")
	if min != 1 || max != 1 {
		t.Fatalf("fallback should mirror known bound: %v/%v", min, max)
	}
}

func TestDenominationVisible(t *testing.T) {
	r := Rules{MaxFaceValueUSD: 10000}
	if !r.DenominationVisible(100) || !r.DenominationVisible(0) {
		t.Fatal("under-cap and unknown denominations must be visible")
	}
	if r.DenominationVisible(10001) {
		t.Fatal("over-cap denomination must be hidden")
	}
	off := Rules{}
	if !off.DenominationVisible(1e9) {
		t.Fatal("no cap = everything visible")
	}
}

func TestGateQuote(t *testing.T) {
	r := Rules{
		HiddenFamilies:   []string{"hidden brand"},
		BannedCategories: []string{"e-money"},
		BannedKinds:      []string{"mobile_recharge"},
		HiddenCountries:  []string{"RU"},
		MaxFaceValueUSD:  10000,
	}
	if err := GateQuote(&r, "fine", "gaming", "giftcard", "US", nil, 5, 100); err != nil {
		t.Fatalf("valid quote gated: %v", err)
	}
	if err := GateQuote(&r, "Hidden Brand", "gaming", "giftcard", "US", nil, 5, 100); err == nil {
		t.Fatal("hidden family must gate")
	}
	if err := GateQuote(&r, "x", "e-money", "giftcard", "US", nil, 5, 100); err == nil {
		t.Fatal("banned category must gate")
	}
	if err := GateQuote(&r, "x", "gaming", "giftcard", "US", []string{"e-money"}, 5, 100); err == nil {
		t.Fatal("additional banned category must gate")
	}
	if err := GateQuote(&r, "x", "gaming", "mobile_recharge", "US", nil, 5, 100); err == nil {
		t.Fatal("banned kind must gate")
	}
	if err := GateQuote(&r, "x", "gaming", "giftcard", "RU", nil, 5, 100); err == nil {
		t.Fatal("hidden country must gate")
	}
	if err := GateQuote(&r, "x", "gaming", "giftcard", "US", nil, 5, 20000); err == nil {
		t.Fatal("over-cap total must gate")
	}
	if err := GateQuote(&r, "x", "gaming", "giftcard", "US", nil, 12000, 1); err == nil {
		t.Fatal("family min above cap must gate")
	}
	// quantity is the caller's job: 2×$60 = $120 total against a $100 cap
	// must gate on the TOTAL.
	tight := Rules{MaxFaceValueUSD: 100}
	if err := GateQuote(&tight, "x", "gaming", "giftcard", "US", nil, 60, 120); err == nil {
		t.Fatal("total (qty included) above cap must gate")
	}
	if err := GateQuote(&tight, "x", "gaming", "giftcard", "US", nil, 60, 99.99); err != nil {
		t.Fatalf("under-cap total gated: %v", err)
	}
}
