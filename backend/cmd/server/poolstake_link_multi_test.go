package main

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

/*
poolstake_link_multi_test.go — several staking wallets on ONE account.

A buyer's stake rarely lives in a single wallet: a hardware wallet holds the
bulk, a phone wallet holds an old delegation, and the wallet they shop through
Nimiq Pay with may hold nothing at all. Every wallet they prove ownership of
therefore earns for the account, and these tests pin the two rules that make
that safe:

	stakes and ledger boosts ADD UP  (300 NIM split three ways earns what
	                                  300 NIM in one wallet earns)
	the staker BASE is counted ONCE  (splitting a stake must not multiply the
	                                  shop's payout, or buyers would be told
	                                  to split wallets to earn more)

plus the operational edges: the per-account cap, removing one wallet while the
others keep earning, and clearing all of them.
*/

// multiLinkStack boots the shop with a chain stub that knows about SEVERAL
// staking wallets and signs a buyer in through the Nimiq Pay wallet (which holds
// no stake, exactly like the customer who reported the confusion).
func multiLinkStack(t *testing.T, stakesLuna []int64) (*testStack, *linkTestWallet, []linkTestWallet, map[string]string) {
	t.Helper()
	account := newLinkTestWallet(t) // checks out through Nimiq Pay
	stakers := make([]linkTestWallet, 0, len(stakesLuna))
	withUs := make(map[string]int64, len(stakesLuna))
	for _, luna := range stakesLuna {
		wallet := newLinkTestWallet(t)
		withUs[canonAddress(wallet.address)] = luna
		stakers = append(stakers, wallet)
	}
	pool := emptyPoolStub(t)
	s := bootStack(t, stackOptions{env: map[string]string{
		"POOL_API_URL":                 pool.URL,
		"POOL_VALIDATOR_ADDRESS":       linkTestValidator,
		"POOL_FEED_API_KEY":            "test-feed-key",
		"NIMIQ_RPC_URL":                linkChainStub(t, withUs, nil).URL,
		"POOL_STAKE_CACHE_TTL_SECONDS": "1",
	}})
	auth, _ := s.signInAs(account.pub, account.priv)
	return s, &account, stakers, auth
}

// linkWallet runs the whole two-step flow (challenge → signature → link) with
// the wallet's own key and returns the response.
func linkWallet(t *testing.T, s *testStack, auth map[string]string, wallet linkTestWallet) resp {
	t.Helper()
	message, token := requestLinkChallenge(t, s, auth, wallet.address)
	return s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   wallet.address,
		"public_key":      hex.EncodeToString(wallet.pub),
		"signature":       wallet.signMessage(message),
	}, auth)
}

// mustLink links a wallet and fails the test with the server's own words if the
// shop refuses.
func mustLink(t *testing.T, s *testStack, auth map[string]string, wallet linkTestWallet) map[string]any {
	t.Helper()
	res := linkWallet(t, s, auth, wallet)
	if res.status != 200 {
		t.Fatalf("link %s: %d %s", wallet.address, res.status, res.body)
	}
	return res.json(t)
}

// unlinkURL builds the DELETE target for one wallet. Nimiq addresses are shown
// spaced, and the buyer-facing UI passes that spelling straight through, so the
// query has to be escaped — an unescaped space is a malformed request line, not
// a malformed address, and testing the wrong failure proves nothing.
func unlinkURL(address string) string {
	return "/api/poolstake/stake-address?address=" + url.QueryEscape(address)
}

// stakeAddressRows reads the per-wallet breakdown out of a /poolstake/me view.
func stakeAddressRows(t *testing.T, view map[string]any) []map[string]any {
	t.Helper()
	raw, ok := view["stake_addresses"].([]any)
	if !ok {
		t.Fatalf("stake_addresses missing or not a list: %v", view["stake_addresses"])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		row, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("stake_addresses entry is not an object: %v", entry)
		}
		rows = append(rows, row)
	}
	return rows
}

// TestSeveralStakeWalletsSumIntoOneRate is the buyer-visible promise: link three
// wallets holding 100, 150 and 50 NIM and the shop reports 300 NIM of stake, one
// staker rate, and a per-wallet breakdown that adds up to it.
func TestSeveralStakeWalletsSumIntoOneRate(t *testing.T) {
	s, account, stakers, auth := multiLinkStack(t, []int64{10_000_000, 15_000_000, 5_000_000})

	// Before any link the sign-in wallet has no stake at all — this is exactly
	// the customer's complaint: his stake is invisible to the account he shops
	// with.
	before := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)
	if before["staked"] != false || num(before["stake_nim"]) != 0 {
		t.Fatalf("before linking: staked = %v stake_nim = %v, want no stake", before["staked"], before["stake_nim"])
	}
	baseBefore := num(before["base_bps"])

	for i, wallet := range stakers {
		linked := mustLink(t, s, auth, wallet)
		if got := int(num(linked["linked_count"])); got != i+1 {
			t.Fatalf("linked_count = %d after linking wallet %d, want %d", got, i+1, i+1)
		}
	}

	me := s.do(http.MethodGet, "/api/poolstake/me", nil, auth)
	if me.status != 200 {
		t.Fatalf("poolstake/me: %d %s", me.status, me.body)
	}
	view := me.json(t)

	if view["staked"] != true {
		t.Fatalf("three staking wallets linked but staked = %v (%s)", view["staked"], me.body)
	}
	if got := num(view["stake_nim"]); got != 300 {
		t.Fatalf("stake_nim = %v, want 300 (100 + 150 + 50 summed across the linked wallets)", got)
	}
	// THE base rule: the staker base is applied once per account. It may rise
	// from the non-staker base to the staker base, but it must never be a
	// multiple of it because three wallets are linked.
	stakerBase := num(view["staker_base_bps"])
	if got := num(view["base_bps"]); got != stakerBase {
		t.Fatalf("base_bps = %v, want the staker base %v counted ONCE (three wallets must not triple it)", got, stakerBase)
	}
	if stakerBase <= baseBefore {
		t.Fatalf("the staker base %v did not beat the non-staker base %v; the test cannot prove anything",
			stakerBase, baseBefore)
	}
	if got, want := num(view["cashback_bps"]), stakerBase+num(view["boost_bps"]); got != want {
		t.Fatalf("cashback_bps = %v, want base %v + boost %v = %v", got, stakerBase, num(view["boost_bps"]), want)
	}
	if canonAddress(str(view["account_address"])) != canonAddress(account.address) {
		t.Fatalf("account_address = %v, want the sign-in wallet %s (linking never moves the payout)",
			view["account_address"], account.address)
	}
	if canonAddress(str(view["address"])) != canonAddress(stakers[0].address) {
		t.Fatalf("the named earning wallet = %v, want the first linked wallet %s", view["address"], stakers[0].address)
	}
	if got := int(num(view["linked_wallet_limit"])); got <= 0 {
		t.Fatalf("linked_wallet_limit = %v, want the published cap", view["linked_wallet_limit"])
	}

	// The per-wallet breakdown: the sign-in wallet plus one row per link, each
	// with its own stake, summing to the total the rate was computed from.
	rows := stakeAddressRows(t, view)
	if len(rows) != len(stakers)+1 {
		t.Fatalf("stake_addresses has %d rows, want %d (sign-in wallet + %d links)", len(rows), len(stakers)+1, len(stakers))
	}
	var sum float64
	linkedRows := 0
	for _, row := range rows {
		sum += num(row["stake_nim"])
		if row["linked"] == true {
			linkedRows++
			if _, ok := row["linked_at"]; !ok {
				t.Fatalf("a linked wallet reports no linked_at: %v", row)
			}
		}
	}
	if linkedRows != len(stakers) {
		t.Fatalf("%d rows are marked linked, want %d", linkedRows, len(stakers))
	}
	if sum != num(view["stake_nim"]) {
		t.Fatalf("the per-wallet stakes sum to %v but the total says %v", sum, view["stake_nim"])
	}
	// The earning set the shop itself uses must be the same four wallets.
	earning, _ := view["earning_addresses"].([]any)
	if len(earning) != len(stakers)+1 {
		t.Fatalf("earning_addresses = %v, want %d wallets", earning, len(stakers)+1)
	}

	// The chain-only position endpoint sums the same wallets, because that total
	// is what the rate is computed from — a card showing one wallet of four
	// would contradict the shop's own math.
	pos := s.do(http.MethodGet, "/api/poolstake/position", nil, auth)
	if pos.status != 200 {
		t.Fatalf("position: %d %s", pos.status, pos.body)
	}
	pj := pos.json(t)
	if got := num(pj["active_nim"]); got != 300 {
		t.Fatalf("position active_nim = %v, want 300 summed over every earning wallet", got)
	}
	positions, _ := pj["positions"].([]any)
	if len(positions) != len(stakers)+1 {
		t.Fatalf("position rows = %d, want %d", len(positions), len(stakers)+1)
	}
}

// TestSplitStakePaysTheSameAsOneWallet is the HTTP-level form of the "base once"
// rule, and the one that protects the shop's money: 300 NIM in a single wallet
// and 300 NIM split over three wallets must produce the SAME rate on the same
// account shape. If the base were applied per wallet, splitting a stake would
// triple the payout for identical stake.
func TestSplitStakePaysTheSameAsOneWallet(t *testing.T) {
	oneStack, _, oneStakers, oneAuth := multiLinkStack(t, []int64{30_000_000})
	mustLink(t, oneStack, oneAuth, oneStakers[0])
	one := oneStack.do(http.MethodGet, "/api/poolstake/me", nil, oneAuth).json(t)

	splitStack, _, splitStakers, splitAuth := multiLinkStack(t, []int64{10_000_000, 10_000_000, 10_000_000})
	for _, wallet := range splitStakers {
		mustLink(t, splitStack, splitAuth, wallet)
	}
	split := splitStack.do(http.MethodGet, "/api/poolstake/me", nil, splitAuth).json(t)

	if num(one["stake_nim"]) != num(split["stake_nim"]) {
		t.Fatalf("stake_nim = %v for one wallet vs %v for three: the split stake was not summed",
			one["stake_nim"], split["stake_nim"])
	}
	if num(split["stake_nim"]) != 300 {
		t.Fatalf("stake_nim = %v, want 300 either way", split["stake_nim"])
	}
	if num(one["base_bps"]) != num(split["base_bps"]) {
		t.Fatalf("base_bps = %v for one wallet but %v for three wallets holding the same total: the base is not counted once",
			one["base_bps"], split["base_bps"])
	}
	if num(one["cashback_bps"]) != num(split["cashback_bps"]) {
		t.Fatalf("cashback_bps = %v vs %v for the same total stake: splitting wallets must not change the rate",
			one["cashback_bps"], split["cashback_bps"])
	}
	if num(one["cashback_bps"]) <= 0 {
		t.Fatalf("cashback_bps = %v, want a real staker rate", one["cashback_bps"])
	}
}

// TestStakeWalletLimitIsEnforcedBeforeASignature covers the cap: five linked
// wallets are allowed, the sixth is refused — and refused at the CHALLENGE step,
// because making a buyer sign in their wallet only to tell them no is the worst
// place to discover a limit they can act on.
func TestStakeWalletLimitIsEnforcedBeforeASignature(t *testing.T) {
	stakes := make([]int64, 0, 6)
	for i := 0; i < 6; i++ {
		stakes = append(stakes, int64((i+1)*10_000_000))
	}
	s, _, stakers, auth := multiLinkStack(t, stakes)

	for i := 0; i < 5; i++ {
		linked := mustLink(t, s, auth, stakers[i])
		if got := int(num(linked["linked_count"])); got != i+1 {
			t.Fatalf("linked_count = %d, want %d", got, i+1)
		}
	}

	// The sixth wallet: the challenge itself must refuse, naming the limit.
	res := s.do(http.MethodPost, "/api/poolstake/stake-address/challenge",
		map[string]any{"stake_address": stakers[5].address}, auth)
	if res.status != 409 {
		t.Fatalf("challenge at the cap = %d, want 409 (%s)", res.status, res.body)
	}
	if !strings.Contains(strings.ToLower(string(res.body)), "maximum") {
		t.Fatalf("the refusal does not name the limit: %s", res.body)
	}

	// Re-linking a wallet that is ALREADY on the list stays allowed at the cap:
	// a refresh or a double click must not fail the buyer.
	message, token := requestLinkChallenge(t, s, auth, stakers[0].address)
	again := s.do(http.MethodPost, "/api/poolstake/stake-address", map[string]any{
		"challenge_token": token,
		"stake_address":   stakers[0].address,
		"public_key":      hex.EncodeToString(stakers[0].pub),
		"signature":       stakers[0].signMessage(message),
	}, auth)
	if again.status != 200 {
		t.Fatalf("re-linking an already linked wallet at the cap = %d, want 200 (%s)", again.status, again.body)
	}
	if got := int(num(again.json(t)["linked_count"])); got != 5 {
		t.Fatalf("linked_count = %d after a duplicate link, want 5 (no slot consumed)", got)
	}

	// Removing one frees a slot, and the sixth wallet then links normally.
	del := s.do(http.MethodDelete, unlinkURL(stakers[4].address), nil, auth)
	if del.status != 200 {
		t.Fatalf("unlink one at the cap: %d %s", del.status, del.body)
	}
	sixth := mustLink(t, s, auth, stakers[5])
	if got := int(num(sixth["linked_count"])); got != 5 {
		t.Fatalf("linked_count = %d after freeing a slot and linking the sixth wallet, want 5", got)
	}
}

// TestUnlinkOneStakeWalletKeepsTheOthers is the per-wallet control the buyer
// needs: dropping one wallet must leave the rest earning (and the total must
// fall by exactly that wallet's stake), dropping an unknown wallet must say so
// rather than claim success, and dropping everything must put the account back
// on its own wallet.
func TestUnlinkOneStakeWalletKeepsTheOthers(t *testing.T) {
	s, account, stakers, auth := multiLinkStack(t, []int64{10_000_000, 15_000_000, 5_000_000})
	for _, wallet := range stakers {
		mustLink(t, s, auth, wallet)
	}

	// Remove the middle wallet: 300 NIM becomes 150 NIM, two links remain.
	// The spaced, escaped spelling a real UI sends.
	del := s.do(http.MethodDelete, unlinkURL(stakers[1].address), nil, auth)
	if del.status != 200 {
		t.Fatalf("unlink one: %d %s", del.status, del.body)
	}
	body := del.json(t)
	if got := int(num(body["linked_count"])); got != 2 {
		t.Fatalf("linked_count = %d after removing one of three, want 2", got)
	}
	if body["linked"] != true {
		t.Fatalf("linked = %v, want true: two wallets still earn", body["linked"])
	}
	view := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)
	if got := num(view["stake_nim"]); got != 150 {
		t.Fatalf("stake_nim = %v after removing the 150 NIM wallet, want 150 (100 + 50)", got)
	}
	if view["staked"] != true {
		t.Fatal("removing one wallet must not make the buyer a non-staker")
	}
	for _, row := range stakeAddressRows(t, view) {
		if canonAddress(str(row["address"])) == canonAddress(stakers[1].address) {
			t.Fatalf("the removed wallet still earns: %v", row)
		}
	}

	// Removing a wallet that is not linked must not report a success this
	// request did not cause (a stale page can offer exactly that).
	unknown := newLinkTestWallet(t)
	if res := s.do(http.MethodDelete, unlinkURL(unknown.address), nil, auth); res.status != 409 {
		t.Fatalf("unlinking a wallet that is not linked = %d, want 409 (%s)", res.status, res.body)
	}
	if res := s.do(http.MethodDelete, unlinkURL("NQ00 NOT AN ADDRESS"), nil, auth); res.status != 400 {
		t.Fatalf("unlinking a malformed address = %d, want 400 (%s)", res.status, res.body)
	}
	// The canonical (unspaced) spelling must work too: the backend stores that
	// form, and a caller that reads it back out of a response should not have to
	// re-space it first.
	if res := s.do(http.MethodDelete, unlinkURL(canonAddress(stakers[2].address)), nil, auth); res.status != 200 {
		t.Fatalf("unlinking with the canonical spelling = %d, want 200 (%s)", res.status, res.body)
	}

	// Removing everything: the account earns from its own wallet again, and the
	// payout wallet never changed at any point.
	all := s.do(http.MethodDelete, "/api/poolstake/stake-address", nil, auth)
	if all.status != 200 {
		t.Fatalf("unlink all: %d %s", all.status, all.body)
	}
	cleared := all.json(t)
	if cleared["linked"] != false || int(num(cleared["linked_count"])) != 0 {
		t.Fatalf("after clearing: linked = %v count = %v (%s)", cleared["linked"], cleared["linked_count"], all.body)
	}
	after := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)
	if after["staked"] != false || num(after["stake_nim"]) != 0 {
		t.Fatalf("after clearing: staked = %v stake_nim = %v, want the sign-in wallet's own (empty) standing",
			after["staked"], after["stake_nim"])
	}
	if canonAddress(str(after["address"])) != canonAddress(account.address) {
		t.Fatalf("after clearing the earning wallet = %v, want the sign-in wallet %s", after["address"], account.address)
	}
	if canonAddress(str(after["account_address"])) != canonAddress(account.address) {
		t.Fatalf("account_address = %v, want %s — no link may ever move the payout wallet",
			after["account_address"], account.address)
	}
	if res := s.do(http.MethodDelete, "/api/poolstake/stake-address", nil, auth); res.status != 409 {
		t.Fatalf("a second clear = %d, want 409 (%s)", res.status, res.body)
	}
}

// TestOneStakeWalletStillServesOneAccount keeps the uniqueness rule honest now
// that an account may link several wallets: a wallet that already earns for
// somebody else cannot be claimed by a second account, however many slots that
// account has left. Without it two buyers could both collect cashback from one
// delegation.
func TestOneStakeWalletStillServesOneAccount(t *testing.T) {
	s, _, stakers, auth := multiLinkStack(t, []int64{10_000_000, 15_000_000})
	mustLink(t, s, auth, stakers[0])

	// A second buyer signs in with their own wallet and tries to claim the same
	// staking wallet.
	other := newLinkTestWallet(t)
	otherAuth, _ := s.signInAs(other.pub, other.priv)
	res := linkWallet(t, s, otherAuth, stakers[0])
	if res.status != 409 {
		t.Fatalf("a second account claiming a linked wallet = %d, want 409 (%s)", res.status, res.body)
	}
	if !strings.Contains(strings.ToLower(string(res.body)), "another account") {
		t.Fatalf("the refusal does not say why: %s", res.body)
	}

	// The second buyer can still link a wallet nobody else claimed.
	mustLink(t, s, otherAuth, stakers[1])
	view := s.do(http.MethodGet, "/api/poolstake/me", nil, otherAuth).json(t)
	if got := num(view["stake_nim"]); got != 150 {
		t.Fatalf("the second account's stake_nim = %v, want 150 from its own single link", got)
	}
	// And the first account's total is untouched by any of it.
	first := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)
	if got := num(first["stake_nim"]); got != 100 {
		t.Fatalf("the first account's stake_nim = %v, want 100 (its own wallet only)", got)
	}
}
