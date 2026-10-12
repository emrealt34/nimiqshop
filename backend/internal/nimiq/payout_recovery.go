package nimiq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PayoutExpiryProof authorizes replacing an expired transaction only after all
// configured archival RPCs agree on a finalized macro block beyond its entire
// validity window AND all report the canonical hash absent after that check.
// A timeout, pruned/invalid response or disagreement is never proof of absence.
type PayoutExpiryProof struct {
	Hash            string `json:"hash"`
	ValidityStart   uint32 `json:"validity_start"`
	FinalizedHeight uint32 `json:"finalized_height"`
	FinalizedHash   string `json:"finalized_hash"`
	Window          uint32 `json:"window"`
	FailedBlock     uint32 `json:"failed_block,omitempty"`
}

func (c *Client) ProvePayoutExpired(ctx context.Context, raw string) (PayoutExpiryProof, bool, error) {
	hash, start, network, err := SignedTxInfo(raw)
	if err != nil {
		return PayoutExpiryProof{}, false, err
	}
	if err = c.requireURLs(); err != nil {
		return PayoutExpiryProof{}, false, err
	}
	height, err := c.GetBlockNumber(ctx)
	if err != nil {
		return PayoutExpiryProof{}, false, err
	}
	if uint64(height) <= uint64(start)+7200 {
		return PayoutExpiryProof{}, false, nil
	}
	var agreed PayoutExpiryProof
	for _, url := range c.urls {
		var policy struct {
			Window uint32 `json:"transactionValidityWindow"`
		}
		if err = c.callURL(ctx, url, "getPolicyConstants", []interface{}{}, &policy); err != nil {
			return agreed, false, err
		}
		// Supported Albatross mainnet/testnet policy: 120 batches x 60 blocks.
		// A future policy change must be reviewed instead of guessing at expiry.
		if policy.Window != 7200 {
			return agreed, false, fmt.Errorf("unsupported payout validity window %d", policy.Window)
		}
		if uint64(height) <= uint64(start)+uint64(policy.Window) {
			return agreed, false, nil
		}
		var macroHeight uint32
		if err = c.callURL(ctx, url, "getLastMacroBlock", []interface{}{height}, &macroHeight); err != nil {
			return agreed, false, err
		}
		if uint64(macroHeight) <= uint64(start)+uint64(policy.Window) || int64(macroHeight) > height {
			return agreed, false, nil
		}
		var block struct {
			Hash          string      `json:"hash"`
			Number        uint32      `json:"number"`
			Type          string      `json:"type"`
			Network       string      `json:"network"`
			Justification interface{} `json:"justification"`
		}
		if err = c.callURL(ctx, url, "getBlockByNumber", []interface{}{macroHeight, false}, &block); err != nil {
			return agreed, false, err
		}
		wantNetwork := "MainAlbatross"
		if network == NetworkTestnet {
			wantNetwork = "TestAlbatross"
		} else if network != NetworkMainnet {
			return agreed, false, fmt.Errorf("unsupported payout network")
		}
		if !IsTxHash(block.Hash) || block.Number != macroHeight || !strings.EqualFold(block.Type, "macro") || block.Network != wantNetwork || block.Justification == nil {
			return agreed, false, fmt.Errorf("invalid finalized payout checkpoint")
		}
		proof := PayoutExpiryProof{Hash: hash, ValidityStart: start, FinalizedHeight: macroHeight, FinalizedHash: strings.ToLower(block.Hash), Window: policy.Window}
		// A finalized execution failure did not credit the recipient either.
		// It may be retried only once the original validity window is sealed.
		var receipt json.RawMessage
		err = c.callURL(ctx, url, "getTransactionByHash", []interface{}{hash}, &receipt)
		if err != nil {
			var rpcErr *RPCError
			if !errors.As(err, &rpcErr) || !strings.EqualFold(strings.TrimSpace(rpcErr.Message), "Transaction not found: "+hash) {
				return agreed, false, err
			}
		} else if string(receipt) != "null" {
			var tx struct {
				Hash    string  `json:"hash"`
				Block   *uint32 `json:"blockNumber"`
				Success *bool   `json:"executionResult"`
			}
			if err = json.Unmarshal(receipt, &tx); err != nil {
				return agreed, false, err
			}
			if !strings.EqualFold(tx.Hash, hash) || tx.Block == nil || tx.Success == nil {
				return agreed, false, fmt.Errorf("invalid payout receipt")
			}
			if *tx.Success || *tx.Block == 0 || *tx.Block >= macroHeight {
				return agreed, false, nil
			}
			proof.FailedBlock = *tx.Block
		}
		if agreed.Hash != "" && agreed != proof {
			return agreed, false, fmt.Errorf("payout expiry checkpoints disagree")
		}
		agreed = proof
	}
	return agreed, true, nil
}
