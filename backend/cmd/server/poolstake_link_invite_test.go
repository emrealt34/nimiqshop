package main

import (
	"encoding/hex"
	"net/http"
	"testing"

	"nimiqshop/internal/db"
)

// TestStakeAddressInviteLinksPhonePayAndPCHubSeamlessly tests the exact
// cross-device flow: Device 1 (e.g. PC signed in as `account`) creates an
// account-bound pairing link (?stake_link=<token>); Device 2 (e.g. Phone Nimiq
// Pay signed into `staker` — a DIFFERENT shop account — or completely signed
// out) opens the link, verifies the target account, signs the challenge with
// `staker`, and claims it directly onto Device 1's account without sharing
// session cookies.
func TestStakeAddressInviteLinksPhonePayAndPCHubSeamlessly(t *testing.T) {
	account := newLinkTestWallet(t)   // PC account (0 NIM staked)
	payStaker := newLinkTestWallet(t) // Phone Nimiq Pay wallet (500 NIM staked)
	hubStaker := newLinkTestWallet(t) // Second staking wallet (300 NIM staked)
	unstaked := newLinkTestWallet(t)  // Wallet with 0 stake

	rpc := linkChainStub(t, map[string]int64{
		canonAddress(payStaker.address): 500_00000,
		canonAddress(hubStaker.address): 300_00000,
	}, nil)
	pool := emptyPoolStub(t)
	s := bootStack(t, stackOptions{env: map[string]string{
		"POOL_API_URL":                 pool.URL,
		"POOL_VALIDATOR_ADDRESS":       linkTestValidator,
		"POOL_FEED_API_KEY":            "test-feed-key",
		"NIMIQ_RPC_URL":                rpc.URL,
		"POOL_STAKE_CACHE_TTL_SECONDS": "1",
	}})

	pcAuth, _ := s.signInAs(account.pub, account.priv)
	// Device 2 is ALSO signed in as its own wallet (payStaker) inside Nimiq Pay!
	phoneAuth, _ := s.signInAs(payStaker.pub, payStaker.priv)

	// Step 1: Device 1 creates an account-bound invite token.
	invRes := s.do(http.MethodPost, "/api/poolstake/stake-address/invite", map[string]any{}, pcAuth)
	if invRes.status != 200 {
		t.Fatalf("invite create: %d %s", invRes.status, invRes.body)
	}
	invBody := invRes.json(t)
	inviteToken := str(invBody["invite_token"])
	if inviteToken == "" {
		t.Fatalf("missing invite_token: %s", invRes.body)
	}
	if canonAddress(str(invBody["account_address"])) != canonAddress(account.address) {
		t.Fatalf("account_address = %v, want %s", invBody["account_address"], account.address)
	}

	// Step 2: Device 2 inspects the invite token (even while signed in as payStaker!).
	infoRes := s.do(http.MethodGet, "/api/poolstake/stake-address/invite?token="+inviteToken, nil, phoneAuth)
	if infoRes.status != 200 {
		t.Fatalf("invite info: %d %s", infoRes.status, infoRes.body)
	}
	infoBody := infoRes.json(t)
	if infoBody["valid"] != true || canonAddress(str(infoBody["account_address"])) != canonAddress(account.address) {
		t.Fatalf("unexpected invite info: %s", infoRes.body)
	}

	// Refusal check: Device 1's own wallet cannot claim its own invite.
	ownChal := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/challenge", map[string]any{
		"invite_token":  inviteToken,
		"stake_address": account.address,
	}, nil)
	if ownChal.status != http.StatusConflict {
		t.Fatalf("own wallet challenge status = %d, want 409: %s", ownChal.status, ownChal.body)
	}

	// Step 3: Device 2 (carrying phoneAuth — a DIFFERENT session!) requests a challenge and claims with payStaker.
	chalRes := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/challenge", map[string]any{
		"invite_token":  inviteToken,
		"stake_address": payStaker.address,
	}, phoneAuth)
	if chalRes.status != 200 {
		t.Fatalf("invite challenge: %d %s", chalRes.status, chalRes.body)
	}
	chalBody := chalRes.json(t)
	msg := str(chalBody["message"])
	chalToken := str(chalBody["challenge_token"])

	// Security check: a regular session challenge cannot be swapped into invite/claim.
	_, sessionChalToken := requestLinkChallenge(t, s, pcAuth, "")
	badSwap := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/claim", map[string]any{
		"invite_token":    inviteToken,
		"challenge_token": sessionChalToken,
		"stake_address":   payStaker.address,
		"public_key":      hex.EncodeToString(payStaker.pub),
		"signature":       payStaker.signMessage(msg),
	}, phoneAuth)
	if badSwap.status != http.StatusUnauthorized {
		t.Fatalf("swapped challenge status = %d, want 401: %s", badSwap.status, badSwap.body)
	}

	// Refusal check: an unstaked wallet is rejected with 422 even with a valid signature.
	unstakedChal := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/challenge", map[string]any{
		"invite_token":  inviteToken,
		"stake_address": unstaked.address,
	}, nil).json(t)
	unstakedClaim := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/claim", map[string]any{
		"invite_token":    inviteToken,
		"challenge_token": str(unstakedChal["challenge_token"]),
		"stake_address":   unstaked.address,
		"public_key":      hex.EncodeToString(unstaked.pub),
		"signature":       unstaked.signMessage(str(unstakedChal["message"])),
	}, nil)
	if unstakedClaim.status != http.StatusUnprocessableEntity {
		t.Fatalf("unstaked wallet claim = %d, want 422: %s", unstakedClaim.status, unstakedClaim.body)
	}

	// Real claim from Phone Nimiq Pay (carrying phoneAuth): attaches payStaker to PC account!
	claimRes := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/claim", map[string]any{
		"invite_token":    inviteToken,
		"challenge_token": chalToken,
		"stake_address":   payStaker.address,
		"public_key":      hex.EncodeToString(payStaker.pub),
		"signature":       payStaker.signMessage(msg),
	}, phoneAuth)
	if claimRes.status != 200 {
		t.Fatalf("invite claim: %d %s", claimRes.status, claimRes.body)
	}
	claimBody := claimRes.json(t)
	if claimBody["linked"] != true || claimBody["via_invite"] != true {
		t.Fatalf("unexpected claim response: %s", claimRes.body)
	}
	if canonAddress(str(claimBody["account_address"])) != canonAddress(account.address) {
		t.Fatalf("linked to wrong account: %s", claimRes.body)
	}

	// Step 4: Completely signed-out browser claims a SECOND staking wallet (hubStaker) onto the same account.
	chal2 := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/challenge", map[string]any{
		"invite_token": inviteToken,
	}, nil).json(t)
	claim2 := s.do(http.MethodPost, "/api/poolstake/stake-address/invite/claim", map[string]any{
		"invite_token":    inviteToken,
		"challenge_token": str(chal2["challenge_token"]),
		"stake_address":   hubStaker.address,
		"public_key":      hex.EncodeToString(hubStaker.pub),
		"signature":       hubStaker.signMessage(str(chal2["message"])),
	}, nil)
	if claim2.status != 200 {
		t.Fatalf("second invite claim (signed out): %d %s", claim2.status, claim2.body)
	}

	// Step 5: Device 1 polls /api/poolstake/me and sees BOTH wallets summed (500 + 300 = 800 NIM)
	// with the staker base rate counted strictly ONCE!
	meRes := s.do(http.MethodGet, "/api/poolstake/me", nil, pcAuth)
	if meRes.status != 200 {
		t.Fatalf("poolstake/me: %d %s", meRes.status, meRes.body)
	}
	me := meRes.json(t)
	if me["staked"] != true {
		t.Fatalf("want staked=true after invite claims: %s", meRes.body)
	}
	if gotNim, _ := me["stake_nim"].(float64); int(gotNim+0.5) != 800 {
		t.Fatalf("stake_nim = %v, want 800 (500 + 300 summed)", me["stake_nim"])
	}
	baseBps, _ := me["base_bps"].(float64)
	stakerBaseBps, _ := me["staker_base_bps"].(float64)
	if baseBps != stakerBaseBps || baseBps <= 0 {
		t.Fatalf("base_bps = %v, staker_base_bps = %v: want single staker base counted once (not multiplied by 2 wallets)",
			baseBps, stakerBaseBps)
	}
	// Payout address remains the inviting account.
	if canonAddress(str(me["account_address"])) != canonAddress(account.address) {
		t.Fatalf("account_address = %v, want %s", me["account_address"], account.address)
	}
	// Limit check: verify MaxLinkedStakeAddresses is reported accurately.
	if lim, _ := me["linked_wallet_limit"].(float64); int(lim) != db.MaxLinkedStakeAddresses {
		t.Fatalf("linked_wallet_limit = %v, want %d", lim, db.MaxLinkedStakeAddresses)
	}
}
