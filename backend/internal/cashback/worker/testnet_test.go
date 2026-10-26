package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

// Explicit opt-in only. Creates an ephemeral testnet key and uses official
// faucet funds; it never reads production secrets or contacts mainnet RPCs.
func TestLiveNimiqTestnetPayoutRecovery(t *testing.T) {
	if os.Getenv("NIMSHOP_RUN_TESTNET") != "1" {
		t.Skip("set NIMSHOP_RUN_TESTNET=1 for real testnet verification")
	}
	sender := make([]byte, 32)
	recipient := make([]byte, 32)
	if _, err := rand.Read(sender); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(recipient); err != nil {
		t.Fatal(err)
	}
	seed := hex.EncodeToString(sender)
	from, err := nimiq.AddressFromKeyHex(seed)
	if err != nil {
		t.Fatal(err)
	}
	to, err := nimiq.AddressFromKeyHex(hex.EncodeToString(recipient))
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.QueueManualCashback(to, "nimshop testnet recovery", 100000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.QueueManualCashback(to, "nimshop testnet recovery", 100000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rpcURL := "https://rpc.testnet.nimiqwatch.com"
	rpc := nimiq.NewClient(rpcURL)
	worker := &Worker{Store: WithQuizPayouts(store), RPC: rpc, Enabled: true, SeedHex: seed, NetworkID: nimiq.NetworkTestnet, FeeLuna: 1}
	worker.tick(context.Background())
	queued, err := store.ListCashbacksByStatus(db.CashbackQueued, 0)
	if err != nil || len(queued) != 2 {
		t.Fatal("unfunded obligations not preserved", err)
	}
	for _, row := range queued {
		if row.SignedTxHex != "" {
			t.Fatal("unfunded signature created")
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.PostForm("https://faucet.pos.nimiq-testnet.com/tapit", url.Values{"address": {from}, "amount": {"5"}})
	if err != nil {
		t.Fatal("testnet faucet unavailable", err)
	}
	var faucet struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	err = json.NewDecoder(response.Body).Decode(&faucet)
	_ = response.Body.Close()
	if err != nil || !faucet.Success {
		t.Fatalf("faucet did not fund test wallet: %v %s", err, faucet.Msg)
	}
	t.Logf("TESTNET ONLY: sender=%s recipient=%s", from, to)
	deadline := time.Now().Add(90 * time.Second)
	for {
		balance, e := rpc.GetAccountBalance(context.Background(), from)
		if e == nil && balance >= 200002 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("testnet funding not confirmed")
		}
		time.Sleep(3 * time.Second)
	}
	// Forward the real broadcast but suppress its first acknowledgement. This
	// reproduces an accepted transaction followed by an RPC/network failure.
	var dropped atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		request, e := http.NewRequestWithContext(r.Context(), http.MethodPost, rpcURL, strings.NewReader(string(body)))
		if e != nil {
			http.Error(w, "request", http.StatusInternalServerError)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		reply, e := client.Do(request)
		if e != nil {
			http.Error(w, "RPC timeout", http.StatusBadGateway)
			return
		}
		defer func() { _ = reply.Body.Close() }()
		data, e := io.ReadAll(reply.Body)
		if e != nil {
			http.Error(w, "reply", http.StatusBadGateway)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		var result struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(data, &result)
		if req.Method == "pushTransaction" && len(result.Error) == 0 && dropped.CompareAndSwap(false, true) {
			http.Error(w, "simulated lost acknowledgement after real testnet acceptance", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer proxy.Close()
	worker.RPC = nimiq.NewClient(proxy.URL)
	deadline = time.Now().Add(4 * time.Minute)
	for {
		worker.tick(context.Background())
		paid, e := store.ListCashbacksByStatus(db.CashbackPaid, 0)
		if e == nil && len(paid) == 2 {
			if paid[0].TxHash == paid[1].TxHash {
				t.Fatal("two obligations collapsed into one transfer")
			}
			for _, row := range paid {
				t.Logf("confirmed TESTNET payout id=%s tx=%s memo=%q", row.ID, row.TxHash, row.Memo)
			}
			break
		}
		if time.Now().After(deadline) {
			for _, status := range []string{db.CashbackQueued, db.CashbackSending, db.CashbackBroadcast} {
				rows, _ := store.ListCashbacksByStatus(status, 0)
				for _, row := range rows {
					t.Logf("TESTNET pending id=%s status=%s tx=%s last_error=%s", row.ID, row.Status, row.TxHash, row.LastError)
				}
			}
			t.Fatalf("testnet payouts not finalized within four minutes (lost acknowledgement injected=%t)", dropped.Load())
		}
		time.Sleep(3 * time.Second)
	}
	// Restart the sender and retry the same queue repeatedly after settlement.
	worker = newWorker(WithQuizPayouts(store), nimiq.NewClient(rpcURL))
	worker.SeedHex = seed
	runTicks(worker, 5)
	balance, err := rpc.GetAccountBalance(context.Background(), to)
	if err != nil || balance != 200000 {
		t.Fatalf("recipient received %d luna instead of exactly 200000: %v", balance, err)
	}
	paid, _ := store.ListCashbacksByStatus(db.CashbackPaid, 0)
	if len(paid) != 2 || (paid[0].ID != first.ID && paid[0].ID != second.ID) {
		t.Fatal("obligations changed")
	}
	if !dropped.Load() {
		t.Fatal("real lost-acknowledgement path not exercised")
	}
}
