package catalog

import (
	"math"
	"testing"
)

func TestDisplayFaceLabelPreservesPurchasedUnits(t *testing.T) {
	for _, tc := range []struct {
		label                   string
		value                   float64
		currency, country, want string
	}{
		{"10", 10, "EUR", "FR", "10 €"},
		{"range", 10.5, "EUR", "FR", "10.5 €"},
		{"RANGE", 12.25, "USD", "FR", "12.25 USD"},
		{"1.000,50", 1000.5, "EUR", "FR", "1000.5 €"},
		{"", 25, "TRY", "US", "25 TRY"},
		{"10", 10, "CAD", "CA", "10 CAD"},
		{"10", 10, "USD", "FR", "10 USD"},
		{"10,5", 0, "", "FR", "10.5 €"},
		{"10", 0, "", "ZZ", "10"},
		{"10", 10, "PTS", "FR", "10"},
		{"50 GB 90 days", 16.16, "USD", "FR", "50 GB 90 days"},
		{"575 Points", 5, "USD", "US", "575 Points"},
		{"1000 V-Bucks", 8, "USD", "US", "1000 V-Bucks"},
		{"10 EUR", 10, "EUR", "FR", "10 EUR"},
		{"€10", 10, "EUR", "FR", "€10"},
		{"10 €", 10, "EUR", "FR", "10 €"},
		{"range", math.Inf(1), "EUR", "FR", ""},
		{"range", math.NaN(), "EUR", "FR", ""},
	} {
		t.Run(tc.label+"/"+tc.currency+"/"+tc.country, func(t *testing.T) {
			if got := DisplayFaceLabel(tc.label, tc.value, tc.currency, tc.country); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
