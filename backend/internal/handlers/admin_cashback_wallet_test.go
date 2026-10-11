package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/config"
	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

func TestAdminCashbackWalletUsesPayoutSignerAndCachesBalance(t *testing.T) {
	// Public test vector only; this key never holds funds.
	seed := strings.Repeat("01", 32)
	wantAddress, err := nimiq.AddressFromKeyHex(seed)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != "getAccountByAddress" || len(req.Params) != 1 || req.Params[0] != wantAddress {
			t.Errorf("must read only the payout address: %+v (%v)", req, err)
		}
		_, _ = w.Write([]byte(`{"result":{"data":{"balance":123456789},"metadata":{}}}`))
	}))
	defer rpc.Close()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	h := New(store, config.Config{CashbackWalletSeed: seed, CashbackEnabled: true, NimiqRPCURL: rpc.URL}, nil)
	var ctx fasthttp.RequestCtx
	h.AdminDashboard(&ctx)
	var dashboard struct {
		Wallet adminCashbackWalletView `json:"cashback_wallet"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &dashboard); err != nil {
		t.Fatal(err)
	}
	v := dashboard.Wallet
	if ctx.Response.StatusCode() != 200 || !v.Available || !v.Configured || !v.Enabled || v.Address != wantAddress || v.Network != "mainnet" || v.BalanceLuna == nil || *v.BalanceLuna != 123456789 || v.BalanceNIM == nil || *v.BalanceNIM != 1234.56789 || v.ObservedAt == nil {
		t.Fatalf("wrong payout wallet response: %+v", v)
	}
	if strings.Contains(string(ctx.Response.Body()), seed) || strings.Contains(string(ctx.Response.Body()), "private_key") {
		t.Fatal("dashboard must not expose signing material")
	}
	if second := h.adminCashbackWallet(); second.ObservedAt != v.ObservedAt && !second.ObservedAt.Equal(*v.ObservedAt) {
		t.Fatal("cached balance must retain its observation time")
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent Overview cards must share a short cache: %d calls", calls.Load())
	}
	// An outage after expiry clears the old balance rather than making it
	// look newly observed, and a later poll can recover.
	rpc.Close()
	h.cashbackWallet.expires = time.Time{}
	failed := h.adminCashbackWallet()
	if failed.Available || failed.BalanceNIM != nil || failed.ObservedAt != nil || failed.Address != wantAddress {
		t.Fatalf("outage must preserve address, not a stale or invented balance: %+v", failed)
	}
}

func TestAdminCashbackWalletUnavailableAndZeroStates(t *testing.T) {
	for _, tc := range []struct {
		name, seed, response, reason string
		configured, available        bool
	}{
		{name: "unconfigured", reason: "not_configured"},
		{name: "invalid secret", seed: "invalid", reason: "invalid_configuration"},
		{name: "missing rpc", seed: strings.Repeat("02", 32), configured: true, reason: "rpc_unavailable"},
		{name: "rpc error", seed: strings.Repeat("02", 32), configured: true, response: `{"error":{"code":-1,"message":"offline"}}`, reason: "balance_unavailable"},
		{name: "null account", seed: strings.Repeat("02", 32), configured: true, response: `{"result":null}`, reason: "balance_unavailable"},
		{name: "missing balance", seed: strings.Repeat("02", 32), configured: true, response: `{"result":{}}`, reason: "balance_unavailable"},
		{name: "negative balance", seed: strings.Repeat("02", 32), configured: true, response: `{"result":{"balance":-1}}`, reason: "balance_unavailable"},
		{name: "zero balance with payouts disabled", seed: strings.Repeat("02", 32), configured: true, response: `{"result":{"balance":0}}`, available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handlers{Cfg: config.Config{CashbackWalletSeed: tc.seed, CashbackNetwork: "testnet"}}
			if tc.response != "" {
				rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) }))
				defer rpc.Close()
				h.NIMRPC = nimiq.NewClient(rpc.URL)
			}
			v := h.adminCashbackWallet()
			if v.Configured != tc.configured || v.Available != tc.available || v.Reason != tc.reason || v.Network != "testnet" || v.Enabled {
				t.Fatalf("wrong state: %+v", v)
			}
			if tc.available {
				if v.BalanceNIM == nil || *v.BalanceNIM != 0 || v.BalanceLuna == nil || *v.BalanceLuna != 0 || v.ObservedAt == nil {
					t.Fatal("zero must be a successful observed balance")
				}
			} else if v.BalanceNIM != nil || v.BalanceLuna != nil || v.ObservedAt != nil {
				t.Fatal("unavailable must never be shown as zero")
			}
		})
	}
}
