package nimiq

import "testing"

// FuzzValidateAddress makes sure the IBAN-style address checker never panics
// on arbitrary input and that every accepted address pretty-prints and
// re-validates.
func FuzzValidateAddress(f *testing.F) {
	f.Add("NQ07 0000 0000 0000 0000 0000 0000 0000 0000")
	f.Add("nq070000000000000000000000000000000000")
	f.Add("")
	f.Add("NQ")
	f.Add("NQ99 XXXX")
	f.Fuzz(func(t *testing.T, addr string) {
		if err := ValidateAddress(addr); err != nil {
			return
		}
		pretty, err := PrettyAddress(addr)
		if err != nil {
			t.Fatalf("valid address %q failed to pretty-print: %v", addr, err)
		}
		if err := ValidateAddress(pretty); err != nil {
			t.Fatalf("pretty form %q of valid %q is invalid: %v", pretty, addr, err)
		}
	})
}
