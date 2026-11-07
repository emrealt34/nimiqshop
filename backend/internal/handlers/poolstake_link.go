package handlers

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/auth"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/poolstake"
)

/*
Linking a staking wallet to a shop account.

The confusion this exists to remove, in the words of a real customer: he stakes
from his regular wallet, his cashback card shows that staking wallet, but the
shop asks him to open Nimiq Pay — and Nimiq Pay signs him in as whichever wallet
he picks there. Because a shop account IS an address
(db.FindOrCreateUserByAddress creates exactly one account per wallet), paying
from a different wallet means a different account: no stake, base rate only, and
no way for him to tell the two apart from the UI.

So an account may name additional wallets — the ones that stake — after proving
ownership of each with a Hub signature, exactly the same proof that signs him
in. Buyers split a stake over several wallets (hardware, phone, an old address),
so the link is a LIST capped at db.MaxLinkedStakeAddresses. From then on the
stakes of all of them are summed into one rate — with the staker base counted
once per account, never once per wallet — while payouts still go to the wallet
he authenticated with.

What is deliberately NOT true:
  - Cashback never depended on the payment rail. It is granted from the stake
    recorded for the account's earning address (db/cashback.go), so paying with
    Lightning or a bank transfer earns just as much. "Open in Nimiq Pay" is a
    convenience, not an eligibility rule.
  - Linking is not a way to earn from someone else's stake: the signature is
    verified with nimiq.VerifySignedMessage, which checks that the public key
    derives to the claimed address. No key, no link — for any of the wallets.
  - Linking more wallets cannot multiply the base rate. Stakes and ledger
    boosts add up; the flat staker base is applied once (buildStakeMeView and
    db.AggregateStakerStake both encode that rule).
  - Linking does not rewrite history. Past ledger entries stay under the address
    that earned them; only reads from now on follow the linked wallet.
*/

// stakeLinkMessage is the exact text Hub signs to link the wallet selected in
// its signer picker. It binds the proof to the account and one-time nonce, but
// does not require the buyer to type or paste the staking address. The link
// endpoint derives that address from Hub's signed result and verifies the
// signature against it.
func stakeLinkMessage(siteName, accountAddress, nonce string) string {
	return siteName + " link staking wallet to account " + accountAddress + ": " + nonce
}

// stakeWalletLimitMessage is the refusal a buyer gets once their account links
// db.MaxLinkedStakeAddresses wallets. It names the limit AND the way out
// (unlink a wallet they no longer use), because a bare "could not link" would
// send them to support for a rule they can act on themselves.
const stakeWalletLimitMessage = "that account already links the maximum number of staking wallets; unlink one first"

// linkUser loads the signed-in account or answers 401 and reports false.
func (h *Handlers) linkUser(ctx *fasthttp.RequestCtx) (db.User, bool) {
	user, err := h.Store.GetUser(middleware.UserID(ctx))
	if err != nil || user.NimiqAddress == "" {
		writeError(ctx, fasthttp.StatusUnauthorized, "session expired")
		return db.User{}, false
	}
	return user, true
}

// POST /api/poolstake/stake-address/challenge
//
// Step 1: issue a purpose-scoped message. The buyer chooses the wallet to sign
// in Hub; no address is entered or trusted at challenge time.
func (h *Handlers) StakeAddressChallenge(ctx *fasthttp.RequestCtx) {
	user, ok := h.linkUser(ctx)
	if !ok {
		return
	}

	// Keep accepting the legacy optional address field so an older client can
	// request a challenge during rollout, but never put it in the signed text.
	var legacy struct {
		StakeAddress string `json:"stake_address"`
	}
	if len(ctx.PostBody()) > 0 {
		if err := readJSON(ctx, &legacy); err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	if legacy.StakeAddress != "" {
		if err := nimiq.ValidateAddress(legacy.StakeAddress); err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
			return
		}
	}

	account := nimiq.NormalizeAddress(user.NimiqAddress)
	// The cap is refused HERE, before a signature, whenever the client names the
	// wallet it wants: making a buyer sign in his wallet only to be told no is
	// the worst place to discover a limit he can act on. Two requests stay
	// allowed at the cap.
	//
	//   - no address named: there is nothing to judge yet, and the link step
	//     still refuses a wallet that would be new (AddStakeAddress returns
	//     ErrTooManyStakeAddresses, which becomes the same 409);
	//   - an address that is ALREADY linked: AddStakeAddress documents a
	//     duplicate as a successful no-op that consumes no slot, so a refresh or
	//     a double click must not fail the buyer — and refusing the challenge
	//     would make that two-step flow impossible to complete for an operation
	//     that changes nothing.
	if len(user.LinkedStakeWallets()) >= db.MaxLinkedStakeAddresses &&
		legacy.StakeAddress != "" &&
		user.StakeAddressLinkedAt(legacy.StakeAddress).IsZero() {
		writeError(ctx, fasthttp.StatusConflict, stakeWalletLimitMessage)
		return
	}

	nonce, err := auth.NewNonce()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not generate challenge")
		return
	}
	token, err := auth.IssueChallengeFor(h.Cfg.JWTSecret, nonce, auth.PurposeStakeLink)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not issue challenge")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, challengeResponse{
		Nonce:   nonce,
		Message: stakeLinkMessage(h.Cfg.SiteName(), account, nonce),
		Token:   token,
	})
}

// POST /api/poolstake/stake-address
//
// Step 2: verify the signature over the challenge message and, if the wallet
// really does stake with our validator, record it as this account's earning
// address. The response carries the refreshed /api/poolstake/me view so the
// page updates without a second round trip.
type stakeAddressLinkRequest struct {
	ChallengeToken string `json:"challenge_token"`
	StakeAddress   string `json:"stake_address"`
	PublicKey      string `json:"public_key"` // hex or base64, from Hub's signMessage
	Signature      string `json:"signature"`  // hex or base64, from Hub's signMessage
}

func (h *Handlers) StakeAddressLink(ctx *fasthttp.RequestCtx) {
	user, ok := h.linkUser(ctx)
	if !ok {
		return
	}

	var req stakeAddressLinkRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ChallengeToken == "" || req.StakeAddress == "" || req.PublicKey == "" || req.Signature == "" {
		writeError(ctx, fasthttp.StatusBadRequest,
			"challenge_token, stake_address, public_key, and signature are required")
		return
	}

	// A login challenge must not double as a linking proof.
	claims, err := auth.ParseChallengeFor(h.Cfg.JWTSecret, req.ChallengeToken, auth.PurposeStakeLink)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "challenge expired or invalid, request a new one")
		return
	}

	linked, _, _, source, ok := h.completeStakeAddressLink(ctx, user, claims.Nonce, req.StakeAddress, req.PublicKey, req.Signature)
	if !ok {
		return
	}
	h.writeLinkResult(ctx, linked, source)
}

// completeStakeAddressLink runs the shared verification + persistence ladder
// for both in-session linking and cross-device invite linking.
func (h *Handlers) completeStakeAddressLink(
	ctx *fasthttp.RequestCtx,
	user db.User,
	nonce, rawStakeAddress, rawPubKey, rawSig string,
) (linked db.User, stakeAddress string, stakeLuna int64, source string, ok bool) {
	if err := nimiq.ValidateAddress(rawStakeAddress); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
		return db.User{}, "", 0, "", false
	}
	stakeAddress = nimiq.NormalizeAddress(rawStakeAddress)
	account := nimiq.NormalizeAddress(user.NimiqAddress)
	if stakeAddress == account {
		writeError(ctx, fasthttp.StatusConflict,
			"that is the wallet this account signed in with; it already earns your cashback")
		return db.User{}, "", 0, "", false
	}

	pubKey, err := decodeFlexible(rawPubKey)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "public_key must be hex or base64")
		return db.User{}, "", 0, "", false
	}
	sig, err := decodeFlexible(rawSig)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "signature must be hex or base64")
		return db.User{}, "", 0, "", false
	}

	// Rebuild the message from the challenge, not from the request: the nonce
	// comes from the server-signed token, the account from the session/invite,
	// and the wallet from the (re-validated) body. VerifySignedMessage also
	// checks that pubKey derives to stakeAddress, so this is genuine proof of
	// ownership.
	message := stakeLinkMessage(h.Cfg.SiteName(), account, nonce)
	if err := nimiq.VerifySignedMessage(pubKey, sig, message, stakeAddress); err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "signature verification failed")
		return db.User{}, "", 0, "", false
	}

	// One wallet, one account: a stake may only feed the cashback of a single
	// buyer, otherwise two accounts could both claim the same delegation.
	if existing, err := h.Store.GetUserByStakeAddress(stakeAddress); err == nil && existing.ID != user.ID {
		writeError(ctx, fasthttp.StatusConflict, "that staking wallet is already linked to another account")
		return db.User{}, "", 0, "", false
	} else if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not check the staking wallet")
		return db.User{}, "", 0, "", false
	}

	// Require a real delegation to OUR validator before the rate follows this
	// wallet. Ownership alone would let a buyer point their account at a wallet
	// that earns nothing and then wonder why; the pool → chain → local-watch
	// ladder is the same one the standing page uses, so the answer here and
	// there cannot disagree.
	var staked bool
	var elsewhere string
	staked, stakeLuna, source, elsewhere = h.verifyLinkableStake(ctx, stakeAddress)
	if !staked {
		msg := "that wallet has no stake with our validator yet, so there is nothing to earn from"
		if elsewhere != "" {
			msg = "that wallet stakes with a different validator (" + elsewhere +
				"); move it to ours first and the cashback will follow"
		}
		writeJSON(ctx, fasthttp.StatusUnprocessableEntity, map[string]any{
			"error":           msg,
			"staked":          false,
			"other_validator": elsewhere,
		})
		return db.User{}, "", 0, "", false
	}

	now := time.Now().UTC()
	linked, err = h.Store.AddStakeAddress(user.ID, stakeAddress, now)
	switch {
	case errors.Is(err, db.ErrTooManyStakeAddresses):
		writeError(ctx, fasthttp.StatusConflict, stakeWalletLimitMessage)
		return db.User{}, "", 0, "", false
	case err != nil:
		log.Printf("poolstake: could not link stake wallet %s to account %s: %v",
			poolstake.CanonicalAddress(stakeAddress), user.ID, err)
		writeError(ctx, fasthttp.StatusInternalServerError, "could not link the staking wallet")
		return db.User{}, "", 0, "", false
	}
	log.Printf("poolstake: account %s now earns from %d wallet(s), latest %s (%s NIM via %s)",
		user.ID, len(linked.EarningStakeAddresses()), poolstake.CanonicalAddress(stakeAddress), fmtNIM(stakeLuna), source)

	// Seed the watch/ledger clocks for the new earning address so the loyalty
	// age and the stake-weighted ledger start from this moment instead of
	// waiting for the next page view. Best-effort by design. Each wallet keeps
	// its OWN book: summing them into one would let a fresh wallet spend credit
	// its stake has not earned.
	if watch, werr := h.Store.ObserveStake(stakeAddress, stakeLuna, now); werr == nil {
		_ = h.Store.ObserveStakeLedger(stakeAddress, stakeLuna, now)
		_ = watch
	}

	return linked, stakeAddress, stakeLuna, source, true
}

// inviteUser resolves the target account of a cross-device pairing link token.
func (h *Handlers) inviteUser(ctx *fasthttp.RequestCtx, rawToken string) (db.User, *auth.StakeLinkInviteClaims, bool) {
	claims, err := auth.ParseStakeLinkInvite(h.Cfg.JWTSecret, strings.TrimSpace(rawToken))
	if err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "invite link expired or invalid")
		return db.User{}, nil, false
	}
	user, err := h.Store.GetUser(claims.UserID)
	if err != nil || user.NimiqAddress == "" || poolstake.CanonicalAddress(user.NimiqAddress) != poolstake.CanonicalAddress(claims.Account) {
		writeError(ctx, fasthttp.StatusUnauthorized, "invite link expired or invalid")
		return db.User{}, nil, false
	}
	return user, claims, true
}

// POST /api/poolstake/stake-address/invite
//
// Mint a short-lived account-bound pairing token so the buyer can open a link
// on another device/wallet (e.g. Phone Nimiq Pay ↔ PC Nimiq Hub) and attach
// that wallet directly to THIS account without sharing session cookies.
func (h *Handlers) StakeAddressInviteCreate(ctx *fasthttp.RequestCtx) {
	user, ok := h.linkUser(ctx)
	if !ok {
		return
	}
	if len(user.LinkedStakeWallets()) >= db.MaxLinkedStakeAddresses {
		writeError(ctx, fasthttp.StatusConflict, stakeWalletLimitMessage)
		return
	}
	account := nimiq.NormalizeAddress(user.NimiqAddress)
	token, exp, err := auth.IssueStakeLinkInvite(h.Cfg.JWTSecret, user.ID, account)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not create invite link")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"invite_token":        token,
		"account_address":     account,
		"expires_at":          exp.Unix(),
		"ttl_seconds":         int(auth.StakeLinkInviteTTL.Seconds()),
		"linked_count":        len(user.LinkedStakeWallets()),
		"linked_wallet_limit": db.MaxLinkedStakeAddresses,
	})
}

// GET /api/poolstake/stake-address/invite?token=...
//
// Inspect an account-bound pairing link on the receiving device before signing.
// Returns only the target account's public address and remaining capacity — no
// private order/ledger history is exposed.
func (h *Handlers) StakeAddressInviteInfo(ctx *fasthttp.RequestCtx) {
	rawToken := strings.TrimSpace(string(ctx.QueryArgs().Peek("token")))
	if rawToken == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "token is required")
		return
	}
	user, claims, ok := h.inviteUser(ctx, rawToken)
	if !ok {
		return
	}
	linkedCount := len(user.LinkedStakeWallets())
	var expUnix int64
	if claims.ExpiresAt != nil {
		expUnix = claims.ExpiresAt.Unix()
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"valid":               true,
		"account_address":     nimiq.NormalizeAddress(user.NimiqAddress),
		"expires_at":          expUnix,
		"at_limit":            linkedCount >= db.MaxLinkedStakeAddresses,
		"linked_count":        linkedCount,
		"linked_wallet_limit": db.MaxLinkedStakeAddresses,
	})
}

// POST /api/poolstake/stake-address/invite/challenge
//
// Step 1 of cross-device pairing: issue a purpose-bound challenge pinned to the
// inviting account. Works whether the receiving device is signed out or signed
// into a different wallet.
func (h *Handlers) StakeAddressInviteChallenge(ctx *fasthttp.RequestCtx) {
	var req struct {
		InviteToken  string `json:"invite_token"`
		StakeAddress string `json:"stake_address"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.InviteToken) == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "invite_token is required")
		return
	}
	if req.StakeAddress != "" {
		if err := nimiq.ValidateAddress(req.StakeAddress); err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
			return
		}
	}
	user, _, ok := h.inviteUser(ctx, req.InviteToken)
	if !ok {
		return
	}
	account := nimiq.NormalizeAddress(user.NimiqAddress)
	if req.StakeAddress != "" && poolstake.CanonicalAddress(req.StakeAddress) == poolstake.CanonicalAddress(account) {
		writeError(ctx, fasthttp.StatusConflict,
			"that is the wallet this account signed in with; it already earns your cashback")
		return
	}
	if len(user.LinkedStakeWallets()) >= db.MaxLinkedStakeAddresses &&
		(req.StakeAddress == "" || user.StakeAddressLinkedAt(req.StakeAddress).IsZero()) {
		writeError(ctx, fasthttp.StatusConflict, stakeWalletLimitMessage)
		return
	}
	nonce, err := auth.NewNonce()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not generate challenge")
		return
	}
	token, err := auth.IssueStakeLinkInviteChallenge(h.Cfg.JWTSecret, nonce, user.ID)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not issue challenge")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"nonce":           nonce,
		"message":         stakeLinkMessage(h.Cfg.SiteName(), account, nonce),
		"challenge_token": token,
		"account_address": account,
	})
}

// POST /api/poolstake/stake-address/invite/claim
//
// Step 2 of cross-device pairing: verify the wallet signature and delegation,
// then attach the staking wallet directly to the inviting account.
func (h *Handlers) StakeAddressInviteClaim(ctx *fasthttp.RequestCtx) {
	var req struct {
		InviteToken    string `json:"invite_token"`
		ChallengeToken string `json:"challenge_token"`
		StakeAddress   string `json:"stake_address"`
		PublicKey      string `json:"public_key"`
		Signature      string `json:"signature"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.InviteToken == "" || req.ChallengeToken == "" || req.StakeAddress == "" || req.PublicKey == "" || req.Signature == "" {
		writeError(ctx, fasthttp.StatusBadRequest,
			"invite_token, challenge_token, stake_address, public_key, and signature are required")
		return
	}
	user, _, ok := h.inviteUser(ctx, req.InviteToken)
	if !ok {
		return
	}
	claims, err := auth.ParseStakeLinkInviteChallenge(h.Cfg.JWTSecret, req.ChallengeToken, user.ID)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "challenge expired or invalid, request a new one")
		return
	}
	linked, stakeAddress, stakeLuna, source, ok := h.completeStakeAddressLink(
		ctx, user, claims.Nonce, req.StakeAddress, req.PublicKey, req.Signature,
	)
	if !ok {
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"linked":              true,
		"via_invite":          true,
		"account_address":     nimiq.NormalizeAddress(linked.NimiqAddress),
		"stake_address":       stakeAddress,
		"stake_nim":           float64(stakeLuna) / 100_000.0,
		"stake_source":        source,
		"linked_count":        len(linked.LinkedStakeWallets()),
		"linked_wallet_limit": db.MaxLinkedStakeAddresses,
	})
}

// DELETE /api/poolstake/stake-address
//
// Drop a link. With ?address=NQ… it unlinks THAT wallet and the rest keep
// earning; without it, every linked wallet is dropped and the account earns
// from the wallet it signs in with again. Existing ledger entries are untouched
// either way — unlinking changes where future reads come from, not history.
func (h *Handlers) StakeAddressUnlink(ctx *fasthttp.RequestCtx) {
	user, ok := h.linkUser(ctx)
	if !ok {
		return
	}
	if !user.HasLinkedStakeAddress() {
		writeError(ctx, fasthttp.StatusConflict, "no staking wallet is linked to this account")
		return
	}

	target := strings.TrimSpace(string(ctx.QueryArgs().Peek("address")))
	var (
		cleared db.User
		err     error
	)
	if target == "" {
		cleared, err = h.Store.ClearStakeAddresses(user.ID)
	} else {
		if verr := nimiq.ValidateAddress(target); verr != nil {
			writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
			return
		}
		cleared, err = h.Store.RemoveStakeAddress(user.ID, target)
	}
	switch {
	case errors.Is(err, db.ErrNotFound):
		// A stale page can offer to unlink a wallet that was already dropped.
		// Say so instead of reporting a success this request did not cause.
		writeError(ctx, fasthttp.StatusConflict, "that wallet is not linked to this account")
		return
	case err != nil:
		log.Printf("poolstake: could not unlink stake wallet %q of account %s: %v",
			poolstake.CanonicalAddress(target), user.ID, err)
		writeError(ctx, fasthttp.StatusInternalServerError, "could not unlink the staking wallet")
		return
	}
	if target == "" {
		log.Printf("poolstake: account %s unlinked every stake wallet; earning from %s again",
			user.ID, poolstake.CanonicalAddress(cleared.NimiqAddress))
	} else {
		log.Printf("poolstake: account %s unlinked stake wallet %s; %d earning wallet(s) left",
			user.ID, poolstake.CanonicalAddress(target), len(cleared.EarningStakeAddresses()))
	}
	h.writeLinkResult(ctx, cleared, "")
}

// writeLinkResult answers a link/unlink with the new state plus the full
// refreshed standing, so the cashback page can re-render in one step.
func (h *Handlers) writeLinkResult(ctx *fasthttp.RequestCtx, user db.User, stakeSource string) {
	links := user.LinkedStakeWallets()
	linkRows := make([]map[string]any, 0, len(links))
	for _, link := range links {
		linkRows = append(linkRows, map[string]any{
			"address":   link.Address,
			"linked_at": link.LinkedAt,
		})
	}
	resp := map[string]any{
		"linked":              user.HasLinkedStakeAddress(),
		"account_address":     user.NimiqAddress,
		"stake_addresses":     linkRows,
		"linked_count":        len(links),
		"linked_wallet_limit": db.MaxLinkedStakeAddresses,
		// The primary wallet the rate card names, and every address whose stake
		// is summed into the rate.
		"stake_address":           user.EffectiveStakeAddress(),
		"stake_address_linked_at": user.StakeAddressLinkedAt(user.EffectiveStakeAddress()),
		"effective_stake_address": user.EffectiveStakeAddress(),
		"earning_addresses":       user.EarningStakeAddresses(),
	}
	if stakeSource != "" {
		resp["stake_source"] = stakeSource
	}
	if me, ok := h.buildStakeMeView(ctx, user); ok {
		resp["me"] = me
	} else {
		adminStoreError(ctx)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, resp)
}

// verifyLinkableStake answers "does this wallet delegate to our validator?"
// with the same pool → chain → durable-local-watch ladder the standing page
// uses, and reports which source answered. elsewhere is the foreign validator
// when the wallet stakes with someone else, which is the one case worth naming
// explicitly to the buyer.
func (h *Handlers) verifyLinkableStake(ctx *fasthttp.RequestCtx, address string) (staked bool, stakeLuna int64, source, elsewhere string) {
	if h.Pool != nil && h.Cfg.PoolAPIURL != "" {
		if st, err := h.poolStake(ctx, address); err == nil && st.Staked {
			return true, st.StakeLuna, "pool", ""
		}
	}
	if cs, err := h.chainStake(ctx, address); err == nil {
		if cs.Staked {
			return true, cs.StakeLuna, "chain", ""
		}
		if cs.Elsewhere {
			return false, 0, "chain", cs.Delegation
		}
	}
	// A pool outage must not cost a real staker their link: the durable local
	// observation is the same fallback the standing page trusts.
	if cached := h.Store.CachedStakerStake(address, time.Now().UTC()); cached.Staked {
		return true, cached.StakeLuna, "local_watch", ""
	}
	return false, 0, "", ""
}
