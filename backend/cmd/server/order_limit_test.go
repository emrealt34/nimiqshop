package main

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"nimiqshop/internal/catalog"
)

// Live bug of 2026-10-20: Amazon.fr (FR) was advertised as €5–€445, the
// buyer typed €200, entered the email, and the LAST checkout step answered
// "Cryptorefills could not approve this request" — the supplier's Lightning
// rail caps the order total at 100 EUR per order
// (LIGHTNING_ORDER_LIMIT_EXCEEDED). The shop now mirrors that ceiling:
// ranges stop at the face cap and say why, fixed denominations above it
// disappear, and a stale page that still submits the old amount gets the
// real maximum back instead of a generic refusal.
func TestSupplierOrderLimitShapesCatalogAndCheckout(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{
		"SUPPLIER_ORDER_LIMIT_EUR": "100", "SUPPLIER_ORDER_MARGIN_PCT": "5",
	}})
	limit := catalog.OrderLimit{TotalEUR: 100, MarginPct: 5}
	capUSD := math.Floor(limit.FaceCapUSD()) // the mock catalog is priced in USD, step 1

	t.Run("range is clamped to the face cap and annotated", func(t *testing.T) {
		res := s.do(http.MethodGet, "/api/catalog/products/amazon-usa?country=US", nil, nil)
		if res.status != 200 {
			t.Fatalf("product: %d %s", res.status, truncate(res.body))
		}
		fams := jsonArray(t, res.body)
		products, _ := fams[0]["products"].([]any)
		if len(products) != 1 {
			t.Fatalf("amazon-usa must keep its single range product, got %d", len(products))
		}
		rng, _ := products[0].(map[string]any)["range"].(map[string]any)
		if got, _ := rng["max"].(float64); got != capUSD {
			t.Fatalf("range max %v, want the supplier face cap %v (supplier lists 200)", got, capUSD)
		}
		capInfo, _ := rng["cap"].(map[string]any)
		if capInfo["reason"] != "supplier_order_limit" || capInfo["total_eur"] != 100.0 || capInfo["supplier_max"] != 200.0 {
			t.Fatalf("range cap annotation %+v", capInfo)
		}
		if _, hasDefault := rng["default"]; hasDefault {
			t.Fatalf("the supplier default (102.5, above the cap) must be dropped: %+v", rng)
		}
	})

	auth, _ := s.signIn()
	headers := func(k string) map[string]string {
		return map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": k}
	}

	t.Run("single checkout above the cap is refused with the real maximum", func(t *testing.T) {
		res := s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "amazon-usa", "country": "US", "quantity": 1,
			"denomination": "range", "product_value": 150, "email": "verify@nimiqbase.com",
		}, headers(newIdempotencyKey(t)))
		if res.status != http.StatusForbidden {
			t.Fatalf("want 403, got %d %s", res.status, truncate(res.body))
		}
		body := res.json(t)
		if body["code"] != "SUPPLIER_ORDER_LIMIT" {
			t.Fatalf("code %v", body["code"])
		}
		ol, _ := body["order_limit"].(map[string]any)
		if ol["total_eur"] != 100.0 || ol["currency"] != "USD" || ol["max_face_value"] != capUSD {
			t.Fatalf("order_limit %+v (cap %v)", ol, capUSD)
		}
		msg, _ := body["error"].(string)
		if !strings.Contains(msg, "100 EUR in total") || !strings.Contains(msg, "per order") {
			t.Fatalf("message must name the ceiling: %q", msg)
		}
		if n := supplierValidations(t, s); n != 0 {
			t.Fatalf("the refusal must happen before the supplier dry run, saw %d validations", n)
		}
	})

	t.Run("a cart of small items is refused on its total", func(t *testing.T) {
		each := math.Floor(capUSD * 0.6) // alone under the cap, two of them over it
		res := s.do(http.MethodPost, "/api/quotes/batch", map[string]any{
			"items": []map[string]any{
				{"product_id": "amazon-usa", "country": "US", "quantity": 1, "denomination": "range", "product_value": each},
				{"product_id": "test-airbnb", "country": "US", "quantity": 1, "denomination": "range", "product_value": each},
			},
			"email": "verify@nimiqbase.com",
		}, headers(newIdempotencyKey(t)))
		if res.status != http.StatusForbidden || res.json(t)["code"] != "SUPPLIER_ORDER_LIMIT" {
			t.Fatalf("want 403 SUPPLIER_ORDER_LIMIT for %v+%v, got %d %s", each, each, res.status, truncate(res.body))
		}
	})

	t.Run("an order under the cap still reaches the invoice", func(t *testing.T) {
		res := s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "amazon-usa", "country": "US", "quantity": 1,
			"denomination": "range", "product_value": capUSD, "email": "verify@nimiqbase.com",
		}, headers(newIdempotencyKey(t)))
		if id := quoteIDFrom(t, res); id == "" {
			t.Fatal("no quote id")
		}
		if inv, _ := res.json(t)["lightning_invoice"].(string); inv == "" {
			t.Fatalf("an order at the cap must get its invoice: %s", truncate(res.body))
		}
	})

	t.Run("admin sees which ceiling binds", func(t *testing.T) {
		res := s.do(http.MethodGet, "/api/admin/catalog-limits", nil, s.adminHeaders())
		if res.status != 200 {
			t.Fatalf("catalog-limits: %d %s", res.status, truncate(res.body))
		}
		body := res.json(t)
		sol, _ := body["supplier_order_limit"].(map[string]any)
		if sol["binding"] != true || sol["total_eur"] != 100.0 || body["effective_max_face_value_usd"] != limit.FaceCapUSD() || body["operator_max_face_value_usd"] != 0.0 {
			t.Fatalf("limits view %+v", body)
		}
	})
}

// With the mirror switched off the supplier's own refusal must still reach
// the buyer intact — code, limits and order total — exactly as production
// answered on 2026-10-20, and the catalog must show the supplier's range.
func TestSupplierLightningRefusalIsPassedThroughWhenMirrorIsOff(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{"SUPPLIER_ORDER_LIMIT_EUR": "0"}})
	setMockFault(t, s, map[string]any{"lightning_limit_eur": 100})

	res := s.do(http.MethodGet, "/api/catalog/products/amazon-usa?country=US", nil, nil)
	fams := jsonArray(t, res.body)
	products, _ := fams[0]["products"].([]any)
	rng, _ := products[0].(map[string]any)["range"].(map[string]any)
	if rng["max"] != 200.0 || rng["cap"] != nil {
		t.Fatalf("mirror off: the supplier range must be untouched, got %+v", rng)
	}

	auth, _ := s.signIn()
	res = s.do(http.MethodPost, "/api/quotes", map[string]any{
		"product_id": "amazon-usa", "country": "US", "quantity": 1,
		"denomination": "range", "product_value": 150, "email": "verify@nimiqbase.com",
	}, map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)})
	if res.status != http.StatusConflict {
		t.Fatalf("want 409 from the supplier refusal, got %d %s", res.status, truncate(res.body))
	}
	body := res.json(t)
	if body["code"] != "SUPPLIER_PROBLEMS" || body["supplier_error"] != true || body["upstream_status"] != 422.0 {
		t.Fatalf("envelope %+v", body)
	}
	problems, _ := body["problems"].([]any)
	if len(problems) != 1 {
		t.Fatalf("problems %+v", body["problems"])
	}
	p, _ := problems[0].(map[string]any)
	details, _ := p["moreDetails"].(map[string]any)
	limits, _ := details["limits"].(map[string]any)
	perOrder, _ := limits["per_order"].(map[string]any)
	total, _ := details["order_total"].(map[string]any)
	if p["problem"] != "LIGHTNING_ORDER_LIMIT_EXCEEDED" || perOrder["amount"] != 100.0 || perOrder["currency"] != "EUR" || total["amount"].(float64) < 150 {
		t.Fatalf("the supplier's numbers must survive the trip: %+v", p)
	}
	if n := supplierValidations(t, s); n != 1 {
		t.Fatalf("exactly one supplier dry run expected, saw %d", n)
	}
	if orders := supplierOrders(t, s); orders != 0 {
		t.Fatalf("a refused validation must create no supplier order, saw %d", orders)
	}
}

/* ------------------------------ helpers --------------------------------- */

func jsonArray(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(body, &out); err != nil || len(out) == 0 {
		t.Fatalf("response is not a non-empty JSON array: %v\n%s", err, truncate(body))
	}
	return out
}

func setMockFault(t *testing.T, s *testStack, fault map[string]any) {
	t.Helper()
	b, _ := json.Marshal(fault)
	res, err := http.Post(s.supplier.URL+"/mock/fault", "application/json", bytes.NewReader(b)) //nolint:noctx // in-process fixture
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mock fault: %d", res.StatusCode)
	}
}

// supplierValidations counts the dry runs the supplier mock has received.
func supplierValidations(t *testing.T, s *testStack) int {
	t.Helper()
	res, err := http.Get(s.supplier.URL + "/mock/requests") //nolint:noctx // in-process fixture
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var items []map[string]string
	if err := json.NewDecoder(res.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range items {
		if it["method"] == http.MethodPost && strings.HasSuffix(it["path"], "/orders/validations") {
			n++
		}
	}
	return n
}

// supplierOrders counts the orders the supplier mock actually created.
func supplierOrders(t *testing.T, s *testStack) int {
	t.Helper()
	res, err := http.Get(s.supplier.URL + "/mock/state") //nolint:noctx // in-process fixture
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var st struct {
		OrderCount int `json:"order_count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st.OrderCount
}

// The supplier's fee is not one number: Amazon.co.jp costs +20 % over face
// where Amazon.fr costs +4 % (live, 2026-10-20). With a flat 5 % margin the
// product page offered ¥16,926 and the supplier refused it at €114 total.
// The cap is therefore calibrated per family from the supplier's own price
// quote: the product page stops where that product really fits, and the
// gate agrees with the page.
func TestSupplierOrderLimitFollowsTheFamilysRealFee(t *testing.T) {
	s := bootStack(t, stackOptions{testMode: false, env: map[string]string{
		"SUPPLIER_ORDER_LIMIT_EUR": "100", "SUPPLIER_ORDER_MARGIN_PCT": "5",
	}})
	setMockFault(t, s, map[string]any{"price_margin_pct": map[string]any{"amazon-usa": 20}})
	flat := catalog.OrderLimit{TotalEUR: 100, MarginPct: 5}
	// The mock quotes +20 %; the shop keeps one point of drift headroom.
	real := catalog.OrderLimit{TotalEUR: 100, MarginPct: 21}
	flatCap, realCap := math.Floor(flat.FaceCapUSD()), math.Floor(real.FaceCapUSD())
	if !(realCap < flatCap) {
		t.Fatalf("test premise: %v must be below %v", realCap, flatCap)
	}

	t.Run("the product page stops where this family really fits", func(t *testing.T) {
		res := s.do(http.MethodGet, "/api/catalog/products/amazon-usa?country=US", nil, nil)
		if res.status != 200 {
			t.Fatalf("product: %d %s", res.status, truncate(res.body))
		}
		products, _ := jsonArray(t, res.body)[0]["products"].([]any)
		rng, _ := products[0].(map[string]any)["range"].(map[string]any)
		if got, _ := rng["max"].(float64); got != realCap {
			t.Fatalf("range max %v, want %v (flat-margin cap would be %v)", got, realCap, flatCap)
		}
		if capInfo, _ := rng["cap"].(map[string]any); capInfo["reason"] != "supplier_order_limit" {
			t.Fatalf("cap annotation %+v", capInfo)
		}
	})

	t.Run("a family with the ordinary fee is not tightened", func(t *testing.T) {
		// test-airbnb lists 10–100 USD and costs +0.39 %: its own maximum
		// is below the configured cap, so nothing is clamped and no cap
		// annotation appears — the +20 % learnt for amazon-usa must not
		// leak onto another family.
		res := s.do(http.MethodGet, "/api/catalog/products/test-airbnb?country=US", nil, nil)
		if res.status != 200 {
			t.Fatalf("product: %d %s", res.status, truncate(res.body))
		}
		products, _ := jsonArray(t, res.body)[0]["products"].([]any)
		rng, _ := products[0].(map[string]any)["range"].(map[string]any)
		if got, _ := rng["max"].(float64); got != 100 {
			t.Fatalf("range max %v, want the supplier's own 100", got)
		}
		if _, capped := rng["cap"]; capped {
			t.Fatalf("no cap annotation expected: %+v", rng)
		}
	})

	auth, _ := s.signIn()
	headers := func() map[string]string {
		return map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": newIdempotencyKey(t)}
	}

	t.Run("the gate refuses what the page no longer offers, naming the real maximum", func(t *testing.T) {
		res := s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "amazon-usa", "country": "US", "quantity": 1,
			"denomination": "range", "product_value": flatCap, "email": "verify@nimiqbase.com",
		}, headers())
		if res.status != http.StatusForbidden || res.json(t)["code"] != "SUPPLIER_ORDER_LIMIT" {
			t.Fatalf("want 403 SUPPLIER_ORDER_LIMIT for %v, got %d %s", flatCap, res.status, truncate(res.body))
		}
		ol, _ := res.json(t)["order_limit"].(map[string]any)
		if ol["max_face_value"] != realCap {
			t.Fatalf("order_limit %+v, want max_face_value %v", ol, realCap)
		}
	})

	t.Run("the real maximum still reaches the invoice", func(t *testing.T) {
		res := s.do(http.MethodPost, "/api/quotes", map[string]any{
			"product_id": "amazon-usa", "country": "US", "quantity": 1,
			"denomination": "range", "product_value": realCap, "email": "verify@nimiqbase.com",
		}, headers())
		if res.status != http.StatusCreated {
			t.Fatalf("want 201 for %v, got %d %s", realCap, res.status, truncate(res.body))
		}
	})
}
