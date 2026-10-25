package nimiq

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// The live answer from rpc.nimiqwatch.com for an address that never staked
// (captured 2026-10-25 with the owner's own example address):
//
//	{"jsonrpc":"2.0","id":1,
//	 "error":{"code":-32603,"message":"Internal error",
//	          "data":"No staker with address: NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6"}}
//
// The sentence a caller needs is in `data`; `message` is a generic
// "Internal error". Reading only `message` made an unstaked address look like a
// chain outage — /api/wallet/stake answered 502 and the wallet strip said the
// stake could not be read.
const liveNoStakerBody = `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error","data":"No staker with address: NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6"}}`

// The older NimiqWatch wording, which carries the verdict in `message`. Both
// shapes must be recognized — a provider upgrade must not change behaviour.
const legacyNoStakerBody = `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"no staker with this address in this pool"}}`

func rpcServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// GetStaker surfaces the provider's answer as ErrNoStaker, for BOTH shapes.
func TestGetStakerNoStakerIsTyped(t *testing.T) {
	for name, body := range map[string]string{
		"data member (live shape)": liveNoStakerBody,
		"message member (legacy)":  legacyNoStakerBody,
		"bare null result":         `{"jsonrpc":"2.0","id":1,"result":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := rpcServer(t, body)
			c := NewClient(srv.URL)
			_, err := c.GetStaker(context.Background(), "NQ146MRC30QRYDCBU9N8CVGA1TH7K5D1X2H6")
			if err == nil {
				t.Fatal("an address with no staking record must not be a silent success")
			}
			if !errors.Is(err, ErrNoStaker) || !IsNoStaker(err) {
				t.Fatalf("want ErrNoStaker, got %v", err)
			}
		})
	}
}

// A real outage stays an error: it must never be read as "not staked".
func TestGetStakerOutageIsNotNoStaker(t *testing.T) {
	bodies := map[string]string{
		"internal error, no data": `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`,
		"chain unavailable":       `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"chain unavailable"}}`,
		"rate limited":            `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"request rate limited"}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := rpcServer(t, body)
			c := NewClient(srv.URL)
			_, err := c.GetStaker(context.Background(), "NQ146MRC30QRYDCBU9N8CVGA1TH7K5D1X2H6")
			if err == nil {
				t.Fatal("an outage must surface as an error")
			}
			if IsNoStaker(err) {
				t.Fatalf("outage misread as 'no staker': %v", err)
			}
		})
	}
}

// An HTTP failure is not a staking verdict either.
func TestGetStakerHTTPFailureIsNotNoStaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL).GetStaker(context.Background(), "NQ146MRC30QRYDCBU9N8CVGA1TH7K5D1X2H6")
	if err == nil || IsNoStaker(err) {
		t.Fatalf("HTTP 503 must be an error and must not be a no-staker verdict: %v", err)
	}
}

// IsNoStaker must be safe on nil and on plain errors.
func TestIsNoStakerIgnoresNonRPCErrors(t *testing.T) {
	if IsNoStaker(nil) {
		t.Fatal("nil is not a verdict")
	}
	if IsNoStaker(errors.New("no staker with this address in this pool")) {
		t.Fatal("a plain error is not an RPC answer and must not be trusted")
	}
	if !IsNoStaker(ErrNoStaker) || !IsNoStaker(errors.Join(errors.New("x"), ErrNoStaker)) {
		t.Fatal("the typed verdict must be recognized, wrapped or not")
	}
}

// A healthy position still reads as a position.
func TestGetStakerReadsAPosition(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"data":{"address":"NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6","balance":10000000,"delegation":"NQ49 N8MB XYCR XBUP 404C KXKK L49M A7BT F082","inactiveBalance":500,"inactiveFrom":null,"retiredBalance":0},"metadata":{"blockNumber":1}}}`
	srv := rpcServer(t, body)
	st, err := NewClient(srv.URL).GetStaker(context.Background(), "NQ146MRC30QRYDCBU9N8CVGA1TH7K5D1X2H6")
	if err != nil {
		t.Fatalf("GetStaker: %v", err)
	}
	var got map[string]any
	raw, _ := json.Marshal(st)
	_ = json.Unmarshal(raw, &got)
	if st.Balance != 10000000 || st.InactiveBalance != 500 || !st.HasStake() {
		t.Fatalf("position misread: %+v", st)
	}
}

// TestLiveNoStakerVerdict asks the REAL provider about the owner's example
// address (an address that never staked) and asserts the verdict the bug was
// about. It is opt-in — CI stays offline:
//
//	NIMIQ_LIVE=1 go test ./internal/nimiq -run LiveNoStaker -v
//
// Owner's report (2026-10-25): "Hiç stake etmemiş bir hesapla girince 'Stake
// miktarı okunamadı' çıkıyor … /api/wallet/stake 502 dönüyor. Örnek:
// NQ14 6MRC 30QR YDCB U9N8 CVGA 1TH7 K5D1 X2H6".
func TestLiveNoStakerVerdict(t *testing.T) {
	if os.Getenv("NIMIQ_LIVE") != "1" {
		t.Skip("set NIMIQ_LIVE=1 to run the live-provider check")
	}
	const neverStaked = "NQ146MRC30QRYDCBU9N8CVGA1TH7K5D1X2H6"
	c := NewClient("https://rpc.nimiqwatch.com")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := c.GetStaker(ctx, neverStaked)
	if err == nil {
		// A position appeared since the report: the address now stakes.
		t.Fatalf("address now reports a position (%+v); pick another unstaked address", st)
	}
	if !IsNoStaker(err) || !errors.Is(err, ErrNoStaker) {
		t.Fatalf("a never-staked address must read as no-staker, not an outage: %v", err)
	}
	t.Logf("live verdict: %v", err)
}
