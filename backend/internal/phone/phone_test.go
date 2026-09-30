package phone

import "testing"

func TestNormalizationContracts(t *testing.T) {
	for _, tc := range []struct{ raw, country, want string }{
		{"+90 (555) 123-45.67", "", "+905551234567"},
		{"0090 555/1234567", "", "+905551234567"},
		{"0555 123 45 67", " tr ", "+905551234567"},
		{"06 1234 5678", "IT", "+390612345678"},
		{"+1\t415\n555\r1234", "US", "+14155551234"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := Normalize(tc.raw, tc.country)
			if err != nil || got != tc.want {
				t.Fatalf("Normalize: %q %v; want %q", got, err, tc.want)
			}
			if err = Validate(got); err != nil {
				t.Fatalf("normalized result invalid: %v", err)
			}
		})
	}
	for _, tc := range []struct{ raw, country string }{
		{"", "TR"}, {"---", "TR"}, {"+", "TR"}, {"++905551234567", "TR"}, {"+90abc5551234", "TR"},
		{"+123", ""}, {"00123", ""}, {"000905551234567", ""}, {"05551234567", "XX"},
		{"0555", "TR"}, {"5551234567", "TR"}, {"+0123456789", ""}, {"+1234567890123456", ""},
	} {
		if got, err := Normalize(tc.raw, tc.country); err == nil || got != "" {
			t.Errorf("accepted ambiguous/invalid %q: %q %v", tc.raw, got, err)
		}
	}
	for _, value := range []string{"", "12345678", "+012345678", "+123", "+1234567890123456", "+1234567x"} {
		if Validate(value) == nil {
			t.Errorf("Validate accepted %q", value)
		}
	}
	for _, value := range []string{"", "012345678", "1234567x", "1234567890123456"} {
		if validDigits(value) {
			t.Errorf("validDigits accepted %q", value)
		}
	}
	if !validDigits("12345678") || isSeparator('x') {
		t.Fatal("digit/separator boundary")
	}
}
