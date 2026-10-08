package nimiq

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// TxDetail is what a rating verifier needs from one on-chain transaction. The
// RPC reports it as: {"hash","blockNumber","from","to","value","recipientData",
// "executionResult",…} (shape verified live against rpc.nimiqwatch.com).
// recipientData is the hex of the transaction's data field, i.e. the memo.
type TxDetail struct {
	Hash    string
	From    string // normalised NQ address, no spaces
	To      string // normalised NQ address, no spaces
	Value   int64  // Luna
	Data    []byte // decoded recipientData (the memo bytes)
	Mined   bool   // a block number is present
	Success bool   // executionResult is true
}

var txHashRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// IsTxHash reports whether s is a 64-hex Nimiq transaction hash.
func IsTxHash(s string) bool { return txHashRe.MatchString(s) }

// GetTransactionDetail reads one transaction from every configured RPC. found is
// false when no RPC knows the hash yet (still propagating). Every RPC that
// knows it must report the same sender, recipient, value and memo, otherwise
// the answer is an error rather than a guess.
func (c *Client) GetTransactionDetail(ctx context.Context, hash string) (TxDetail, bool, error) {
	if err := c.requireURLs(); err != nil {
		return TxDetail{}, false, err
	}
	type res struct {
		d     TxDetail
		found bool
		err   error
	}
	out := make([]res, len(c.urls))
	var wg sync.WaitGroup
	for i, u := range c.urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			d, f, err := c.txDetailOne(ctx, u, hash)
			out[i] = res{d, f, err}
		}(i, u)
	}
	wg.Wait()

	var agreed TxDetail
	have := false
	mined := true
	for _, r := range out {
		if r.err != nil {
			return TxDetail{}, false, fmt.Errorf("nimiq rpc: %w", r.err)
		}
		if !r.found {
			continue
		}
		if !have {
			agreed = r.d
			have = true
			mined = r.d.Mined
			continue
		}
		if r.d.From != agreed.From || r.d.To != agreed.To || r.d.Value != agreed.Value ||
			string(r.d.Data) != string(agreed.Data) || r.d.Success != agreed.Success {
			return TxDetail{}, false, errors.New("nimiq rpc: transaction details disagree")
		}
		mined = mined && r.d.Mined
	}
	if !have {
		return TxDetail{}, false, nil
	}
	agreed.Mined = mined
	return agreed, true, nil
}

func (c *Client) txDetailOne(ctx context.Context, baseURL, hash string) (TxDetail, bool, error) {
	var out struct {
		Hash            *string `json:"hash"`
		BlockNumber     *int64  `json:"blockNumber"`
		From            string  `json:"from"`
		To              string  `json:"to"`
		Value           int64   `json:"value"`
		RecipientData   string  `json:"recipientData"`
		ExecutionResult *bool   `json:"executionResult"`
	}
	if err := c.callURL(ctx, baseURL, "getTransactionByHash", []interface{}{hash}, &out); err != nil {
		return TxDetail{}, false, err
	}
	if out.Hash == nil {
		return TxDetail{}, false, nil
	}
	data, err := hex.DecodeString(out.RecipientData)
	if err != nil {
		return TxDetail{}, false, errors.New("recipientData is not hex")
	}
	return TxDetail{
		Hash:    strings.ToLower(*out.Hash),
		From:    NormalizeAddress(out.From),
		To:      NormalizeAddress(out.To),
		Value:   out.Value,
		Data:    data,
		Mined:   out.BlockNumber != nil,
		Success: out.ExecutionResult != nil && *out.ExecutionResult,
	}, true, nil
}
