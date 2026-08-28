package handlers

import "testing"

// TestParseMoneyAmountCurrencies locks the country-proof denomination
// parser across every real-world currency formatting habit. These are the
// exact shapes the supplier emits per country — the old parser read
// "150.000 IDR" as 150 and "1 000 AED" as 0, which dropped whole countries
// from the storefront and failed checkout with "denomination is required".
func TestParseMoneyAmountCurrencies(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		// plain + symbols + glued/prefixed/suffixed codes
		{"100", 100},
		{"100 USD", 100},
		{"$25", 25},
		{"TRY300", 300},
		{"TRY 300", 300},
		{"€10", 10},
		{"25 TRY", 25},
		{"₩5,000", 5000},
		{"₹500", 500},
		{"120 TL", 120},
		{"Riot Cash 120 TL", 120},
		{"$ 1,250.99", 1250.99},
		// dot-grouped thousands (IDR, VND, IQD, LBP, COP, UZS, KHR, SYP…)
		{"150.000 IDR", 150000},
		{"1.000.000 VND", 1000000},
		{"5.000 IQD", 5000},
		{"1.000.000 LBP", 1000000},
		{"50.000 COP", 50000},
		{"10.000 UZS", 10000},
		{"1.000 AED", 1000},
		{"150.000", 150000},
		{"1.000", 1000},
		// comma-grouped thousands (US/UK/…)
		{"1,000", 1000},
		{"1,000,000 VND", 1000000},
		{"5,000 IQD", 5000},
		{"12,345.67 USD", 12345.67},
		// decimal comma (EU/LATAM/TR formal)
		{"25,50 EUR", 25.5},
		{"1.234,56 EUR", 1234.56},
		{"0,99 EUR", 0.99},
		// decimal dot stays decimal (groups of 1-2 digits after the dot)
		{"10.5 USD", 10.5},
		{"25.50 EUR", 25.5},
		{"5.00", 5},
		{"0.99 USD", 0.99},
		// space-grouped thousands
		{"1 000 AED", 1000},
		{"10 000 RUB", 10000},
		{"1 000 000 KRW", 1000000},
		// unicode digits (Arabic-Indic, Extended, Devanagari)
		{"٥٠٠ SAR", 500},
		{"۱٬۰۰۰ IRR", 1000}, // Arabic thousands sep drops out → 1000
		{"५०० INR", 500},
		// noise around the number
		{"  100  ", 100},
		{"Gift 25 USD card", 25},
		{"x1.000x", 1000},
	}
	for _, c := range cases {
		if got := parseMoneyAmount(c.in); got != c.want {
			t.Errorf("parseMoneyAmount(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestResolveFaceValueCountries: the quote path must accept every real
// denomination shape, and never fail a purchase because a FIXED product's
// label carries no number (the supplier prices by the exact label).
func TestResolveFaceValueCountries(t *testing.T) {
	cases := []struct {
		denom  string
		value  float64
		face   float64
		label  string
		hasErr bool
	}{
		{denom: "range", value: 10, face: 10, label: "range"},
		{denom: "range", value: 0, hasErr: true},
		{denom: "", value: 0, hasErr: true},
		{denom: "", value: 26.95, face: 26.95, label: "fixed"},
		{denom: "100 USD", face: 100, label: "100 USD"},
		{denom: "TRY300", face: 300, label: "TRY300"},
		{denom: "150.000 IDR", face: 150000, label: "150.000 IDR"},
		{denom: "1.000.000 VND", face: 1000000, label: "1.000.000 VND"},
		{denom: "5.000 IQD", face: 5000, label: "5.000 IQD"},
		{denom: "1 000 AED", face: 1000, label: "1 000 AED"},
		{denom: "Java & Bedrock Ed", face: 0, label: "Java & Bedrock Ed"}, // label-only, NO error
		{denom: "Java & Bedrock Ed", value: 26.95, face: 0, label: "Java & Bedrock Ed"},
	}
	for _, c := range cases {
		face, label, err := resolveFaceValue(c.denom, c.value)
		if c.hasErr {
			if err == nil {
				t.Fatalf("resolveFaceValue(%q,%v): expected error", c.denom, c.value)
			}
			continue
		}
		if err != nil {
			t.Fatalf("resolveFaceValue(%q,%v): unexpected error %v (this is the 'denomination is required' checkout bug)", c.denom, c.value, err)
		}
		if face != c.face || label != c.label {
			t.Errorf("resolveFaceValue(%q,%v) = (%v,%q), want (%v,%q)", c.denom, c.value, face, label, c.face, c.label)
		}
	}
}
