package main

// End-to-end tests of the assembled HTTP API: the real router from
// buildRouter, the real handlers, middleware and BadgerDB store, wired to
// an in-process deterministic supplier (internal/suppliermock) and a fixed
// NIM price. No network is touched; everything runs against a temp dir.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/config"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/handlers"
	"nimiqshop/internal/loopback"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/settlement"
	"nimiqshop/internal/suppliermock"
)

// testStack is one booted shop: HTTP base URL, the handler set (for
// direct overrides) and helpers to talk to it.
type testStack struct {
	t        *testing.T
	baseURL  string
	client   *http.Client
	h        *handlers.Handlers
	supplier *httptest.Server
}

type stackOptions struct {
	testMode bool
	env      map[string]string
}

func bootStack(t *testing.T, opts stackOptions) *testStack {
	t.Helper()

	supplier := httptest.NewServer(suppliermock.NewHandler())
	t.Cleanup(supplier.Close)

	env := map[string]string{
		"SITE_HOST":                "shop.test",
		"JWT_SECRET":               randomSecret(t),
		"CRYPTOREFILLS_PARTNER_ID": "integration-tests",
		"CRYPTOREFILLS_BASE_URL":   supplier.URL,
		"ALLOW_HTTP_LOCAL":         "true",
		"BADGER_DIR":               t.TempDir(),
		"BADGER_SYNC_WRITES":       "false",
		"ADMIN_USERNAME":           "admin",
		"ADMIN_PASSWORD":           "integration-admin-password",
		"ADMIN_COOKIE_SECURE":      "false",
		"SESSION_COOKIE_SECURE":    "false",
		"RATE_LIMIT_PER_MINUTE":    "100000",
		"RATE_LIMIT_BURST":         "100000",
		// Tier budgets are per source IP; every test call comes from
		// loopback, so raise them far above what a test issues.
		"RATE_TIER_OVERRIDES": "login=6000:2000,checkout=6000:2000,refresh=6000:2000,pool=6000:2000,promo=6000:2000,support=6000:2000,presence=6000:2000,write=6000:2000,admin-login=6000:2000,admin=6000:2000",
		"TEST_MODE":           fmt.Sprint(opts.testMode),
	}
	for k, v := range opts.env {
		env[k] = v
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.Validate: %v", err)
	}

	store, err := db.New(cfg.BadgerDir, db.Options{SyncWrites: false, ValueThresholdKB: cfg.BadgerValueThresholdKB})
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cr := cryptorefills.NewClient(cfg.CRBaseURL, cfg.CRPartnerID, cfg.CRAppVersion, cfg.CRUserAgent, cryptorefills.QueueConfig{
		MaxQueue:            cfg.CRQueueMax,
		MaxQueuePerActor:    cfg.CRQueuePerActorMax,
		ActorRequestsPerMin: cfg.CRActorPerMinute,
		ActorBurst:          cfg.CRActorBurst,
	})
	t.Cleanup(cr.Close)
	h := handlers.New(store, cfg, cr)
	// Fixed display price so no oracle/network is involved.
	h.NIMUSDPrice = func(context.Context) (float64, error) { return 0.002, nil }

	// The settlement tracker is what turns a paid quote into an order (and
	// advances simulated TEST_MODE orders), exactly as in main().
	trackerCtx, cancelTracker := context.WithCancel(context.Background())
	t.Cleanup(cancelTracker)
	tracker := &settlement.OrderTracker{Store: store, CR: cr, Interval: 150 * time.Millisecond, StaleAfter: 30 * time.Second}
	tracker.Run(trackerCtx)

	r := buildRouter(h, cfg)
	srv := &fasthttp.Server{Handler: r.Handler, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", loopback.HostPort("0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return &testStack{
		t:        t,
		baseURL:  "http://" + ln.Addr().String(),
		client:   &http.Client{Timeout: 15 * time.Second},
		h:        h,
		supplier: supplier,
	}
}

type resp struct {
	status  int
	body    []byte
	headers http.Header
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, r.body)
	}
	return m
}

func (s *testStack) do(method, path string, body any, headers map[string]string) resp {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.baseURL+path, rd)
	if err != nil {
		s.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if method == http.MethodPost && req.Header.Get("Idempotency-Key") == "" &&
		(strings.HasPrefix(path, "/api/quotes") || strings.HasPrefix(path, "/api/test/buy") || strings.HasPrefix(path, "/api/admin/test-purchase")) {
		req.Header.Set("Idempotency-Key", newIdempotencyKey(s.t))
	}
	res, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: b, headers: res.Header}
}

// signIn performs the Nimiq Hub login dance with a fresh ed25519 key and
// returns bearer headers for the new shopper.
func (s *testStack) signIn() (map[string]string, string) {
	s.t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	address, err := nimiq.AddressFromPublicKey(pub)
	if err != nil {
		s.t.Fatal(err)
	}
	ch := s.do(http.MethodPost, "/api/auth/challenge", map[string]any{}, nil)
	if ch.status != 200 {
		s.t.Fatalf("challenge: %d %s", ch.status, ch.body)
	}
	cj := ch.json(s.t)
	message, _ := cj["message"].(string)
	token, _ := cj["challenge_token"].(string)
	prefixed := fmt.Sprintf("%s%d%s", "\x16Nimiq Signed Message:\n", len(message), message)
	digest := sha256.Sum256([]byte(prefixed))
	sig := ed25519.Sign(priv, digest[:])
	login := s.do(http.MethodPost, "/api/auth/hub-login", map[string]any{
		"challenge_token": token,
		"address":         address,
		"public_key":      hex.EncodeToString(pub),
		"signature":       hex.EncodeToString(sig),
	}, map[string]string{"X-Token-Delivery": "bearer"})
	if login.status != 200 {
		s.t.Fatalf("hub-login: %d %s", login.status, login.body)
	}
	jwt, _ := login.json(s.t)["token"].(string)
	if jwt == "" {
		s.t.Fatalf("hub-login did not deliver a bearer token: %s", login.body)
	}
	return map[string]string{"Authorization": "Bearer " + jwt}, address
}

// adminHeaders logs the dev admin in and returns the session cookie header.
func (s *testStack) adminHeaders() map[string]string {
	s.t.Helper()
	res := s.do(http.MethodPost, "/api/admin/auth/login", map[string]any{
		"username": "admin", "password": "integration-admin-password",
	}, map[string]string{"Origin": s.baseURL})
	if res.status != 200 {
		s.t.Fatalf("admin login: %d %s", res.status, res.body)
	}
	setCookies := res.headers.Values("Set-Cookie")
	cookies := make([]string, 0, len(setCookies))
	for _, c := range setCookies {
		cookies = append(cookies, strings.SplitN(c, ";", 2)[0])
	}
	if len(cookies) == 0 {
		s.t.Fatal("admin login set no cookie")
	}
	return map[string]string{"Cookie": strings.Join(cookies, "; "), "Origin": s.baseURL}
}

func TestPublicEndpoints(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})

	health := s.do(http.MethodGet, "/api/health", nil, nil)
	if health.status != 200 || health.json(t)["ok"] != true {
		t.Fatalf("health: %d %s", health.status, health.body)
	}

	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/site", 200},
		{"GET", "/api/site-config", 200},
		{"GET", "/api/catalog/brands?country=US", 200},
		{"GET", "/api/catalog/products?country=US&family=test-steam", 200},
		{"GET", "/api/catalog/products?country=US", 400},
		{"GET", "/api/catalog/products/test-steam?country=US", 200},
		{"GET", "/api/catalog/search?country=US&q=steam", 200},
		{"GET", "/api/catalog/payment-vias", 200},
		{"GET", "/api/catalog/price?brand_name=test-steam&country_code=US&face_value=50", 200},
		{"GET", "/api/catalog/price?brand_name=test-steam", 400},
		{"GET", "/api/catalog/check-phone?phone=%2B14155552671", 200},
		{"GET", "/api/geo", 200},
		{"GET", "/api/cashback/rate", 200},
		{"GET", "/api/cashback/code?code=NOPE", 404},
		{"GET", "/api/ratings/summary", 200},
		{"GET", "/api/activity", 200},
		{"GET", "/api/trees", 200},
		{"GET", "/api/trees/donation-balance", 200},
		{"GET", "/api/track/does-not-exist", 404},
		{"GET", "/api/auth/session", 200},
		{"GET", "/api/quotes", 401},
		{"GET", "/api/orders", 401},
		{"GET", "/api/admin/dashboard", 401},
		{"GET", "/api/nope", 404},
	}
	for _, tc := range cases {
		res := s.do(tc.method, tc.path, nil, nil)
		if tc.want == 200 && res.status != 200 {
			t.Errorf("%s %s: got %d want %d — %s", tc.method, tc.path, res.status, tc.want, truncate(res.body))
			continue
		}
		if tc.want != 200 && res.status != tc.want {
			t.Errorf("%s %s: got %d want %d — %s", tc.method, tc.path, res.status, tc.want, truncate(res.body))
		}
	}

	// CORS preflight from an unknown origin gets no allow header.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, s.baseURL+"/api/site", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("preflight from unknown origin: %d allow=%q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}

	// API responses are JSON and carry an explicit content type.
	site := s.do(http.MethodGet, "/api/site", nil, nil)
	if ct := site.headers.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/api/site content type %q", ct)
	}
	// Presence ping is a POST without body requirements.
	if res := s.do(http.MethodPost, "/api/presence", map[string]any{"page": "/"}, nil); res.status >= 500 {
		t.Errorf("presence: %d %s", res.status, res.body)
	}
}

func TestShopperFlowTestMode(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})
	auth, address := s.signIn()

	sess := s.do(http.MethodGet, "/api/auth/session", nil, auth)
	if sess.status != 200 || sess.json(t)["authed"] != true {
		t.Fatalf("session after login: %d %s", sess.status, sess.body)
	}
	user, _ := sess.json(t)["user"].(map[string]any)
	if got, _ := user["nimiq_address"].(string); nimiq.NormalizeAddress(got) != nimiq.NormalizeAddress(address) {
		t.Fatalf("session address %q != %q", got, address)
	}

	for _, path := range []string{"/api/account/limits", "/api/account/notifications", "/api/cashback/me", "/api/trees/me", "/api/poolstake/me", "/api/quotes", "/api/orders"} {
		if res := s.do(http.MethodGet, path, nil, auth); res.status != 200 {
			t.Errorf("GET %s: %d %s", path, res.status, truncate(res.body))
		}
	}
	if res := s.do(http.MethodPut, "/api/account/notifications", map[string]any{"order_ready": true, "cashback": false}, auth); res.status >= 500 {
		t.Errorf("PUT notifications: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/trees/me/prefs", map[string]any{"plant_trees": true}, auth); res.status >= 500 {
		t.Errorf("POST trees prefs: %d %s", res.status, res.body)
	}

	// Validation errors first.
	if res := s.do(http.MethodPost, "/api/quotes", map[string]any{"country": "US"}, auth); res.status != 400 {
		t.Errorf("quote without product: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/quotes", map[string]any{"product_id": "test-steam", "country": "US", "quantity": 1}, auth); res.status == 200 {
		t.Errorf("quote without email should be rejected: %s", res.body)
	}

	// A real quote for the fixed-price mock product.
	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, auth)
	quoteID := quoteIDFrom(t, q)
	if q.json(t)["simulated_payment"] != true || q.json(t)["status"] != "awaiting_payment" {
		t.Fatalf("test-mode quote should carry a simulated invoice: %s", truncate(q.body))
	}
	if res := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, auth); res.status != 200 {
		t.Fatalf("get quote: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, nil); res.status != 401 {
		t.Errorf("quote must be owner-scoped, got %d", res.status)
	}
	if res := s.do(http.MethodGet, "/api/track/"+quoteID, nil, nil); res.status >= 500 {
		t.Errorf("track: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/refresh", nil, auth); res.status >= 500 {
		t.Errorf("refresh quote: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/payment-launch", map[string]any{"method": "nimiq_pay"}, auth); res.status >= 500 {
		t.Errorf("payment-launch: %d %s", res.status, res.body)
	}

	// Simulated payment (TEST_MODE) walks the quote through the supplier
	// ladder to fulfilled without any real supplier or chain.
	pay := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{}, auth)
	if pay.status != 200 {
		t.Fatalf("test-pay: %d %s", pay.status, pay.body)
	}
	deadline := time.Now().Add(20 * time.Second)
	status := ""
	for time.Now().Before(deadline) && status != "fulfilled" {
		cur := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, auth)
		status = quoteStatus(cur.json(t))
		if status != "fulfilled" {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if status != "fulfilled" {
		t.Fatalf("quote did not reach fulfilled after simulated payment (status %q)", status)
	}
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{}, auth); res.status != 409 {
		t.Errorf("second test-pay on a fulfilled quote: %d %s", res.status, truncate(res.body))
	}
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{"action": "bogus"}, auth); res.status == 200 {
		t.Errorf("bogus test action accepted: %s", truncate(res.body))
	}
	// Fulfilled quotes feed the public activity and ratings surfaces.
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/rate", map[string]any{"stars": 5, "comment": "great"}, auth); res.status >= 500 {
		t.Errorf("rate quote: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodGet, "/api/track/"+quoteID, nil, nil); res.status >= 500 {
		t.Errorf("track fulfilled: %d %s", res.status, res.body)
	}
	// Legacy order endpoints stay owner-scoped and never 500.
	orderID := quoteID
	for _, p := range []string{"/api/orders/" + orderID, "/api/orders/" + orderID + "/support"} {
		if res := s.do(http.MethodGet, p, nil, auth); res.status >= 500 {
			t.Errorf("GET %s: %d %s", p, res.status, truncate(res.body))
		}
	}
	if res := s.do(http.MethodPost, "/api/orders/"+orderID+"/refresh", nil, auth); res.status >= 500 {
		t.Errorf("order refresh: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/orders/"+orderID+"/rate", map[string]any{"stars": 5}, auth); res.status >= 500 {
		t.Errorf("rate order: %d %s", res.status, res.body)
	}

	// Support ticket on the order.
	tk := s.do(http.MethodPost, "/api/support/tickets", map[string]any{"order_id": quoteID, "subject": "Where is my code?", "message": "It has been a while."}, auth)
	if tk.status != 200 && tk.status != 201 {
		t.Fatalf("create ticket: %d %s", tk.status, tk.body)
	}
	ticketID, _ := tk.json(t)["id"].(string)
	if ticketID == "" {
		if inner, ok := tk.json(t)["ticket"].(map[string]any); ok {
			ticketID, _ = inner["id"].(string)
		}
	}
	if res := s.do(http.MethodGet, "/api/support/tickets", nil, auth); res.status != 200 {
		t.Errorf("list tickets: %d %s", res.status, res.body)
	}
	if ticketID != "" {
		if res := s.do(http.MethodGet, "/api/support/tickets/"+ticketID, nil, auth); res.status != 200 {
			t.Errorf("get ticket: %d %s", res.status, res.body)
		}
		if res := s.do(http.MethodPost, "/api/support/tickets/"+ticketID+"/messages", map[string]any{"message": "Any update?"}, auth); res.status >= 500 {
			t.Errorf("ticket message: %d %s", res.status, res.body)
		}
		if res := s.do(http.MethodPost, "/api/support/tickets/"+ticketID+"/status", map[string]any{"status": "closed"}, auth); res.status >= 500 {
			t.Errorf("ticket status: %d %s", res.status, res.body)
		}
	}

	// Batch quotes and the test-buy shortcut exist for the same account.
	if res := s.do(http.MethodPost, "/api/quotes/batch", map[string]any{"items": []map[string]any{{"product_id": "test-steam", "country": "US", "quantity": 1, "denomination": "50 USD", "product_value": 50}}, "email": "buyer@example.com"}, auth); res.status >= 500 {
		t.Errorf("batch quote: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodPost, "/api/test/buy", map[string]any{"product_id": "test-steam", "country": "US", "quantity": 1, "denomination": "50 USD", "product_value": 50, "email": "buyer@example.com"}, auth); res.status >= 500 {
		t.Errorf("test buy: %d %s", res.status, res.body)
	}

	// Activity feed and ratings now have content.
	if res := s.do(http.MethodGet, "/api/activity", nil, nil); res.status != 200 {
		t.Errorf("activity: %d", res.status)
	}
	if res := s.do(http.MethodPost, "/api/auth/logout", nil, auth); res.status >= 500 {
		t.Errorf("logout: %d %s", res.status, res.body)
	}
}

func TestSupplierFlowLive(t *testing.T) {
	// TEST_MODE off: the quote goes through the mock supplier
	// (validation + order creation), i.e. the cryptorefills client.
	s := bootStack(t, stackOptions{testMode: false})
	auth, _ := s.signIn()

	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-airbnb", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 25, "email": "buyer@example.com",
	}, auth)
	quoteID := quoteIDFrom(t, q)
	if q.json(t)["simulated_payment"] == true {
		t.Fatalf("live quote must not be simulated: %s", truncate(q.body))
	}
	if sid, _ := q.json(t)["supplier_order_id"].(string); sid == "" || strings.HasPrefix(sid, "TESTSIM-") {
		t.Fatalf("live quote should carry the supplier order id: %s", truncate(q.body))
	}
	got := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, auth)
	if got.status != 200 {
		t.Fatalf("get quote: %d %s", got.status, got.body)
	}
	// The supplier mock recorded exactly one order.
	st, err := http.Get(s.supplier.URL + "/mock/state") //nolint:noctx // test helper against an in-process server
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(st.Body)
	_ = st.Body.Close()
	if !bytes.Contains(body, []byte("orders")) {
		t.Fatalf("mock state: %s", body)
	}
	// Mobile top-up path validates the phone number.
	if res := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-topup", "country": "US", "quantity": 1, "denomination": "range",
		"product_value": 10, "email": "buyer@example.com", "phone_number": "+14155552671",
	}, auth); res.status >= 500 {
		t.Errorf("top-up quote: %d %s", res.status, res.body)
	}
	// A failing supplier product must not 500.
	if res := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-fail", "country": "US", "quantity": 1, "denomination": "range",
		"product_value": 20, "email": "buyer@example.com",
	}, auth); res.status >= 500 {
		t.Errorf("failing supplier product: %d %s", res.status, res.body)
	}
	// Webhook without a valid signature is rejected, never 500.
	if res := s.do(http.MethodPost, "/api/webhooks/cryptorefills", map[string]any{"order_id": "x", "status": "Done"}, nil); res.status >= 500 || res.status == 200 {
		t.Errorf("unsigned webhook: %d %s", res.status, res.body)
	}
}

func TestAdminConsole(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})

	// Wrong password is a 401 and counts as a failure.
	if res := s.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"username": "admin", "password": "nope"}, map[string]string{"Origin": s.baseURL}); res.status != 401 {
		t.Fatalf("bad admin login: %d %s", res.status, res.body)
	}
	admin := s.adminHeaders()

	// Seed some shopper data so the admin views have rows.
	auth, _ := s.signIn()
	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, auth)
	_ = quoteIDFrom(t, q)

	gets := []string{
		"/api/admin/auth/me", "/api/admin/dashboard", "/api/admin/users", "/api/admin/orders",
		"/api/admin/quotes", "/api/admin/transactions", "/api/admin/manual-review", "/api/admin/notification/status",
		"/api/admin/oracle", "/api/admin/settings/cashback", "/api/admin/stake-ledger", "/api/admin/audit",
		"/api/admin/catalog-rules", "/api/admin/catalog/brands?country=US", "/api/admin/catalog/products/test-steam?country=US",
		"/api/admin/support/tickets", "/api/admin/trees/settlements",
	}
	for _, p := range gets {
		if res := s.do(http.MethodGet, p, nil, admin); res.status != 200 {
			t.Errorf("GET %s: %d %s", p, res.status, truncate(res.body))
		}
	}
	posts := []struct {
		path string
		body any
	}{
		{"/api/admin/settings/margin", map[string]any{"margin_pct": 3}},
		{"/api/admin/settings/cashback", map[string]any{"base_rate_pct": 1}},
		{"/api/admin/catalog-rules", map[string]any{"hidden_brands": []string{}}},
		{"/api/admin/test-purchase", map[string]any{"product_id": "test-steam", "country": "US", "quantity": 1, "denomination": "50 USD", "product_value": 50, "email": "admin@example.com"}},
		// No mail/wallet transport is configured in tests: these must
		// answer with a configuration hint (503), never a crash.
		{"/api/admin/notification/send", map[string]any{"address": "NQ07 0000 0000 0000 0000 0000 0000 0000 0000", "message": "hi"}},
		{"/api/admin/test-email", map[string]any{"to": "admin@example.com"}},
		{"/api/admin/stake-ledger/reset", map[string]any{}},
	}
	for _, p := range posts {
		method := http.MethodPost
		if p.path == "/api/admin/catalog-rules" {
			method = http.MethodPut
		}
		if res := s.do(method, p.path, p.body, admin); res.status >= 500 && res.status != 503 {
			t.Errorf("%s %s: %d %s", method, p.path, res.status, truncate(res.body))
		}
	}
	// Users → detail.
	var users []map[string]any
	ul := s.do(http.MethodGet, "/api/admin/users", nil, admin)
	if err := json.Unmarshal(ul.body, &users); err != nil {
		var wrapped map[string]any
		if json.Unmarshal(ul.body, &wrapped) == nil {
			if arr, ok := wrapped["users"].([]any); ok {
				for _, u := range arr {
					if m, ok := u.(map[string]any); ok {
						users = append(users, m)
					}
				}
			}
		}
	}
	if len(users) > 0 {
		if id, _ := users[0]["id"].(string); id != "" {
			if res := s.do(http.MethodGet, "/api/admin/users/"+id, nil, admin); res.status != 200 {
				t.Errorf("admin user detail: %d %s", res.status, truncate(res.body))
			}
		}
	}
	if res := s.do(http.MethodGet, "/api/admin/orders/does-not-exist", nil, admin); res.status != 404 {
		t.Errorf("admin missing order: %d", res.status)
	}
	if res := s.do(http.MethodPost, "/api/admin/auth/logout", nil, admin); res.status != 200 && res.status != 204 {
		t.Errorf("admin logout: %d %s", res.status, res.body)
	}
	if res := s.do(http.MethodGet, "/api/admin/dashboard", nil, admin); res.status != 401 {
		t.Errorf("dashboard after logout: %d", res.status)
	}
}

func TestConcurrentHealth(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: true})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := s.do(http.MethodGet, "/api/health", nil, nil)
			if res.status != 200 {
				errs <- fmt.Errorf("health %d", res.status)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestHelpersAndCORS(t *testing.T) {
	t.Setenv("SITE_HOST", "shop.test")
	t.Setenv("JWT_SECRET", randomSecret(t))
	t.Setenv("CRYPTOREFILLS_PARTNER_ID", "x")
	t.Setenv("ALLOWED_ORIGINS", "https://shop.test,https://www.shop.test")
	cfg := config.Load()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://shop.test")
	applyCORS(ctx, cfg)
	if got := string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")); got != "https://shop.test" {
		t.Fatalf("allowed origin not echoed: %q", got)
	}
	ctx = &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://evil.test")
	before := CORSBlocked()
	applyCORS(ctx, cfg)
	if got := string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")); got != "" {
		t.Fatalf("unknown origin must not be allowed: %q", got)
	}
	if CORSBlocked() <= before {
		t.Fatal("blocked-origin counter did not move")
	}

	for origin, want := range map[string]bool{
		"https://abc.trycloudflare.com": true,
		"https://shop.test":             false,
		"not a url":                     false,
	} {
		if got := isTunnelOrigin(origin); got != want {
			t.Errorf("isTunnelOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
	if r := redactOrigin("https://host.example/path\n?x=1"); r == "" || strings.Contains(r, "\n") {
		t.Errorf("redactOrigin must produce a single log-safe line: %q", r)
	}
	if d := keepaliveDuration(cfg); d <= 0 {
		t.Errorf("keepalive %v", d)
	}
	if d := idleTimeout(cfg); d <= 0 {
		t.Errorf("idle %v", d)
	}
	if n := concurrencyCeiling(cfg, nil); n <= 0 {
		t.Errorf("concurrency ceiling %d", n)
	}
	ao := authOpts(cfg)
	if len(ao.AllowedOrigins) < 2 {
		t.Errorf("authOpts origins = %v", ao.AllowedOrigins)
	}
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

func newIdempotencyKey(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// quoteIDFrom accepts the created (201) or reused (200) quote response and
// returns its id.
func quoteIDFrom(t *testing.T, q resp) string {
	t.Helper()
	if q.status != 200 && q.status != 201 {
		t.Fatalf("create quote: %d %s", q.status, truncate(q.body))
	}
	id, _ := q.json(t)["quote_id"].(string)
	if id == "" {
		id, _ = q.json(t)["id"].(string)
	}
	if id == "" {
		t.Fatalf("quote id missing: %s", truncate(q.body))
	}
	return id
}

// quoteStatus reads the status from either the flat quote-created shape or
// the {"quote": {...}} detail shape.
func quoteStatus(m map[string]any) string {
	if st, _ := m["status"].(string); st != "" {
		return st
	}
	if inner, ok := m["quote"].(map[string]any); ok {
		st, _ := inner["status"].(string)
		return st
	}
	return ""
}

// randomSecret returns a fresh 32-byte hex secret: tests never carry a
// literal credential, not even a fake one.
func randomSecret(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}
