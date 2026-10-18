package handlers

import "testing"

// The acknowledgement of the checkout gate must not change what a request
// "is": the same cart with and without ack_active_checkout has the same
// request AND purchase fingerprints, for both endpoints. Otherwise an order
// opened beside a live one can never be recognised as the same cart again.
func TestCheckoutFingerprintsIgnoreTheGateAcknowledgement(t *testing.T) {
	plain := createQuoteRequest{ProductID: "test-airbnb", Country: "US", Denomination: "range", ProductValue: 25, Quantity: 1, Email: "buyer@example.com", CashbackCode: "PROMO"}
	acked := plain
	acked.AckActiveCheckout = true
	reqA, purA := singleFingerprints(plain)
	reqB, purB := singleFingerprints(acked)
	if reqA != reqB || purA != purB {
		t.Fatalf("single: ack must not change the fingerprints (request %v, purchase %v)", reqA == reqB, purA == purB)
	}
	// The cosmetics rule still holds: a different promo code is a different
	// request but the same purchase.
	other := plain
	other.CashbackCode = "OTHER"
	if reqC, purC := singleFingerprints(other); reqC == reqA || purC != purA {
		t.Fatalf("single: cashback code must change the request fingerprint only (request %v, purchase %v)", reqC == reqA, purC == purA)
	}
	// And a different cart is a different purchase.
	different := plain
	different.ProductValue = 50
	if _, purD := singleFingerprints(different); purD == purA {
		t.Fatal("single: a different face value must be a different purchase")
	}

	bplain := batchQuoteRequest{Items: []batchQuoteItem{{ProductID: "test-airbnb", Country: "US", Denomination: "range", ProductValue: 25, Quantity: 1}}, Email: "buyer@example.com"}
	backed := bplain
	backed.AckActiveCheckout = true
	breqA, bpurA := batchFingerprints(bplain)
	breqB, bpurB := batchFingerprints(backed)
	if breqA != breqB || bpurA != bpurB {
		t.Fatalf("batch: ack must not change the fingerprints (request %v, purchase %v)", breqA == breqB, bpurA == bpurB)
	}
	bdifferent := bplain
	bdifferent.Items = []batchQuoteItem{{ProductID: "test-airbnb", Country: "US", Denomination: "range", ProductValue: 50, Quantity: 1}}
	if _, bpurD := batchFingerprints(bdifferent); bpurD == bpurA {
		t.Fatal("batch: a different face value must be a different purchase")
	}
}
