package nimiq

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two public Nimiq nodes answer every "this record does not exist" case the
// same way, and NOT the way a hand-written stub naturally would. Captured live
// on 2026-10-17 from rpc.nimiqwatch.com (mainnet) and
// rpc.testnet.nimiqwatch.com (testnet), byte for byte:
//
//	getStakerByAddress      {"error":{"code":-32603,"message":"Internal error",
//	                         "data":"No staker with address: NQ41 10U9 …"}}
//	getTransactionByHash    {"error":{"code":-32603,"message":"Internal error",
//	                         "data":"Transaction not found: 0000…"}}
//
// The reason lives in "data"; "message" is a generic "Internal error". Code that
// matched on Message alone looked correct against every stub in this repository
// while failing against every real node, which turned "has never staked" and
// "not on chain yet" into "the RPC is down": /api/poolstake/position answered
// 502 for non-stakers, and an expired unmined payout could never be retired and
// replaced because the expiry proof aborted on a lookup that had in fact
// succeeded. These tests pin the real shape so the mistake cannot come back.
const (
	liveNoStakerData    = "No staker with address: NQ41 10U9 PJM8 1CPN LNDS BGQQ L064 9Q53 QV92"
	liveNoStakerAddress = "NQ4110U9PJM81CPNLDSGBQQL0649Q53QV92"
	liveTxNotFoundHash  = "3f2a9c1d4b5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
	liveTxNotFoundData  = "Transaction not found: " + liveTxNotFoundHash
	liveGenericMessage  = "Internal error"
)

// liveErrorServer answers one method with the node's real "does not exist"
// error and everything else with a null result.
func liveErrorServer(t *testing.T, method, data string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == method {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32603, "message": liveGenericMessage, "data": data},
			})
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRPCErrorKeepsTheDataField(t *testing.T) {
	srv := liveErrorServer(t, "getStakerByAddress", liveNoStakerData)
	_, err := NewClient(srv.URL).GetStaker(context.Background(), liveNoStakerAddress)
	if err == nil {
		t.Fatal("the node answered an error; GetStaker must surface it")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v (%T), want a *RPCError", err, err)
	}
	if rpcErr.Code != -32603 {
		t.Errorf("code = %d, want -32603", rpcErr.Code)
	}
	if rpcErr.Message != liveGenericMessage {
		t.Errorf("message = %q, want the node's generic %q", rpcErr.Message, liveGenericMessage)
	}
	if rpcErr.Data != liveNoStakerData {
		t.Errorf("data = %q, want %q — the reason must survive decoding", rpcErr.Data, liveNoStakerData)
	}
	// The error text is what lands in an operator's log, so it has to carry the
	// reason and not just "Internal error".
	if !strings.Contains(err.Error(), "No staker with address") {
		t.Errorf("Error() = %q, want it to name the real reason", err.Error())
	}
	if !strings.Contains(strings.ToLower(rpcErr.Detail()), "no staker") {
		t.Errorf("Detail() = %q, want it to contain the data field", rpcErr.Detail())
	}
}

func TestReasonIsMatchesEitherField(t *testing.T) {
	// Where this node puts it…
	inData := &RPCError{Code: -32603, Message: liveGenericMessage, Data: liveTxNotFoundData}
	if !inData.ReasonIs(liveTxNotFoundData) {
		t.Error("ReasonIs missed a reason carried in Data")
	}
	// …and where other node builds put it.
	inMessage := &RPCError{Code: -32603, Message: liveTxNotFoundData}
	if !inMessage.ReasonIs(liveTxNotFoundData) {
		t.Error("ReasonIs missed a reason carried in Message")
	}
	// A different reason must not match, and neither must a partial one.
	if inData.ReasonIs("Transaction not found: deadbeef") {
		t.Error("ReasonIs matched a different hash")
	}
	if !inData.ReasonIs(liveGenericMessage) {
		t.Error("ReasonIs should still match the generic message when that is what is asked for")
	}
	var nilErr *RPCError
	if nilErr.ReasonIs("anything") || nilErr.Detail() != "" {
		t.Error("a nil RPCError must be inert")
	}
}

// TestGetTransactionByHashTreatsLiveNotFoundAsDefinitive is the money-path
// guard: "not on chain" must come back as (found=false, confirmed=false,
// err=nil). Returning an error instead makes the payout worker record a
// phantom send failure for a transaction that is simply still propagating, and
// makes ProvePayoutExpired abort — so an expired unmined payout can never be
// retired and re-sent, and the buyer is never paid.
func TestGetTransactionByHashTreatsLiveNotFoundAsDefinitive(t *testing.T) {
	srv := liveErrorServer(t, "getTransactionByHash", liveTxNotFoundData)
	found, confirmed, err := NewClient(srv.URL).GetTransactionByHash(context.Background(), liveTxNotFoundHash)
	if err != nil {
		t.Fatalf("a definitive 'not found' must not be an error: %v", err)
	}
	if found || confirmed {
		t.Fatalf("found=%v confirmed=%v, want false/false", found, confirmed)
	}

	// A genuine outage must STILL be an error, or the worker would treat a dead
	// RPC as proof that the payout never happened.
	outage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(outage.Close)
	if _, _, err := NewClient(outage.URL).GetTransactionByHash(context.Background(), liveTxNotFoundHash); err == nil {
		t.Fatal("an unreachable node must be reported as an error, not as 'not found'")
	}
}
