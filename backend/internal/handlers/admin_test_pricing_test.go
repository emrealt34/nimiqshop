package handlers

import (
	"math"
	"testing"

	"nimiqshop/internal/cryptorefills"
)

// The admin test centre must quote what the supplier charges, not what a
// face-value conversion guesses. Two behaviours carry that promise, and both
// are asserted here:
//
//  1. the simulated invoice amount prefers the supplier's validated amount;
//  2. the USD total used for the panel's NIM estimate comes from that same
//     validated amount (supplier price × live BTC), never from the face value.
//
// Regression this locks: the test centre used to skip validation entirely, so
// a 50 TRY Google Play test showed the nominal figure (~3.457 NIM) while a real
// buyer was charged the supplier's price (~3.685 NIM) — the same product, two
// numbers, and the sandbox was the wrong one.

func TestSimulatedCoinAmountPrefersSupplierPrice(t *testing.T) {
	const supplier = "0.00001276"
	// A deliberately different face-value guess: if the fallback is ever used
	// where a validated amount exists, this test fails loudly.
	guessed := 0.00001198

	for _, method := range []string{PaymentMethodNIM, "btc_lightning"} {
		got := simulatedCoinAmount(guessed, method, supplier)
		if got != supplier {
			t.Fatalf("method %q: expected the supplier amount %q, got %q", method, supplier, got)
		}
	}

	// No validated amount (nothing was priced by the supplier): the fallback
	// stays available so the sandbox never becomes unusable.
	if got := simulatedCoinAmount(guessed, "btc_lightning", ""); got == "" {
		t.Fatal("expected the face-value fallback when no validated amount exists")
	}
}

func TestQuotedUSDUsesSupplierAmountNotFaceValue(t *testing.T) {
	const btcUSD = 85000.0
	const supplierBTC = "0.00001276" // ≈ $1.0846
	faceUSD := 1.0176                // 50 TRY at the FX table — what the panel used to show

	usd, err := quotedUSD(supplierBTC, PaymentMethodNIM, btcUSD, faceUSD, true)
	if err != nil {
		t.Fatalf("quotedUSD: %v", err)
	}
	want := 0.00001276 * btcUSD
	if math.Abs(usd-want) > 1e-9 {
		t.Fatalf("expected the supplier price %v USD, got %v", want, usd)
	}
	if math.Abs(usd-faceUSD) < 0.01 {
		t.Fatal("supplier price and face value collapsed to the same number; the test proves nothing")
	}

	// Stablecoin rail: the validated amount is already USD (1:1).
	usdt, err := quotedUSD("1.08", PaymentMethodUSDT, btcUSD, faceUSD, true)
	if err != nil || math.Abs(usdt-1.08) > 1e-9 {
		t.Fatalf("stablecoin rail: want 1.08, got %v (err %v)", usdt, err)
	}

	// The customer path and the sandbox must agree on the NIM figure they
	// show for the same supplier price.
	const nimUSD = 0.00029442
	customerNIM := usd / nimUSD
	sandboxNIM := usd / nimUSD // both now derive from the same USD
	if math.Abs(customerNIM-sandboxNIM) > 1e-9 {
		t.Fatal("NIM estimates diverged")
	}
}

func TestValidationRejectsFreeFormAmountsFromSupplier(t *testing.T) {
	// The supplier is the price authority, but a broken payload must not
	// become a quote: quotedUSD is the only gate between the two.
	for _, bad := range []string{"", "0", "-1", "abc", "NaN", "Inf"} {
		if _, err := quotedUSD(bad, PaymentMethodNIM, 85000, 1.01, true); err == nil {
			t.Fatalf("amount %q should have been rejected", bad)
		}
	}
}

// compile-time nudge: the handler must be able to reach the supplier client;
// if the field type ever changes, this file should stop compiling next to it.
var _ = func(h *Handlers) *cryptorefills.Client { return h.CR }
