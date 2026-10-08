package notification

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nimiqshop/internal/db"
	"nimiqshop/internal/nimiq"
)

const testSeedHex = "0101010101010101010101010101010101010101010101010101010101010101"

// testBuyer is a real, checksum-valid buyer address derived from a second test
// key, so the transfer is built against a genuine recipient.
var testBuyer = func() string {
	addr, err := nimiq.AddressFromSeed("0202020202020202020202020202020202020202020202020202020202020202")
	if err != nil {
		panic(err)
	}
	return addr
}()

// fakeRPC answers the two JSON-RPC calls the notifier makes and records every
// transaction it is asked to broadcast, so the test can inspect the real signed
// bytes the shop would have sent.
type fakeRPC struct {
	mu     sync.Mutex
	pushes []string
	srv    *httptest.Server
}

func newFakeRPC(t *testing.T) *fakeRPC {
	t.Helper()
	f := &fakeRPC{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result interface{}
		switch req.Method {
		case "getBlockNumber":
			result = 1000
		case "pushTransaction":
			var txHex string
			_ = json.Unmarshal(req.Params[0], &txHex)
			f.mu.Lock()
			f.pushes = append(f.pushes, txHex)
			f.mu.Unlock()
			result = strings.Repeat("ab", 32)
		default:
			http.Error(w, "unexpected rpc method "+req.Method, http.StatusInternalServerError)
			return
		}
		b, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": json.RawMessage(b)})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRPC) broadcasts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pushes...)
}

func newTestNotifier(t *testing.T, enabled bool) (*Notifier, *fakeRPC, *db.Store) {
	t.Helper()
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := newFakeRPC(t)
	n := New(nimiq.NewClient(f.srv.URL), store, testSeedHex, byte(nimiq.NetworkMainnet), 1, enabled)
	return n, f, store
}

// A fulfilled product order sends exactly one 1-Luna transfer to the buyer,
// carrying the "order is on its way" memo, and never a second one for the same
// order.
func TestFulfilledOrderSendsOneLunaMemoToBuyer(t *testing.T) {
	n, f, _ := newTestNotifier(t, true)
	ctx := context.Background()

	sent, err := n.NotifyReason(ctx, ReasonOrderFulfilled, "quote:q-1", testBuyer, "")
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !sent {
		t.Fatal("fulfilled order was not sent to the buyer")
	}
	pushes := f.broadcasts()
	if len(pushes) != 1 {
		t.Fatalf("expected exactly one broadcast, got %d", len(pushes))
	}
	raw, err := hex.DecodeString(pushes[0])
	if err != nil {
		t.Fatalf("broadcast is not hex: %v", err)
	}
	recipient, err := nimiq.DecodeUserFriendlyAddress(testBuyer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, recipient) {
		t.Fatal("transaction does not pay the buyer's address")
	}
	memo := Memo(ReasonOrderFulfilled, "")
	if !bytes.Contains(raw, []byte(memo)) {
		t.Fatalf("transaction does not carry the memo %q", memo)
	}

	// Same order again (tracker re-run, crash-restart): never a second send.
	if _, err := n.NotifyReason(ctx, ReasonOrderFulfilled, "quote:q-1", testBuyer, ""); err != nil {
		t.Fatalf("second notify: %v", err)
	}
	if got := len(f.broadcasts()); got != 1 {
		t.Fatalf("same order was broadcast %d times", got)
	}
}

// An opted-out buyer receives nothing, and nothing is broadcast.
func TestFulfilledOrderRespectsOptOut(t *testing.T) {
	n, f, store := newTestNotifier(t, true)
	if err := store.SetWalletNotifyEnabled(testBuyer, false); err != nil {
		t.Fatal(err)
	}
	sent, err := n.NotifyReason(context.Background(), ReasonOrderFulfilled, "quote:q-2", testBuyer, "")
	if err != nil {
		t.Fatal(err)
	}
	if sent || len(f.broadcasts()) != 0 {
		t.Fatal("opted-out buyer was messaged")
	}
}

// With no seed the channel is off: nothing is built or broadcast.
func TestNotifierOffWithoutSeed(t *testing.T) {
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	f := newFakeRPC(t)
	n := New(nimiq.NewClient(f.srv.URL), store, "", byte(nimiq.NetworkMainnet), 1, true)
	if n.Enabled() {
		t.Fatal("notifier must be off without a seed")
	}
	if sent, _ := n.NotifyReason(context.Background(), ReasonOrderFulfilled, "quote:q-3", testBuyer, ""); sent {
		t.Fatal("sent without a seed")
	}
	if len(f.broadcasts()) != 0 {
		t.Fatal("broadcast without a seed")
	}
}

// Every memo fits the 64-byte ceiling, and the fulfilled one tells the buyer to
// check their email, even with a long shop name.
func TestMemosFitTheChainLimit(t *testing.T) {
	prev := ShopName
	defer func() { ShopName = prev }()
	for _, name := range []string{"nimiqshop.io", "a-very-long-shop-domain.example.co.uk", strings.Repeat("x", 80)} {
		ShopName = name
		for _, r := range Reasons() {
			if m := Memo(r, ""); len(m) > maxMemoLen {
				t.Fatalf("memo for %s with shop %q is %d bytes: %q", r, name, len(m), m)
			}
		}
		m := Memo(ReasonOrderFulfilled, "")
		if !strings.Contains(m, "check your email") {
			t.Fatalf("fulfilled memo lost its call to action: %q", m)
		}
	}
}
