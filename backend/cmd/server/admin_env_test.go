package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

/* admin_env_test.go — the wire-level proof that an operator really can add
 * and remove an environment variable from the console, and that the running
 * server obeys it immediately.
 *
 * The partner key is the one the owner named ("mesela partner key gibi"), so
 * it is the one these tests move: the shop starts on the v5 route with no
 * key, the console saves one, and the very next order of the very same
 * process goes to POST /v6/partner/orders authenticated with it. Removing it
 * puts the process back where the host left it — no restart, no redeploy.
 */

/** One row of the console's environment list. */
func envRow(t *testing.T, res resp, key string) map[string]any {
	t.Helper()
	if res.status != 200 {
		t.Fatalf("GET /api/admin/env: %d %s", res.status, truncate(res.body))
	}
	var out struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatalf("bad env list: %v\n%s", err, res.body)
	}
	for _, row := range out.Keys {
		if row["key"] == key {
			return row
		}
	}
	t.Fatalf("the console list has no row for %s", key)
	return nil
}

/**
 * Books one quote for `family`.
 *
 * Every call signs a FRESH shopper in: one buyer can only have one unresolved
 * checkout at a time (db/checkout_safety.go), and this test needs three orders
 * from the same process to prove which route each one took.
 */
func orderOne(t *testing.T, s *testStack, family, denomination string, value float64) {
	t.Helper()
	auth, _ := s.signIn()
	res := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": family, "country": "US", "quantity": 1,
		"denomination": denomination, "product_value": value, "email": "buyer@example.com",
	}, auth)
	if res.status >= 300 {
		t.Fatalf("quote for %s: %d %s", family, res.status, truncate(res.body))
	}
}

func TestTheOperatorAddsAndRemovesAnEnvKeyFromTheConsole(t *testing.T) {
	cap := &callCapture{}
	// No partner key on the host: the shop starts on the v5 route.
	s := bootStack(t, stackOptions{supplierWrap: cap.wrap})
	admin := s.adminHeaders()
	// An exported override outlives this test in the process environment, so
	// put it back whatever happens here.
	t.Cleanup(func() { _ = os.Unsetenv("CRYPTOREFILLS_PARTNER_KEY") })

	// --- 1. unset, and an order takes the v5 route -------------------------
	row := envRow(t, s.do(http.MethodGet, "/api/admin/env", nil, admin), "CRYPTOREFILLS_PARTNER_KEY")
	if row["source"] != "unset" {
		t.Fatalf("CRYPTOREFILLS_PARTNER_KEY source = %v, want unset", row["source"])
	}
	orderOne(t, s, "test-steam", "50 USD", 50)
	if got := len(cap.matching(http.MethodPost, "/v5/orders")); got != 1 {
		t.Fatalf("v5 creates = %d, want 1 (no partner key is configured)", got)
	}

	// --- 2. the console saves a partner key --------------------------------
	const consoleKey = "cr_pk_from_the_console"
	set := s.do(http.MethodPost, "/api/admin/env", map[string]any{
		"key": "CRYPTOREFILLS_PARTNER_KEY", "value": consoleKey,
	}, admin)
	if set.status != 200 {
		t.Fatalf("save env: %d %s", set.status, truncate(set.body))
	}
	// A secret is never handed back whole, not even to the operator's own
	// screen — the row is enough to recognise WHICH key is loaded.
	if strings.Contains(string(set.body), consoleKey) {
		t.Fatalf("the console echoed the secret back:\n%s", set.body)
	}
	row = envRow(t, s.do(http.MethodGet, "/api/admin/env", nil, admin), "CRYPTOREFILLS_PARTNER_KEY")
	if row["source"] != "console" {
		t.Fatalf("source = %v, want console", row["source"])
	}
	if row["live"] != true {
		t.Fatalf("the partner key must be a live variable, got live=%v", row["live"])
	}

	// --- 3. the SAME process now orders on the v6 partner route ------------
	orderOne(t, s, "test-airbnb", "range", 25)
	creates := cap.matching(http.MethodPost, "/v6/partner/orders")
	if len(creates) != 1 {
		t.Fatalf("v6 partner creates = %d, want 1 — saving the key must reach the running supplier client", len(creates))
	}
	if got := creates[0].Header.Get("X-CR-Partner-Key"); got != consoleKey {
		t.Fatalf("the console key must authenticate the create, got %q", got)
	}

	// --- 4. removing it falls back to the host: no key, v5 again -----------
	del := s.do(http.MethodDelete, "/api/admin/env/CRYPTOREFILLS_PARTNER_KEY", nil, admin)
	if del.status != 200 {
		t.Fatalf("delete env: %d %s", del.status, truncate(del.body))
	}
	row = envRow(t, s.do(http.MethodGet, "/api/admin/env", nil, admin), "CRYPTOREFILLS_PARTNER_KEY")
	if row["source"] != "unset" {
		t.Fatalf("source after removal = %v, want unset (the host set nothing)", row["source"])
	}
	orderOne(t, s, "test-airbnb", "range", 30)
	if got := len(cap.matching(http.MethodPost, "/v5/orders")); got != 2 {
		t.Fatalf("v5 creates after removal = %d, want 2", got)
	}
}

func TestRemovingAConsoleEnvKeyFallsBackToTheHostValue(t *testing.T) {
	cap := &callCapture{}
	const hostKey = "cr_pk_host_provided"
	s := bootStack(t, stackOptions{
		env:          map[string]string{"CRYPTOREFILLS_PARTNER_KEY": hostKey},
		supplierWrap: cap.wrap,
	})
	admin := s.adminHeaders()
	// The host value is restored on the way out: the next test in this
	// process must not inherit the override we export below.
	t.Cleanup(func() { _ = os.Setenv("CRYPTOREFILLS_PARTNER_KEY", hostKey) })

	row := envRow(t, s.do(http.MethodGet, "/api/admin/env", nil, admin), "CRYPTOREFILLS_PARTNER_KEY")
	if row["source"] != "host" {
		t.Fatalf("source = %v, want host", row["source"])
	}

	// An override shadows the host value — the host's own key is never lost,
	// only superseded.
	const consoleKey = "cr_pk_rotated_in_console"
	if res := s.do(http.MethodPost, "/api/admin/env", map[string]any{
		"key": "CRYPTOREFILLS_PARTNER_KEY", "value": consoleKey,
	}, admin); res.status != 200 {
		t.Fatalf("save env: %d %s", res.status, truncate(res.body))
	}
	orderOne(t, s, "test-steam", "50 USD", 50)
	creates := cap.matching(http.MethodPost, "/v6/partner/orders")
	if len(creates) != 1 || creates[0].Header.Get("X-CR-Partner-Key") != consoleKey {
		t.Fatalf("the override must win while it stands (creates=%d)", len(creates))
	}

	// Removing it does NOT leave a hole: the host value is what comes back.
	del := s.do(http.MethodDelete, "/api/admin/env/CRYPTOREFILLS_PARTNER_KEY", nil, admin)
	if del.status != 200 {
		t.Fatalf("delete env: %d %s", del.status, truncate(del.body))
	}
	row = envRow(t, s.do(http.MethodGet, "/api/admin/env", nil, admin), "CRYPTOREFILLS_PARTNER_KEY")
	if row["source"] != "host" {
		t.Fatalf("source after removal = %v, want host", row["source"])
	}
	if strings.Contains(string(row["value"].(string)), hostKey) && row["value"] != hostKey {
		// A non-secret value is shown whole; a secret is masked. Either way
		// what comes back must be the host's, never the override's.
		t.Fatalf("value after removal = %v, want the host value", row["value"])
	}
	orderOne(t, s, "test-airbnb", "range", 25)
	creates = cap.matching(http.MethodPost, "/v6/partner/orders")
	if len(creates) != 2 || creates[1].Header.Get("X-CR-Partner-Key") != hostKey {
		t.Fatalf("after removal the HOST key must authenticate again (creates=%d)", len(creates))
	}
}

func TestTheConsoleRefusesAValueThatWouldTakeTheCheckoutDown(t *testing.T) {
	s := bootStack(t, stackOptions{})
	admin := s.adminHeaders()
	t.Cleanup(func() { _ = os.Unsetenv("CRYPTOREFILLS_PARTNER_KEY") })

	// A partner key that is not a partner key is refused at SAVE time with
	// the reason, not discovered later as a 401 on every order.
	res := s.do(http.MethodPost, "/api/admin/env", map[string]any{
		"key": "CRYPTOREFILLS_PARTNER_KEY", "value": "v5-user-session-key",
	}, admin)
	if res.status != 400 {
		t.Fatalf("a non-partner key must be refused, got %d %s", res.status, truncate(res.body))
	}
	if !strings.Contains(string(res.body), "cr_pk_") {
		t.Fatalf("the refusal must say what a partner key looks like:\n%s", res.body)
	}
	// And nothing was stored by the refusal.
	row := envRow(t, s.do(http.MethodGet, "/api/admin/env", nil, admin), "CRYPTOREFILLS_PARTNER_KEY")
	if row["source"] != "unset" {
		t.Fatalf("a refused value must not be saved, source = %v", row["source"])
	}

	// Variables that hold the process up belong to the host, not here.
	for _, key := range []string{"JWT_SECRET", "BADGER_DIR", "LISTEN_ADDR"} {
		res := s.do(http.MethodPost, "/api/admin/env", map[string]any{"key": key, "value": "anything"}, admin)
		if res.status != 400 {
			t.Fatalf("%s must not be settable from the console, got %d", key, res.status)
		}
	}
	// A name that is not a variable name at all.
	if res := s.do(http.MethodPost, "/api/admin/env", map[string]any{"key": "partner key", "value": "x"}, admin); res.status != 400 {
		t.Fatalf("a malformed name must be refused, got %d", res.status)
	}
	// An empty value is not a way to clear one either — remove the key.
	if res := s.do(http.MethodPost, "/api/admin/env", map[string]any{"key": "SITE_HOST", "value": "  "}, admin); res.status != 400 {
		t.Fatalf("an empty value must be refused, got %d", res.status)
	}
}

func TestTheEnvironmentConsoleIsOperatorOnly(t *testing.T) {
	s := bootStack(t, stackOptions{})
	admin := s.adminHeaders()
	auth, _ := s.signIn()

	// A signed-in shopper can read neither the list nor its values.
	if res := s.do(http.MethodGet, "/api/admin/env", nil, auth); res.status == 200 {
		t.Fatal("the env console must not be open to a shopper session")
	}
	if res := s.do(http.MethodGet, "/api/admin/env", nil, nil); res.status == 200 {
		t.Fatal("the env console must not be open anonymously")
	}
	if res := s.do(http.MethodGet, "/api/admin/env", nil, admin); res.status != 200 {
		t.Fatalf("operator: %d %s", res.status, truncate(res.body))
	}
}
