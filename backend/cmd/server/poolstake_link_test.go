package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nimiqshop/internal/nimiq"
)

// linkTestValidator is the operator's validator in these tests. A wallet only
// counts as staked when the chain says it delegates HERE.
const linkTestValidator = "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082"

// canonAddress compares addresses the way the shop does internally: upper case,
// no spaces. Nimiq addresses are shown spaced and stored canonical, and a test
// that compares the two spellings literally fails for the wrong reason.
func canonAddress(addr string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(addr), " ", ""))
}

func mustAddress(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	addr, err := nimiq.AddressFromPublicKey(pub)
	if err != nil {
		t.Fatalf("address from public key: %v", err)
	}
	return addr
}

// linkTestWallet is a Nimiq wallet the test holds the key for, which is what
// makes the whole link flow testable end to end: the browser gets its signature
// from Nimiq Hub, the test signs the same digest itself.
type linkTestWallet struct {
	address string
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
}

func newLinkTestWallet(t *testing.T) linkTestWallet {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return linkTestWallet{address: mustAddress(t, pub), pub: pub, priv: priv}
}

// signMessage reproduces Nimiq Hub's signMessage(): sign
// sha256("\x16Nimiq Signed Message:\n" + len(message) + message) with the
// wallet's ed25519 key. This is exactly what nimiq.VerifySignedMessage checks,
// so a signature made here is indistinguishable from a real Hub one.
func (w linkTestWallet) signMessage(message string) string {
	prefixed := fmt.Sprintf("%s%d%s", "\x16Nimiq Signed Message:\n", len(message), message)
	digest := sha256.Sum256([]byte(prefixed))
	return hex.EncodeToString(ed25519.Sign(w.priv, digest[:]))
}

// linkChainStub answers getStakerByAddress per address, so a test can say "this
// wallet delegates to our validator, that one delegates elsewhere, this one has
// never staked" — the three answers the linking endpoint has to distinguish.
func linkChainStub(t *testing.T, withUs map[string]int64, elsewhere map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		addr := ""
		if len(req.Params) > 0 {
			addr = canonAddress(req.Params[0])
		}
		if req.Method != "getStakerByAddress" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		if luna, ok := withUs[addr]; ok {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"data":{"address":%q,"balance":%d,"delegation":%q,"inactiveBalance":0,"retiredBalance":0},"metadata":{"blockNumber":63301077}}}`,
				req.Params[0], luna, linkTestValidator)
			return
		}
		if validator, ok := elsewhere[addr]; ok {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"data":{"address":%q,"balance":10000000,"delegation":%q,"inactiveBalance":0,"retiredBalance":0},"metadata":{"blockNumber":63301077}}}`,
				req.Params[0], validator)
			return
		}
		// What the public RPC answers for an address that stakes nothing.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"no staker with this address in this pool"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// emptyPoolStub is the pool as production behaved during the 2026-10-04
// incident: it has never heard of any address. Every stake answer in these
// tests therefore comes from the chain leg, which is the leg that matters.
func emptyPoolStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no staker with this address in this pool"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// linkStackOptions shapes what the chain says about the staking wallet.
type linkStackOptions struct {
	// stakerDelegation, when set, makes the chain report the staking wallet as
	// delegated to THAT validator instead of ours — the "staked, but not with
	// us" case the linking endpoint has to refuse by name.
	stakerDelegation string
	// stakerStakeLuna overrides the delegation size (default 100 NIM).
	stakerStakeLuna int64
	// rpcURL replaces the chain stub with a real node — the testnet test points
	// it at the public testnet RPC so the stake verdict is live.
	rpcURL string
}

// linkStack boots the shop with the chain stub as its only stake source and
// signs a buyer in through the Nimiq Pay wallet. It returns the stack, the
// account wallet, the staking wallet and the account's auth headers.
func linkStack(t *testing.T, opts linkStackOptions) (*testStack, *linkTestWallet, *linkTestWallet, map[string]string) {
	t.Helper()
	account := newLinkTestWallet(t) // checks out through Nimiq Pay
	staker := newLinkTestWallet(t)  // the regular wallet that actually stakes
	withUs := map[string]int64{}
	elsewhere := map[string]string{}
	if opts.stakerDelegation != "" {
		elsewhere[canonAddress(staker.address)] = opts.stakerDelegation
	} else {
		luna := opts.stakerStakeLuna
		if luna == 0 {
			luna = 10_000_000 // 100 NIM
		}
		withUs[canonAddress(staker.address)] = luna
	}
	rpcURL := opts.rpcURL
	if rpcURL == "" {
		rpcURL = linkChainStub(t, withUs, elsewhere).URL
	}
	pool := emptyPoolStub(t)
	s := bootStack(t, stackOptions{env: map[string]string{
		"POOL_API_URL":                 pool.URL,
		"POOL_VALIDATOR_ADDRESS":       linkTestValidator,
		"POOL_FEED_API_KEY":            "test-feed-key",
		"NIMIQ_RPC_URL":                rpcURL,
		"POOL_STAKE_CACHE_TTL_SECONDS": "1",
	}})
	auth, _ := s.signInAs(account.pub, account.priv)
	return s, &account, &staker, auth
}

// requestLinkChallenge performs step 1 and returns the message + token.
func requestLinkChallenge(t *testing.T, s *testStack, auth map[string]string, stakeAddress string) (string, string) {
	t.Helper()
	res := s.do(http.MethodPost, "/api/poolstake/stake-address/challenge",
		map[string]any{"stake_address": stakeAddress}, auth)
	if res.status != 200 {
		t.Fatalf("challenge: %d %s", res.status, res.body)
	}
	body := res.json(t)
	message, _ := body["message"].(string)
	token, _ := body["challenge_token"].(string)
	if message == "" || token == "" {
		t.Fatalf("challenge returned no message/token: %s", res.body)
	}
	return message, token
}

// TestStakeAddressLinkMovesTheEarningWallet is the customer's exact scenario:
// stake from a regular wallet, shop through Nimiq Pay, and have the shop
// recognise the stake. Before the feature the two wallets were two different
// accounts and the buyer earned the base rate with no way to fix it.
func TestStakeAddressLinkMovesTheEarningWallet(t *testing.T) {
	s, account, staker, auth := linkStack(t, linkStackOptions{})

	// Before linking: signed in as the Nimiq Pay wallet, which has no stake.
	me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth)
	if me.status != 200 {
		t.Fatalf("poolstake/me: %d %s", me.status, me.body)
	}
	body := me.json(t)
	if body["staked"] != false {
		t.Fatalf("the sign-in wallet must not be staked: %s", me.body)
	}
	if canonAddress(str(body["account_address"])) != canonAddress(account.address) {
		t.Fatalf("account_address = %v, want %s", body["account_address"], account.address)
	}
	if body["stake_address_linked"] != false {
		t.Fatalf("a fresh account reports a linked wallet: %s", me.body)
	}

	// Step 1: ask for a challenge naming the wallet that stakes.
	message, token := requestLinkChallenge(t, s, auth, staker.address)
	// The message must bind BOTH wallets and the nonce, so it cannot be reused
	// for another account or another wallet.
	for _, want := range []string{canonAddress(account.address), canonAddress(staker.address)} {
		if !strings.Contains(canonAddress(message), want) {
			t.Fatalf("challenge message does not name %s: %q", want, message)
		}
	}
	if strings.Contains(message, " login: ") {
		t.Fatalf("the link message must not look like a login message: %q", message)
	}

	// Step 2: prove ownership with the staking wallet's key.
	res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(message),
	}, auth)
	if res.status != 200 {
		t.Fatalf("link: %d %s", res.status, res.body)
	}
	linked := res.json(t)
	if linked["linked"] != true {
		t.Fatalf("linked = %v, want true (%s)", linked["linked"], res.body)
	}
	if canonAddress(str(linked["effective_stake_address"])) != canonAddress(staker.address) {
		t.Fatalf("effective_stake_address = %v, want %s", linked["effective_stake_address"], staker.address)
	}
	if canonAddress(str(linked["account_address"])) != canonAddress(account.address) {
		t.Fatalf("account_address = %v, want %s (linking must not change the sign-in wallet)", linked["account_address"], account.address)
	}

	// The refreshed standing inside the same response, and on a fresh read.
	for _, view := range []map[string]any{linked["me"].(map[string]any), s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)} {
		if view["staked"] != true {
			t.Fatalf("after linking the buyer must be staked: %v", view)
		}
		if canonAddress(str(view["address"])) != canonAddress(staker.address) {
			t.Fatalf("the earning address = %v, want the linked wallet %s", view["address"], staker.address)
		}
		if canonAddress(str(view["account_address"])) != canonAddress(account.address) {
			t.Fatalf("account_address = %v, want %s", view["account_address"], account.address)
		}
		if view["stake_address_linked"] != true {
			t.Fatalf("stake_address_linked = %v, want true", view["stake_address_linked"])
		}
		if num(view["stake_nim"]) != 100 {
			t.Fatalf("stake_nim = %v, want 100 (the linked wallet's delegation)", view["stake_nim"])
		}
		if num(view["cashback_bps"]) <= 0 {
			t.Fatalf("cashback_bps = %v, want the staker rate — the linked stake was not honoured", view["cashback_bps"])
		}
	}

	// The chain-only position endpoint reports the same wallet split.
	pos := s.do(http.MethodGet, "/api/poolstake/position", nil, auth)
	if pos.status != 200 {
		t.Fatalf("position: %d %s", pos.status, pos.body)
	}
	pj := pos.json(t)
	if canonAddress(str(pj["address"])) != canonAddress(staker.address) {
		t.Fatalf("position address = %v, want the linked wallet", pj["address"])
	}
	if canonAddress(str(pj["account_address"])) != canonAddress(account.address) {
		t.Fatalf("position account_address = %v, want the sign-in wallet", pj["account_address"])
	}
	if pj["stake_address_linked"] != true {
		t.Fatalf("position stake_address_linked = %v, want true", pj["stake_address_linked"])
	}

	// Unlinking puts the account back on its own wallet — and must not error.
	del := s.do(http.MethodDelete, "/api/poolstake/stake-address", nil, auth)
	if del.status != 200 {
		t.Fatalf("unlink: %d %s", del.status, del.body)
	}
	if del.json(t)["linked"] != false {
		t.Fatalf("unlink reported linked = %v (%s)", del.json(t)["linked"], del.body)
	}
	after := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)
	if after["staked"] != false || after["stake_address_linked"] != false {
		t.Fatalf("after unlinking the account must be back on its own (unstaked) wallet: %v", after)
	}
	if canonAddress(str(after["address"])) != canonAddress(account.address) {
		t.Fatalf("after unlinking the earning address = %v, want %s", after["address"], account.address)
	}
	// A second unlink has nothing to clear.
	if again := s.do(http.MethodDelete, "/api/poolstake/stake-address", nil, auth); again.status != 409 {
		t.Fatalf("second unlink = %d, want 409 (%s)", again.status, again.body)
	}
}

// TestStakeAddressLinkRejectsABorrowedChallenge proves the purpose binding in
// both directions: a login challenge cannot be spent on linking, and a link
// challenge cannot be spent on signing in. Without it, any signature a buyer
// ever made to log in would also authorise a stake-wallet link.
func TestStakeAddressLinkRejectsABorrowedChallenge(t *testing.T) {
	s, _, staker, auth := linkStack(t, linkStackOptions{})

	// A login challenge, signed by the staking wallet, presented to the link
	// endpoint.
	login := s.do(http.MethodPost, "/api/auth/challenge", map[string]any{}, nil)
	if login.status != 200 {
		t.Fatalf("login challenge: %d %s", login.status, login.body)
	}
	lj := login.json(t)
	loginMessage, _ := lj["message"].(string)
	loginToken, _ := lj["challenge_token"].(string)

	res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": loginToken,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(loginMessage),
	}, auth)
	if res.status != 401 {
		t.Fatalf("a login challenge spent on linking = %d, want 401 (%s)", res.status, res.body)
	}

	// And the mirror image: a link challenge presented to hub-login.
	message, token := requestLinkChallenge(t, s, auth, staker.address)
	in := s.do(http.MethodPost, "/api/auth/hub-login", map[string]any{
		"challenge_token": token,
		"address":         staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(message),
	}, map[string]string{"X-Token-Delivery": "bearer"})
	if in.status != 401 {
		t.Fatalf("a link challenge spent on login = %d, want 401 (%s)", in.status, in.body)
	}

	// The link challenge is still perfectly usable for its own purpose.
	res = s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(message),
	}, auth)
	if res.status != 200 {
		t.Fatalf("the same challenge for its own flow = %d, want 200 (%s)", res.status, res.body)
	}
}

// TestStakeAddressLinkRejectsASignatureFromAnotherWallet is the ownership
// guarantee: knowing an address is not enough, and a signature from a different
// key must not link it.
func TestStakeAddressLinkRejectsASignatureFromAnotherWallet(t *testing.T) {
	s, _, staker, auth := linkStack(t, linkStackOptions{})
	impostor := newLinkTestWallet(t)

	message, token := requestLinkChallenge(t, s, auth, staker.address)

	// Right message, wrong key: the signature is valid but the public key does
	// not derive to the claimed address.
	res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(impostor.pub),
		"signature":       impostor.signMessage(message),
	}, auth)
	if res.status != 401 {
		t.Fatalf("a signature from another wallet = %d, want 401 (%s)", res.status, res.body)
	}

	// Right key, wrong message: one character of the nonce changed, which is
	// what a replay against a slightly different challenge would look like.
	tampered := message[:len(message)-1] + string(rune(message[len(message)-1]^0x01))
	res = s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(tampered),
	}, auth)
	if res.status != 401 {
		t.Fatalf("a signature over a different message = %d, want 401 (%s)", res.status, res.body)
	}

	// Nothing was linked by either attempt.
	if me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t); me["stake_address_linked"] != false {
		t.Fatalf("a rejected attempt still linked a wallet: %v", me)
	}
}

// TestStakeAddressLinkRefusesAWalletThatDoesNotStakeWithUs keeps the promise
// honest: linking a wallet earns nothing unless it delegates to our validator,
// so the endpoint says so instead of storing a link that silently pays base.
func TestStakeAddressLinkRefusesAWalletThatDoesNotStakeWithUs(t *testing.T) {
	const foreignValidator = "NQ86 0YQ3 V4XT VVH8 GD1A 3K0Q T2P4 5RLM J1CV"

	// Staked, but with somebody else's validator.
	s, _, staker, auth := linkStack(t, linkStackOptions{stakerDelegation: foreignValidator})
	message, token := requestLinkChallenge(t, s, auth, staker.address)
	res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(message),
	}, auth)
	if res.status != 422 {
		t.Fatalf("a wallet staked elsewhere = %d, want 422 (%s)", res.status, res.body)
	}
	body := res.json(t)
	if canonAddress(str(body["other_validator"])) != canonAddress(foreignValidator) {
		t.Fatalf("other_validator = %v, want %s (the buyer has to be told where the stake is)", body["other_validator"], foreignValidator)
	}
	if me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t); me["stake_address_linked"] != false {
		t.Fatalf("a refused link was stored anyway: %v", me)
	}

	// Not staked at all: a wallet nobody has ever delegated from.
	s2, _, _, auth2 := linkStack(t, linkStackOptions{})
	fresh := newLinkTestWallet(t)
	message2, token2 := requestLinkChallenge(t, s2, auth2, fresh.address)
	res2 := s2.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token2,
		"stake_address":   fresh.address,
		"public_key":      hex.EncodeToString(fresh.pub),
		"signature":       fresh.signMessage(message2),
	}, auth2)
	if res2.status != 422 {
		t.Fatalf("a wallet with no stake = %d, want 422 (%s)", res2.status, res2.body)
	}
	if str(res2.json(t)["other_validator"]) != "" {
		t.Fatalf("an unstaked wallet must not name a foreign validator: %s", res2.body)
	}
}

// TestStakeAddressLinkIsOneAccountPerWallet stops two accounts from claiming
// the same delegation, which would let one stake pay two buyers' boosts.
func TestStakeAddressLinkIsOneAccountPerWallet(t *testing.T) {
	s, _, staker, auth := linkStack(t, linkStackOptions{})
	message, token := requestLinkChallenge(t, s, auth, staker.address)
	if res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(message),
	}, auth); res.status != 200 {
		t.Fatalf("first link: %d %s", res.status, res.body)
	}

	// A second, genuinely different account tries to link the same wallet. It
	// can produce a perfectly valid signature for it (the same person could own
	// both wallets), so what has to stop it is the rule that one delegation
	// feeds exactly one account — not the ownership proof.
	rival := newLinkTestWallet(t)
	otherAuth, _ := s.signInAs(rival.pub, rival.priv)
	message2, token2 := requestLinkChallenge(t, s, otherAuth, staker.address)
	res := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token2,
		"stake_address":   staker.address,
		"public_key":      hex.EncodeToString(staker.pub),
		"signature":       staker.signMessage(message2),
	}, otherAuth)
	// Signing in with the staking wallet makes it that account's OWN address,
	// which is a different (and also rejected) case: no link is needed.
	if res.status != 409 {
		t.Fatalf("a second account claiming the same wallet = %d, want 409 (%s)", res.status, res.body)
	}
	if !strings.Contains(string(res.body), "another account") {
		t.Fatalf("the refusal should say the wallet is linked elsewhere: %s", res.body)
	}
	// The first account keeps its link.
	if me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t); me["stake_address_linked"] != true {
		t.Fatalf("the refused attempt disturbed the existing link: %v", me)
	}
}

// TestStakeAddressEndpointsRequireASession: linking changes what a buyer earns,
// so none of it is reachable anonymously.
func TestStakeAddressEndpointsRequireASession(t *testing.T) {
	s, _, staker, _ := linkStack(t, linkStackOptions{})

	if res := s.do(http.MethodPost, "/api/poolstake/stake-address/challenge",
		map[string]any{"stake_address": staker.address}, nil); res.status != 401 {
		t.Fatalf("anonymous challenge = %d, want 401", res.status)
	}
	if res := s.do(http.MethodPost, "/api/poolstake/stake-address",
		map[string]any{"stake_address": staker.address}, nil); res.status != 401 {
		t.Fatalf("anonymous link = %d, want 401", res.status)
	}
	if res := s.do(http.MethodDelete, "/api/poolstake/stake-address", nil, nil); res.status != 401 {
		t.Fatalf("anonymous unlink = %d, want 401", res.status)
	}
}

// TestStakeAddressChallengeRejectsNonsense guards the input edge cases: no
// address, a malformed address, and the wallet the account already signs in
// with (which needs no link and must not create one).
func TestStakeAddressChallengeRejectsNonsense(t *testing.T) {
	s, account, _, auth := linkStack(t, linkStackOptions{})

	if res := s.do(http.MethodPost, "/api/poolstake/stake-address/challenge", map[string]any{}, auth); res.status != 400 {
		t.Fatalf("missing stake_address = %d, want 400 (%s)", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/poolstake/stake-address/challenge",
		map[string]any{"stake_address": "NQ00 NOT VALID"}, auth); res.status != 400 {
		t.Fatalf("malformed stake_address = %d, want 400 (%s)", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/poolstake/stake-address/challenge",
		map[string]any{"stake_address": account.address}, auth); res.status != 409 {
		t.Fatalf("linking the sign-in wallet = %d, want 409 (%s)", res.status, res.body)
	}
}
