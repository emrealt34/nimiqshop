package phone

import "testing"

// FuzzNormalize feeds arbitrary input to the E.164 normaliser. It must never
// panic and, whenever it accepts a number, the result must round-trip through
// Validate and Normalize unchanged.
func FuzzNormalize(f *testing.F) {
	seeds := []struct{ raw, iso string }{
		{"+90 532 000 00 00", "TR"},
		{"05320000000", "TR"},
		{"+1 (415) 555-0100", "US"},
		{"", ""},
		{"abc", "DE"},
		{"+", "GB"},
	}
	for _, s := range seeds {
		f.Add(s.raw, s.iso)
	}
	f.Fuzz(func(t *testing.T, raw, iso string) {
		out, err := Normalize(raw, iso)
		if err != nil {
			return
		}
		if verr := Validate(out); verr != nil {
			t.Fatalf("Normalize(%q,%q)=%q but Validate rejects it: %v", raw, iso, out, verr)
		}
		again, err := Normalize(out, iso)
		if err != nil || again != out {
			t.Fatalf("Normalize not idempotent: %q -> %q -> %q (%v)", raw, out, again, err)
		}
	})
}
