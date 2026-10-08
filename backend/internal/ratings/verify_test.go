package ratings

import (
	"errors"
	"testing"

	"nimiqshop/internal/nimiq"
)

func goodTx() nimiq.TxDetail {
	return nimiq.TxDetail{
		From: "NQ22111111111111111111111111111111111111", To: "NQ07000000000000000000000000000000000000",
		Value: PriceLuna, Data: []byte("5 ok"), Mined: true, Success: true,
	}
}

func TestVerifyTxAcceptsTheExactRating(t *testing.T) {
	// Addresses are compared normalised, so the spaced and lower-case forms match.
	if err := VerifyTx(goodTx(), "nq22 1111 1111 1111 1111 1111 1111 1111 1111 1111", "NQ07000000000000000000000000000000000000", "5 ok"); err != nil {
		t.Fatalf("exact rating rejected: %v", err)
	}
}

func TestVerifyTxRejectsEveryMismatch(t *testing.T) {
	const buyer = "NQ22111111111111111111111111111111111111"
	const shop = "NQ07000000000000000000000000000000000000"
	mutations := map[string]func(d *nimiq.TxDetail){
		"sender":    func(d *nimiq.TxDetail) { d.From = "NQ99000000000000000000000000000000000000" },
		"recipient": func(d *nimiq.TxDetail) { d.To = "NQ99000000000000000000000000000000000000" },
		"amount":    func(d *nimiq.TxDetail) { d.Value = 100000 },
		"memo":      func(d *nimiq.TxDetail) { d.Data = []byte("5 okay") },
		"failed":    func(d *nimiq.TxDetail) { d.Success = false },
	}
	for name, mut := range mutations {
		d := goodTx()
		mut(&d)
		if err := VerifyTx(d, buyer, shop, "5 ok"); !errors.Is(err, ErrNotThisRating) {
			t.Errorf("%s mismatch: err = %v, want ErrNotThisRating", name, err)
		}
	}
}

func TestVerifyTxWaitsForInclusion(t *testing.T) {
	const buyer = "NQ22111111111111111111111111111111111111"
	const shop = "NQ07000000000000000000000000000000000000"
	d := goodTx()
	d.Mined = false
	if err := VerifyTx(d, buyer, shop, "5 ok"); !errors.Is(err, ErrPending) {
		t.Fatalf("unmined: err = %v, want ErrPending", err)
	}
}
