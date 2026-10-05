package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The owner's report (2026-10-05): "your wallet'deki NIM miktarım yanlış" —
// the shop showed 2 NIM while the wallet showed 102 NIM.
//
// Both numbers were real: `getAccountByAddress` (and the Mini App SDK's
// `getBalance`, which reads the same chain state) returns the LIQUID balance
// only, and the wallet's own figure adds the buyer's stake. Live example used
// for the stub below: 200,000 luna liquid and 10,000,000 luna actively staked
// (2 NIM spendable, 102 NIM held). The shop may only ever spend the liquid
// part, so the response must carry both — and the affordability maths must use
// the spendable one.
func TestWalletBalanceSeparatesSpendableFromStaked(t *testing.T) {
	var calls int64
	rpc := chainStub(t, chainStubOptions{liquidLuna: 200000, staking: true, calls: &calls})
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})
	auth, address := s.signIn()

	res := s.do(http.MethodGet, "/api/wallet/balance", nil, auth)
	if res.status != 200 {
		t.Fatalf("wallet balance: %d %s", res.status, res.body)
	}
	j := res.json(t)

	if got := str(j["address"]); normaliseAddress(got) != normaliseAddress(address) {
		t.Fatalf("address = %q, want the SESSION wallet %q", got, address)
	}
	if got := num(j["balance_luna"]); got != 200000 {
		t.Fatalf("balance_luna = %v, want 200000", got)
	}
	// 200,000 luna = 2 NIM, the number that is actually spendable.
	if got := num(j["balance_nim"]); !approx(got, 2) {
		t.Fatalf("balance_nim = %v, want 2 (the liquid balance)", got)
	}
	// 10,000,000 luna = 100 NIM delegated to a validator: never spendable,
	// but part of what the buyer's wallet calls their NIM.
	if got := num(j["staked_nim"]); !approx(got, 100) {
		t.Fatalf("staked_nim = %v, want 100", got)
	}
	if got := num(j["inactive_nim"]); !approx(got, 0.005) {
		t.Fatalf("inactive_nim = %v, want 0.005", got)
	}
	if got := num(j["retired_nim"]); got != 0 {
		t.Fatalf("retired_nim = %v, want 0", got)
	}
	// The figure the wallet app shows its owner: available + staked + inactive.
	if got := num(j["total_nim"]); !approx(got, 102.005) {
		t.Fatalf("total_nim = %v, want 102.005 — this is the number that was wrong", got)
	}
	if got := str(j["network"]); got != "mainnet" {
		t.Fatalf("network = %q, want mainnet", got)
	}
	if cached, _ := j["cached"].(bool); cached {
		t.Fatal("the first read must not be reported as cached")
	}
	if str(j["observed_at"]) == "" {
		t.Fatal("observed_at is empty: the UI shows the reading time on the card")
	}
	if available, _ := j["available"].(bool); !available {
		t.Fatal("available = false on a successful read")
	}

	// A page-view storm must not hammer the public RPC: the second read inside
	// the TTL is served from the 20-second cache and does not touch the chain.
	before := atomic.LoadInt64(&calls)
	res2 := s.do(http.MethodGet, "/api/wallet/balance", nil, auth)
	if res2.status != 200 {
		t.Fatalf("cached read: %d %s", res2.status, res2.body)
	}
	if cached, _ := res2.json(t)["cached"].(bool); !cached {
		t.Fatal("a read inside the TTL must be served from the cache")
	}
	if after := atomic.LoadInt64(&calls); after != before {
		t.Fatalf("a cached read hit the chain (%d calls, want %d)", after, before)
	}

	// The buyer tapped refresh: ?fresh=1 must bypass the cache.
	res3 := s.do(http.MethodGet, "/api/wallet/balance?fresh=1", nil, auth)
	if res3.status != 200 {
		t.Fatalf("fresh read: %d %s", res3.status, res3.body)
	}
	if cached, _ := res3.json(t)["cached"].(bool); cached {
		t.Fatal("?fresh=1 must not be answered from the cache")
	}
	if after := atomic.LoadInt64(&calls); after <= before {
		t.Fatalf("?fresh=1 did not read the chain (%d calls, want > %d)", after, before)
	}
}

// A wallet with no staking position: the public RPC answers an error for
// getStakerByAddress, which means "not staked", never "the lookup failed".
func TestWalletBalanceWithoutStakeIsStillAValidRead(t *testing.T) {
	rpc := chainStub(t, chainStubOptions{liquidLuna: 12400000})
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})
	auth, _ := s.signIn()

	res := s.do(http.MethodGet, "/api/wallet/balance", nil, auth)
	if res.status != 200 {
		t.Fatalf("wallet balance: %d %s", res.status, res.body)
	}
	j := res.json(t)
	if got := num(j["staked_nim"]); got != 0 {
		t.Fatalf("staked_nim = %v, want 0", got)
	}
	if got, want := num(j["total_nim"]), num(j["balance_nim"]); !approx(got, want) {
		t.Fatalf("total_nim = %v, want the spendable %v when nothing is staked", got, want)
	}
}

// A chain that cannot be read is NOT a zero balance: the buyer must be able to
// tell "0 NIM" from "we could not look", so the endpoint answers 502 with a
// retry hint and no figure at all.
func TestWalletBalanceChainFailureIsNotAZeroBalance(t *testing.T) {
	rpc := chainStub(t, chainStubOptions{liquidLuna: 200000, staking: true, failLiquid: true})
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})
	auth, _ := s.signIn()

	res := s.do(http.MethodGet, "/api/wallet/balance", nil, auth)
	if res.status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", res.status, res.body)
	}
	if got := res.headers.Get("Retry-After"); got != "3" {
		t.Fatalf("Retry-After = %q, want 3", got)
	}
	if strings.Contains(string(res.body), "balance_nim") {
		t.Fatalf("a failed lookup must not answer with a figure: %s", res.body)
	}
}

// chainStubOptions describes one fake chain state.
type chainStubOptions struct {
	liquidLuna int64
	staking    bool
	failLiquid bool
	calls      *int64
}

// chainStub answers the two public methods this endpoint reads, in the exact
// envelope rpc.nimiqwatch.com uses: {"result":{"data":…,"metadata":…}}.
func chainStub(t *testing.T, opts chainStubOptions) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if opts.calls != nil {
			atomic.AddInt64(opts.calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "getAccountByAddress":
			if opts.failLiquid {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"chain unavailable"}}`))
				return
			}
			// staticcheck QF1012: write the formatted line straight to the response.
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"data":{"address":"stub","balance":%d,"type":"basic"},"metadata":{"blockNumber":1}}}`, opts.liquidLuna)
		case "getStakerByAddress":
			if !opts.staking {
				// What the public RPC answers for an address that stakes nothing.
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"no staker with this address in this pool"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"data":{"address":"stub","balance":10000000,"delegation":"NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082","inactiveBalance":500,"retiredBalance":0},"metadata":{"blockNumber":1}}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func normaliseAddress(a string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(a), " ", ""))
}
