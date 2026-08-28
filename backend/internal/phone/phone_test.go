package phone

import (
	"strings"
	"testing"
)

func TestNormalizeValid(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		country string
		want    string
	}{
		// Already international, verbatim.
		{"plain", "+905551234567", "TR", "+905551234567"},
		{"us", "+14155551234", "US", "+14155551234"},
		// Separators stripped.
		{"spaces", "+90 555 123 45 67", "TR", "+905551234567"},
		{"mixed", "+90-(555) 123.45/67", "TR", "+905551234567"},
		{"leading-trailing-space", "  +14155551234  ", "US", "+14155551234"},
		{"dash-only", "+1-415-555-1234", "US", "+14155551234"},
		// International access prefix 00.
		{"00", "00905551234567", "TR", "+905551234567"},
		{"00-separators", "00 90 555 123 45 67", "TR", "+905551234567"},
		{"00-us", "0014155551234", "US", "+14155551234"},
		// National format (trunk 0) + product country.
		{"tr-domestic", "05551234567", "TR", "+905551234567"},
		{"tr-domestic-sep", "0555 123 45 67", "TR", "+905551234567"},
		{"tr-domestic-lower", "05551234567", "tr", "+905551234567"},
		{"de-mobile", "0151 12345678", "DE", "+4915112345678"},
		{"gb-mobile", "07700 900123", "GB", "+447700900123"},
		{"fr-mobile", "06 12 34 56 78", "FR", "+33612345678"},
		{"it-landline-keep0", "06 1234 5678", "IT", "+390612345678"},
		{"it-mobile-no-zero-rejected-differently", "333 123 45 67", "IT", ""}, // exercised in invalid
		{"au-mobile", "0412 345 678", "AU", "+61412345678"},
		{"nz-mobile", "021 123 4567", "NZ", "+64211234567"},
		{"br-mobile", "011 99876 5432", "BR", "+5511998765432"},
		{"ar-buenosaires", "011 5555 1234", "AR", "+541155551234"},
		{"sa-mobile", "0501234567", "SA", "+966501234567"},
		{"ae-mobile", "050 123 4567", "AE", "+971501234567"},
		// Boundary lengths.
		{"min-8-digits", "+12345678", "US", "+12345678"},
		{"max-15-digits", "+123456789012345", "US", "+123456789012345"},
	}
	for _, c := range cases {
		if c.want == "" {
			continue
		}
		got, err := Normalize(c.raw, c.country)
		if err != nil {
			t.Errorf("%s: Normalize(%q, %q) error: %v", c.name, c.raw, c.country, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: Normalize(%q, %q) = %q, want %q", c.name, c.raw, c.country, got, c.want)
		}
	}
}

func TestNormalizeInvalid(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		country string
		wantSub string // substring that must appear in the error
	}{
		{"empty", "", "TR", "required"},
		{"whitespace", "   ", "TR", "required"},
		{"plus-only", "+", "TR", "required"},
		{"separators-only", "  - ( ) ", "TR", "required"},
		// Wrong digit count.
		{"too-short-7", "+1234567", "US", "E.164"},
		{"too-long-16", "+1234567890123456", "US", "E.164"},
		{"plus-zero", "+05551234567", "TR", "E.164"},
		{"00-then-truncated", "0090555", "TR", "E.164"},
		// Bare national number: never guess the country.
		{"bare-tr-no-zero", "5551234567", "TR", "country code"},
		{"bare-us", "14155551234", "US", "country code"},
		// National format but country unknown/missing: fail closed.
		{"domestic-unknown-country", "05551234567", "XX", "country code"},
		{"domestic-no-country", "05551234567", "", "country code"},
		{"domestic-mx-excluded", "015555551234", "MX", "country code"},
		// Malformed characters.
		{"letters", "0555abcd1234", "TR", "invalid characters"},
		{"letter-after-plus", "+90555123456x", "TR", "invalid characters"},
		{"double-plus", "++905551234567", "TR", "invalid characters"},
		{"plus-not-first", "90+5551234567", "TR", "invalid characters"},
		{"email-looks", "user@example.com", "TR", "invalid characters"},
		{"it-mobile-no-zero", "333 123 45 67", "IT", "country code"},
	}
	for _, c := range cases {
		got, err := Normalize(c.raw, c.country)
		if err == nil {
			t.Errorf("%s: Normalize(%q, %q) = %q, want error", c.name, c.raw, c.country, got)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.wantSub)
		}
	}
}

func TestValidate(t *testing.T) {
	valid := []string{
		"+905551234567", "+14155551234", "+12345678", "+123456789012345",
	}
	for _, s := range valid {
		if err := Validate(s); err != nil {
			t.Errorf("Validate(%q) = %v, want ok", s, err)
		}
	}
	invalid := []string{
		"", "905551234567", "+9055512", "+9055512345678901",
		"+05551234567", "user@example.com", "+1 415 555 1234", "+90 555",
	}
	for _, s := range invalid {
		if err := Validate(s); err == nil {
			t.Errorf("Validate(%q) = ok, want error", s)
		}
	}
}

// TestNormalizeIdempotent: normalizing an already-normalized number must be
// a no-op — the backend normalizes on every entry point, and stored values
// are re-normalized on some paths.
func TestNormalizeIdempotent(t *testing.T) {
	for _, s := range []string{"+905551234567", "+14155551234", "+4915112345678"} {
		once, err := Normalize(s, "XX")
		if err != nil {
			t.Fatalf("Normalize(%q): %v", s, err)
		}
		twice, err := Normalize(once, "XX")
		if err != nil {
			t.Fatalf("re-normalize(%q): %v", once, err)
		}
		if once != s || twice != s {
			t.Errorf("idempotence broken: %q -> %q -> %q", s, once, twice)
		}
	}
}
