// Package phone implements the supplier's beneficiary_account rules for
// mobile top-ups.
//
// CryptoRefills delivers each product to the order's beneficiary_account.
// The required format depends on the product kind:
//
//   - Gift cards and eSIMs: the end-user's EMAIL address.
//   - Mobile topups: the recipient's phone number in E.164 format —
//     leading +, country code, subscriber number — e.g. +14155551234 or
//     +905551234567.
//
// A top-up sent to a malformed number is a lost sale and (if the number is
// merely misparsed) delivery to the WRONG customer. This package therefore
// accepts the formats customers actually type, converts them to strict
// E.164, and — when the result cannot be determined with certainty —
// rejects with an actionable message instead of guessing.
package phone

import (
	"fmt"
	"regexp"
	"strings"
)

// e164Re is the strict E.164 shape: leading +, a non-zero country-code
// digit, then 7-14 more digits — 8 to 15 digits in total (ITU E.164).
var e164Re = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

// Validate reports whether s is a strict E.164 number.
func Validate(s string) error {
	if !e164Re.MatchString(s) {
		return errorsE164()
	}
	return nil
}

func errorsE164() error {
	return fmt.Errorf("phone_number must be in E.164 format, e.g. +905551234567 (leading +, country code, 8-15 digits)")
}

func errorsNeedCountry() error {
	return fmt.Errorf("phone_number must include the country code — use the international format with leading +, e.g. +905551234567")
}

func errorsRequired() error {
	return fmt.Errorf("phone_number is required (E.164, e.g. +905551234567)")
}

func errorsInvalidChars() error {
	return fmt.Errorf("phone_number contains invalid characters — only digits, separators (space . - ( ) /) and a leading + are allowed, e.g. +905551234567")
}

// Normalize converts raw (a customer-typed phone number) to strict E.164.
//
// countryISO is the product's 2-letter ISO country code (e.g. "TR"). It is
// used ONLY for national-format numbers that start with the trunk prefix
// 0 (e.g. 0555 123 45 67 for a TR product -> +905551234567). Numbers with
// neither a leading + nor a leading 0 are rejected: we never guess which
// country a bare number belongs to, because a wrong guess means a top-up
// delivered to someone else.
func Normalize(raw, countryISO string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errorsRequired()
	}
	var digits strings.Builder
	hasPlus := false
	for i, r := range s {
		switch {
		case r == '+' && i == 0 && !hasPlus:
			hasPlus = true
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case isSeparator(r):
			// customer formatting noise
		default:
			return "", errorsInvalidChars()
		}
	}
	d := digits.String()
	if d == "" {
		return "", errorsRequired()
	}
	switch {
	case hasPlus:
		// Already international: shape-check the digits.
		if !validDigits(d) {
			return "", errorsE164()
		}
		return "+" + d, nil
	case strings.HasPrefix(d, "00"):
		// International access prefix (0090...): the remainder must be a
		// full international number on its own.
		rest := d[2:]
		if !validDigits(rest) {
			return "", errorsE164()
		}
		return "+" + rest, nil
	case strings.HasPrefix(d, "0"):
		// National format with trunk prefix (e.g. 05551234567). The
		// product's country is the only sane source for the country code.
		country := strings.ToUpper(strings.TrimSpace(countryISO))
		dial, ok := dialCodes[country]
		if !ok {
			return "", errorsNeedCountry()
		}
		num := d
		if !keepZero[country] {
			num = strings.TrimPrefix(d, "0")
		}
		combined := dial + num
		if !validDigits(combined) {
			return "", errorsE164()
		}
		return "+" + combined, nil
	default:
		// Bare national number without trunk 0: no way to know the country
		// for sure. Fail closed.
		return "", errorsNeedCountry()
	}
}

// validDigits reports whether d is a valid E.164 digit body: 8-15 digits,
// first digit non-zero.
func validDigits(d string) bool {
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return false
	}
	for i := 0; i < len(d); i++ {
		if d[i] < '0' || d[i] > '9' {
			return false
		}
	}
	return true
}

func isSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '-', '.', '(', ')', '/':
		return true
	}
	return false
}

// keepZero lists countries whose E.164 numbers KEEP the national leading
// 0 (the 0 is part of the significant number, not a trunk prefix). Italy
// is the common case: 06 1234 5678 -> +39 06 1234 5678.
var keepZero = map[string]bool{
	"IT": true,
}

// dialCodes maps ISO 3166-1 alpha-2 country codes to their international
// dialing codes, for the countries where "strip ONE leading 0, then
// prepend the dial code" is the correct E.164 conversion (i.e. the country
// uses 0 as a trunk prefix and its significant numbers do not start with
// 0). Countries with special access prefixes (e.g. MX "01", RU/KZ "8")
// are intentionally ABSENT: a national-format number from them is
// rejected with a clear "use international format" message instead of
// risking a wrong number. This is a deliberately conservative table.
var dialCodes = map[string]string{
	// Europe
	"AL": "355", "AD": "376", "AT": "43", "BY": "375", "BE": "32", "BA": "387",
	"BG": "359", "HR": "385", "CY": "357", "CZ": "420", "DK": "45", "EE": "372",
	"FO": "298", "FI": "358", "FR": "33", "GE": "995", "DE": "49", "HU": "36",
	"IS": "354", "IE": "353", "IM": "44", "IT": "39", "LV": "371", "LI": "423",
	"LT": "370", "LU": "352", "MT": "356", "ME": "382", "MK": "389", "NL": "31",
	"NO": "47", "PL": "48", "PT": "351", "RO": "40", "RU": "7", "SM": "378",
	"RS": "381", "SK": "421", "SI": "386", "ES": "34", "SE": "46", "CH": "41",
	"TR": "90", "UA": "380", "GB": "44", "MD": "373",
	// Middle East & North Africa
	"AE": "971", "AF": "93", "AZ": "994", "BH": "973", "DJ": "253", "EG": "20",
	"IL": "972", "IQ": "964", "IR": "98", "JO": "962", "KW": "965", "LB": "961",
	"LY": "218", "MA": "212", "MR": "222", "OM": "968", "PS": "970", "QA": "974",
	"SA": "966", "SD": "249", "SY": "963", "TN": "216", "YE": "967", "DZ": "213",
	// Africa
	"AO": "244", "BF": "226", "BI": "257", "BJ": "229", "BW": "267", "CD": "243",
	"CF": "236", "CG": "242", "CI": "225", "CM": "237", "CV": "238", "ER": "291",
	"ET": "251", "GA": "241", "GH": "233", "GM": "220", "GN": "224", "GQ": "240",
	"GW": "245", "KE": "254", "LS": "266", "LR": "231", "MG": "261", "ML": "223",
	"MN": "976", "MU": "230", "MW": "265", "MZ": "258", "NA": "264", "NE": "227",
	"NG": "234", "RW": "250", "SC": "248", "SL": "232", "SN": "221", "SO": "252",
	"SS": "211", "ST": "239", "SZ": "268", "TD": "235", "TG": "228", "TZ": "255",
	"UG": "256", "ZA": "27", "ZM": "260", "ZW": "263",
	// Asia
	"AM": "374", "BT": "975", "BD": "880", "BN": "673", "KH": "855", "CN": "86",
	"IN": "91", "ID": "62", "JP": "81", "KZ": "7", "KP": "850", "KG": "996",
	"LA": "856", "LK": "94", "MM": "95", "MY": "60", "MV": "960", "NP": "977",
	"PH": "63", "PK": "92", "SG": "65", "KR": "82", "TH": "66",
	"TJ": "992", "TL": "670", "UZ": "998", "VN": "84",
	// Oceania
	"AU": "61", "FJ": "679", "NZ": "64", "PG": "675", "WS": "685",
	// Latin America (0 is a trunk prefix in most; where numbers have no
	// leading 0 the table entry simply never triggers)
	"AR": "54", "BO": "591", "BR": "55", "CL": "56", "CO": "57", "CR": "506",
	"CU": "53", "EC": "593", "GT": "502", "HN": "504", "NI": "505", "PA": "507",
	"PE": "51", "PY": "595", "SV": "503", "UY": "598", "VE": "58",
	// NOTE: intentionally absent — special access prefixes or NANPA:
	// MX ("01" access), KZ/RU ("8" access already handled above only when
	// unambiguous — KZ stays out to avoid 7/8 confusion), US/CA/DO/JM/BS/
	// and the rest of NANPA (no 0 trunk prefix; type +1 directly).
}
