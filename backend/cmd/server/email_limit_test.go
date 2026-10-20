package main

import (
	"net/http"
	"strings"
	"testing"

	"nimiqshop/internal/catalog"
)

/* email_limit_test.go — the supplier's per-email Lightning rule, whole.
 *
 * Live, 2026-10-20: a 104 USD Walmart card was created (97.70 EUR with the
 * fee), left unpaid, and the next attempt with the same delivery email was
 * refused with LIGHTNING_DAILY_LIMIT_EXCEEDED — a code the shop did not
 * know. The buyer read "Cryptorefills could not approve this request.
 * Review the order and contact support". The rule has three parts (per
 * order, per day, per month — fee included, paid or not, per delivery
 * email); the shop mirrored only the first.
 *
 * Now: a per-email refusal names its reason in the headline, carries the
 * buyer's own orders that used the budget (and which can still be paid),
 * a different email goes through, the brand card never promises more
 * than the product page, and the rule is published for the storefront to
 * state before the last step.
 */

func TestDailyEmailBudgetRefusalIsExplainedWithTheBuyersOwnOrder(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{
		"SUPPLIER_ORDER_LIMIT_EUR": "100", "SUPPLIER_ORDER_MARGIN_PCT": "5",
		"SUPPLIER_DAILY_LIMIT_EUR": "100", "SUPPLIER_MONTHLY_LIMIT_EUR": "300",
	}})
	setMockFault(t, s, map[string]any{"lightning_limit_eur": 100})
	auth, _ := s.signIn()
	headers := func() map[string]string {
		return map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)}
	}
	order := func(value float64, email string) resp {
		return s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "amazon-usa", "country": "US", "quantity": 1,
			"denomination": "range", "product_value": value, "email": email,
			"ack_active_checkout": true,
		}, headers())
	}

	// 60 × 1.0404 = 62.42 of the day's 100: fits on its own.
	first := order(60, "buyer@example.test")
	if first.status != http.StatusCreated {
		t.Fatalf("first order: want 201, got %d %s", first.status, truncate(first.body))
	}
	firstID, _ := first.json(t)["quote_id"].(string)
	if firstID == "" {
		t.Fatalf("first order carries no quote_id: %s", truncate(first.body))
	}

	// The same email again: 62.42 + 62.42 > 100 — the unpaid first order
	// already holds the day's budget, exactly as live.
	second := order(60, "Buyer@Example.test")
	if second.status != http.StatusConflict {
		t.Fatalf("second order: want 409 from the supplier's daily budget, got %d %s", second.status, truncate(second.body))
	}
	body := second.json(t)
	if body["code"] != "SUPPLIER_PROBLEMS" {
		t.Fatalf("envelope %+v", body)
	}
	headline, _ := body["error"].(string)
	if !strings.Contains(headline, "daily") || !strings.Contains(headline, "email") {
		t.Fatalf("the headline must name the reason, got %q", headline)
	}
	problems, _ := body["problems"].([]any)
	if len(problems) != 1 {
		t.Fatalf("problems %+v", body["problems"])
	}
	p, _ := problems[0].(map[string]any)
	if p["problem"] != "LIGHTNING_DAILY_LIMIT_EXCEEDED" {
		t.Fatalf("problem %+v", p)
	}
	details, _ := p["moreDetails"].(map[string]any)
	if limits, _ := details["limits"].(map[string]any); limits["daily"] == nil {
		t.Fatalf("the supplier's own numbers must survive: %+v", details)
	}
	shop, _ := details["shop"].(map[string]any)
	if shop["period"] != "daily" || shop["pending_count"] != 1.0 || !strings.EqualFold(shop["email"].(string), "buyer@example.test") {
		t.Fatalf("shop block %+v", shop)
	}
	orders, _ := shop["orders"].([]any)
	if len(orders) != 1 {
		t.Fatalf("want the buyer's one counted order, got %+v", shop["orders"])
	}
	o, _ := orders[0].(map[string]any)
	if o["quote_id"] != firstID || o["pending"] != true || o["status"] != "awaiting_payment" || o["product_id"] != "amazon-usa" {
		t.Fatalf("counted order %+v, want the unpaid first order %s", o, firstID)
	}
	if total, _ := o["total_usd"].(float64); total < 60 {
		t.Fatalf("counted order must carry the supplier total (fee included), got %v", o["total_usd"])
	}

	// Another delivery email is another budget.
	third := order(60, "other@example.test")
	if third.status != http.StatusCreated {
		t.Fatalf("different email: want 201, got %d %s", third.status, truncate(third.body))
	}
}

func TestBrandCardNeverPromisesMoreThanTheProductPage(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{
		"SUPPLIER_ORDER_LIMIT_EUR": "100", "SUPPLIER_ORDER_MARGIN_PCT": "5",
	}})
	limit := catalog.OrderLimit{TotalEUR: 100, MarginPct: 5}
	wantMax := int64(limit.FaceCapIn("USD"))

	res := s.do(http.MethodGet, "/api/catalog/brands?country=US", nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("brands: %d %s", res.status, truncate(res.body))
	}
	cats, _ := res.json(t)["categories"].([]any)
	found := false
	for _, c := range cats {
		brands, _ := c.(map[string]any)["brands"].([]any)
		for _, b := range brands {
			card, _ := b.(map[string]any)
			if card["family"] != "amazon-usa" {
				continue
			}
			found = true
			v, code := catalog.ParseDenominationLabel(card["max"].(string))
			if code != "USD" || int64(v) != wantMax {
				t.Fatalf("card max %q, want $%d — the supplier's $200 must not be repeated on the card", card["max"], wantMax)
			}
			if minV, _ := catalog.ParseDenominationLabel(card["min"].(string)); minV != 5 {
				t.Fatalf("card min %q must stay the supplier's", card["min"])
			}
		}
	}
	if !found {
		t.Fatal("amazon-usa card missing from the US directory")
	}

	// The page the card leads to agrees.
	res = s.do(http.MethodGet, "/api/catalog/products/amazon-usa?country=US", nil, nil)
	fams := jsonArray(t, res.body)
	products, _ := fams[0]["products"].([]any)
	rng, _ := products[0].(map[string]any)["range"].(map[string]any)
	if int64(rng["max"].(float64)) != wantMax {
		t.Fatalf("product page max %v, card says %d", rng["max"], wantMax)
	}
	capInfo, _ := rng["cap"].(map[string]any)
	if capInfo["daily_eur"] != 100.0 || capInfo["monthly_eur"] != 300.0 || capInfo["total_eur"] != 100.0 {
		t.Fatalf("the range cap must state the whole rule, got %+v", capInfo)
	}
}

func TestTheSupplierRuleIsPublishedForTheStorefront(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{
		"SUPPLIER_ORDER_LIMIT_EUR": "100", "SUPPLIER_DAILY_LIMIT_EUR": "100", "SUPPLIER_MONTHLY_LIMIT_EUR": "300",
	}})
	res := s.do(http.MethodGet, "/api/catalog/limits", nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("limits: %d %s", res.status, truncate(res.body))
	}
	rule, _ := res.json(t)["supplier_order_limit"].(map[string]any)
	if rule["enabled"] != true || rule["total_eur"] != 100.0 || rule["daily_eur"] != 100.0 || rule["monthly_eur"] != 300.0 || rule["scope"] != "delivery_email" || rule["unpaid_orders_count"] != true {
		t.Fatalf("rule %+v", rule)
	}
}
