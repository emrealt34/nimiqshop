// Package catalog — fx.go converts LOCAL currency amounts to their
// USD-equivalent so the admin's single MaxFaceValueUSD price cap applies
// fairly in every country. The admin writes "20" (USD); an Indonesian
// "150.000 IDR" card (~$10) stays visible while a "500.000 IDR" (~$33)
// is hidden — the old code compared 150000 > 20 raw and hid entire
// countries.
//
// The rates are curated approximations, refreshed with the code (fiat
// moves slowly; a price CAP does not need tick precision — it needs to be
// within a sane band). Unknown codes are treated 1:1 as USD only when the
// code IS "USD"; any other unknown code FAILS OPEN for browsing (no cap)
// and for the quote gate the raw amount is used — never a wrong hide.
package catalog

import (
	"regexp"
	"strconv"
	"strings"
)

// usdPerUnit maps ISO-4217 code → how many USD one unit is worth
// (approximate, curated; materially stale entries refreshed 2026-10-03
//
//	against open.er-api.com — the rate only feeds the admin's USD price
//	CAP and pre-quote display estimates, never an actual charge: a real
//	order is priced from the supplier's own validated amount).
var usdPerUnit = map[string]float64{
	"AED": 0.272, "AFN": 0.014, "ALL": 0.011, "AMD": 0.0026, "ANG": 0.555,
	"AOA": 0.0011, "ARS": 0.0006565614, "AUD": 0.65, "AWG": 0.555, "AZN": 0.588,
	"BAM": 0.555, "BBD": 0.5, "BDT": 0.0085, "BGN": 0.555, "BHD": 2.65,
	"BIF": 0.00034, "BMD": 1.0, "BND": 0.74, "BOB": 0.08309521, "BRL": 0.185,
	"BSD": 1.0, "BTN": 0.01037614, "BWP": 0.073, "BYN": 0.30, "BZD": 0.497,
	"CAD": 0.73, "CDF": 0.000432955, "CHF": 1.13, "CLP": 0.00105, "CNY": 0.138,
	"COP": 0.0003019391, "CRC": 0.0019, "CUP": 0.037, "CVE": 0.0104, "CZK": 0.044,
	"DJF": 0.0056, "DKK": 0.146, "DOP": 0.0165, "DZD": 0.0075, "EGP": 0.0205,
	"ERN": 0.0667, "ETB": 0.006141066, "EUR": 1.08, "FJD": 0.445, "FKP": 1.27,
	"GBP": 1.27, "GEL": 0.37, "GHS": 0.0862349, "GIP": 1.27, "GMD": 0.014,
	"GNF": 0.000115, "GTQ": 0.128, "GYD": 0.0048, "HKD": 0.128, "HNL": 0.0395,
	"HRK": 0.143, "HTG": 0.0076, "HUF": 0.00275, "IDR": 0.000062, "ILS": 0.3274182,
	"INR": 0.0118, "IQD": 0.00076, "IRR": 0.000001288239, "ISK": 0.0073,
	"JMD": 0.0063, "JOD": 1.41, "JPY": 0.0066, "KES": 0.0077, "KGS": 0.0114,
	"KHR": 0.000245, "KMF": 0.0022, "KRW": 0.00073, "KWD": 3.25, "KYD": 1.2,
	"KZT": 0.0021, "LAK": 0.000046, "LBP": 0.0000112, "LKR": 0.0034,
	"LRD": 0.0052, "LSL": 0.055, "LYD": 0.1563888, "MAD": 0.10, "MDL": 0.057,
	"MGA": 0.00022, "MKD": 0.0178, "MMK": 0.00048, "MNT": 0.00029,
	"MOP": 0.132, "MRU": 0.025, "MUR": 0.0215, "MVR": 0.065, "MWK": 0.00058,
	"MXN": 0.055, "MYR": 0.225, "MZN": 0.0156, "NAD": 0.055, "NGN": 0.00065,
	"NIO": 0.027, "NOK": 0.093, "NPR": 0.0074, "NZD": 0.60, "OMR": 2.60,
	"PAB": 1.0, "PEN": 0.27, "PGK": 0.25, "PHP": 0.0175, "PKR": 0.0036,
	"PLN": 0.25, "PYG": 0.0001712849, "QAR": 0.274, "RON": 0.217, "RSD": 0.0093,
	"RUB": 0.0105, "RWF": 0.00073, "SAR": 0.266, "SBD": 0.12, "SCR": 0.07,
	"SDG": 0.0017, "SEK": 0.095, "SGD": 0.74, "SHP": 1.27, "SLE": 0.04056061,
	"SLL": 0.00004056061, "SOS": 0.00175, "SRD": 0.028, "SSP": 0.0001767697,
	"STN": 0.04592538, "SVC": 0.114, "SYP": 0.008209578, "SZL": 0.055, "THB": 0.029,
	"TJS": 0.1083966, "TMT": 0.286, "TND": 0.32, "TOP": 0.42, "TRY": 0.02035214,
	"TTD": 0.147, "TWD": 0.031, "TZS": 0.00038, "UAH": 0.024, "UGX": 0.00027,
	"USD": 1.0, "UYU": 0.025, "UZS": 0.00008, "VES": 0.00114762, "VND": 0.0000395,
	"VUV": 0.0084, "WST": 0.36, "XAF": 0.00165, "XCD": 0.37, "XOF": 0.00165,
	"XPF": 0.0084, "YER": 0.004, "ZAR": 0.055, "ZMW": 0.05076607, "ZWL": 0.03736181,
}

// ToUSD converts an amount expressed in `code` to its approximate
// USD-equivalent. An unknown/empty code is treated as USD (1:1) — the
// supplier's default currency — so a cap never wrongly hides catalog data.
func ToUSD(amount float64, code string) float64 {
	rate, ok := usdPerUnit[strings.ToUpper(strings.TrimSpace(code))]
	if !ok {
		return amount
	}
	return amount * rate
}

// CurrencyKnown reports whether code is a RECOGNIZED ISO-4217 currency in
// the FX table. Game-currency labels ("575 Points", "1000 V-Bucks") parse to
// a value with NO real currency code — their face value is not convertible
// to USD, so price-cap comparisons must SKIP them (fail open) instead of
// treating the raw point count as dollars (which nuked whole families).
func CurrencyKnown(code string) bool {
	_, ok := usdPerUnit[strings.ToUpper(strings.TrimSpace(code))]
	return ok
}

// UsdPerUnit exposes the rate (tests / admin display).
func UsdPerUnit(code string) (float64, bool) {
	r, ok := usdPerUnit[strings.ToUpper(strings.TrimSpace(code))]
	return r, ok
}

// ParseDenominationLabel parses supplier monetary min/max fields. Checkout
// and fixed-product filtering NEVER call it with SKU labels. Only a whole
// money string is accepted, not numbers inside names.
func ParseDenominationLabel(label string) (value float64, code string) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(label) {
		switch {
		case r >= 0x0660 && r <= 0x0669:
			b.WriteRune(r - 0x0660 + '0')
		case r >= 0x06F0 && r <= 0x06F9:
			b.WriteRune(r - 0x06F0 + '0')
		case r >= 0x0966 && r <= 0x096F:
			b.WriteRune(r - 0x0966 + '0')
		default:
			b.WriteRune(r)
		}
	}
	raw := b.String()
	token := ""
	if m := moneyPrefix.FindStringSubmatch(raw); m != nil {
		token = m[1]
		raw = strings.TrimSpace(raw[len(m[0]):])
	} else if m := moneySuffix.FindStringSubmatch(raw); m != nil {
		token = m[1]
		raw = strings.TrimSpace(raw[:len(raw)-len(m[0])])
	}
	if !moneyNumber.MatchString(raw) {
		return 0, ""
	}
	symbols := map[string]string{"$": "USD", "€": "EUR", "£": "GBP", "¥": "JPY", "₩": "KRW", "₺": "TRY", "₹": "INR", "₽": "RUB", "﷼": "SAR", "TL": "TRY"}
	code = token
	if c := symbols[token]; c != "" {
		code = c
	}
	return parseMoneyAmountRaw(raw), code
}

var (
	moneyPrefix = regexp.MustCompile(`^([$€£¥₩₺₹₽﷼]|[A-Z]{3}|TL)\s*`)
	moneySuffix = regexp.MustCompile(`\s*([$€£¥₩₺₹₽﷼]|[A-Z]{3}|TL)$`)
	moneyNumber = regexp.MustCompile(`^[0-9](?:[0-9.,\s\x{00A0}\x{202F}]*[0-9])?$`)
)

// parseMoneyAmountRaw: the shared numeric core (grouping-aware).
func parseMoneyAmountRaw(s string) float64 {
	if s == "" {
		return 0
	}
	var b []rune
	lastDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b = append(b, r)
			lastDigit = true
		case r == '.' || r == ',':
			b = append(b, r)
			lastDigit = false
		case r == ' ' || r == '\u00A0' || r == '\u202F':
			if lastDigit {
				b = append(b, ' ')
				lastDigit = false
			}
		default:
			lastDigit = false
		}
	}
	first, last := -1, -1
	for i, r := range b {
		if r >= '0' && r <= '9' {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0
	}
	t := strings.ReplaceAll(string(b[first:last+1]), " ", "")
	lastDot := strings.LastIndexByte(t, '.')
	lastComma := strings.LastIndexByte(t, ',')
	switch {
	case lastDot >= 0 && lastComma >= 0:
		if lastDot > lastComma {
			t = strings.ReplaceAll(t, ",", "")
		} else {
			t = strings.ReplaceAll(strings.ReplaceAll(t, ".", ""), ",", ".")
		}
	case lastComma >= 0:
		parts := strings.Split(t, ",")
		grouped := len(parts) > 1
		for _, p := range parts[1:] {
			if len(p) != 3 {
				grouped = false
				break
			}
		}
		if grouped {
			t = strings.ReplaceAll(t, ",", "")
		} else {
			t = strings.Replace(t, ",", ".", 1)
		}
	case lastDot >= 0:
		parts := strings.Split(t, ".")
		grouped := len(parts) > 1
		for _, p := range parts[1:] {
			if len(p) != 3 {
				grouped = false
				break
			}
		}
		if grouped {
			t = strings.ReplaceAll(t, ".", "")
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// FXTable returns a copy of the curated USD-per-unit rate table. The
// /api/market/fx endpoint serves it so the FRONTEND converts local face
// values with the SAME rates the backend cap filter uses — one source of
// truth on both sides.
func FXTable() map[string]float64 {
	out := make(map[string]float64, len(usdPerUnit))
	for k, v := range usdPerUnit {
		out[k] = v
	}
	return out
}

// countryCurrency is the ISO-3166 -> ISO-4217 fallback used ONLY for
// DISPLAY totals, when neither the product family meta nor the denomination
// label carries a currency code. It never influences pricing.
var countryCurrency = map[string]string{
	"US": "USD", "TR": "TRY", "GB": "GBP", "CA": "CAD", "AU": "AUD",
	"JP": "JPY", "KR": "KRW", "IN": "INR", "BR": "BRL", "MX": "MXN",
	"PL": "PLN", "SE": "SEK", "NO": "NOK", "DK": "DKK", "CH": "CHF",
	"RU": "RUB", "ID": "IDR", "PH": "PHP", "TH": "THB", "VN": "VND",
	"MY": "MYR", "SG": "SGD", "ZA": "ZAR", "AE": "AED", "SA": "SAR",
	"NG": "NGN", "EG": "EGP", "AR": "ARS", "CL": "CLP", "CO": "COP",
	"NZ": "NZD", "CZ": "CZK", "HU": "HUF", "RO": "RON", "UA": "UAH",
	"DE": "EUR", "FR": "EUR", "ES": "EUR", "IT": "EUR", "NL": "EUR",
	"BE": "EUR", "AT": "EUR", "PT": "EUR", "IE": "EUR", "FI": "EUR",
	"GR": "EUR", "SK": "EUR", "SI": "EUR", "EE": "EUR", "LV": "EUR",
	"LT": "EUR", "LU": "EUR", "MT": "EUR", "CY": "EUR", "HR": "EUR",
}

// CurrencyForCountry returns the display currency for a country code, or ""
// when the country is unknown. DISPLAY ONLY — see countryCurrency.
func CurrencyForCountry(country string) string {
	return countryCurrency[strings.ToUpper(strings.TrimSpace(country))]
}
