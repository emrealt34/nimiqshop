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
	d.Success = false // pending RPC transactions do not have an execution result
	if err := VerifyTx(d, buyer, shop, "5 ok"); !errors.Is(err, ErrPending) {
		t.Fatalf("unmined: err = %v, want ErrPending", err)
	}
}

// Nimiq Pay pays from the user's HTLC: `from` is the contract, the user's key
// is in the proof. Observed on mainnet, tx 86cf3188…: from NQ23 E6AH… (HTLC,
// fromType 2), signers = Nimiq Pay co-signer + the user NQ73 SE1X….
func nimiqPayTx() nimiq.TxDetail {
	return nimiq.TxDetail{
		From:     "NQ23E6AHUL22YN31RNTVGS97JJA4TKT78YSK",
		FromType: nimiq.AccountTypeHTLC,
		Signers:  []string{"NQ14LU5RUH5492SHGEN4U63CSV4V7N49YYU4", "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"},
		To:       "NQ12CPDG4KKU70UBNC7U0Q82T8U7US17PYHU",
		Value:    PriceLuna, Data: []byte("5 kinda cool"), Mined: true, Success: true,
	}
}

func TestVerifyTxAcceptsANimiqPayPaymentSignedByTheBuyer(t *testing.T) {
	err := VerifyTx(nimiqPayTx(), "NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD", "NQ12 CPDG 4KKU 70UB NC7U 0Q82 T8U7 US17 PYHU", "5 kinda cool")
	if err != nil {
		t.Fatalf("Nimiq Pay rating rejected: %v", err)
	}
}

func TestVerifyTxStillRejectsAStrangerForAContractPayment(t *testing.T) {
	const shop = "NQ12CPDG4KKU70UBNC7U0Q82T8U7US17PYHU"
	d := nimiqPayTx()
	if err := VerifyTx(d, "NQ22111111111111111111111111111111111111", shop, "5 kinda cool"); !errors.Is(err, ErrNotThisRating) {
		t.Fatalf("stranger accepted for an HTLC payment: %v", err)
	}
	// The contract address itself is not a buyer anyone can sign in as, but
	// it must still only match when it is the claimed buyer.
	if err := VerifyTx(d, d.From, shop, "5 kinda cool"); err != nil {
		t.Fatalf("debited account rejected: %v", err)
	}
	// Signers are only consulted for contract senders. For a basic account the
	// proof's key IS the sender, so a differing signer list is not trusted.
	basic := goodTx()
	basic.Signers = []string{"NQ99000000000000000000000000000000000000"}
	if err := VerifyTx(basic, "NQ99000000000000000000000000000000000000", "NQ07000000000000000000000000000000000000", "5 ok"); !errors.Is(err, ErrNotThisRating) {
		t.Fatalf("signer list overrode a basic sender: %v", err)
	}
	// Memo, amount and recipient rules are unchanged for contract payments.
	wrongMemo := nimiqPayTx()
	wrongMemo.Data = []byte("5 kinda cold")
	if err := VerifyTx(wrongMemo, "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD", shop, "5 kinda cool"); !errors.Is(err, ErrNotThisRating) {
		t.Fatalf("wrong memo accepted: %v", err)
	}
}

func TestPayerCandidatesListContractThenSigners(t *testing.T) {
	got := PayerCandidates(nimiqPayTx())
	want := []string{"NQ23E6AHUL22YN31RNTVGS97JJA4TKT78YSK", "NQ14LU5RUH5492SHGEN4U63CSV4V7N49YYU4", "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
	basic := goodTx()
	basic.Signers = []string{basic.From}
	if got := PayerCandidates(basic); len(got) != 1 || got[0] != basic.From {
		t.Fatalf("basic candidates = %v", got)
	}
}
