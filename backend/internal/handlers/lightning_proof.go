package handlers

import (
	"context"
	"encoding/json"
	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"strings"
	"time"
)

// LightningProof only reads the chain; it never sends payment, retries an
// invoice or changes supplier settlement state. Public access excludes all
// anonymous and simulated purchases, including already saved proof.
func (h *Handlers) LightningProof(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	q, err := h.Store.GetQuote(id)
	private := string(ctx.Method()) == "POST"
	if err != nil || q.TestMode || !strings.EqualFold(q.Coin, "BTC") || !strings.EqualFold(q.Network, "Lightning") ||
		(private && q.UserID != middleware.UserID(ctx)) || (!private && q.Anonymous) {
		writeError(ctx, 404, "payment proof unavailable")
		return
	}
	if q.LightningChainProof != nil && q.LightningChainProof.SettlementTx != "" {
		writeJSON(ctx, 200, map[string]any{"chain_proof": q.LightningChainProof})
		return
	}
	funding := q.LightningFundingHint
	if q.LightningChainProof != nil {
		funding = q.LightningChainProof.FundingTx
	}
	if private {
		var body struct {
			Hash string `json:"hash"`
		}
		if json.Unmarshal(ctx.PostBody(), &body) != nil {
			writeError(ctx, 400, "invalid proof request")
			return
		}
		funding = strings.ToLower(strings.TrimSpace(body.Hash))
		if !nimiq.IsTxHash(strings.TrimPrefix(funding, "0x")) {
			writeError(ctx, 400, "invalid funding hash")
			return
		}
		// The SDK reports submission before mining. Keep the hint privately
		// so a later public lookup can verify it after chain confirmation.
		if h.Store.SaveLightningFundingHint(q.ID, q.UserID, funding) != nil {
			writeError(ctx, 503, "funding hint could not be saved")
			return
		}
	}
	// Polygon submissions are not Nimiq HTLC proofs. Persist the SDK
	// identifier without claiming Lightning settlement or querying the wrong chain.
	if strings.HasPrefix(funding, "0x") {
		writeJSON(ctx, 200, map[string]any{"chain_proof": nil})
		return
	}
	address := ""
	if user, e := h.Store.GetUser(q.UserID); e == nil {
		address = user.NimiqAddress
	}
	cctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	proof, err := h.NIMRPC.FindLightningProof(cctx, q.LightningPaymentHash, address, funding)
	if err != nil && proof == nil {
		writeError(ctx, 503, "chain proof could not be verified")
		return
	}
	if proof == nil {
		writeJSON(ctx, 200, map[string]any{"chain_proof": nil})
		return
	}
	saved := db.LightningChainProof{FundingTx: proof.FundingTx, Contract: proof.Contract, SettlementTx: proof.SettlementTx, Preimage: proof.Preimage}
	if h.Store.SaveLightningChainProof(q.ID, q.LightningPaymentHash, saved) != nil {
		writeError(ctx, 503, "chain proof could not be saved")
		return
	}
	writeJSON(ctx, 200, map[string]any{"chain_proof": saved})
}
