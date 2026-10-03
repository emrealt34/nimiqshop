package handlers

import (
	"encoding/json"
	"testing"

	"nimiqshop/internal/cryptorefills"
)

// A range product is bought by the amount the buyer typed, and the supplier
// prices THAT amount only when the delivery carries product_value. The
// customer paths set it (quote_handlers.go, quote_batch.go) and the admin test
// centre did not: the delivery said `denomination: "range"` with a null
// product_value, which the supplier answers with OUT_OF_STOCK — so not a
// single range product could be tested from the sandbox.
//
// This locks the wire shape: for a range selection the payload keeps the
// amount AND normalises the label to "range" (docs), while fixed SKUs never
// carry product_value.
func TestRangeDeliveryCarriesProductValue(t *testing.T) {
	v := 5000.0
	req := &cryptorefills.CreateOrderRequest{
		Deliveries: []cryptorefills.Delivery{{
			BrandName: "Amazon.com.tr", CountryCode: "TR",
			Denomination: "range", ProductValue: &v, BeneficiaryAccount: "a@b.co",
		}},
		Payment: cryptorefills.OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: "BTC"},
		Lang:    "en",
	}
	raw, err := cryptorefills.MarshalCreateRequest(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Deliveries []struct {
			Denomination string   `json:"denomination"`
			ProductValue *float64 `json:"product_value"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Deliveries) != 1 {
		t.Fatalf("expected one delivery, got %d", len(got.Deliveries))
	}
	d := got.Deliveries[0]
	if d.Denomination != "range" {
		t.Fatalf("range selection must keep the literal \"range\" label, got %q", d.Denomination)
	}
	if d.ProductValue == nil || *d.ProductValue != v {
		t.Fatalf("range selection must carry product_value %v, got %v", v, d.ProductValue)
	}

	// Fixed SKU: no product_value on the wire, exactly as before.
	fixed := &cryptorefills.CreateOrderRequest{
		Deliveries: []cryptorefills.Delivery{{
			BrandName: "Google Play", CountryCode: "TR", Denomination: "50 TRY",
			ProductValue: &v, BeneficiaryAccount: "a@b.co",
		}},
		Payment: cryptorefills.OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: "BTC"},
		Lang:    "en",
	}
	rawFixed, _ := cryptorefills.MarshalCreateRequest(fixed)
	var gotFixed struct {
		Deliveries []struct {
			ProductValue *float64 `json:"product_value"`
		} `json:"deliveries"`
	}
	_ = json.Unmarshal(rawFixed, &gotFixed)
	if gotFixed.Deliveries[0].ProductValue != nil {
		t.Fatalf("a fixed SKU must not send product_value, got %v", *gotFixed.Deliveries[0].ProductValue)
	}
}
