package cryptorefills

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

/* v6 PARTNER API tests (2026-10-19).
 *
 * The shop moved order traffic from /v5/orders (a user session, which earns
 * nothing) to /v6/partner/orders, which is what pays commission. The switch is
 * small in code and expensive to get wrong, so the things that are easy to
 * break — the endpoint generation, the partner headers, the idempotency key
 * that stops a retry from creating a SECOND order, the end-customer IP the
 * partner API rejects a create without, and the state names that changed
 * spelling between the two generations — are pinned here.
 *
 * Every test drives a local httptest server: no order is ever placed with the
 * real supplier by this file.
 */

type recordedCall struct {
	Method string
	Path   string
	Header http.Header
	Body   string
}

type callRecorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (r *callRecorder) add(c recordedCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *callRecorder) last() recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return recordedCall{}
	}
	return r.calls[len(r.calls)-1]
}

func (r *callRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// supplierStub answers every request with the given status/body and records
// what it was asked.
func supplierStub(t *testing.T, rec *callRecorder, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		rec.add(recordedCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: string(buf[:n])})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const partnerOrder = `{"order_id":"ord_v6","status":"WAITING_FOR_PAYMENT","coin":"BTC","coin_amount":"0.00001276","network":"Lightning","wallet_address":"lnbc1probe","commission_rate_pct":0.05}`

func partnerClient(t *testing.T, baseURL, key string) *Client {
	t.Helper()
	c := NewClient(ClientOptions{BaseURL: baseURL, PartnerID: "partner-id", PartnerKey: key, AppVersion: "test/1.0", UserAgent: "test-agent"})
	t.Cleanup(c.Close)
	return c
}

func giftCardRequest() *CreateOrderRequest {
	return &CreateOrderRequest{
		Deliveries: []Delivery{{BrandName: "Amazon", CountryCode: "US", Denomination: "25 USD", BeneficiaryAccount: "buyer@example.com"}},
		Payment:    OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: "BTC", Network: "Lightning"},
		User:       &OrderUser{Email: "buyer@example.com"},
		Lang:       "en",
	}
}

// --- routing ---------------------------------------------------------------

func TestPartnerModeCreatesReadsAndCancelsOnTheV6Endpoint(t *testing.T) {
	rec := &callRecorder{}
	srv := supplierStub(t, rec, http.StatusOK, partnerOrder)
	c := partnerClient(t, srv.URL, "cr_pk_test_key")

	if !c.PartnerMode() {
		t.Fatal("a client with a partner key must be in partner mode")
	}
	ctx := WithEndUserIP(context.Background(), "203.0.113.7")
	if _, err := c.CreateOrder(ctx, giftCardRequest()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := rec.last(); got.Method != http.MethodPost || got.Path != "/v6/partner/orders" {
		t.Fatalf("create must go to POST /v6/partner/orders, got %s %s", got.Method, got.Path)
	}
	if _, err := c.GetOrderFresh(context.Background(), "ord_v6"); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := rec.last(); got.Method != http.MethodGet || got.Path != "/v6/partner/orders/ord_v6" {
		t.Fatalf("read must go to GET /v6/partner/orders/{id}, got %s %s", got.Method, got.Path)
	}
	if _, err := c.CancelOrder(context.Background(), "ord_v6"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := rec.last(); got.Method != http.MethodDelete || got.Path != "/v6/partner/orders/ord_v6" {
		t.Fatalf("cancel must go to DELETE /v6/partner/orders/{id}, got %s %s", got.Method, got.Path)
	}
}

func TestWithoutAPartnerKeyOrdersStayOnV5(t *testing.T) {
	rec := &callRecorder{}
	srv := supplierStub(t, rec, http.StatusOK, partnerOrder)
	c := partnerClient(t, srv.URL, "")

	if c.PartnerMode() {
		t.Fatal("no key must mean v5, not the partner API")
	}
	req := giftCardRequest()
	req.ExternalOrderID = "quote-should-not-be-sent-on-v5"
	if _, err := c.CreateOrder(context.Background(), req); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := rec.last()
	if got.Method != http.MethodPost || got.Path != "/v5/orders" {
		t.Fatalf("v5 create expected, got %s %s", got.Method, got.Path)
	}
	if strings.Contains(got.Body, "external_order_id") {
		t.Fatalf("v5 must not carry the partner idempotency key: %s", got.Body)
	}
	if _, err := c.CancelOrder(context.Background(), "ord_v6"); err == nil {
		t.Fatal("cancel is a partner-API operation; v5 must refuse it")
	}
}

// --- the two partner-only request requirements -----------------------------

func TestPartnerCreateCarriesTheQuoteIDAsTheIdempotencyKey(t *testing.T) {
	rec := &callRecorder{}
	srv := supplierStub(t, rec, http.StatusOK, partnerOrder)
	c := partnerClient(t, srv.URL, "cr_pk_test_key")

	req := giftCardRequest()
	req.ExternalOrderID = "quote-7f3a"
	if _, err := c.CreateOrder(context.Background(), req); err != nil {
		t.Fatalf("create: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.last().Body), &body); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if body["external_order_id"] != "quote-7f3a" {
		t.Fatalf("the quote id must be sent as external_order_id so a retry cannot create a second order: %v", body["external_order_id"])
	}
	// The key is what makes a crash-recovery redelivery safe, so it must
	// survive the round trip through the durable outbox payload exactly.
	raw, err := MarshalCreateRequest(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"external_order_id":"quote-7f3a"`) {
		t.Fatalf("the frozen (write-ahead) payload must carry the idempotency key: %s", raw)
	}
}

func TestPartnerCreateForwardsTheEndCustomerIP(t *testing.T) {
	rec := &callRecorder{}
	srv := supplierStub(t, rec, http.StatusOK, partnerOrder)
	c := partnerClient(t, srv.URL, "cr_pk_test_key")

	// The live partner API answers 400 missing_required_header without it.
	ctx := WithEndUserIP(context.Background(), "203.0.113.7")
	if _, err := c.CreateOrder(ctx, giftCardRequest()); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := rec.last()
	if want := "203.0.113.7"; got.Header.Get("X-CR-Forwarded-For") != want {
		t.Fatalf("X-CR-Forwarded-For = %q, want %q", got.Header.Get("X-CR-Forwarded-For"), want)
	}
	if got.Header.Get("X-CR-Partner-Key") != "cr_pk_test_key" {
		t.Fatalf("X-CR-Partner-Key = %q", got.Header.Get("X-CR-Partner-Key"))
	}
}

// TestAnOrderWithoutTheBuyersEmailIsNeverDispatchedWithAnInventedOne is the
// standing rule: the shop has NO fallback address and must never invent one.
// The v6 partner API rejects any order whose user block carries no email
// (400 "user.email is required", verified live), so a buyer who gives no
// address simply cannot order until they do — the checkout refuses before a
// single request reaches the supplier.
func TestAnOrderWithoutTheBuyersEmailIsNeverDispatchedWithAnInventedOne(t *testing.T) {
	// A mobile top-up: the credit lands on the number, and the buyer may not
	// have entered an email at all.
	topUp := &CreateOrderRequest{
		Deliveries: []Delivery{{BrandName: "Turk Telekom", CountryCode: "TR", Denomination: "100 TRY", BeneficiaryAccount: "+905551234567"}},
		Payment:    OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: "BTC"},
		Lang:       "tr",
	}
	raw, err := MarshalCreateRequest(topUp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// No email anywhere in the payload — not the shop's, not a placeholder.
	if strings.Contains(string(raw), "@") {
		t.Fatalf("an order with no buyer email must carry no address at all: %s", raw)
	}
	// Which is exactly the payload the partner API answers 400 to. The shop
	// refuses it earlier, with a sentence the buyer can act on.
	withEmail := *topUp
	withEmail.User = &OrderUser{Email: "buyer@example.com"}
	raw2, _ := MarshalCreateRequest(&withEmail)
	if !strings.Contains(string(raw2), `"email":"buyer@example.com"`) {
		t.Fatalf("the buyer's own email must be sent exactly as given: %s", raw2)
	}
}

// --- v6 responses ----------------------------------------------------------

func TestV6StateNamesMapExactlyLikeTheV5Ones(t *testing.T) {
	pairs := [][2]string{
		{"Created", "CREATED"},
		{"WaitingForPayment", "WAITING_FOR_PAYMENT"},
		{"PaymentStarted", "PAYMENT_STARTED"},
		{"PaymentReceived", "PAYMENT_RECEIVED"},
		{"WaitingForDelivery", "WAITING_FOR_DELIVERY"},
		{"Done", "DONE"},
		{"Expired", "EXPIRED"},
		{"Refunded", "REFUNDED"},
	}
	for _, p := range pairs {
		want := MapToQuoteStatus(p[0])
		if got := MapToQuoteStatus(p[1]); got != want {
			t.Fatalf("%s mapped to %q but the v6 spelling %s mapped to %q", p[0], want, p[1], got)
		}
	}
	if MapToQuoteStatus("WAITING_FOR_PAYMENT") != QuoteAwaitingPay {
		t.Fatal("a v6 order awaiting payment must still be awaiting payment")
	}
	// A cancel (ours or the supplier's) is a dead, unpaid order locally.
	if got := MapToQuoteStatus("CANCELED"); got != QuoteExpired {
		t.Fatalf("CANCELED mapped to %q, want %q (never payable again)", got, QuoteExpired)
	}
	if !IsTerminal("CANCELED") || IsPaidOrBeyond("CANCELED") {
		t.Fatal("a cancelled order is terminal and must never count as paid")
	}
}

func TestAV6OrderIsStillRecognisedAsPayable(t *testing.T) {
	// The invoice guard asks "is this order still awaiting payment?" — if the
	// new state spelling were not understood, EVERY partner order would be
	// refused an invoice.
	for _, st := range []string{"WAITING_FOR_PAYMENT", "WaitingForPayment", "CREATED"} {
		if !orderIsAwaitingPayment(&Order{ID: "ord_v6", Status: st}) {
			t.Fatalf("state %s must still be payable", st)
		}
	}
	for _, st := range []string{"PAYMENT_RECEIVED", "CANCELED", "EXPIRED", "DONE"} {
		if orderIsAwaitingPayment(&Order{ID: "ord_v6", Status: st}) {
			t.Fatalf("state %s must not be payable", st)
		}
	}
}

func TestPartnerOrderReadFillsTheFlatFieldsFromANestedPaymentBlock(t *testing.T) {
	// A v6 order that nests its payment details must not look unpayable just
	// because the flat v5 field is empty.
	const body = `{"order_id":"ord_v6","status":"WAITING_FOR_PAYMENT",` +
		`"payment":{"coin":"BTC","coin_amount":0.00001276,"network":"Lightning","wallet_address":"lnbc1nested"}}`
	var o Order
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.WalletAddress != "lnbc1nested" || o.Coin != "BTC" || o.Network != "Lightning" || o.CoinAmount != "0.00001276" {
		t.Fatalf("nested payment was not lifted: %+v", o)
	}
	// And the flat shape still wins when both are present.
	const flat = `{"order_id":"ord_v6","status":"WAITING_FOR_PAYMENT","wallet_address":"lnbc1flat",` +
		`"payment":{"wallet_address":"lnbc1nested"}}`
	var o2 Order
	_ = json.Unmarshal([]byte(flat), &o2)
	if o2.WalletAddress != "lnbc1flat" {
		t.Fatalf("the flat value must not be overwritten: %q", o2.WalletAddress)
	}
}

func TestCommissionIsReportedButNeverTurnedIntoAPrice(t *testing.T) {
	var o Order
	if err := json.Unmarshal([]byte(partnerOrder), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rate, ok := o.CommissionRate()
	if !ok || rate <= 0 {
		t.Fatalf("the partner commission_rate_pct must be readable, got %v %v", rate, ok)
	}
	// Reported as the supplier stated it — the shop never assumes a unit.
	if rate != 0.05 {
		t.Fatalf("commission was altered on the way in: %v", rate)
	}
	var none Order
	if _, ok := none.CommissionRate(); ok {
		t.Fatal("an order without a commission must report none rather than zero-as-a-price")
	}
}

// --- cancellation ----------------------------------------------------------

func TestCancelIsFinalWhenTheSupplierRefusesAndRetryableWhenItIsBusy(t *testing.T) {
	cases := []struct {
		status     int
		retryable  bool
		what       string
		cancelable bool
	}{
		{http.StatusConflict, false, "409 order_not_cancellable (money may be arriving)", false},
		{http.StatusNotFound, false, "404 no such order", true},
		{http.StatusTooManyRequests, true, "429 rate limited", true},
		{http.StatusServiceUnavailable, true, "503 cancel_temporarily_unavailable", true},
	}
	for _, tc := range cases {
		rec := &callRecorder{}
		srv := supplierStub(t, rec, tc.status, `{"error":"nope"}`)
		c := partnerClient(t, srv.URL, "cr_pk_test_key")
		_, err := c.CancelOrder(context.Background(), "ord_v6")
		if err == nil {
			t.Fatalf("%s: cancel must report a failure", tc.what)
		}
		var ce *OrderCancelError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: expected an OrderCancelError, got %T", tc.what, err)
		}
		if ce.Retryable != tc.retryable {
			t.Fatalf("%s: retryable = %v, want %v", tc.what, ce.Retryable, tc.retryable)
		}
	}
}

func TestCancelIsIdempotentAndConfirmsTheOrder(t *testing.T) {
	rec := &callRecorder{}
	// The partner API answers 200 with the cancelled order; a second cancel
	// of the same order answers 200 again rather than 404.
	srv := supplierStub(t, rec, http.StatusOK, `{"order_id":"ord_v6","status":"CANCELED"}`)
	c := partnerClient(t, srv.URL, "cr_pk_test_key")
	o, err := c.CancelOrder(context.Background(), "ord_v6")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if o.ID != "ord_v6" || !IsTerminal(o.Status) {
		t.Fatalf("the cancelled order must come back terminal: %+v", o)
	}
	if _, err := c.CancelOrder(context.Background(), "ord_v6"); err != nil {
		t.Fatalf("a repeated cancel must be harmless: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("both cancels must have been issued, got %d", rec.count())
	}
}

// --- problem decoding across the two generations ---------------------------

func TestProblemArrayIsReadInBothShapes(t *testing.T) {
	// v6 partner: a BARE array using the "problem" key.
	v6 := []byte(`[{"problem":"NOT_AVAILABLE_PRODUCT","moreDetails":{"delivery":{"brand_name":"NoSuchBrandXYZ"}}}]`)
	err := responseProblems(v6, http.StatusUnprocessableEntity)
	var pe *ProblemError
	if !errors.As(err, &pe) || len(pe.Problems) != 1 || pe.Problems[0].Code != "NOT_AVAILABLE_PRODUCT" {
		t.Fatalf("the v6 bare array must be decoded, got %v (%T)", err, err)
	}
	// Regression: the supplier also returns ARRAYS of perfectly healthy data
	// (a catalogue). Those are not problems and must not fail the call.
	catalog := []byte(`[{"family":"Mixed Card","brand":"Retail Card","products":[{"product_id":"retail"}]}]`)
	if err := responseProblems(catalog, http.StatusOK); err != nil {
		t.Fatalf("a product array must never be read as a problem list: %v", err)
	}
	// v5 shape still works.
	v5 := []byte(`{"problems":[{"problem":"OUT_OF_STOCK"}]}`)
	if err := responseProblems(v5, http.StatusOK); !errors.As(err, &pe) || pe.Problems[0].Code != "OUT_OF_STOCK" {
		t.Fatalf("the v5 envelope must still decode, got %v", err)
	}
}

// --- rate limiting ---------------------------------------------------------

func TestPartnerEndpointsHaveTheirOwnBudgetNotTheUnknownFallback(t *testing.T) {
	// An unrecognised path is throttled to 10/min, which would stall the
	// tracker. The partner paths must be recognised.
	for _, tc := range []struct {
		method, path, want string
	}{
		{http.MethodPost, "/v6/partner/orders", "POST /v6/partner/orders"},
		{http.MethodGet, "/v6/partner/orders/ord_v6", "GET /v6/partner/orders/{id}"},
		{http.MethodDelete, "/v6/partner/orders/ord_v6", "DELETE /v6/partner/orders/{id}"},
	} {
		if got := policyFor(tc.method, tc.path); got.name != tc.want {
			t.Fatalf("policyFor(%s %s) = %q, want %q", tc.method, tc.path, got.name, tc.want)
		}
	}
	if got := policyFor(http.MethodGet, "/v6/partner/orders/ord_v6"); got.limit <= 10 || got.window != time.Minute {
		t.Fatalf("the partner poll policy is too tight: %+v", got)
	}
}

// TestPartnerGuideProblemCodesAreAnswerable pins the v6 partner guide's codes
// that are NOT part of the old v5 catalogue. An unknown code is rendered as a
// generic "could not approve" sentence, which tells the buyer nothing — and
// the KYC/velocity codes are exactly the ones a real buyer will hit.
func TestPartnerGuideProblemCodesAreAnswerable(t *testing.T) {
	generic := ProblemMessage("SOMETHING_UNHEARD_OF")
	for _, code := range []string{
		"FULLNAME_MISSING", "CUSTOMER_BLOCKED", "KYC_MISSING", "KYC_PENDING",
		"PARTNER_DAILY_ORDER_VELOCITY_EXCEEDED", "PARTNER_MONTHLY_ORDER_VELOCITY_EXCEEDED",
	} {
		if _, ok := LookupProblem(code); !ok {
			t.Fatalf("%s is not in the customer-safe catalogue", code)
		}
		if got := ProblemMessage(code); got == "" || got == generic {
			t.Fatalf("%s falls back to the generic sentence: %q", code, got)
		}
	}
}

// TestRevokedKeyFailsOverToTheBackup pins the partner guide's rotation rule:
// a 401 (invalid/revoked key) must be served by the configured backup key
// instead of taking the checkout down until someone deploys.
func TestRevokedKeyFailsOverToTheBackup(t *testing.T) {
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-CR-Partner-Key"))
		if r.Header.Get("X-CR-Partner-Key") != "cr_pk_primary" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(partnerOrder))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{
		BaseURL: srv.URL, PartnerKey: "cr_pk_primary", PartnerKeyBackup: "cr_pk_backup",
		AppVersion: "test/1.0", UserAgent: "test-agent",
	})
	defer c.Close()

	if _, err := c.CreateOrder(context.Background(), giftCardRequest()); err != nil {
		t.Fatalf("a revoked primary key must be served by the backup: %v", err)
	}
	if len(seen) != 2 || seen[0] != "cr_pk_primary" || seen[1] != "cr_pk_backup" {
		t.Fatalf("expected primary-then-backup, got %v", seen)
	}
	// Staying on the backup matters: a revoked primary must not cost a failed
	// request on every single call afterwards.
	seen = nil
	if _, err := c.CreateOrder(context.Background(), giftCardRequest()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(seen) != 1 || seen[0] != "cr_pk_backup" {
		t.Fatalf("after the switch every call must go straight to the backup, got %v", seen)
	}
}

func TestWithoutABackupKeyARevokedKeyIsJustAFailure(t *testing.T) {
	srv := supplierStub(t, &callRecorder{}, http.StatusUnauthorized, `{"error":"unauthorized"}`)
	c := partnerClient(t, srv.URL, "cr_pk_primary")
	if _, err := c.CreateOrder(context.Background(), giftCardRequest()); err == nil {
		t.Fatal("a rejected key with no backup must fail loudly, not silently")
	}
}
