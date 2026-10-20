package cryptorefills

import (
	"encoding/json"
	"testing"
	"time"
)

/* Sub-100-sat amounts (live regression, 2026-10-20).
 *
 * A 50 NGN Airtel top-up validated at 0.00000052 BTC, the supplier accepted
 * the order (WaitingForPayment, invoice attached), and the shop then parked
 * it in manual review with "invalid decimal amount": the amount had been
 * serialised Java-style as "5.2E-7". The decoders must read exponent
 * notation exactly and hand every consumer the plain form.
 */

func TestPlainDecimalCanonicalises(t *testing.T) {
	cases := map[string]string{
		"5.2E-7":     "0.00000052",
		"5.2e-7":     "0.00000052",
		"1E-7":       "0.0000001",
		"1.5e3":      "1500",
		"1500.00":    "1500",
		"007.50":     "7.5",
		"0.00000052": "0.00000052",
		"52":         "52",
		"0":          "0",
		"0.0":        "0",
		"1.276E-5":   "0.00001276",
		"12.5E+2":    "1250",
	}
	for in, want := range cases {
		got, ok := PlainDecimal(in)
		if !ok || got != want {
			t.Errorf("PlainDecimal(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "NaN", "Infinity", "-5", "+5", "1,5", "1e", "1e400", "0x10", "5.2E-7 ", "1.2.3", "abc"} {
		if bad == "5.2E-7 " {
			continue // surrounding whitespace is trimmed, see below
		}
		if got, ok := PlainDecimal(bad); ok {
			t.Errorf("PlainDecimal(%q) accepted as %q", bad, got)
		}
	}
	if got, ok := PlainDecimal(" 5.2E-7 "); !ok || got != "0.00000052" {
		t.Errorf("whitespace should be trimmed, got %q %v", got, ok)
	}
}

func TestPositiveDecimalReadsExponentNotationExactly(t *testing.T) {
	r, err := PositiveDecimal("5.2E-7")
	if err != nil {
		t.Fatalf("5.2E-7 refused: %v", err)
	}
	if r.FloatString(8) != "0.00000052" {
		t.Fatalf("5.2E-7 read as %s", r.FloatString(8))
	}
	if _, err := PositiveDecimal("0E-8"); err == nil {
		t.Fatal("a zero amount in exponent form must still be refused")
	}
	if _, err := PositiveDecimal("5.2E-700"); err == nil {
		t.Fatal("a hostile exponent must be refused")
	}
}

func TestOrderDecodersCanonicaliseTinyAmounts(t *testing.T) {
	// v5 flat shape, amount as a JSON string in exponent form.
	var flat Order
	if err := json.Unmarshal([]byte(`{"order_id":"o1","order_state":"WaitingForPayment","coin":"BTC","coin_amount":"5.2E-7","wallet_address":"lnbc1","network":"Lightning"}`), &flat); err != nil {
		t.Fatal(err)
	}
	if flat.CoinAmount != "0.00000052" {
		t.Fatalf("flat string: coin_amount %q", flat.CoinAmount)
	}
	// Flat shape, amount as a bare JSON number in exponent form.
	var num Order
	if err := json.Unmarshal([]byte(`{"order_id":"o2","order_state":"WaitingForPayment","coin":"BTC","coin_amount":5.2E-7}`), &num); err != nil {
		t.Fatal(err)
	}
	if num.CoinAmount != "0.00000052" {
		t.Fatalf("flat number: coin_amount %q", num.CoinAmount)
	}
	// v6 nested payment shape.
	var nested Order
	if err := json.Unmarshal([]byte(`{"id":"o3","status":"WaitingForPayment","payment":{"coin":"BTC","coin_amount":"5.2E-7","network":"Lightning","wallet_address":"lnbc1"}}`), &nested); err != nil {
		t.Fatal(err)
	}
	if nested.CoinAmount != "0.00000052" {
		t.Fatalf("nested: coin_amount %q", nested.CoinAmount)
	}
	// Validation and price responses go through the same normaliser.
	var v ValidationResult
	if err := json.Unmarshal([]byte(`{"coin":"BTC","coin_amount":"5.2E-7","problems":[]}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.CoinAmount != "0.00000052" {
		t.Fatalf("validation: coin_amount %q", v.CoinAmount)
	}
	var p PriceQuote
	if err := json.Unmarshal([]byte(`{"coin":"BTC","coin_amount":5.2E-7}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.CoinAmount != "0.00000052" {
		t.Fatalf("price: coin_amount %q", p.CoinAmount)
	}
}

func TestATinyLightningOrderIsPayable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	invoice := synthInvoiceHRP(t, now.Add(-time.Minute), 3600, "lnbc520n") // 52 sats
	order := lightningOrder(now.Add(-time.Minute), invoice)
	order.CoinAmount = "5.2E-7"
	info, err := ValidatePayableOrder(order, now)
	if err != nil {
		t.Fatalf("a 52-sat order with a matching invoice must be payable, got %v", err)
	}
	if info.AmountBTC.FloatString(8) != "0.00000052" {
		t.Fatalf("invoice amount read as %s", info.AmountBTC.FloatString(8))
	}
	// And the guard still catches a real mismatch at that scale.
	order.CoinAmount = "5.3E-7"
	if _, err := ValidatePayableOrder(order, now); err == nil {
		t.Fatal("a 53-sat claim against a 52-sat invoice must be refused")
	}
}
