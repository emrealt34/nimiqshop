package chainstake

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nimiqshop/internal/nimiq"
)

// A real, checksum-valid pair: the buyer address from the live incident and
// the operator's validator (both verified against rpc.nimiqwatch.com).
const (
	buyerAddr = "NQ73SE1XYRRFQ8NCDQCPHLJMNR858P7V2HPD"
	validator = "NQ49N8MBXYCRXBUP404CKXKKL49MA7BTF082"
	otherVal  = "NQ12CPDG4KKU70UBNC7U0Q82T8U7US17PYHU"
)

// rpcServer answers getStakerByAddress with the given payload, counting calls.
func rpcServer(t *testing.T, payload map[string]any, errMsg string, calls *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if errMsg != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32603, "message": errMsg},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"data": payload, "metadata": map[string]any{"blockNumber": 63_301_077}},
		})
	}))
}

func verifierFor(t *testing.T, payload map[string]any, errMsg string, calls *int64) *Verifier {
	t.Helper()
	srv := rpcServer(t, payload, errMsg, calls)
	t.Cleanup(srv.Close)
	return New(nimiq.NewClient(srv.URL), validator, 0, 0)
}

func TestVerifyDelegationToOurValidatorIsStaked(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{
		"address": "NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD",
		"balance": 10_000_000, "delegation": "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082",
		"inactiveBalance": 0,
	}, "", &calls)

	got, err := v.Verify(context.Background(), buyerAddr)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !got.Staked || got.StakeLuna != 10_000_000 {
		t.Fatalf("want staked 10000000 luna, got %+v", got)
	}
	if got.Delegation != validator {
		t.Fatalf("delegation not canonicalised: %q", got.Delegation)
	}
	// Spaced input must hit the same cache entry as the compact form.
	if _, err := v.Verify(context.Background(), "NQ73 SE1X YRRF Q8NC DQCP HLJM NR85 8P7V 2HPD"); err != nil {
		t.Fatalf("spaced verify: %v", err)
	}
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("want 1 RPC call (cache), got %d", n)
	}
}

func TestVerifyDelegationToAnotherValidatorIsNotStaked(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{
		"balance": 500_000, "delegation": "NQ12 CPDG 4KKU 70UB NC7U 0Q82 T8U7 US17 PYHU",
	}, "", &calls)

	got, err := v.Verify(context.Background(), buyerAddr)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Staked || got.StakeLuna != 0 {
		t.Fatalf("a delegation to someone else is not our staker: %+v", got)
	}
}

func TestVerifyZeroBalanceIsNotStaked(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 0, "delegation": "NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082"},
		"", &calls)
	got, err := v.Verify(context.Background(), buyerAddr)
	if err != nil || got.Staked {
		t.Fatalf("zero balance must not be staked: %+v err=%v", got, err)
	}
}

// The RPC answers "no staking record" as a JSON-RPC error. That is a
// definitive "not staked" — never an outage, or every non-staker would look
// like a broken lookup and keep retrying forever.
func TestVerifyNoStakerRecordIsDefinitive(t *testing.T) {
	var calls int64
	v := verifierFor(t, nil, "No staker found for the given address", &calls)
	got, err := v.Verify(context.Background(), buyerAddr)
	if err != nil {
		t.Fatalf("no-record must not be an error: %v", err)
	}
	if got.Staked {
		t.Fatalf("no record must be not-staked: %+v", got)
	}
}

// A real RPC failure stays an error: callers rely on that to avoid resetting
// a staker's loyalty clock or downgrading a known staker.
func TestVerifyTransportErrorIsAnError(t *testing.T) {
	v := New(nimiq.NewClient("http://127.0.0.1:1"), validator, 0, 0) // closed port
	if _, err := v.Verify(context.Background(), buyerAddr); err == nil {
		t.Fatal("want an error when the RPC cannot be reached")
	}
}

// An address that fails its own checksum can never be a staker. It must be
// answered WITHOUT a network call — this is the shape that produced an
// endless 400-retry loop against the pool in production.
func TestVerifyInvalidAddressNeverCallsTheNetwork(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 1, "delegation": validator}, "", &calls)
	got, err := v.Verify(context.Background(), "NQ08D44A44B90F772E228C13C3456FBDXKH9")
	if err != nil {
		t.Fatalf("invalid address must be a clean 'no': %v", err)
	}
	if got.Staked {
		t.Fatal("invalid address must not be staked")
	}
	if n := atomic.LoadInt64(&calls); n != 0 {
		t.Fatalf("invalid address must not reach the RPC, got %d calls", n)
	}
}

func TestNegativeAnswersAreCachedBriefly(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 0}, "", &calls)
	for i := 0; i < 3; i++ {
		if _, err := v.Verify(context.Background(), buyerAddr); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("negative answers must be cached, got %d calls", n)
	}
	if v.ttl <= 0 || v.negativeTTL <= 0 || v.negativeTTL > v.ttl {
		t.Fatalf("negative TTL must be positive and shorter than the positive one: %v / %v", v.ttl, v.negativeTTL)
	}
}

func TestForgetDropsTheCachedAnswer(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 0}, "", &calls)
	if _, err := v.Verify(context.Background(), buyerAddr); err != nil {
		t.Fatal(err)
	}
	v.Forget(buyerAddr)
	if _, err := v.Verify(context.Background(), buyerAddr); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&calls); n != 2 {
		t.Fatalf("Forget must force a fresh lookup, got %d calls", n)
	}
}

func TestDisabledVerifierIsNotStakedAndReadyFalse(t *testing.T) {
	for name, v := range map[string]*Verifier{
		"no client":    New(nil, validator, 0, 0),
		"no validator": New(nimiq.NewClient("http://127.0.0.1:1"), "", 0, 0),
	} {
		if v.Ready() {
			t.Fatalf("%s: must not be ready", name)
		}
		got, err := v.Verify(context.Background(), buyerAddr)
		if err != nil || got.Staked {
			t.Fatalf("%s: disabled verifier must answer not-staked: %+v err=%v", name, got, err)
		}
	}
}

func TestCacheIsBounded(t *testing.T) {
	var calls int64
	v := verifierFor(t, map[string]any{"balance": 0}, "", &calls)
	v.ttl = time.Millisecond
	v.negativeTTL = time.Millisecond
	for i := 0; i < 5000; i++ {
		if _, err := v.Verify(context.Background(), buyerAddr); err != nil {
			t.Fatal(err)
		}
	}
	v.mu.Lock()
	n := len(v.cache)
	v.mu.Unlock()
	if n > 4096 {
		t.Fatalf("cache must stay bounded, got %d entries", n)
	}
}
