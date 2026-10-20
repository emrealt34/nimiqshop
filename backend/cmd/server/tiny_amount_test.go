package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Live regression (2026-10-20): a 50 NGN Airtel top-up — 52 sats — was
// accepted by the supplier, and the shop then parked the quote in manual
// review with "invalid decimal amount" because the amount arrived as
// "5.2E-7" (Java BigDecimal notation, used below 100 sats). The supplier
// mock replays that serialisation; the order must reach its invoice and
// every amount the shop stores or shows must be the plain decimal.
func TestSubHundredSatOrderReachesItsInvoice(t *testing.T) {
	// $5 at ten million dollars per bitcoin is 50 sats: the amount range
	// where the supplier switches to scientific notation.
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{"MOCK_BTC_USD": "10000000"}})
	setMockFault(t, s, map[string]any{"java_decimals": true})

	auth, _ := s.signIn()
	headers := map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)}
	res := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "amazon-usa", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 5, "email": "tiny@example.com",
	}, headers)
	if res.status != http.StatusCreated {
		t.Fatalf("want 201, got %d %s", res.status, truncate(res.body))
	}
	body := res.json(t)
	if code, _ := body["code"].(string); code != "" {
		t.Fatalf("a payable order must not carry a safety code, got %q: %s", code, truncate(res.body))
	}
	plain := regexp.MustCompile(`^0\.000000[0-9]*[1-9]$`) // under 100 sats, no exponent, no trailing zeros
	amount, _ := body["coin_amount"].(string)
	if !plain.MatchString(amount) {
		t.Fatalf("coin_amount must be the plain sub-100-sat decimal, got %q", amount)
	}
	if inv, _ := body["lightning_invoice"].(string); !strings.HasPrefix(inv, "lnbc") {
		t.Fatalf("the order must carry its invoice: %s", truncate(res.body))
	}
	id := quoteIDFrom(t, res)

	detail := s.do(http.MethodGet, "/api/quotes/"+id, nil, headers)
	if detail.status != 200 {
		t.Fatalf("quote detail: %d %s", detail.status, truncate(detail.body))
	}
	d := detail.json(t)
	if d["can_pay"] != true {
		t.Fatalf("the quote must be payable, got %s", truncate(detail.body))
	}
	q, _ := d["quote"].(map[string]any)
	if q["status"] != "awaiting_payment" || q["payment_blocked"] == true {
		t.Fatalf("status %v blocked %v: %s", q["status"], q["payment_blocked"], truncate(detail.body))
	}
	if v, _ := q["validated_coin_amount"].(string); !plain.MatchString(v) {
		t.Fatalf("validated_coin_amount must be stored plain, got %q", v)
	}
	if v, _ := q["coin_amount"].(string); !plain.MatchString(v) {
		t.Fatalf("stored coin_amount must be plain, got %q", v)
	}
}
