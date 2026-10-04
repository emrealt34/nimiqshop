// Package nimiq provides Nimiq chain utilities: address derivation and
// signature verification for wallet login, plus a minimal RPC client used
// by the operator notification feature. shop.nimiqbase.com itself is non-custodial:
// it never watches addresses for incoming payments and never holds customer
// funds. We deliberately don't run a full node — the tradeoff is trusting
// the RPC provider's view of the chain, which is fine for the notification
// feature but should move to a self-hosted node once volume justifies it.
//
// Two independent public RPCs are the default (nimiqwatch + nimiqscan).
// Read methods (height, tx receipt) require them to agree before the
// cashback worker treats a result as fact; broadcasts are submitted to
// every configured endpoint so a single provider outage cannot stall a send.
package nimiq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxHeightSkew is how far two healthy RPCs may disagree on head height
// before we refuse to sign. Albatross blocks are ~1s; a few blocks of
// lag is normal, a large gap means one provider is on a different view.
const maxHeightSkew = int64(10)

type Client struct {
	urls []string
	http *http.Client
}

// NewClient builds an RPC client. Empty / duplicate URLs are dropped.
// One URL = no cross-check. Two or more = reads must agree.
func NewClient(urls ...string) *Client {
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		u = strings.TrimSpace(strings.TrimRight(u, "/"))
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return &Client{
		urls: out,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// Endpoints is the de-duplicated RPC URL list (for logs / health).
func (c *Client) Endpoints() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.urls...)
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// callURL posts one JSON-RPC request. NimiqWatch-style endpoints (rpc.nimiqwatch.com,
// rpc.testnet.nimiqwatch.com) wrap every result as {"data": <payload>,
// "metadata": …}; that wrapper is unwrapped here so every caller below can
// unmarshal the payload directly (int for getBlockNumber, string for
// pushTransaction, object for getAccountByAddress, array for
// getTransactionsByAddress). Endpoints that return the payload bare keep
// working — the unwrap only applies when the wrapper is present.
func (c *Client) callURL(ctx context.Context, baseURL, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nimiq rpc %s %s: %w", baseURL, method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var rr rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return fmt.Errorf("decode rpc response from %s: %w", baseURL, err)
	}
	if rr.Error != nil {
		return fmt.Errorf("nimiq rpc %s error %d: %s", baseURL, rr.Error.Code, rr.Error.Message)
	}
	if out != nil {
		if len(rr.Result) == 0 || string(rr.Result) == "null" {
			return nil
		}
		// Unwrap {"data": <payload>, "metadata": …} when present. Both keys
		// must exist — a bare transaction object may itself carry a "data"
		// (memo) field and must never be mistaken for the wrapper.
		var probe map[string]json.RawMessage
		if json.Unmarshal(rr.Result, &probe) == nil {
			if d, hasData := probe["data"]; hasData && len(d) > 0 {
				if _, hasMeta := probe["metadata"]; hasMeta {
					rr.Result = d
				}
			}
		}
		return json.Unmarshal(rr.Result, out)
	}
	return nil
}

func (c *Client) requireURLs() error {
	if c == nil || len(c.urls) == 0 {
		return fmt.Errorf("no nimiq rpc url configured")
	}
	return nil
}

// Transaction mirrors the fields we actually use from getTransactionsByAddress
// (shape verified live against rpc.nimiqwatch.com). Nimiq amounts are in Luna
// (1 NIM = 100000 Luna); we convert to NIM.
type Transaction struct {
	Hash          string `json:"hash"`
	From          string `json:"from"`
	To            string `json:"to"`
	Value         int64  `json:"value"` // Luna
	BlockHeight   int64  `json:"blockNumber"`
	Timestamp     int64  `json:"timestamp"`
	Confirmations int64  `json:"confirmations"`
}

func (t Transaction) ValueNIM() float64 {
	return float64(t.Value) / 100000.0
}

// GetAccountBalance reads a public account's current balance in Luna.
// callURL already unwraps NimiqWatch's {"data": …} envelope, so the account
// object arrives bare. No signing or wallet access is involved.
func (c *Client) GetAccountBalance(ctx context.Context, addr string) (int64, error) {
	if err := c.requireURLs(); err != nil {
		return 0, err
	}
	var out struct {
		Balance int64 `json:"balance"`
	}
	// A public balance read is safe to use against the primary endpoint. The
	// handler adds a short cache so this endpoint is not hit on every page view.
	if err := c.callURL(ctx, c.urls[0], "getAccountByAddress", []interface{}{addr}, &out); err != nil {
		return 0, err
	}
	return out.Balance, nil
}

// StakerInfo is the chain's answer about one address' staking position
// (getStakerByAddress). Balance is the ACTIVE stake in Luna; Delegation is the
// validator the address delegates to ("NQ49 …" spaced form, as the RPC prints
// it); InactiveBalance is stake that is no longer active (kept so a caller can
// tell "unstaked" from "never staked").
//
// Verified live: rpc.nimiqwatch.com returns
// {"data":{"address":"…","balance":10000000,"delegation":"NQ49 …","inactiveBalance":0,
// "inactiveFrom":null,"retiredBalance":0},"metadata":{…}} and callURL already
// unwraps the envelope. An address with no staking record makes the RPC answer
// an error, which is surfaced as an error here (callers treat "no record" as
// not staked, never as a lookup failure).
type StakerInfo struct {
	Address         string `json:"address"`
	Balance         int64  `json:"balance"`
	Delegation      string `json:"delegation"`
	InactiveBalance int64  `json:"inactiveBalance"`
	RetiredBalance  int64  `json:"retiredBalance"`
}

// HasStake reports whether the chain sees an active stake worth using.
func (s StakerInfo) HasStake() bool { return s.Balance > 0 }

// GetStaker reads the address' staking position from the chain. This is the
// same provider used for payment verification, so no new dependency is added
// and no wallet access is involved — a staking position is public data.
func (c *Client) GetStaker(ctx context.Context, addr string) (StakerInfo, error) {
	if err := c.requireURLs(); err != nil {
		return StakerInfo{}, err
	}
	var out StakerInfo
	if err := c.callURL(ctx, c.urls[0], "getStakerByAddress", []interface{}{addr}, &out); err != nil {
		return StakerInfo{}, err
	}
	return out, nil
}

// GetTransactionsByAddress returns recent transactions touching addr, newest
// first. NimiqWatch's third parameter is a pagination cursor: the hash of the
// LAST transaction of the previous page (pass "" for… a rejected empty string
// is not accepted, so callers page by always passing the previous last hash;
// the first page starts from a known recent tx hash). Cursor semantics
// verified live: with cursor = an account's Nth tx hash, older txs than that
// hash are returned.
// Cross-check: every RPC must succeed; we return the primary list when the
// first-hash of each list matches (same head of history).
func (c *Client) GetTransactionsByAddress(ctx context.Context, addr string, limit int, cursor string) ([]Transaction, error) {
	if err := c.requireURLs(); err != nil {
		return nil, err
	}
	if len(c.urls) == 1 {
		var txs []Transaction
		err := c.callURL(ctx, c.urls[0], "getTransactionsByAddress", []interface{}{addr, limit, cursor}, &txs)
		return txs, err
	}
	type res struct {
		txs []Transaction
		err error
	}
	out := make([]res, len(c.urls))
	var wg sync.WaitGroup
	for i, u := range c.urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			var txs []Transaction
			err := c.callURL(ctx, u, "getTransactionsByAddress", []interface{}{addr, limit, cursor}, &txs)
			out[i] = res{txs, err}
		}(i, u)
	}
	wg.Wait()
	for _, r := range out {
		if r.err != nil {
			return nil, fmt.Errorf("nimiq rpc cross-check incomplete: %w", r.err)
		}
	}
	primary := out[0].txs
	if len(primary) > 0 {
		for i := 1; i < len(out); i++ {
			if len(out[i].txs) == 0 || out[i].txs[0].Hash != primary[0].Hash {
				return nil, fmt.Errorf("nimiq rpc cross-check: transaction lists disagree")
			}
		}
	}
	return primary, nil
}

// GetBlockNumber returns the current head height. With two RPCs the lower
// of the two agreeing heights is used (safer validity-start for signing).
func (c *Client) GetBlockNumber(ctx context.Context) (int64, error) {
	if err := c.requireURLs(); err != nil {
		return 0, err
	}
	type res struct {
		h   int64
		err error
	}
	out := make([]res, len(c.urls))
	var wg sync.WaitGroup
	for i, u := range c.urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			var h int64
			err := c.callURL(ctx, u, "getBlockNumber", nil, &h)
			out[i] = res{h, err}
		}(i, u)
	}
	wg.Wait()
	var heights []int64
	var firstErr error
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		heights = append(heights, r.h)
	}
	if len(c.urls) == 1 {
		if firstErr != nil {
			return 0, firstErr
		}
		return heights[0], nil
	}
	if len(heights) < len(c.urls) {
		if firstErr == nil {
			firstErr = fmt.Errorf("nimiq rpc cross-check incomplete")
		}
		return 0, fmt.Errorf("nimiq rpc cross-check incomplete: %w", firstErr)
	}
	min, max := heights[0], heights[0]
	for _, h := range heights[1:] {
		if h < min {
			min = h
		}
		if h > max {
			max = h
		}
	}
	if max-min > maxHeightSkew {
		return 0, fmt.Errorf("nimiq rpc cross-check: head height skew %d (limit %d)", max-min, maxHeightSkew)
	}
	return min, nil
}

// BroadcastRawTransaction submits an already-signed, hex-encoded Nimiq
// transaction to every configured RPC (method: pushTransaction — the
// Albatross JSON-RPC used by rpc.nimiqwatch.com; the response payload is the
// tx hash as a bare JSON string after the data-envelope unwrap in callURL).
// Returns the first accepted hash.
func (c *Client) BroadcastRawTransaction(ctx context.Context, txHex string) (string, error) {
	if err := c.requireURLs(); err != nil {
		return "", err
	}
	type res struct {
		hash string
		err  error
		url  string
	}
	out := make([]res, len(c.urls))
	var wg sync.WaitGroup
	for i, u := range c.urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			var hash string
			err := c.callURL(ctx, u, "pushTransaction", []interface{}{txHex}, &hash)
			out[i] = res{hash, err, u}
		}(i, u)
	}
	wg.Wait()
	var accepted string
	var firstErr error
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			log.Printf("nimiq rpc broadcast %s: %v", r.url, r.err)
			continue
		}
		if r.hash == "" {
			continue
		}
		if accepted == "" {
			accepted = r.hash
		} else if accepted != r.hash {
			log.Printf("nimiq rpc broadcast hash mismatch: %s vs %s", accepted, r.hash)
		}
	}
	if accepted != "" {
		return accepted, nil
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("nimiq rpc broadcast: empty hash from all endpoints")
	}
	return "", firstErr
}

type txLookup struct {
	found     bool
	confirmed bool
	err       error
}

// getTxOne asks one RPC about a transaction hash. Response shape (verified
// live): {"hash", "blockNumber", "confirmations", "executionResult", …} —
// a mempool tx has no blockNumber/confirmations yet.
func (c *Client) getTxOne(ctx context.Context, baseURL, hash string) (found bool, confirmed bool, err error) {
	var out struct {
		Hash            *string `json:"hash"`
		BlockNumber     *int64  `json:"blockNumber"`
		Confirmations   int64   `json:"confirmations"`
		ExecutionResult *bool   `json:"executionResult"`
	}
	if err := c.callURL(ctx, baseURL, "getTransactionByHash", []interface{}{hash}, &out); err != nil {
		return false, false, err
	}
	if out.Hash == nil {
		return false, false, nil // unknown to this RPC
	}
	confirmed = (out.Confirmations > 0 && out.ExecutionResult != nil && *out.ExecutionResult) ||
		(out.BlockNumber != nil && out.ExecutionResult != nil && *out.ExecutionResult)
	return true, confirmed, nil
}

// GetTransactionByHash reports whether a transaction with this hash exists on
// the network (accepted, mining or confirmed) and returns its receipt status.
// With two RPCs both must succeed; confirmed only when BOTH say confirmed.
func (c *Client) GetTransactionByHash(ctx context.Context, hash string) (found bool, confirmed bool, err error) {
	if err := c.requireURLs(); err != nil {
		return false, false, err
	}
	if len(c.urls) == 1 {
		return c.getTxOne(ctx, c.urls[0], hash)
	}
	out := make([]txLookup, len(c.urls))
	var wg sync.WaitGroup
	for i, u := range c.urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			f, conf, e := c.getTxOne(ctx, u, hash)
			out[i] = txLookup{f, conf, e}
		}(i, u)
	}
	wg.Wait()
	var firstErr error
	ok := 0
	foundN, confN := 0, 0
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		ok++
		if r.found {
			foundN++
		}
		if r.confirmed {
			confN++
		}
	}
	if ok < len(c.urls) {
		if firstErr == nil {
			firstErr = fmt.Errorf("nimiq rpc cross-check incomplete")
		}
		return false, false, fmt.Errorf("nimiq rpc cross-check incomplete: %w", firstErr)
	}
	if foundN == 0 {
		return false, false, nil
	}
	if foundN < len(c.urls) {
		// One RPC sees it, the other does not — wait, do not mark paid.
		return false, false, nil
	}
	return true, confN == len(c.urls), nil
}
