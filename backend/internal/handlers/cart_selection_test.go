package handlers

import (
	"nimiqshop/internal/cryptorefills"
	"testing"
)

func TestCartSelectionBrandAndChannel(t *testing.T) {
	phone := cryptorefills.Product{ProductID: "shared", Denomination: "100 TRY", DeliveryType: "by_phone", Amount: 100, CurrencyCode: "TRY"}
	email := cryptorefills.Product{ProductID: "shared", Denomination: "100 TRY", DeliveryType: "by_email", Amount: 100, CurrencyCode: "TRY"}
	m := familyMeta{Known: true, Kind: "mobile_recharge", Products: []cryptorefills.Product{phone, email}, ProductGroups: []familyProductGroup{
		{Brand: "Credits", BrandID: "credit", Products: []cryptorefills.Product{phone}},
		{Brand: "Voucher", BrandID: "voucher", Products: []cryptorefills.Product{email}},
	}}
	for _, tc := range []struct{ brand, id, want string }{{"Credits", "credit", "by_phone"}, {"Voucher", "voucher", "by_email"}} {
		selected, err := m.scopeSelection(tc.brand, tc.id, "100 TRY")
		if err != nil {
			t.Fatal(err)
		}
		if got := selected.deliveryChannelFor("shared", "100 TRY"); got != tc.want {
			t.Fatalf("%s got %s, want %s", tc.brand, got, tc.want)
		}
		if selected.BrandName != tc.brand {
			t.Fatalf("wrong supplier brand %s", selected.BrandName)
		}
	}
	if _, err := m.scopeSelection("", "", "100 TRY"); err == nil {
		t.Fatal("missing brand must not choose arbitrarily")
	}
	if _, err := m.scopeSelection("Credits", "voucher", "100 TRY"); err == nil {
		t.Fatal("mismatched brand and id must fail")
	}
	if _, err := m.scopeSelection("not-a-brand", "", "100 TRY"); err == nil {
		t.Fatal("unknown brand must fail")
	}
}

func TestCartSelectionSameIDUsesDenomination(t *testing.T) {
	m := familyMeta{Products: []cryptorefills.Product{
		{ProductID: "shared", Denomination: "50 TRY", DeliveryType: "by_email"},
		{ProductID: "shared", Denomination: "100 TRY", DeliveryType: "by_phone"},
	}}
	if got := m.deliveryChannelFor("shared", "100 TRY"); got != "by_phone" {
		t.Fatalf("got %s", got)
	}
}

func TestCartSelectionConflictingSameBrandIsRefused(t *testing.T) {
	m := familyMeta{ProductGroups: []familyProductGroup{{Brand: "Credits", Products: []cryptorefills.Product{
		{ProductID: "same", Denomination: "100 TRY", DeliveryType: "by_email"},
		{ProductID: "same", Denomination: "100 TRY", DeliveryType: "by_phone"},
	}}}}
	if _, err := m.scopeSelection("Credits", "", "100 TRY"); err == nil {
		t.Fatal("conflicting deliveries must never pick the first")
	}
}

// A multi-row batch can reconcile to one line. Converting it to the single
// flow must not drop the supplier brand, privacy choice or duplicate override.
func TestSingleQuoteFromMergedBatchPreservesBuyerChoices(t *testing.T) {
	item := batchQuoteItem{ProductID: "Vodafone", Country: "TR", Brand: "Vodafone Credits", BrandID: "turkey", Denomination: "250 TRY", ProductValue: 250, Quantity: 2, PhoneNumber: "+905551234567"}
	batch := batchQuoteRequest{Items: []batchQuoteItem{item}, Email: "buyer@example.com", CashbackCode: "PROMO", PaymentMethod: "nimiq_pay", CashbackDestination: "burn", Anonymous: true, AckActiveCheckout: true, GiftChannel: "email", GiftMessage: "hello", GifterIdenticon: "avatar"}
	got := singleQuoteFromBatch(batch, item)
	if got.ProductID != item.ProductID || got.Country != item.Country || got.Brand != item.Brand || got.BrandID != item.BrandID || got.Denomination != item.Denomination || got.ProductValue != item.ProductValue || got.Quantity != item.Quantity || got.PhoneNumber != item.PhoneNumber || got.Email != batch.Email || got.CashbackCode != batch.CashbackCode || got.PaymentMethod != batch.PaymentMethod || got.CashbackDestination != batch.CashbackDestination || !got.Anonymous || !got.AckActiveCheckout || got.GiftChannel != batch.GiftChannel || got.GiftMessage != batch.GiftMessage || got.GifterIdenticon != batch.GifterIdenticon {
		t.Fatalf("single conversion lost buyer choices: %+v", got)
	}
}
