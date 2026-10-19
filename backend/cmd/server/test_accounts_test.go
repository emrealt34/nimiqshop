package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"nimiqshop/internal/nimiq"
)

// TestTestAccountSimulatedPayment runs with TEST_MODE OFF (the live setting).
// Only a wallet the admin lists gets the pay button; another signed-in wallet
// is unaffected and cannot pay the order.
//
// The listed wallet's order is a REAL order: it goes to the supplier and comes
// back with a real, payable invoice. The single simulated thing is the pay
// button, which settles the order without any money moving — so the invoice a
// test wallet sees is the same artefact a real buyer pays.
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

	// The listed wallet's order is a REAL supplier order with a REAL invoice.
	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "buyer@example.com",
	}, listed)
	if q.status >= 300 {
		t.Fatalf("listed wallet quote: %d %s", q.status, truncate(q.body))
	}
	if q.json(t)["simulated_payment"] == true {
		t.Fatalf("listed wallet quote must not be a simulated payment: %s", truncate(q.body))
	}
	sid, _ := q.json(t)["supplier_order_id"].(string)
	if sid == "" || strings.HasPrefix(sid, "TESTSIM-") {
		t.Fatalf("listed wallet quote should carry a real supplier order id: %s", truncate(q.body))
	}
	if inv, _ := q.json(t)["wallet_address"].(string); inv == "" {
		t.Fatalf("listed wallet quote should carry a real invoice: %s", truncate(q.body))
	}
	quoteID := quoteIDFrom(t, q)
	view := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, listed)
	if view.status != 200 || view.json(t)["test_pay"] != true {
		t.Fatalf("listed wallet should see test_pay on its quote: %d %s", view.status, truncate(view.body))
	}
	// An unlisted wallet's own quote offers no button: nothing about the
	// normal checkout changed for everybody else.
	plain := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-steam", "country": "US", "quantity": 1,
		"denomination": "50 USD", "product_value": 50, "email": "other@example.com",
	}, other)
	if plain.status >= 300 {
		t.Fatalf("unlisted wallet quote: %d %s", plain.status, truncate(plain.body))
	}
	plainID := quoteIDFrom(t, plain)
	plainView := s.do(http.MethodGet, "/api/quotes/"+plainID, nil, other)
	if plainView.status != 200 || plainView.json(t)["test_pay"] == true {
		t.Fatalf("an unlisted wallet must not be offered test_pay: %d %s", plainView.status, truncate(plainView.body))
	}

	// Another wallet cannot pay that order, even though it is not listed.
	if res := s.do(http.MethodPost, "/api/quotes/"+quoteID+"/test-pay", map[string]any{}, other); res.status == 200 {
		t.Errorf("unlisted wallet must not simulate a payment, got 200")
	}

	// The listed wallet presses the button: the order fulfils, and the
	// money is the only thing that never moved.
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

// TestTestAccountInvoiceIsReallyPayable is the point of the whole fix: a
// listed test wallet is handed the SAME payable invoice a real buyer gets, so
// paying it really delivers the product. It used to be handed a fabricated
// one — checksum-valid but unpayable, always priced at 0.00001 BTC, with no
// supplier order behind it — so nothing downstream of the invoice could be
// tested at all. Only the pay button is simulated now.
func TestTestAccountInvoiceIsReallyPayable(t *testing.T) {
	s := bootStack(t, stackOptions{})
	admin := s.adminHeaders()
	listed, address := s.signIn()

	if add := s.do(http.MethodPost, "/api/admin/test-accounts", map[string]any{"address": address}, admin); add.status != 201 {
		t.Fatalf("add test account: %d %s", add.status, truncate(add.body))
	}

	q := s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "test-airbnb", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 25, "email": "buyer@example.com",
	}, listed)
	if q.status != 201 {
		t.Fatalf("listed wallet quote: %d %s", q.status, truncate(q.body))
	}
	sid, _ := q.json(t)["supplier_order_id"].(string)
	if sid == "" || strings.HasPrefix(sid, "TESTSIM-") {
		t.Fatalf("a listed test wallet must get a real supplier order: %s", truncate(q.body))
	}
	if inv, _ := q.json(t)["wallet_address"].(string); inv == "" {
		t.Fatalf("a listed test wallet must get a real invoice: %s", truncate(q.body))
	}
	quoteID := quoteIDFrom(t, q)

	// Pay the invoice at the SUPPLIER — only possible because it is real.
	mockSupplier := func(action string) {
		t.Helper()
		r, err := http.Post(s.supplier.URL+"/mock/orders/"+sid+"/"+action, "application/json", strings.NewReader("{}")) //nolint:noctx // in-process fixture
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("supplier fixture %s: %d", action, r.StatusCode)
		}
	}
	mockSupplier("pay")
	mockSupplier("advance")

	status := ""
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && status != "fulfilled"; {
		s.do(http.MethodPost, "/api/quotes/"+quoteID+"/refresh", nil, listed)
		status = quoteStatus(s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, listed).json(t))
		if status != "fulfilled" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if status != "fulfilled" {
		t.Fatalf("paying the real invoice should deliver the order (status %q)", status)
	}

	// And the code it delivers is the supplier's, not the simulated one.
	detail := s.do(http.MethodGet, "/api/quotes/"+quoteID, nil, listed)
	if detail.status != 200 {
		t.Fatalf("quote detail: %d %s", detail.status, truncate(detail.body))
	}
	body := string(detail.body)
	if strings.Contains(body, "Nothing to redeem") {
		t.Fatalf("a really paid invoice must not deliver the simulated code: %s", truncate(detail.body))
	}
	if !strings.Contains(body, "Redeem the code at the brand's website") {
		t.Fatalf("a really paid invoice must deliver the supplier's real code: %s", truncate(detail.body))
	}
}
