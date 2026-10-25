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
				// What rpc.nimiqwatch.com ACTUALLY answers for an address that
				// stakes nothing (captured 2026-10-25): the verdict lives in
				// `data`, while `message` is a generic "Internal error". The
				// strip used to carry the older message-only wording, which is
				// why a 502 for unstaked wallets never showed up in CI.
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error","data":"No staker with address: NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6"}}`))
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

func TestPublicWalletStakeWithoutShopLogin(t *testing.T) {
	var calls int64
	rpc := chainStub(t, chainStubOptions{staking: true, calls: &calls})
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})
	address := "NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082"
	route := "/api/wallet/stake?address=" + address
	res := s.do(http.MethodGet, route, nil, nil)
	if res.status != 200 {
		t.Fatalf("guest stake: %d %s", res.status, res.body)
	}
	j := res.json(t)
	if str(j["address"]) != address || num(j["staked_nim"]) != 100 || num(j["inactive_nim"]) != 0.005 {
		t.Fatalf("stake response: %v", j)
	}
	if _, exists := j["balance_nim"]; exists {
		t.Fatal("stake endpoint must not read or return liquid funds")
	}
	first := atomic.LoadInt64(&calls)
	if first != 1 {
		t.Fatalf("wanted one fixed staking query, got %d", first)
	}
	if got := s.do(http.MethodGet, route, nil, nil); got.status != 200 {
		t.Fatalf("cached: %d", got.status)
	}
	if atomic.LoadInt64(&calls) != first {
		t.Fatal("cached guest read hit RPC again")
	}
	if got := s.do(http.MethodGet, route+"&fresh=1", nil, nil); got.status != 200 {
		t.Fatalf("fresh: %d", got.status)
	}
	if atomic.LoadInt64(&calls) != first+1 {
		t.Fatal("manual refresh did not bypass cache")
	}
	if got := s.do(http.MethodGet, "/api/wallet/stake?address=invalid", nil, nil); got.status != 400 {
		t.Fatalf("invalid: %d", got.status)
	}
	if atomic.LoadInt64(&calls) != first+1 {
		t.Fatal("invalid address reached RPC")
	}
}

// The owner's report (2026-10-25): "Hiç stake etmemiş bir adresle girince
// 'Stake miktarı okunamadı' çıkıyor … /api/wallet/stake 502 dönüyor. Örnek:
// NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6".
//
// An address that never staked has a stake of ZERO — that is a fact about the
// chain, not a failed read. The public endpoint must answer 200 with 0, and the
// session balance read must report its stake as known-zero (no
// "stake could not be read" line), for the exact response shape the live
// provider sends (verdict in `data`, "Internal error" in `message`).
func TestPublicWalletStakeForANeverStakedAddressIsZeroNot502(t *testing.T) {
	const address = "NQ146MRC30QRYDCBU9N8CVGA1TH7K5D1X2H6"
	rpc := chainStub(t, chainStubOptions{liquidLuna: 12400000})
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})

	res := s.do(http.MethodGet, "/api/wallet/stake?address="+address, nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("never-staked address: %d %s — want 200 with zero stake", res.status, res.body)
	}
	j := res.json(t)
	if got := str(j["address"]); normaliseAddress(got) != address {
		t.Fatalf("address = %q, want %q", got, address)
	}
	for _, key := range []string{"staked_nim", "inactive_nim", "retired_nim"} {
		if got := num(j[key]); got != 0 {
			t.Fatalf("%s = %v, want 0", key, got)
		}
	}

	// The same rule through the signed-in balance read: the stake is KNOWN and
	// zero, so the strip must not print "Stake miktarı okunamadı".
	auth, _ := s.signIn()
	bal := s.do(http.MethodGet, "/api/wallet/balance", nil, auth)
	if bal.status != http.StatusOK {
		t.Fatalf("wallet balance for an unstaked wallet: %d %s", bal.status, bal.body)
	}
	bj := bal.json(t)
	if got := num(bj["staked_nim"]); got != 0 {
		t.Fatalf("staked_nim = %v, want 0", got)
	}
	if available, exists := bj["stake_available"]; exists && available == false {
		t.Fatalf("stake_available = false for a definitive zero: %s", bal.body)
	}
}

// An address that DOES stake must keep working after the same normalization.
func TestPublicWalletStakeForAStakingAddressStillReads(t *testing.T) {
	rpc := chainStub(t, chainStubOptions{staking: true})
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})
	res := s.do(http.MethodGet, "/api/wallet/stake?address=NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082", nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("staked address: %d %s", res.status, res.body)
	}
	if got := num(res.json(t)["staked_nim"]); got != 100 {
		t.Fatalf("staked_nim = %v, want 100", got)
	}
}

func TestPublicWalletStakeOutageDoesNotPretendZero(t *testing.T) {
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer rpc.Close()
	s := bootStack(t, stackOptions{env: map[string]string{"NIMIQ_RPC_URL": rpc.URL}})
	res := s.do(http.MethodGet, "/api/wallet/stake?address=NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082", nil, nil)
	if res.status != 502 {
		t.Fatalf("outage: %d %s", res.status, res.body)
	}
}
