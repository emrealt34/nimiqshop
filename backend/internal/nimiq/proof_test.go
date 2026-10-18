package nimiq

import (
	"encoding/hex"
	"testing"
)

// Real mainnet transactions (rpc.nimiqwatch.com, getTransactionByHash).

// 86cf31881e4e1e0901aeeec54d400dae78b3392501a5d3a1613259d9a28dba06: a 1-Luna
// "5 kinda cool" rating paid through Nimiq Pay. from = the user's HTLC
// NQ23 E6AH…, fromType 2, EarlyResolve proof signed by the Nimiq Pay co-signer
// (the HTLC recipient) and by the user (the HTLC sender).
const nimiqPayRatingProof = "010091b21f4b100273bd7034f6369c29d1f7ba72dba7de6720ad3cd8b8191621891300866a4c60f1be3f88fc1bc67b70b46ae36ca42c03606ca75e313bc07292ff344571f7dcb5acc89e23b1e7ebcf3bda3baf87c08a914cf6c149f0d14851c77c6c0900ad4272a1f4302adc01af44cd4b97d75df19717239af2886dcbe7a1cc7182f629009159304b51e62dc4f5207fbda47c6e0069f9e7dcfe5ac557c249891edb60c1b27b2da5789bcef743546bd59f3f0799341337059386d81dc6dab75c8ff4c86006"

// 2722ed384f92b7f96d3dc12e002aa953be96dcda4c422dad31eb91f535a4fa54: an
// ordinary basic-account payment, from NQ16 2SSN 82TL SMQS KXT3 Q01V CMAL NU6F 1LJG.
const basicProof = "004b93586f7555e72b218bb522d66bf52d9c38b8396bd8bfd45c99c3c282c53ba1001db46a3ad3b3049ff70abf81f48b7bde409ca483eef08d3879ae14cf9ae22b3bace85820f2c751eb3b6e113a7e3dc6a740c6bbd5d96fd18e103ad8920b895e0a"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProofSignersRecoverTheNimiqPayUserBehindTheHTLC(t *testing.T) {
	got := ProofSigners(AccountTypeHTLC, mustHex(t, nimiqPayRatingProof))
	want := []string{
		NormalizeAddress("NQ14 LU5R UH54 92SH GEN4 U63C SV4V 7N49 YYU4"), // Nimiq Pay co-signer (HTLC recipient)
		NormalizeAddress("NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD"), // the user (HTLC sender, sign-in address)
	}
	if len(got) != len(want) {
		t.Fatalf("signers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("signer %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestProofSignersOfABasicTransactionIsItsSender(t *testing.T) {
	got := ProofSigners(AccountTypeBasic, mustHex(t, basicProof))
	if len(got) != 1 || got[0] != NormalizeAddress("NQ16 2SSN 82TL SMQS KXT3 Q01V CMAL NU6F 1LJG") {
		t.Fatalf("signers = %v", got)
	}
}

func TestProofSignersFailClosed(t *testing.T) {
	full := mustHex(t, nimiqPayRatingProof)
	cases := map[string]struct {
		fromType int
		proof    []byte
	}{
		"empty":                 {AccountTypeHTLC, nil},
		"staking contract":      {AccountTypeStaking, full},
		"unknown htlc variant":  {AccountTypeHTLC, append([]byte{7}, full[1:]...)},
		"unknown key algorithm": {AccountTypeBasic, append([]byte{0x05}, full[2:]...)},
	}
	for name, c := range cases {
		if got := ProofSigners(c.fromType, c.proof); len(got) != 0 {
			t.Errorf("%s: signers = %v, want none", name, got)
		}
	}
	// A truncated EarlyResolve proof yields only the signer that is complete.
	if got := ProofSigners(AccountTypeHTLC, full[:len(full)-10]); len(got) != 1 {
		t.Errorf("truncated proof: signers = %v, want exactly the first signer", got)
	}
	// A proof with a merkle path (multi-signature account) carries no single
	// address for that key, and must not be mistaken for the account owner.
	multi := append([]byte{0x00}, full[2:34]...) // Ed25519 key
	multi = append(multi, 0x01, 0x01, 0x80)      // one merkle node: left_bits len 1, bits
	multi = append(multi, 0x01)                  // one hash follows
	multi = append(multi, make([]byte, 32)...)
	multi = append(multi, make([]byte, 64)...) // signature
	if got := ProofSigners(AccountTypeBasic, multi); len(got) != 0 {
		t.Errorf("multisig proof: signers = %v, want none", got)
	}
}
