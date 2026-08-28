// Package nimiq provides Nimiq chain utilities: address derivation and
// signature verification for wallet login, plus a minimal RPC client used
// by the operator notification feature. nim.shop itself is non-custodial:
// it never watches addresses for incoming payments and never holds customer
// funds. We deliberately don't run a full node — the tradeoff is trusting
// the RPC provider's view of the chain, which is fine for the notification
// feature but should move to a self-hosted node once volume justifies it.
package nimiq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
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

func (c *Client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nimiq rpc call %s: %w", method, err)
	}
	defer resp.Body.Close()

	var rr rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return fmt.Errorf("decode rpc response: %w", err)
	}
	if rr.Error != nil {
		return fmt.Errorf("nimiq rpc error %d: %s", rr.Error.Code, rr.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(rr.Result, out)
	}
	return nil
}

// Transaction mirrors the fields we actually use from getTransactionsByAddress.
// Nimiq amounts are returned in Luna (1 NIM = 100000 Luna); we convert to NIM.
type Transaction struct {
	Hash          string `json:"hash"`
	From          string `json:"from"`
	To            string `json:"to"`
	Value         int64  `json:"value"` // Luna
	BlockHeight   int64  `json:"blockHeight"`
	Timestamp     int64  `json:"timestamp"`
	Confirmations int64  `json:"confirmations"`
}

func (t Transaction) ValueNIM() float64 {
	return float64(t.Value) / 100000.0
}

// GetTransactionsByAddress returns recent transactions touching addr, newest first.
// This is the standard Nimiq JSON-RPC method name; nimiqwatch's public RPC
// implements the same spec as a core node.
func (c *Client) GetTransactionsByAddress(ctx context.Context, addr string, limit int) ([]Transaction, error) {
	var txs []Transaction
	err := c.call(ctx, "getTransactionsByAddress", []interface{}{addr, limit}, &txs)
	return txs, err
}

// GetBlockNumber returns the current head height, used to compute confirmations
// if the RPC response doesn't already include them.
func (c *Client) GetBlockNumber(ctx context.Context) (int64, error) {
	var height int64
	err := c.call(ctx, "getBlockNumber", nil, &height)
	return height, err
}

// BroadcastRawTransaction submits an already-signed, hex-encoded Nimiq
// transaction to the network via the standard mining_sendRawTransaction RPC.
// Returns the transaction hash the network accepted. This is the send path the
// 1-Luna notification system uses to push a memo to a buyer's wallet.
func (c *Client) BroadcastRawTransaction(ctx context.Context, txHex string) (string, error) {
	var hash string
	if err := c.call(ctx, "mining_sendRawTransaction", []interface{}{txHex}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// GetTransactionByHash reports whether a transaction with this hash exists on
// the network (accepted, mining or confirmed) and returns its receipt status.
// Crash-recovery primitive: before re-broadcasting a persisted refund the
// worker asks the chain whether the original broadcast already landed.
func (c *Client) GetTransactionByHash(ctx context.Context, hash string) (found bool, confirmed bool, err error) {
	var out map[string]interface{}
	if err := c.call(ctx, "getTransactionByHash", []interface{}{hash}, &out); err != nil {
		if err != nil && out == nil {
			// Most public RPCs answer "not found" with null result (no error).
			// Some answer with an error; treat any error as "not found yet"
			// ONLY when the caller is in a recovery context — otherwise the
			// broadcast path surfaces it.
		}
		return false, false, err
	}
	if out == nil {
		return false, false, nil
	}
	// The receipt shape varies by RPC implementation; the presence of a
	// "confirmationCount"/"confirmation" field above zero means mined.
	if cc, ok := out["confirmationCount"]; ok {
		if f, ok2 := cc.(float64); ok2 && f > 0 {
			return true, true, nil
		}
	}
	if rc, ok := out["receipt"]; ok && rc != nil {
		return true, true, nil
	}
	return true, false, nil
}
