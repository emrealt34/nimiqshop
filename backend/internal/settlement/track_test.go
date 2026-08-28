package settlement

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
)

// supplierRecorder captures the POST /v5/orders bodies the tracker sends
// and answers with a payable Lightning order.
type supplierRecorder struct {
	mu     sync.Mutex
	bodies []map[string]interface{}
}

func (r *supplierRecorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || !strings.HasPrefix(req.URL.Path, "/v5/orders") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(req.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"order_id": "recorder-order-1",
			"order_state": "WaitingForPayment",
			"wallet_address": "lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6nywasd9s2psp5v6my3tq7dsgk4d9qaw577d3jjaa4qrpw5ce0s9awqgl7yz03lpqqftmeuq",
			"coin": "BTC",
			"coin_amount": "0.00032833",
			"network": "Lightning",
			"created_at": "1787679708",
			"updated_at": "1787679708"
		}`))
	}
}

// lastBeneficiary returns the beneficiary_account the supplier last saw on
// the first delivery of the most recent CreateOrder call.
func (r *supplierRecorder) lastBeneficiary(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		t.Fatal("no supplier order recorded")
	}
	body := r.bodies[len(r.bodies)-1]
	deliveries, _ := body["deliveries"].([]interface{})
	if len(deliveries) == 0 {
		t.Fatal("recorded order has no deliveries")
	}
	first, _ := deliveries[0].(map[string]interface{})
	ben, _ := first["beneficiary_account"].(string)
	return ben
}

func newTrackerWithRecorder(t *testing.T) (*OrderTracker, *supplierRecorder, *db.Store) {
	t.Helper()
	rec := &supplierRecorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)
	store, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &OrderTracker{Store: store, CR: cryptorefills.NewClient(srv.URL, "partner", "1.0", "nimshop-test", cryptorefills.QueueConfig{})}, rec, store
}

func seedQuote(t *testing.T, store *db.Store, q db.Quote) {
	t.Helper()
	if q.Status == "" {
		q.Status = "order_creating"
	}
	now := time.Now().UTC()
	q.CreatedAt = now
	q.UpdatedAt = now
	q.ExpiresAt = now.Add(30 * time.Minute)
	if err := store.CreateQuoteWithDailyLimits(q, 100, 1_000_000_000, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
}

// TestRedeliverCreationBeneficiary is the regression guard for the
// beneficiary_account rules: crash recovery must re-send EXACTLY what the
// live quote path sent — the E.164 phone for top-ups, the email for gift
// cards — never "phone if present".
func TestRedeliverCreationBeneficiary(t *testing.T) {
	base := db.Quote{
		UserID: "NQTEST", ProductID: "test-brand", ProductCountry: "US",
		Denomination: "range", ProductValue: 10, Quantity: 1,
		ProductUSD:    10_000_000, // $10 in micro-USD
		CustomerEmail: "buyer@example.com", Coin: "BTC", Network: "Lightning",
	}

	cases := []struct {
		name string
		q    db.Quote
		want string
	}{
		{
			name: "topup-sends-phone",
			q: db.Quote{
				ID:                 "q-c09-topup",
				PhoneNumber:        "+14155551234",
				BeneficiaryAccount: "+14155551234",
			},
			want: "+14155551234",
		},
		{
			// THE bug: a gift card that also carries a phone number
			// (optional on non-topup products) must still be delivered to
			// the EMAIL.
			name: "giftcard-with-phone-sends-email",
			q: db.Quote{
				ID:                 "q-c09-giftcard",
				PhoneNumber:        "+905551234567",
				BeneficiaryAccount: "buyer@example.com",
			},
			want: "buyer@example.com",
		},
		{
			name: "legacy-row-with-phone",
			q:    db.Quote{ID: "q-c09-legacy-phone", PhoneNumber: "+14155551234"},
			want: "+14155551234",
		},
		{
			name: "legacy-row-email",
			q:    db.Quote{ID: "q-c09-legacy-email"},
			want: "buyer@example.com",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, rec, store := newTrackerWithRecorder(t)
			q := base
			q.ID = c.q.ID
			q.PhoneNumber = c.q.PhoneNumber
			q.BeneficiaryAccount = c.q.BeneficiaryAccount
			seedQuote(t, store, q)
			stored, err := store.GetQuote(q.ID)
			if err != nil {
				t.Fatalf("get quote: %v", err)
			}
			w.redeliverCreation(context.Background(), stored)

			if got := rec.lastBeneficiary(t); got != c.want {
				t.Fatalf("beneficiary_account = %q, want %q", got, c.want)
			}
			// The re-sent order must have been attached to the quote.
			after, err := store.GetQuote(q.ID)
			if err != nil {
				t.Fatalf("get quote after: %v", err)
			}
			if after.Status != "awaiting_payment" || after.SupplierOrderID == "" || after.WalletAddress == "" {
				t.Fatalf("quote not attached after re-dispatch: %+v", after)
			}
		})
	}
}
