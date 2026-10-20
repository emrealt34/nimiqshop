package suppliermock

import "testing"

func TestWireDecimalMatchesJavaBigDecimal(t *testing.T) {
	m := &mockState{faults: map[string]any{"java_decimals": true}}
	m.javaDecimals.Store(true)
	cases := map[string]string{
		"0.00000052": "5.2E-7", // 52 sats — the live case
		"0.00000099": "9.9E-7",
		"0.00000001": "1E-8",
		"0.00000150": "0.00000150", // adjusted exponent -6: Java prints positionally, scale kept
		"0.00001276": "0.00001276",
		"1500.00":    "1500.00",
		"0":          "0",
		"0.00000000": "0",
	}
	for in, want := range cases {
		if got := m.wireDecimal(in); got != want {
			t.Errorf("wireDecimal(%q) = %q, want %q", in, got, want)
		}
	}
	plain := &mockState{faults: map[string]any{}}
	if got := plain.wireDecimal("0.00000052"); got != "0.00000052" {
		t.Errorf("without the fault the amount must pass through, got %q", got)
	}
}
