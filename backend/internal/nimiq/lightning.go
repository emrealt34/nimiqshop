package nimiq

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// LightningProof links a BOLT11 payment hash to an executed mainnet Nimiq
// HTLC. Funding alone is not settlement. Only regular redemption exposes the
// matching SHA-256 preimage. Format: core-rs-albatross htlc_contract.rs.
type LightningProof struct {
	FundingTx    string `json:"funding_tx"`
	Contract     string `json:"contract"`
	SettlementTx string `json:"settlement_tx,omitempty"`
	Preimage     string `json:"preimage,omitempty"`
}
type lightningTx struct {
	Hash        string `json:"hash"`
	From        string `json:"from"`
	To          string `json:"to"`
	FromType    int    `json:"fromType"`
	ToType      int    `json:"toType"`
	NetworkID   int    `json:"networkId"`
	BlockNumber uint64 `json:"blockNumber"`
	Success     bool   `json:"executionResult"`
	Data        string `json:"recipientData"`
	Proof       string `json:"proof"`
}

func fundingMatches(tx lightningTx, hash string) bool {
	data, err := hex.DecodeString(tx.Data)
	return err == nil && tx.NetworkID == 24 && tx.Success && tx.BlockNumber > 0 &&
		tx.ToType == 2 && IsTxHash(tx.Hash) && len(data) == 82 && data[40] == 3 &&
		data[73] == 1 && hex.EncodeToString(data[41:73]) == hash
}
func redemptionPreimage(tx lightningTx, hash, contract string) string {
	proof, err := hex.DecodeString(tx.Proof)
	if err != nil || tx.NetworkID != 24 || !tx.Success || tx.BlockNumber == 0 ||
		tx.FromType != 2 || NormalizeAddress(tx.From) != NormalizeAddress(contract) || !IsTxHash(tx.Hash) ||
		len(proof) < 69 || proof[0] != 0 || proof[1] != 1 || proof[2] != 3 || proof[35] != 32 ||
		hex.EncodeToString(proof[3:35]) != hash {
		return ""
	}
	sum := sha256.Sum256(proof[36:68])
	if hex.EncodeToString(sum[:]) != hash {
		return ""
	}
	return hex.EncodeToString(proof[36:68])
}
func (c *Client) lightningDetail(ctx context.Context, hash string) (lightningTx, error) {
	var agreed lightningTx
	for i, url := range c.urls {
		var tx lightningTx
		if err := c.callURL(ctx, url, "getTransactionByHash", []interface{}{hash}, &tx); err != nil {
			return tx, err
		}
		if !strings.EqualFold(tx.Hash, hash) {
			return tx, errors.New("transaction not confirmed by every RPC")
		}
		if i > 0 && tx != agreed {
			return tx, errors.New("chain proof RPCs disagree")
		}
		agreed = tx
	}
	return agreed, nil
}

// FindLightningProof uses a trusted, persisted quote hash. Client hints never
// bypass invoice matching. Historic discovery is bounded to 100 transactions;
// a different funding wallet or old history honestly yields no proof.
func (c *Client) FindLightningProof(ctx context.Context, paymentHash, address, fundingHash string) (*LightningProof, error) {
	if err := c.requireURLs(); err != nil {
		return nil, err
	}
	hash := strings.ToLower(strings.TrimSpace(paymentHash))
	if !IsTxHash(hash) {
		return nil, errors.New("invalid payment hash")
	}
	var funding lightningTx
	if fundingHash != "" {
		if !IsTxHash(fundingHash) {
			return nil, errors.New("invalid funding hash")
		}
		tx, err := c.lightningDetail(ctx, fundingHash)
		if err != nil {
			return nil, err
		}
		if !fundingMatches(tx, hash) {
			return nil, errors.New("funding transaction does not match this invoice")
		}
		funding = tx
	} else {
		if address == "" {
			return nil, nil
		}
		var txs []lightningTx
		if err := c.callURL(ctx, c.urls[0], "getTransactionsByAddress", []interface{}{address, 100, nil}, &txs); err != nil {
			return nil, err
		}
		for _, tx := range txs {
			if !fundingMatches(tx, hash) {
				continue
			}
			verified, err := c.lightningDetail(ctx, tx.Hash)
			if err != nil {
				return nil, err
			}
			if fundingMatches(verified, hash) {
				funding = verified
				break
			}
		}
		if funding.Hash == "" {
			return nil, nil
		}
	}
	result := &LightningProof{FundingTx: strings.ToLower(funding.Hash), Contract: funding.To}
	var txs []lightningTx
	if err := c.callURL(ctx, c.urls[0], "getTransactionsByAddress", []interface{}{funding.To, 100, nil}, &txs); err != nil {
		return result, err
	}
	for _, tx := range txs {
		secret := redemptionPreimage(tx, hash, funding.To)
		if secret == "" {
			continue
		}
		verified, err := c.lightningDetail(ctx, tx.Hash)
		if err != nil {
			return result, err
		}
		if !bytes.Equal([]byte(secret), []byte(redemptionPreimage(verified, hash, funding.To))) {
			continue
		}
		result.SettlementTx = strings.ToLower(tx.Hash)
		result.Preimage = secret
		break
	}
	return result, nil
}
