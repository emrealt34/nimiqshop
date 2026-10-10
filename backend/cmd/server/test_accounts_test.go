package main

import (
	"net/http"
	"net/url"
	"testing"

	"nimiqshop/internal/nimiq"
)

// TestTestAccountSimulatedPayment runs with TEST_MODE OFF (the live setting).
// Only a wallet the admin lists gets the simulated checkout and the pay
// button; another signed-in wallet is unaffected and cannot pay the order.
func TestTestAccountSimulatedPayment(t *testing.T) {
	s := bootStack(t, stackOptions{})
	admin := s.adminHeaders()
	listed, address := s.signIn()
	other, _ := s.signIn()

	sessionFlag := func(auth map[string]string) bool {
		res := s.do(http.MethodGet, "/api/auth/session", nil, auth)
		if res.status != 200 {
			t.Fatalf("session: %d %s", res.status, truncate(res.body))
		}
		return res.json(t)["is_test_account"] == true
	}

	// Not listed yet: no flag, and a plain shopper cannot reach the admin list.
	if sessionFlag(listed) {
		t.Fatal("wallet is flagged as a test account before the admin lists it")
	}
	if res := s.do(http.MethodGet, "/api/admin/test-accounts", nil, listed); res.status != 401 {
		t.Errorf("shopper must not read the admin test-account list, got %d", res.status)
	}

	// Admin adds the wallet. Validation and duplicates are enforced.
	if res := s.do(http.MethodPost, "/api/admin/test-accounts", map[string]any{"address": "NQ12 3456"}, admin); res.status != 400 {
		t.Errorf("invalid address must be rejected with 400, got %d %s", res.status, res.body)
	}
	add := s.do(http.MethodPost, "/api/admin/test-accounts", map[string]any{"address": address, "label": "Emre phone"}, admin)
	if add.status != 201 {
		t.Fatalf("add test account: %d %s", add.status, truncate(add.body))
	}
	if dup := s.do(http.MethodPost, "/api/admin/test-accounts", map[string]any{"address": address}, admin); dup.status != 409 {
		t.Errorf("duplicate must be rejected with 409, got %d", dup.status)
	}
	if !sessionFlag(listed) {
		t.Fatal("listed wallet should be flagged as a test account")
	}
	if sessionFlag(other) {
		t.Fatal("an unlisted wallet must not be flagged")
	}

	// The listed wallet's order runs the simulated path and offers the button.
	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, listed)
	if q.status >= 300 {
		t.Fatalf("listed wallet quote: %d %s", q.status, truncate(q.body))
	}
	if q.json(t)["simulated_payment"] != true {
		t.Fatalf("listed wallet quote should be simulated: %s", truncate(q.body))
	}
	quoteID := quoteIDFrom(t, q)
	view := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, listed)
	if view.status != 200 || view.json(t)["test_pay"] != true {
		t.Fatalf("listed wallet should see test_pay on its quote: %d %s", view.status, truncate(view.body))
	}

	// Another wallet cannot pay that order, even though it is not listed.
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{}, other); res.status == 200 {
		t.Errorf("unlisted wallet must not simulate a payment, got 200")
	}

	// The listed wallet pays; the simulated order fulfils with no real supplier order.
	pay := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{}, listed)
	if pay.status != 200 {
		t.Fatalf("listed wallet test-pay: %d %s", pay.status, truncate(pay.body))
	}
	if st := quoteStatus(pay.json(t)); st != "fulfilled" {
		t.Errorf("simulated order should fulfil, got %q", st)
	}

	// Removing the wallet switches the flag off and the endpoint closes again.
	// The admin console sends the compact, URL-encoded form (as listed).
	compact := nimiq.NormalizeAddress(address)
	if res := s.do(http.MethodDelete, "/api/admin/test-accounts/"+url.PathEscape(compact), nil, admin); res.status != 200 {
		t.Fatalf("remove test account: %d %s", res.status, truncate(res.body))
	}
	if res := s.do(http.MethodDelete, "/api/admin/test-accounts/"+url.PathEscape(compact), nil, admin); res.status != 404 {
		t.Errorf("removing an unlisted wallet must return 404, got %d", res.status)
	}
	if sessionFlag(listed) {
		t.Error("removed wallet should no longer be flagged")
	}
	list := s.do(http.MethodGet, "/api/admin/test-accounts", nil, admin)
	if list.status != 200 {
		t.Fatalf("list test accounts: %d %s", list.status, list.body)
	}
	if accs, _ := list.json(t)["accounts"].([]any); len(accs) != 0 {
		t.Errorf("list should be empty after removal, got %d", len(accs))
	}
}
