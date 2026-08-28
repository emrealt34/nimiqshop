package handlers

import (
	"encoding/base64"
	"encoding/hex"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/auth"
	"nimiqshop/internal/nimiq"
)

// GET/POST /api/auth/challenge
//
// Step 1 of Nimiq Hub login: hand the client a nonce to sign with
// hubApi.signMessage(). The nonce is embedded in a short-lived JWT so the
// server stays stateless between challenge issuance and verification.
type challengeResponse struct {
	Nonce   string `json:"nonce"`
	Message string `json:"message"` // human-readable text the wallet will show/sign
	Token   string `json:"challenge_token"`
}

func (h *Handlers) AuthChallenge(ctx *fasthttp.RequestCtx) {
	nonce, err := auth.NewNonce()
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not generate challenge")
		return
	}

	token, err := auth.IssueChallenge(h.Cfg.JWTSecret, nonce)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not issue challenge")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, challengeResponse{
		Nonce:   nonce,
		Message: "nim.shop login: " + nonce,
		Token:   token,
	})
}

// POST /api/auth/hub-login
//
// Step 2: client sends back the challenge token plus what Nimiq Hub's
// signMessage() returned (signer public key, signature, and the address the
// user picked in the Hub account selector). We verify the signature covers
// our nonce and matches the claimed address, then find-or-create the user
// and issue a normal session JWT.
type hubLoginRequest struct {
	ChallengeToken string `json:"challenge_token"`
	Address        string `json:"address"`    // user-friendly Nimiq address, e.g. "NQ07 ..."
	PublicKey      string `json:"public_key"` // hex or base64, from Hub's signMessage result
	Signature      string `json:"signature"`  // hex or base64, from Hub's signMessage result
}

func (h *Handlers) HubLogin(ctx *fasthttp.RequestCtx) {
	var req hubLoginRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ChallengeToken == "" || req.Address == "" || req.PublicKey == "" || req.Signature == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "challenge_token, address, public_key, and signature are required")
		return
	}

	claims, err := auth.ParseChallenge(h.Cfg.JWTSecret, req.ChallengeToken)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "challenge expired or invalid, request a new one")
		return
	}

	if err := nimiq.ValidateAddress(req.Address); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "malformed nimiq address")
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

	message := "nim.shop login: " + claims.Nonce
	if err := nimiq.VerifySignedMessage(pubKey, sig, message, req.Address); err != nil {
		writeError(ctx, fasthttp.StatusUnauthorized, "signature verification failed")
		return
	}

	normalizedAddr := nimiq.NormalizeAddress(req.Address)

	// Badger has no ON CONFLICT; the find-or-create upsert (and the
	// uniqueness of nimiq_address) is handled transactionally in the store.
	user, err := h.Store.FindOrCreateUserByAddress(normalizedAddr)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not create or load user")
		return
	}
	userID := user.ID

	token, err := auth.IssueToken(h.Cfg.JWTSecret, userID, h.Cfg.JWTExpiryMins)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not issue token")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"token": token,
		"user": map[string]string{
			"id":            userID,
			"nimiq_address": normalizedAddr,
		},
	})
}

// decodeFlexible accepts either hex or standard/URL-safe base64, since
// different Hub API versions and client wrappers surface signature bytes in
// different encodings.
func decodeFlexible(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}
