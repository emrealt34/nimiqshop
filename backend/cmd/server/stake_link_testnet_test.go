package main

import (
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"testing"
	"time"

	"nimiqshop/internal/chainstake"
	"nimiqshop/internal/nimiq"
)

// testnetRPCURL is the public Nimiq testnet node. Explicit opt-in only (see the
// gate below): these tests talk to real infrastructure, create ephemeral keys
// and never touch mainnet or production secrets.
const testnetRPCURL = "https://rpc.testnet.nimiqwatch.com"

// TestLiveTestnetStakeAddressLink runs the linked-stake-wallet flow against the
// real Nimiq testnet.
//
// What is live here, and why it is the part worth testing live:
//
//   - The ownership proof. The signature is produced with the exact algorithm
//     Nimiq Hub's signMessage() uses (sha256 over "\x16Nimiq Signed Message:\n"
//   - len + message, ed25519) and checked by the production verifier, against
//     a real Nimiq address derived from the key with blake2b. Nothing is
//     stubbed, so a change to the address derivation or the digest cannot slip
//     through.
//   - The stake verdict. The chain leg asks the public testnet node whether the
//     wallet delegates to the operator's validator. A freshly generated wallet
//     has no delegation, so the honest answer is a refusal — and that refusal
//     is the assertion: the endpoint must reach a real node, read its answer
//     and NOT invent a stake. If testnet's "no staker" error shape ever
//     changes, chainstake would start reporting an outage instead of a
//     definitive no, and this test fails.
//
// The operator's POOL has no testnet instance, so that one leg is stubbed as
// unreachable — which is also what forces the decision onto the live chain.
func TestLiveTestnetStakeAddressLink(t *testing.T) {
	if os.Getenv("NIMSHOP_RUN_TESTNET") != "1" {
		t.Skip("set NIMSHOP_RUN_TESTNET=1 for real testnet verification")
	}

	account := newLinkTestWallet(t) // pays through Nimiq Pay
	staker := newLinkTestWallet(t)  // the regular wallet that would stake
	t.Logf("TESTNET ONLY: account=%s staking wallet=%s rpc=%s", account.address, staker.address, testnetRPCURL)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rpc := nimiq.NewClient(testnetRPCURL)

	// Liveness: the node answers about this exact address. Without this, a
	// "no staker" answer below could just mean "the node is broken".
	if _, err := rpc.GetAccountBalance(ctx, staker.address); err != nil {
		t.Fatalf("testnet RPC did not answer for %s: %v", staker.address, err)
	}

	// The chain verdict for a wallet that has never delegated: definitive "no",
	// not an error. This is the branch the linking endpoint depends on.
	verifier := chainstake.New(rpc, linkTestValidator, chainstake.DefaultTTL, chainstake.DefaultNegativeTTL)
	if !verifier.Ready() {
		t.Fatal("chainstake verifier is not ready")
	}
	st, err := verifier.Verify(ctx, staker.address)
	if err != nil {
		t.Fatalf("chainstake.Verify against testnet: %v", err)
	}
	if st.Staked || st.Exists {
		t.Fatalf("a freshly generated wallet must have no testnet delegation: %+v", st)
	}

	// End to end through the real HTTP handlers, with the live node behind them.
	s, _, _, auth := linkStack(t, linkStackOptions{rpcURL: testnetRPCURL})

	message, token := requestLinkChallenge(t, s, auth, staker.address)
	sigHex := staker.signMessage(message)
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		t.Fatalf("signature is not hex: %v", err)
	}

	// The server's own message verifies against the real key...
	if err := nimiq.VerifySignedMessage(staker.pub, sig, message, staker.address); err != nil {
		t.Fatalf("the challenge message the server issued does not verify: %v", err)
	}
	// ...a corrupted signature does not...
	corrupted := append([]byte(nil), sig...)
	corrupted[0] ^= 0xff
	if err := nimiq.VerifySignedMessage(staker.pub, corrupted, message, staker.address); err == nil {
		t.Fatal("a corrupted signature was accepted")
	}
	// ...and neither does the right signature presented by another wallet's key.
	if err := nimiq.VerifySignedMessage(account.pub, sig, message, staker.address); err == nil {
		t.Fatal("a signature was accepted under a different public key")
	}

	// The honest end state on testnet: ownership is proven, but there is no
	// delegation to our validator, so the shop refuses the link and says why.
	res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       sigHex,
	}, auth)
	if res.status != 422 {
		t.Fatalf("testnet link = %d, want 422 (no delegation to our validator): %s", res.status, res.body)
	}
	if me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t); me["stake_address_linked"] != false {
		t.Fatalf("a refused link was stored anyway: %v", me)
	}

	// And the account itself still earns from its own wallet, unstaked.
	if me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t); me["staked"] != false {
		t.Fatalf("the sign-in wallet must not be staked on testnet: %v", me)
	}
	t.Logf("TESTNET ONLY: signature verified, live chain refused the unstaked link as expected")
}
