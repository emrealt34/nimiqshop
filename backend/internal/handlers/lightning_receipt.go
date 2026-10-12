package handlers

import (
	"encoding/hex"
	"strings"

	"nimiqshop/internal/db"
)

// Public metadata, not a claim of cryptographic proof. Never publish the raw
// invoice (it can contain supplier references) or anonymous payment details.
func publicLightningReceipt(q db.Quote) map[string]any {
	if q.Anonymous || q.TestMode || !strings.EqualFold(q.Coin, "BTC") || !strings.EqualFold(q.Network, "Lightning") {
		return nil
	}
	hash := strings.ToLower(strings.TrimSpace(q.LightningPaymentHash))
	if raw, err := hex.DecodeString(hash); err != nil || len(raw) != 32 {
		return nil
	}
	confirmed := q.PaymentObserved || q.PaidAt != nil
	// Legacy fulfilled rows predate persisted PaidAt. Fulfillment is a supplier
	// confirmation too, but do not invent a historic payment observation time.
	if q.Status == "fulfilled" || q.Status == "completed" {
		confirmed = true
	}
	out := map[string]any{"payment_hash": hash, "confirmed": confirmed}
	// Owner-reported submission is explicitly separate from verified chain proof.
	funding := strings.ToLower(strings.TrimSpace(q.LightningFundingHint))
	if raw, err := hex.DecodeString(strings.TrimPrefix(funding, "0x")); err == nil && len(raw) == 32 {
		out["submitted_tx"] = funding
	}
	if q.CoinAmount != "" {
		out["amount_btc"] = q.CoinAmount
	}
	if q.PaidAt != nil {
		out["confirmed_at"] = q.PaidAt
	}
	if q.LightningChainProof != nil {
		out["chain_proof"] = q.LightningChainProof
	}
	return out
}
