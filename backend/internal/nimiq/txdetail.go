package nimiq

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// TxDetail is what a rating verifier needs from one on-chain transaction. The
// RPC reports it as: {"hash","blockNumber","from","fromType","to","value",
// "recipientData","proof","executionResult",…} (shape verified live against
// rpc.nimiqwatch.com). recipientData is the hex of the transaction's data
// field, i.e. the memo.
type TxDetail struct {
	Hash     string
	From     string   // normalised NQ address, no spaces: the debited account
	FromType int      // AccountTypeBasic, AccountTypeVesting, AccountTypeHTLC, …
	Signers  []string // normalised addresses of the keys that signed the proof (see proof.go)
	To       string   // normalised NQ address, no spaces
	Value    int64    // Luna
	Data     []byte   // decoded recipientData (the memo bytes)
	Mined    bool     // a block number is present
	Success  bool     // executionResult is true
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
		if r.d.From != agreed.From || r.d.FromType != agreed.FromType || r.d.To != agreed.To || r.d.Value != agreed.Value ||
			string(r.d.Data) != string(agreed.Data) || r.d.Success != agreed.Success || !slices.Equal(r.d.Signers, agreed.Signers) {
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
		FromType        int     `json:"fromType"`
		To              string  `json:"to"`
		Value           int64   `json:"value"`
		RecipientData   string  `json:"recipientData"`
		Proof           string  `json:"proof"`
		ExecutionResult *bool   `json:"executionResult"`
	}
	if err := c.callURL(ctx, baseURL, "getTransactionByHash", []interface{}{hash}, &out); err != nil {
		// A hash the node has not seen yet (still propagating, or never
		// broadcast) is answered with an RPC *error* by Albatross nodes
		// ("Transaction not found: <hash>"), not with a null result. That is
		// "not found", not an outage.
		if isTxNotFound(err) {
			return TxDetail{}, false, nil
		}
		return TxDetail{}, false, err
	}
	if out.Hash == nil {
		return TxDetail{}, false, nil
	}
	data, err := hex.DecodeString(out.RecipientData)
	if err != nil {
		return TxDetail{}, false, errors.New("recipientData is not hex")
	}
	// The proof is informational for the verifier (who signed); a malformed
	// one yields no signers rather than an error.
	proof, _ := hex.DecodeString(out.Proof)
	return TxDetail{
		Hash:     strings.ToLower(*out.Hash),
		From:     NormalizeAddress(out.From),
		FromType: out.FromType,
		Signers:  ProofSigners(out.FromType, proof),
		To:       NormalizeAddress(out.To),
		Value:    out.Value,
		Data:     data,
		Mined:    out.BlockNumber != nil,
		Success:  out.ExecutionResult != nil && *out.ExecutionResult,
	}, true, nil
}

func isTxNotFound(err error) bool {
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	text := strings.ToLower(rpcErr.Message + " " + rpcErr.Data)
	return strings.Contains(text, "transaction not found") || strings.Contains(text, "unknown transaction")
}
