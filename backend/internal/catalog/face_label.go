package catalog

import (
	"math"
	"strconv"
	"strings"
)

// DisplayFaceLabel adds a missing unit to a receipt label. It is display-only:
// supplier denominations and checkout amounts must never be rewritten from it.
// Package descriptions and point counts keep their own non-monetary units.
func DisplayFaceLabel(label string, value float64, currency, country string) string {
	label = strings.TrimSpace(label)
	if strings.EqualFold(label, "range") {
		label = ""
	}
	parsed, labelCurrency := ParseDenominationLabel(label)
	if label != "" && (parsed <= 0 || labelCurrency != "") {
		return label // already has a unit, or is an opaque package description
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = CurrencyForCountry(country)
	}
	if !CurrencyKnown(currency) {
		return label
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		value = parsed // legacy display fallback, never used to select/price a SKU
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return label
	}
	unit := currency
	if currency == "EUR" {
		unit = "€"
	}
	return strconv.FormatFloat(value, 'f', -1, 64) + " " + unit
}
