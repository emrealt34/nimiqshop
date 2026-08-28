// Command mockstack runs a faithful, deterministic stand-in for the
// CryptoRefills API (api.cryptorefills.com) so the FULL purchase pipeline —
// including crash recovery and webhook re-delivery — can be exercised
// end-to-end WITHOUT spending money and without depending on external
// uptime.
//
// The JSON shapes mirror the real API (payment_vias, brands,
// products/country, products/price, orders/validations, orders,
// orders/{id}). Each order has the documented lifecycle:
//
//	WaitingForPayment -> PaymentReceived -> WaitingForDelivery -> Done
//
// with terminal failure/expiry branches (PaymentFailed, Expired, Refunded).
//
// Control endpoints (NO auth, localhost only) let the test driver inject
// faults:
//
//	POST /mock/fault   {"delay_order_response_ms":2000,"auto_advance_ms":500,
//	                   "force_fail_delivery":true,"payment_window_ms":60000,
//	                   "webhook_base_url":"http://127.0.0.1:PORT",
//	                   "webhook_key":"..."}
//	POST /mock/reset  {}
//	GET  /mock/state
//	POST /mock/orders/{id}/pay        simulate customer payment
//	POST /mock/orders/{id}/advance    advance to delivery/Done (or fail)
//	POST /mock/orders/{id}/status     {"status":"WaitingForManualAction"}
//
// Fault semantics (crash window): a delayed order response is RECORDED
// (pending counter + stored order) BEFORE the delay, so the driver can
// observe "the supplier accepted it" and kill the server while the response
// is still in flight — the exact moment where naive code loses state.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

/* =============================== state =================================== */

type order struct {
	ID             string
	Status         string
	Coin           string
	CoinAmount     string
	Network        string
	WalletAddress  string
	QRText         string
	PaymentWindow  time.Duration
	CreatedAt      time.Time
	PaidAt         time.Time
	Deliveries     []delivery
	ForceFail      bool
	ProductID      string
	AutoAdvance    time.Duration
	autoAdvanceSet bool
}

type delivery struct {
	Kind         string
	Family       string
	Country      string
	Denomination string
	ProductValue *float64
	Beneficiary  string
	DeliveryType string
	CoinAmount   string
	Redeem       string
	HowToRedeem  string
}

type mockState struct {
	mu       sync.Mutex
	orders   map[string]*order
	faults   map[string]interface{}
	webhooks []string
}

func newState() *mockState {
	return &mockState{
		orders: map[string]*order{},
		faults: map[string]interface{}{},
	}
}

func (m *mockState) faultInt(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.faults[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func (m *mockState) faultBool(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.faults[key].(bool); ok {
		return v
	}
	return false
}

func (m *mockState) faultStr(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.faults[key].(string); ok {
		return v
	}
	return ""
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

/* ============================ product fixtures =========================== */

type productDef struct {
	family      string
	kind        string
	category    string
	dynamic     bool
	min, max    float64
	fixed       float64 // for non-dynamic
	deliverBy   string  // by_email | by_phone
	fails       bool
	customDenom string // non-numeric label ("Java & Bedrock Ed") — label-only SKU
	ghost       bool   // listed in brands but products endpoint returns empty
}

var products = []productDef{
	{family: "test-airbnb", kind: "giftcard", category: "travel_flights", dynamic: true, min: 10, max: 100, deliverBy: "by_email"},
	{family: "test-steam", kind: "giftcard", category: "games", dynamic: false, fixed: 50, deliverBy: "by_email"},
	{family: "test-topup", kind: "mobile_recharge", category: "mobile_credits", dynamic: true, min: 5, max: 50, deliverBy: "by_phone"},
	{family: "test-fail", kind: "giftcard", category: "e-commerce", dynamic: true, min: 10, max: 100, deliverBy: "by_email", fails: true},
	{family: "test-minecraft", kind: "giftcard", category: "games", dynamic: false, fixed: 26.95, deliverBy: "by_email", customDenom: "Java & Bedrock Ed"},
	{family: "test-ghost", kind: "giftcard", category: "games", dynamic: true, min: 20, max: 80, deliverBy: "by_email", ghost: true},
	{family: "amazon-usa", kind: "giftcard", category: "e-commerce", dynamic: true, min: 5, max: 200, deliverBy: "by_email"},
}

// bech32ish is the BOLT11 data charset (lowercase alphanumeric, no 1bio).
const bech32ish = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// pseudoBOLT11 builds a deterministic, structurally valid-looking invoice:
// "lnbc" + millisatoshi amount + "1" + 40 data chars from the order id.
func pseudoBOLT11(amount, seed string) string {
	n := 0
	for _, c := range seed {
		n = (n*31 + int(c)) % (1 << 30)
	}
	pick := func(k int) byte {
		n = (n*1103515245 + 12345) % (1 << 30)
		return bech32ish[(n>>8+k)%len(bech32ish)]
	}
	amt := ""
	if f, err := strconv.ParseFloat(strings.TrimSpace(amount), 64); err == nil && f > 0 {
		millis := int64(f * 100000000)
		amt = strconv.FormatInt(millis, 10)
	} else {
		amt = "1000"
	}
	out := []byte("lnbc" + amt + "1")
	for i := 0; i < 60; i++ {
		out = append(out, pick(i))
	}
	return string(out)
}

func findProduct(family string) *productDef {
	// Ghost brands exist in the directory but have no product listing —
	// exactly the supplier mismatch the self-healing list fixes.
	if strings.EqualFold(family, "test-ghost") {
		return nil
	}
	for i := range products {
		if products[i].family == family {
			return &products[i]
		}
	}
	return nil
}

/* ============================ order lifecycle ============================ */

const (
	stCreated   = "Created"
	stWaiting   = "WaitingForPayment"
	stStarted   = "PaymentStarted"
	stReceived  = "PaymentReceived"
	stDeliver   = "WaitingForDelivery"
	stDone      = "Done"
	stManual    = "WaitingForManualAction"
	stExpired   = "Expired"
	stFailed    = "PaymentFailed"
	stSetupFail = "PaymentSetupFailed"
	stRefunded  = "Refunded"
)

func (m *mockState) pushWebhook(o *order) {
	base := m.faultStr("webhook_base_url")
	key := m.faultStr("webhook_key")
	if base == "" {
		return
	}
	url := base + "/api/webhooks/cryptorefills"
	if key != "" {
		url += "?key=" + key
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"order_id": o.ID,
		"status":   o.Status,
	})
	m.webhooks = append(m.webhooks, url+" "+o.Status)
	go func() {
		// Real webhooks retry on non-2xx; the mock does the same so
		// crash tests exercise webhook recovery.
		c := &http.Client{Timeout: 3 * time.Second}
		for attempt := 0; attempt < 24; attempt++ {
			resp, err := c.Post(url, "application/json", bytes.NewReader(payload))
			if err == nil {
				code := resp.StatusCode
				resp.Body.Close()
				if code >= 200 && code < 300 {
					return
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

/* =============================== http api ================================ */

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSONBody(r *http.Request) []byte {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	return buf.Bytes()
}

func (m *mockState) publicOrder(o *order) map[string]interface{} {
	deliveries := make([]map[string]interface{}, 0, len(o.Deliveries))
	for _, d := range o.Deliveries {
		ev := map[string]interface{}{
			"id": newID("del_"), "delivery_state": "Created", "kind": d.Kind,
			"deliverable": map[string]interface{}{
				"denomination":        d.Denomination,
				"family":              d.Family,
				"brand_name":          d.Family,
				"country_code":        d.Country,
				"beneficiary_account": d.Beneficiary,
				"delivery_type":       d.DeliveryType,
				"coin_amount":         d.CoinAmount,
			},
		}
		if d.ProductValue != nil {
			ev["deliverable"].(map[string]interface{})["product_value"] = *d.ProductValue
		}
		if o.Status == stDone {
			ev["delivery_state"] = "Done"
			ev["deliverable"].(map[string]interface{})["pin_code"] = d.Redeem
			ev["deliverable"].(map[string]interface{})["how_to_redeem"] = d.HowToRedeem
		}
		deliveries = append(deliveries, ev)
	}
	return map[string]interface{}{
		// REAL API shape (verified against production 2026-08):
		// order_id / order_state / payment_state / unix created_at, and a
		// ready-to-open qr_text for Lightning invoices.
		"order_id":       o.ID,
		"order_state":    o.Status,
		"payment_state":  paymentStateFor(o.Status),
		"coin":           o.Coin,
		"coin_amount":    o.CoinAmount,
		"wallet_address": o.WalletAddress,
		"network":        o.Network,
		"payment_method": paymentMethodFor(o.Coin, o.Network),
		"payment_value":  m.paymentValue(o),
		"qr_text":        o.QRText,
		"created_at":     strconv.FormatInt(o.CreatedAt.Unix(), 10),
		"updated_at":     strconv.FormatInt(time.Now().Unix(), 10),
		"deliveries":     deliveries,
	}
}

// paymentStateFor mirrors the real supplier's coarse payment sub-states.
func paymentStateFor(status string) string {
	switch status {
	case stWaiting:
		return "PaymentRequested"
	case stStarted, stReceived, stDeliver:
		return "PaymentReceived"
	case stDone:
		return "PaymentCompleted"
	case stRefunded:
		return "Refunded"
	case stFailed:
		return "PaymentFailed"
	}
	return "WalletCreated"
}

func paymentMethodFor(coin, network string) string {
	if strings.EqualFold(network, "Lightning") {
		return coin + "-LIGHTNING"
	}
	return coin + "-" + network
}

// paymentValue is the fiat value of the order (informational).
func (m *mockState) paymentValue(o *order) float64 {
	var total float64
	for _, d := range o.Deliveries {
		if d.ProductValue != nil {
			total += *d.ProductValue
		} else if p := findProduct(d.Family); p != nil && !p.dynamic {
			total += p.fixed
		} else if p := findProduct(d.Family); p != nil {
			total += p.min
		}
	}
	return total
}

func (m *mockState) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "v3/payment_vias":
		writeJSON(w, 200, m.paymentVias())
	case path == "v2/brands":
		m.brands(w, r)
	case path == "v4/products/price":
		m.price(w, r)
	case strings.HasPrefix(path, "v5/products/country/"):
		m.productsCountry(w, r, path)
	case path == "v5/orders/validations" && r.Method == http.MethodPost:
		m.validations(w, r)
	case path == "v5/orders" && r.Method == http.MethodPost:
		m.createOrder(w, r)
	case strings.HasPrefix(path, "v5/orders/"):
		rest := strings.TrimPrefix(path, "v5/orders/")
		if strings.HasSuffix(rest, "/subscribe") {
			m.subscribe(w, strings.TrimSuffix(rest, "/subscribe"))
			return
		}
		m.getOrder(w, rest)
	}
}

func (m *mockState) paymentVias() []map[string]interface{} {
	return []map[string]interface{}{
		{"name": "USER_WALLET", "available": true, "currencies": []map[string]interface{}{
			{"name": "USDT", "threshold": "10", "is_suspended": false, "networks": []map[string]interface{}{
				{"name": "Solana", "base_token": "SOL", "threshold": "0.5", "estimated_transfer_cost": "€0"},
				{"name": "Polygon (Matic)", "chain_id": 137, "base_token": "MATIC", "threshold": "1", "estimated_transfer_cost": "€0"},
				{"name": "ETH Mainnet", "chain_id": 1, "base_token": "ETH", "threshold": "10", "estimated_transfer_cost": "€0"},
			}},
			{"name": "USDC", "threshold": "10", "is_suspended": false, "networks": []map[string]interface{}{
				{"name": "Solana", "base_token": "SOL", "threshold": "0.5", "estimated_transfer_cost": "€0"},
			}},
		}},
	}
}

// countryCurrency returns the local denomination formatting habit for a
// country (multiplier + renderer). Used by the storefront-buy sweep to
// reproduce REAL supplier labels ("150.000 IDR", "1 000 AED", "₩5,000"…).
func countryCurrency(cc string) (mult float64, fmtFn func(float64) string, code string) {
	mult = 1
	code = "USD"
	fmtFn = func(v float64) string { return fmt.Sprintf("%.0f USD", v) }
	switch strings.ToUpper(cc) {
	case "ID", "VN":
		mult = 1000
		code = map[string]string{"ID": "IDR", "VN": "VND"}[strings.ToUpper(cc)]
		fmtFn = func(v float64) string {
			return groupDots(v) + " " + code
		}
	case "IQ", "HU", "EG", "RS", "DZ":
		mult = 1000
		code = map[string]string{"IQ": "IQD", "HU": "HUF", "EG": "EGP", "RS": "RSD", "DZ": "DZD"}[strings.ToUpper(cc)]
		fmtFn = func(v float64) string {
			return groupDots(v) + " " + code
		}
	case "CO", "CL":
		mult = 1000
		code = map[string]string{"CO": "COP", "CL": "CLP"}[strings.ToUpper(cc)]
		fmtFn = func(v float64) string {
			return groupDots(v) + " " + code
		}
	case "PK", "NG", "BD":
		mult = 1000
		code = map[string]string{"PK": "PKR", "NG": "NGN", "BD": "BDT"}[strings.ToUpper(cc)]
		fmtFn = func(v float64) string {
			return groupCommas(v) + " " + code
		}
	case "AE", "SA", "RU", "KZ":
		mult = 1000
		code = map[string]string{"AE": "AED", "RU": "RUB", "KZ": "KZT", "SA": "SAR"}[strings.ToUpper(cc)]
		fmtFn = func(v float64) string {
			return groupSpaces(v) + " " + code
		}
	case "TR":
		code = "TRY"
		fmtFn = func(v float64) string { return "TRY" + fmt.Sprintf("%.0f", v) }
	case "GB":
		code = "GBP"
		fmtFn = func(v float64) string { return "£" + fmt.Sprintf("%.0f", v) }
	case "DE", "FR", "ES", "IT", "NL", "GR", "PT", "FI", "AT", "IE", "SK", "SI", "LT", "LV", "EE":
		code = "EUR"
		fmtFn = func(v float64) string { return strings.Replace(fmt.Sprintf("%.2f EUR", v), ".", ",", 1) }
	case "KR":
		code = "KRW"
		fmtFn = func(v float64) string { return "₩" + groupCommas(v) }
	case "IN":
		code = "INR"
		fmtFn = func(v float64) string { return "₹" + fmt.Sprintf("%.0f", v) }
	case "JP":
		code = "JPY"
		fmtFn = func(v float64) string { return "¥" + groupCommas(v) }
	case "BR":
		code = "BRL"
		fmtFn = func(v float64) string { return "R$ " + fmt.Sprintf("%.0f", v) }
	case "US":
		code = "USD"
		fmtFn = func(v float64) string { return "$" + fmt.Sprintf("%.0f", v) }
	}
	return mult, fmtFn, code
}

func groupDots(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	if len(s) <= 3 {
		return s
	}
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, ".")
}
func groupCommas(v float64) string { return strings.Replace(groupDots(v), ".", ",", 1) }
func groupSpaces(v float64) string { return strings.Replace(groupDots(v), ".", " ", 1) }

func (m *mockState) brands(w http.ResponseWriter, r *http.Request) {
	// serve_all_countries fault: the storefront-buy sweep needs a catalog
	// for EVERY ISO country code, not just the default US mock data.
	cc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country_code")))
	if cc == "" {
		cc = "US"
	}
	if !m.faultBool("serve_all_countries") {
		cc = "US"
	}
	cats := map[string][]map[string]interface{}{}
	for _, p := range products {
		cat := p.kind + ":" + p.category
		b := map[string]interface{}{
			"family": p.family, "brand": p.family, "brand_id": "mock-" + p.family,
			"logo_url": "https://cdn.mock/logos/" + p.family + ".webp",
			"category": p.category, "kind": p.kind, "country_code": cc,
			"is_out_of_stock": false, "product_type": "physical",
		}
		if m.faultBool("country_currencies") {
			mult, fmtFn, _ := countryCurrency(cc)
			if p.dynamic {
				b["min"] = fmtFn(p.min * mult)
				b["max"] = fmtFn(p.max * mult)
			} else {
				b["min"] = fmtFn(p.fixed * mult)
				b["max"] = fmtFn(p.fixed * mult)
			}
		} else if p.dynamic {
			b["min"] = fmt.Sprintf("%.0f USD", p.min)
			b["max"] = fmt.Sprintf("%.0f USD", p.max)
		} else {
			b["min"] = fmt.Sprintf("%.0f USD", p.fixed)
			b["max"] = fmt.Sprintf("%.0f USD", p.fixed)
		}
		cats[cat] = append(cats[cat], b)
	}
	categories := []map[string]interface{}{}
	for key, brands := range cats {
		parts := strings.SplitN(key, ":", 2)
		categories = append(categories, map[string]interface{}{
			"kind": parts[0], "category": parts[1], "brands": brands,
		})
	}
	writeJSON(w, 200, map[string]interface{}{
		"country_code": cc, "last_user_purchases": []interface{}{},
		"suggestions": []interface{}{}, "categories": categories,
	})
}

func (m *mockState) productJSON(p *productDef, coin, cc string) map[string]interface{} {
	prod := map[string]interface{}{
		"product_id":     "mockprod-" + p.family,
		"is_dynamic":     p.dynamic,
		"coin":           coin,
		"payment_method": coin + "-SOL",
		"delivery_type":  p.deliverBy,
		"product_type":   "physical",
	}
	local := m.faultBool("country_currencies") && cc != ""
	mult, fmtFn, curCode := countryCurrency(cc)
	if p.customDenom != "" {
		// Label-only SKU: the supplier prices it by the EXACT label — no
		// numeric denomination fields at all.
		prod["denomination"] = p.customDenom
		prod["localized_denomination"] = p.customDenom
		return prod
	}
	if p.dynamic {
		mn, mx := p.min, p.max
		if local {
			prod["range"] = map[string]interface{}{
				"min": mn * mult, "max": mx * mult, "currency": curCode, "step_size": float64(mult),
			}
			prod["face_value"] = map[string]interface{}{
				"currency_code": curCode,
				"amount":        map[string]interface{}{"type": "range", "min": fmtFn(mn * mult), "max": fmtFn(mx * mult)},
			}
		} else {
			prod["range"] = map[string]interface{}{
				"min": mn, "max": mx, "currency": "USD", "step_size": 1.0,
				"default": fmt.Sprintf("%.1f", (mn+mx)/2),
			}
			prod["face_value"] = map[string]interface{}{
				"currency_code": "USD",
				"amount":        map[string]interface{}{"type": "range", "min": fmt.Sprintf("%.2f", mn), "max": fmt.Sprintf("%.2f", mx)},
			}
		}
	} else {
		if local {
			prod["denomination"] = fmtFn(p.fixed * mult)
			prod["localized_denomination"] = fmtFn(p.fixed * mult)
		} else {
			prod["denomination"] = fmt.Sprintf("%.0f USD", p.fixed)
			prod["localized_denomination"] = fmt.Sprintf("%.0f USD", p.fixed)
		}
		prod["face_value"] = map[string]interface{}{
			"currency_code": "USD",
			"amount":        map[string]interface{}{"type": "fixed", "value": fmt.Sprintf("%.2f", p.fixed)},
		}
	}
	return prod
}

func (m *mockState) productsCountry(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, "v5/products/country/")
	cc := strings.SplitN(rest, "/", 2)[0]
	family := r.URL.Query().Get("family_name")
	coin := r.URL.Query().Get("coin")
	if coin == "" {
		coin = "USDT"
	}
	p := findProduct(family)
	allCC := cc == "" || m.faultBool("serve_all_countries")
	if p == nil || !allCC {
		writeJSON(w, 200, []interface{}{})
		return
	}
	rich := map[string]interface{}{}
	if m.faultBool("serve_all_countries") || cc == "US" {
		// Real-shaped rich content (same field names as the live API).
		rich = map[string]interface{}{
			"markup":              "html",
			"description":         "<p>Use this gift card as a payment method at " + p.family + ". <a href=\"https://example.com\" target=\"_blank\" rel=\"noopener noreferrer\">Official website</a>.</p>",
			"how_to_redeem":       "<ol><li>Pay with Nimiq Pay — the Lightning payment settles in seconds.</li><li>Your code arrives by email and on the Orders page.</li><li>Enter the code at the brand's checkout — the full face value is credited.</li></ol>",
			"term_and_conditions": "<p><strong>Redemption.</strong> The card can be redeemed only in the brand's official store for eligible items. No fees or expiration apply.</p><p><strong>Assistance.</strong> For balance and help visit <a href=\"https://example.com/help\" target=\"_blank\" rel=\"noopener noreferrer\">example.com/help</a>.</p>",
			"redeem_geo":          "in " + cc,
			"locale":              "en",
		}
	}
	famResp := map[string]interface{}{
		"country_code": cc, "category": p.category,
		"additional_categories": []string{}, "kind": p.kind,
		"default_denomination": "",
		"products":             []map[string]interface{}{m.productJSON(p, coin, cc)},
		"family":               p.family, "brand_id": "mock-" + p.family, "brand": p.family,
		"is_out_of_stock": false,
		"logo_url":        "https://cdn.mock/logos/" + p.family + ".webp",
		"product_tc":      "Mock terms for " + p.family + ".",
	}
	if len(rich) > 0 {
		famResp["rich_description"] = rich
	}
	writeJSON(w, 200, []map[string]interface{}{famResp})
}

func (m *mockState) price(w http.ResponseWriter, r *http.Request) {
	brand := r.URL.Query().Get("brand_name")
	fv := r.URL.Query().Get("face_value")
	coin := r.URL.Query().Get("coin")
	if coin == "" {
		coin = "USDT"
	}
	p := findProduct(brand)
	var value float64
	fmt.Sscanf(fv, "%f", &value)
	if p == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "NOT_AVAILABLE_PRODUCT", "detail": "unknown product"})
		return
	}
	if value <= 0 {
		value = p.fixed
		if value <= 0 {
			value = p.min
		}
	}
	amount := fmt.Sprintf("%.2f", value*1.0039)
	writeJSON(w, 200, map[string]interface{}{
		"product_id": "mockprod-" + p.family, "is_dynamic": p.dynamic,
		"coin_amount": amount, "original_coin_amount": amount, "coin": coin,
		"payment_method": coin + "-SOL", "delivery_type": p.deliverBy,
	})
}

// e164Strict mirrors the supplier's rule: mobile top-up beneficiaries must
// be E.164 (leading +, country code, 8-15 digits).
var e164Strict = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

// beneficiaryProblem enforces the documented beneficiary_account rules:
// mobile topups take the recipient's E.164 phone, gift cards/eSIMs take
// the end-user's email. Returns the supplier-style problem on violation.
func beneficiaryProblem(p *productDef, beneficiary string) map[string]interface{} {
	switch p.deliverBy {
	case "by_phone":
		if !e164Strict.MatchString(beneficiary) {
			return map[string]interface{}{
				"problem":     "INVALID_BENEFICIARY_ACCOUNT",
				"moreDetails": map[string]string{"reason": "phone must be E.164, e.g. +14155551234"},
			}
		}
	case "by_email":
		if !looksLikeEmail(beneficiary) {
			return map[string]interface{}{
				"problem":     "INVALID_BENEFICIARY_ACCOUNT",
				"moreDetails": map[string]string{"reason": "email required"},
			}
		}
	}
	return nil
}

func looksLikeEmail(s string) bool {
	return strings.Contains(s, "@") && strings.Contains(s, ".") && !strings.ContainsAny(s, " \t")
}

func (m *mockState) parseDeliveries(body []byte) ([]delivery, string, string, error) {
	var req struct {
		Email      string `json:"email"`
		Deliveries []struct {
			// Flat fields at the delivery level — the documented
			// POST /v5/orders and /v5/orders/validations request shape
			// (beneficiary_account included).
			BrandName    string   `json:"brand_name"`
			CountryCode  string   `json:"country_code"`
			Denomination string   `json:"denomination"`
			Beneficiary  string   `json:"beneficiary_account"`
			ProductValue *float64 `json:"product_value"`
			// Nested deliverable (the RESPONSE shape): accepted as a
			// fallback so both representations work, like the real API.
			Deliverable struct {
				BrandName    string   `json:"brand_name"`
				CountryCode  string   `json:"country_code"`
				Denomination string   `json:"denomination"`
				Beneficiary  string   `json:"beneficiary_account"`
				ProductValue *float64 `json:"product_value"`
			} `json:"deliverable"`
		} `json:"deliveries"`
		Payment struct {
			Coin    string `json:"coin"`
			Network string `json:"network"`
		} `json:"payment"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", "", fmt.Errorf("invalid json")
	}
	if len(req.Deliveries) == 0 {
		return nil, "", "", fmt.Errorf("deliveries required")
	}
	coin := req.Payment.Coin
	if coin == "" {
		coin = "USDT"
	}
	network := req.Payment.Network
	if network == "" {
		network = "Solana"
	}
	out := []delivery{}
	for _, d := range req.Deliveries {
		family := d.BrandName
		if family == "" {
			family = d.Deliverable.BrandName
		}
		p := findProduct(family)
		if p == nil {
			return nil, coin, network, fmt.Errorf("unknown product %s", family)
		}
		denom := d.Denomination
		if denom == "" {
			denom = d.Deliverable.Denomination
		}
		if denom == "" {
			if p.dynamic {
				denom = fmt.Sprintf("%.0f USD", p.min)
			} else {
				denom = fmt.Sprintf("%.0f USD", p.fixed)
			}
		}
		value := d.ProductValue
		if value == nil {
			value = d.Deliverable.ProductValue
		}
		if value == nil && !p.dynamic {
			v := p.fixed
			value = &v
		}
		beneficiary := d.Beneficiary
		if beneficiary == "" {
			beneficiary = d.Deliverable.Beneficiary
		}
		out = append(out, delivery{
			Kind: p.kind, Family: family, Country: d.CountryCode,
			Denomination: denom, ProductValue: value,
			Beneficiary: beneficiary, DeliveryType: p.deliverBy,
		})
	}
	return out, coin, network, nil
}

func (m *mockState) validations(w http.ResponseWriter, r *http.Request) {
	deliveries, coin, network, err := m.parseDeliveries(readJSONBody(r))
	if err != nil {
		problem := "INVALID_REQUEST"
		if strings.HasPrefix(err.Error(), "unknown product") {
			problem = "NOT_AVAILABLE_PRODUCT"
		}
		writeJSON(w, 400, map[string]interface{}{
			"problems": []map[string]interface{}{{"problem": problem, "moreDetails": map[string]string{"reason": err.Error()}}},
		})
		return
	}
	var total float64
	var problems []map[string]interface{}
	for _, d := range deliveries {
		if d.ProductValue != nil {
			total += *d.ProductValue
		} else if p := findProduct(d.Family); p != nil {
			if p.dynamic {
				total += p.min
			} else {
				total += p.fixed
			}
		}
		// The supplier's per-kind beneficiary rules (email vs E.164 phone).
		if p := findProduct(d.Family); p != nil {
			if prob := beneficiaryProblem(p, d.Beneficiary); prob != nil {
				problems = append(problems, prob)
			}
		}
	}
	if len(problems) > 0 {
		writeJSON(w, 400, map[string]interface{}{"problems": problems})
		return
	}
	amount := fmt.Sprintf("%.2f", total*1.0039)
	writeJSON(w, 200, map[string]interface{}{
		"coin": coin, "coin_amount": amount, "original_coin_amount": amount,
		"deliveries": []map[string]interface{}{},
	})
	_ = network
}

func (m *mockState) createOrder(w http.ResponseWriter, r *http.Request) {
	deliveries, coin, network, err := m.parseDeliveries(readJSONBody(r))
	if err != nil {
		code := "INVALID_REQUEST"
		if strings.HasPrefix(err.Error(), "unknown product") {
			code = "NOT_AVAILABLE_PRODUCT"
		}
		writeJSON(w, 400, map[string]interface{}{"error": code, "detail": err.Error()})
		return
	}
	// The real supplier validates beneficiaries at creation time too — an
	// order with a malformed beneficiary_account can never be created.
	for _, d := range deliveries {
		if p := findProduct(d.Family); p != nil {
			if prob := beneficiaryProblem(p, d.Beneficiary); prob != nil {
				detail := ""
				if md, ok := prob["moreDetails"].(map[string]string); ok {
					detail = md["reason"]
				}
				writeJSON(w, 400, map[string]interface{}{"error": "INVALID_BENEFICIARY_ACCOUNT", "detail": detail})
				return
			}
		}
	}
	var total float64
	for _, d := range deliveries {
		if d.ProductValue != nil {
			total += *d.ProductValue
		} else if p := findProduct(d.Family); p != nil {
			if p.dynamic {
				total += p.min
			} else {
				total += p.fixed
			}
		}
		if p := findProduct(d.Family); p != nil && p.fails {
			// delivery failure is exercised via /advance + force_fail
		}
	}
	for _, d := range deliveries {
		d.CoinAmount = fmt.Sprintf("%.8f", total*1.0039)
	}

	oid := newID("ord_")
	window := time.Duration(m.faultInt("payment_window_ms")) * time.Millisecond
	if window <= 0 {
		window = 30 * time.Minute
	}
	autoAdv := time.Duration(m.faultInt("auto_advance_ms")) * time.Millisecond
	wallet := "mockaddr_" + strings.ToLower(oid[4:])
	qrText := ""
	coinAmount := fmt.Sprintf("%.8f", total*1.0039)
	if strings.EqualFold(coin, "BTC") && strings.EqualFold(network, "Lightning") {
		// A faithful pseudo-BOLT11: valid prefix + charset + length, so
		// the whole lightning: URI pipeline (Nimiq Pay handoff, QR, copy)
		// is exercised exactly like production.
		wallet = pseudoBOLT11(coinAmount, oid)
		qrText = "lightning:" + wallet
	}
	o := &order{
		ID: oid, Status: stWaiting,
		Coin: coin, CoinAmount: coinAmount, Network: network,
		WalletAddress: wallet, QRText: qrText,
		PaymentWindow: window, CreatedAt: time.Now(),
		Deliveries: deliveries, ForceFail: m.faultBool("force_fail_delivery"),
		AutoAdvance:    autoAdv,
		autoAdvanceSet: autoAdv > 0,
	}

	// The order record is created NOW (at request time): a real supplier
	// registers the order the moment it accepts the request, so the crash
	// window is "response in flight".
	//
	// NOTE: every fault getter (faultInt/faultBool/faultStr) re-locks m.mu,
	// so none of them may run inside this critical section — sync.Mutex is
	// not re-entrant and a getter in here deadlocked the whole mock on the
	// first order creation (2026-08 crash-suite incident, C04).
	m.mu.Lock()
	m.orders[oid] = o
	m.mu.Unlock()

	if o.AutoAdvance > 0 {
		go func() {
			time.Sleep(o.AutoAdvance / 2)
			m.pay(o)
			time.Sleep(o.AutoAdvance / 2)
			m.advanceLocked(o)
		}()
	}
	m.pushWebhook(o)

	delay := time.Duration(m.faultInt("delay_order_response_ms")) * time.Millisecond
	if delay > 0 {
		time.Sleep(delay)
	}
	writeJSON(w, 201, m.publicOrder(o))
}

func (m *mockState) getOrder(w http.ResponseWriter, id string) {
	m.mu.Lock()
	o, ok := m.orders[id]
	if ok && o.Status == stWaiting && time.Since(o.CreatedAt) > o.PaymentWindow {
		o.Status = stExpired
	}
	if !ok {
		m.mu.Unlock()
		writeJSON(w, 404, map[string]interface{}{"error": "NOT_FOUND", "detail": "order not found"})
		return
	}
	out := m.publicOrder(o)
	m.mu.Unlock()
	writeJSON(w, 200, out)
}

func (m *mockState) pay(o *order) {
	m.mu.Lock()
	if o.Status == stWaiting || o.Status == stStarted {
		o.Status = stStarted
		m.mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		m.mu.Lock()
		if o.Status == stStarted {
			o.Status = stReceived
			o.PaidAt = time.Now()
		}
	}
	m.mu.Unlock()
	m.pushWebhook(o)
}

func (m *mockState) advanceLocked(o *order) {
	m.mu.Lock()
	// One /advance call completes the whole delivery pipeline:
	// PaymentReceived -> WaitingForDelivery -> Done (or failure).
	if o.Status == stReceived || o.Status == stDeliver {
		o.Status = stDone
		for i := range o.Deliveries {
			d := &o.Deliveries[i]
			code := "TEST-CODE-" + strings.ToUpper(o.ID[:10])
			switch d.DeliveryType {
			case "by_phone":
				d.Redeem = "Top-up completed for " + d.Beneficiary
				d.HowToRedeem = "Credits are active."
			default:
				d.Redeem = code
				d.HowToRedeem = "Redeem the code at the brand's website. The code was also sent to " + d.Beneficiary + "."
			}
		}
	} else if o.ForceFail && (o.Status == stWaiting || o.Status == stReceived) {
		o.Status = stFailed
	}
	m.mu.Unlock()
	m.pushWebhook(o)
}

func (m *mockState) subscribe(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	// Minimal SSE: current state, then stop. (The backend uses polling as
	// the primary path; this exists so client code can be exercised.)
	m.mu.Lock()
	o, ok := m.orders[id]
	m.mu.Unlock()
	if !ok {
		fmt.Fprint(w, "event: stop\ndata: {\"error\":\"not found\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	out, _ := json.Marshal(m.publicOrder(o))
	fmt.Fprintf(w, "data: %s\n\n", out)
	fmt.Fprint(w, "event: stop\ndata: {}\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

/* ============================ control API ================================ */

func (m *mockState) control(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/mock")
	switch {
	case path == "/state":
		m.mu.Lock()
		orders := make([]map[string]interface{}, 0, len(m.orders))
		for _, o := range m.orders {
			orders = append(orders, map[string]interface{}{"id": o.ID, "status": o.Status})
		}
		m.mu.Unlock()
		writeJSON(w, 200, map[string]interface{}{
			"orders": orders, "order_count": len(orders), "webhooks": m.webhooks,
		})
	case path == "/fault":
		var f map[string]interface{}
		_ = json.Unmarshal(readJSONBody(r), &f)
		m.mu.Lock()
		if f == nil {
			m.faults = map[string]interface{}{}
		} else {
			m.faults = f
		}
		m.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
	case path == "/reset":
		m.mu.Lock()
		m.orders = map[string]*order{}
		m.faults = map[string]interface{}{}
		m.webhooks = nil
		m.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
	case strings.HasPrefix(path, "/orders/") && strings.HasSuffix(path, "/pay"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/orders/"), "/pay")
		m.mu.Lock()
		o, ok := m.orders[id]
		m.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]bool{"ok": false})
			return
		}
		m.pay(o)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case strings.HasPrefix(path, "/orders/") && strings.HasSuffix(path, "/advance"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/orders/"), "/advance")
		m.mu.Lock()
		o, ok := m.orders[id]
		m.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]bool{"ok": false})
			return
		}
		if o.Status == stWaiting || o.Status == stStarted {
			m.pay(o)
		}
		m.advanceLocked(o)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case strings.HasPrefix(path, "/orders/") && strings.HasSuffix(path, "/status"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/orders/"), "/status")
		var in struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(readJSONBody(r), &in)
		m.mu.Lock()
		o, ok := m.orders[id]
		if ok && in.Status != "" {
			o.Status = in.Status
		}
		m.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]bool{"ok": false})
			return
		}
		m.pushWebhook(o)
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

/* ================================ main =================================== */

func main() {
	port := os.Getenv("MOCK_CR_PORT")
	if port == "" {
		port = "9020"
	}
	m := newState()
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/", func(w http.ResponseWriter, r *http.Request) { m.handle(w, r) })
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) { m.handle(w, r) })
	mux.HandleFunc("/v4/", func(w http.ResponseWriter, r *http.Request) { m.handle(w, r) })
	mux.HandleFunc("/v5/", func(w http.ResponseWriter, r *http.Request) { m.handle(w, r) })
	mux.HandleFunc("/mock/", func(w http.ResponseWriter, r *http.Request) { m.control(w, r) })
	mux.HandleFunc("/v2/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	log.Printf("cryptorefills mock listening on 127.0.0.1:%s", port)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, mux))
}
