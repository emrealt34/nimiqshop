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

// stakeLinkMessage is the exact text the Hub signs to link a wallet. It names
// the account, the wallet being linked and the nonce, so a signature can never
// be lifted from one link request to another — and, being different from
// loginChallengeMessage, it can never be used to sign in either. Both endpoints
// build it through this one function so issuer and verifier cannot drift.
func stakeLinkMessage(siteName, accountAddress, stakeAddress, nonce string) string {
	return siteName + " link stake wallet " + stakeAddress + " to account " + accountAddress + ": " + nonce
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
// Step 1: the buyer names the wallet that stakes and gets back a message to
// sign with hubApi.signMessage() plus the purpose-scoped challenge token.
type stakeAddressChallengeRequest struct {
	StakeAddress string `json:"stake_address"`
}

func (h *Handlers) StakeAddressChallenge(ctx *fasthttp.RequestCtx) {
	user, ok := h.linkUser(ctx)
	if !ok {
		return
	}

	var req stakeAddressChallengeRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.StakeAddress) == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "stake_address is required")
		return
	}
	if err := nimiq.ValidateAddress(req.StakeAddress); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
		return
	}

	// Canonical form only: the message the Hub signs must be byte-identical to
	// the one the verifier rebuilds, and "NQ86 XXXX" vs "NQ86XXXX" is not.
	stakeAddress := nimiq.NormalizeAddress(req.StakeAddress)
	account := nimiq.NormalizeAddress(user.NimiqAddress)
	if stakeAddress == account {
		writeError(ctx, fasthttp.StatusConflict,
			"that is the wallet this account signed in with; it already earns your cashback")
		return
	}

	// Refuse before making the buyer sign anything: at the cap there is no
	// signature that could succeed, and a wallet-signing round trip that ends
	// in an error is the worst place to discover a rule. Re-linking a wallet
	// that is already on the list is still allowed (it is a no-op), so a
	// refresh or a double click never fails.
	if links := user.LinkedStakeWallets(); len(links) >= db.MaxLinkedStakeAddresses {
		already := false
		for _, link := range links {
			if poolstake.CanonicalAddress(link.Address) == poolstake.CanonicalAddress(stakeAddress) {
				already = true
				break
			}
		}
		if !already {
			writeError(ctx, fasthttp.StatusConflict, stakeWalletLimitMessage)
			return
		}
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
		Message: stakeLinkMessage(h.Cfg.SiteName(), account, stakeAddress, nonce),
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
	if err := nimiq.ValidateAddress(req.StakeAddress); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
		return
	}
	stakeAddress := nimiq.NormalizeAddress(req.StakeAddress)
	account := nimiq.NormalizeAddress(user.NimiqAddress)
	if stakeAddress == account {
		writeError(ctx, fasthttp.StatusConflict,
			"that is the wallet this account signed in with; it already earns your cashback")
		return
	}

	pubKey, err := decodeFlexible(req.PublicKey)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "public_key must be hex or base64")
		return
	}
	sig, err := decodeFlexible(req.Signature)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "signature must be hex or base64")
		return
	}

	// Rebuild the message from the challenge, not from the request: the nonce
	// comes from the server-signed token, the account from the session, and the
	// wallet from the (re-validated) body. VerifySignedMessage also checks that
	// pubKey derives to stakeAddress, so this is genuine proof of ownership.
	message := stakeLinkMessage(h.Cfg.SiteName(), account, stakeAddress, claims.Nonce)
	if err := nimiq.VerifySignedMessage(pubKey, sig, message, stakeAddress); err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "signature verification failed")
		return
	}

	// One wallet, one account: a stake may only feed the cashback of a single
	// buyer, otherwise two accounts could both claim the same delegation.
	if existing, err := h.Store.GetUserByStakeAddress(stakeAddress); err == nil && existing.ID != user.ID {
		writeError(ctx, fasthttp.StatusConflict, "that staking wallet is already linked to another account")
		return
	} else if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not check the staking wallet")
		return
	}

	// Require a real delegation to OUR validator before the rate follows this
	// wallet. Ownership alone would let a buyer point their account at a wallet
	// that earns nothing and then wonder why; the pool → chain → local-watch
	// ladder is the same one the standing page uses, so the answer here and
	// there cannot disagree.
	staked, stakeLuna, source, elsewhere := h.verifyLinkableStake(ctx, stakeAddress)
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
		return
	}

	now := time.Now().UTC()
	linked, err := h.Store.AddStakeAddress(user.ID, stakeAddress, now)
	switch {
	case errors.Is(err, db.ErrTooManyStakeAddresses):
		writeError(ctx, fasthttp.StatusConflict, stakeWalletLimitMessage)
		return
	case err != nil:
		log.Printf("poolstake: could not link stake wallet %s to account %s: %v",
			poolstake.CanonicalAddress(stakeAddress), user.ID, err)
		writeError(ctx, fasthttp.StatusInternalServerError, "could not link the staking wallet")
		return
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

	h.writeLinkResult(ctx, linked, source)
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
