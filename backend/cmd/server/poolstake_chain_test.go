package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPoolStakeMeFallsBackToTheChain is the regression test for the live
// incident of 2026-10-04: the pool's own staker index can be empty/behind
// (observed in production: num_stakers 0 while the chain showed delegators to
// the pool's validator), and when it was, every real staker was shown — and
// paid — the non-staker rate of 0.
//
// The pool here does exactly what production did: a 404 for every address. The
// chain answers with the buyer's real delegation. The storefront endpoint must
// still report the buyer as staked, at the staker base rate.
func TestPoolStakeMeFallsBackToTheChain(t *testing.T) {
	const validator = "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082"

	var chainCalls int64
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		atomic.AddInt64(&chainCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "getStakerByAddress" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		// The buyer's position, exactly as rpc.nimiqwatch.com reports it.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"data":{
			"address":"NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD",
			"balance":10000000,
			"delegation":"NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082",
			"inactiveBalance":0,"retiredBalance":0},
			"metadata":{"blockNumber":63301077}}}`))
	}))
	defer rpc.Close()

	// The pool's index is empty, like production: every address is unknown.
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/stakers/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no staker with this address in this pool"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer pool.Close()

	s := bootStack(t, stackOptions{env: map[string]string{
		"POOL_API_URL":                 pool.URL,
		"POOL_VALIDATOR_ADDRESS":       validator,
		"POOL_FEED_API_KEY":            "test-feed-key",
		"NIMIQ_RPC_URL":                rpc.URL,
		"POOL_STAKE_CACHE_TTL_SECONDS": "60",
	}})

	auth, address := s.signIn()

	res := s.do(http.MethodGet, "/api/poolstake/me", nil, auth)
	if res.status != 200 {
		t.Fatalf("poolstake/me: %d %s", res.status, res.body)
	}
	body := res.json(t)

	if body["staked"] != true {
		t.Fatalf("a buyer the chain shows as delegating to our validator must be staked; got %v (%s)", body["staked"], res.body)
	}
	stakeNIM, _ := body["stake_nim"].(float64)
	if stakeNIM != 100 {
		t.Fatalf("stake_nim = %v, want 100 (10000000 luna)", body["stake_nim"])
	}
	if bps, _ := body["cashback_bps"].(float64); bps < 100 {
		t.Fatalf("cashback_bps = %v, want at least the staker base (100)", body["cashback_bps"])
	}
	if src, _ := body["stake_source"].(string); src != "chain" {
		t.Fatalf("stake_source = %q, want \"chain\" so the fallback is visible in production", src)
	}
	if body["stake_check_error"] != nil {
		t.Fatalf("a chain-verified stake is not an error state: %v", body["stake_check_error"])
	}
	if n := atomic.LoadInt64(&chainCalls); n == 0 {
		t.Fatal("the chain was never asked")
	}
	if address == "" {
		t.Fatal("no test address")
	}

	// The same buyer's base rate must be the staker base in the fulfillment
	// resolver too (admin user detail reads exactly that resolver).
	users := s.do(http.MethodGet, "/api/admin/users?sort=registered&dir=desc&limit=20", nil, s.adminHeaders())
	if users.status != 200 {
		t.Fatalf("admin users: %d %s", users.status, users.body)
	}
	var list struct {
		Users []struct {
			ID string `json:"id"`
		} `json:"users"`
	}
	if err := json.Unmarshal(users.body, &list); err != nil {
		t.Fatalf("admin users json: %v", err)
	}
	if len(list.Users) == 0 {
		t.Fatal("no users in the admin list")
	}
	detail := s.do(http.MethodGet, "/api/admin/users/"+list.Users[0].ID, nil, s.adminHeaders())
	if detail.status != 200 {
		t.Fatalf("admin user detail: %d %s", detail.status, detail.body)
	}
	var dv struct {
		Rate struct {
			Staked   bool    `json:"staked"`
			StakeNIM float64 `json:"stake_nim"`
			BaseBps  int     `json:"base_bps"`
		} `json:"rate"`
	}
	if err := json.Unmarshal(detail.body, &dv); err != nil {
		t.Fatalf("admin detail json: %v", err)
	}
	if !dv.Rate.Staked || dv.Rate.StakeNIM != 100 || dv.Rate.BaseBps < 100 {
		t.Fatalf("the money path must see the same stake: %+v", dv.Rate)
	}
}

// TestPoolStakeMeWithNoChainAnswerStaysNotStaked: when neither the pool nor the
// chain can confirm a delegation, the buyer keeps the base rate — the fallback
// must never invent a stake.
func TestPoolStakeMeWithNoChainAnswerStaysNotStaked(t *testing.T) {
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A valid address with no staking position: the RPC answers an error.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"No staker found for the given address"}}`))
	}))
	defer rpc.Close()
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no staker with this address in this pool"}`))
	}))
	defer pool.Close()

	s := bootStack(t, stackOptions{env: map[string]string{
		"POOL_API_URL":           pool.URL,
		"POOL_VALIDATOR_ADDRESS": "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082",
		"POOL_FEED_API_KEY":      "test-feed-key",
		"NIMIQ_RPC_URL":          rpc.URL,
	}})
	auth, _ := s.signIn()

	body := s.do(http.MethodGet, "/api/poolstake/me", nil, auth).json(t)
	if body["staked"] != false {
		t.Fatalf("no delegation anywhere must stay not-staked: %v", body["staked"])
	}
	if bps, _ := body["cashback_bps"].(float64); bps != 0 {
		t.Fatalf("cashback_bps = %v, want 0", body["cashback_bps"])
	}
}
