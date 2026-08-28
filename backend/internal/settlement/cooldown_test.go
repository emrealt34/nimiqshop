package settlement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

// TestTrackerStopsPollingDuringSupplier429 replays the exact production
// incident: several open orders + the supplier starts answering 429. The
// old tracker re-polled EVERY order on EVERY tick even while the endpoint
// was cooling down, flooding the log and deepening the budget hole. The
// fixed tracker must (a) never poll while OrderPollWait() > 2s, and (b)
// back each quote off individually. After the throttle engages, the
// upstream request counter must not move again for the whole window.
func TestTrackerStopsPollingDuringSupplier429(t *testing.T) {
	var gets, total int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&total, 1)
		if req.Method != http.MethodGet || !strings.HasPrefix(req.URL.Path, "/v5/orders/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if atomic.AddInt32(&gets, 1) <= 3 {
			// First poll per order succeeds (state unchanged).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"order_id":"` + strings.TrimPrefix(req.URL.Path, "/v5/orders/") + `","order_state":"WaitingForPayment","wallet_address":"lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6nywasd9s2psp5v6my3tq7dsgk4d9qaw577d3jjaa4qrpw5ce0s9awqgl7yz03lpqqftmeuq","coin":"BTC","coin_amount":"0.00032833","network":"Lightning","created_at":"1787679708","updated_at":"1787679708"}`))
			return
		}
		// Then the supplier budget is gone for 30s.
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer srv.Close()

	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	client := cryptorefills.NewClient(srv.URL, "partner", "1.0", "nimshop-test", cryptorefills.QueueConfig{})

	w := &OrderTracker{Store: store, CR: client, Interval: 50 * time.Millisecond, StaleAfter: 300 * time.Second, MaxOrders: 100}
	w.nextPollAt = make(map[string]time.Time)
	w.consecErrors = make(map[string]int)
	w.lastErrLog = make(map[string]errLog)

	// Seed 3 tracked quotes that are immediately due.
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		q := db.Quote{
			ID: "q-" + string(rune('a'+i)), UserID: "NQTEST", ProductID: "test-brand", ProductCountry: "US",
			Denomination: "range", ProductValue: 10, Quantity: 1, ProductUSD: 10_000_000,
			CustomerEmail: "buyer@example.com", Coin: "BTC", Network: "Lightning",
		}
		if err := store.CreateQuoteWithDailyLimits(q, 1000, 1e12, now.Add(-24*time.Hour)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := store.AttachQuotePayment(q.ID, "ord-"+string(rune('a'+i)), "lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6nywasd9s2psp5v6my3tq7dsgk4d9qaw577d3jjaa4qrpw5ce0s9awqgl7yz03lpqqftmeuq", "BTC", "0.00032833", "Lightning", now.Add(30*time.Minute)); err != nil {
			t.Fatalf("attach: %v", err)
		}
		w.nextPollAt[q.ID] = now // due immediately (skip the stagger for the test)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Tick 1: three successful polls, then the supplier goes 429.
	w.tick(ctx)
	first := atomic.LoadInt32(&gets)
	if first < 3 {
		t.Fatalf("expected 3 initial polls, got %d", first)
	}
	// Wait out the 3s client micro-cache so a poll WOULD reach upstream if
	// the cooldown logic were broken.
	time.Sleep(3100 * time.Millisecond)
	// 30 fast ticks across ~1.5s, with every quote FORCED due each tick:
	// only the cooldown + per-quote backoff can now prevent upstream
	// calls. The old tracker would have polled 3× per tick here.
	for i := 0; i < 30; i++ {
		w.pollMu.Lock()
		for id := range w.nextPollAt {
			w.nextPollAt[id] = time.Now()
		}
		w.pollMu.Unlock()
		w.tick(ctx)
		time.Sleep(50 * time.Millisecond)
	}
	// Allow EXACTLY ONE "discovery" call: someone must make the request
	// that learns the 429 (it then throttles the endpoint locally). After
	// that, zero upstream calls are permitted — the old tracker made ~3
	// per tick for the whole window.
	got := atomic.LoadInt32(&gets)
	if got > first+1 {
		t.Fatalf("upstream polls continued during 429 cooldown: first=%d now=%d (tracker hammered the supplier)", first, got)
	}
	// Every quote must have its 429 recorded as backoff, never escalated.
	tracked, err := store.ListQuotesByStatuses([]string{cryptorefills.QuoteAwaitingPay}, 10)
	if err != nil || len(tracked) != 3 {
		t.Fatalf("tracked quotes = %d, %v (transient 429 must never escalate)", len(tracked), err)
	}
	for _, q := range tracked {
		w.pollMu.Lock()
		consec := w.consecErrors[q.ID]
		w.pollMu.Unlock()
		if consec < 1 {
			t.Fatalf("quote %s shows no recorded backoff: consec=%d", q.ID, consec)
		}
	}
}
