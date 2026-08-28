package cryptorefills

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

/* Fixtures below were captured VERBATIM from the production
 * api.cryptorefills.com (2026-08-25, partner auth) — shapes only, ids and
 * invoice strings truncated/regenerated but structurally identical.
 * They exist so a supplier-side rename (id → order_id etc.) can never
 * silently break the shop again.
 */

const realCreateOrderResponse = `{
  "order_id": "f9d624ad-dc5e-4efd-9ecf-f8fb08a22677",
  "created_at": "1787679708",
  "wallet_address": "lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6nywasd9s2psp5v6my3tq7dsgk4d9qaw577d3jjaa4qrpw5ce0s9awqgl7yz03lpqqftmeuq",
  "payment_method": "BTC-LIGHTNING",
  "network": "Lightning",
  "coin": "BTC",
  "coin_amount": "0.00032833",
  "original_price": "0.00032833",
  "qr_url": "https://api.cryptorefills.com:443/v3/qrs/f9d624ad-dc5e-4efd-9ecf-f8fb08a22677",
  "qr_text": "lightning:lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6n",
  "sent_coin_amount": "0",
  "payment_state": "WalletCreated",
  "payment_via": "USER_WALLET",
  "payment_requested_at": "1787679708",
  "payment_method_protocol": "LIGHTNING_LIKE",
  "payment_id": "5ef721b8-15d8-400e-b0e3-1698de7a84ad",
  "payment_value": 22.1678,
  "deliveries": [{
    "id": "521def0e-7b78-48d9-b907-b4ed130e3015",
    "delivery_state": "WaitingForPayment",
    "kind": "giftcard",
    "deliverable": {
      "denomination": "25 USD",
      "localized_denomination": "$25",
      "family": "Amazon.com",
      "brand_name": "Amazon.com",
      "beneficiary_account": "test@example.com",
      "country_code": "US",
      "delivery_type": "by_email",
      "points": "22",
      "coin_amount": "0.00032833",
      "currency_code": "USD"
    }
  }],
  "order_state": "WaitingForPayment",
  "user": {"email": "test@example.com"},
  "is_payment_processor_external": false
}`

const realGetOrderDone = `{
  "order_id": "0d2c9d55-1111-4bbb-8ccc-000000000001",
  "order_state": "Done",
  "payment_state": "PaymentCompleted",
  "coin": "BTC",
  "coin_amount": "0.00032833",
  "wallet_address": "lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6n",
  "network": "Lightning",
  "payment_method": "BTC-LIGHTNING",
  "payment_method_protocol": "LIGHTNING_LIKE",
  "created_at": "1787679708",
  "updated_at": "1787679900",
  "deliveries": [{
    "id": "521def0e-7b78-48d9-b907-b4ed130e3015",
    "delivery_state": "Done",
    "kind": "giftcard",
    "deliverable": {
      "denomination": "25 USD",
      "family": "Amazon.com",
      "brand_name": "Amazon.com",
      "country_code": "US",
      "beneficiary_account": "test@example.com",
      "delivery_type": "by_email",
      "pin_code": "4GYY-77HKKG-2J6M",
      "pin_serial": "1234",
      "barcode_image_url": "https://cdn.cryptorefills.com/barcodes/x.png",
      "redeem_instructions": "<ol><li>Visit amazon.com</li></ol>"
    }
  }]
}`

const realBrandsUS = `{"last_user_purchases":[],"suggestions":[],"country_code":"US","categories":[
 {"kind":"mobile_recharge","category":"e-sim","brands":[
   {"family":"eSIM","brand_id":"513ce431","brand":"eSIM","logo_url":"https://cdn.cryptorefills.com/logos_v2/esim.webp","logo_base_url":"https://cdn.cryptorefills.com/logos_v2/esim","bg_color":"#0e131f","min":"1 GB 3 days","max":"50 GB 90 days","category":"e-sim","is_out_of_stock":false,"country_code":"US","additional_categories":[],"kind":"mobile_recharge","product_type":"physical","brand_tags":[]}],"popular_brands":[]},
 {"kind":"giftcard","category":"e-commerce","brands":[
   {"family":"Amazon.com","brand_id":"d18e3e04","brand":"Amazon.com","logo_url":"https://cdn.cryptorefills.com/logos_v2/amazon-com.webp","logo_base_url":"https://cdn.cryptorefills.com/logos_v2/amazon-com","bg_color":"#FFFFFF","min":"$5","max":"$500","category":"e-commerce","is_out_of_stock":false,"country_code":"US","additional_categories":["electronics","entertainment","books_learning"],"kind":"giftcard","product_type":"physical","brand_tags":[]},
   {"family":"OOS Brand","brand_id":"dead0000","brand":"OOS Brand","logo_url":"x","min":"$10","max":"$100","category":"e-commerce","is_out_of_stock":true,"country_code":"US","kind":"giftcard"}]}
]}`

const realProductsCountry = `[{
 "country_code":"US","category":"e-commerce","additional_categories":["electronics"],
 "kind":"giftcard","default_denomination":"","family":"Amazon.com","brand_id":"d18e3e04",
 "brand":"Amazon.com","is_out_of_stock":false,"logo_url":"x",
 "products":[{
   "product_id":"356004d6-c53d-48b5-9ebb-7e04044c5c8b","is_dynamic":true,
   "range":{"min":5.0,"max":500.0,"currency":"USD","step_size":1.0,"default":"500.0"},
   "coin":"BTC","payment_method":"BTC","delivery_type":"by_email","points":"4",
   "product_type":"physical",
   "face_value":{"currency_code":"USD","amount":{"type":"range","min":{"amount":5,"currency":"USD"},"max":{"amount":500,"currency":"USD"}}}
 }]
}]`

const realPriceResponse = `{"product_id":"356004d6","is_dynamic":true,"range":{"min":5.0,"max":500.0,"currency":"USD","step_size":1.0,"default":"500.0"},"coin_amount":"0.00032666","coin":"BTC","original_coin_amount":"0.00032666","payment_method":"BTC","delivery_type":"by_email","points":"22","product_type":"physical"}`

const realValidationResponse = `{"coin":"BTC","coin_amount":"0.00032833","original_coin_amount":"0.00032833","coupon_code":null,"loyalty_summary":null,"deliveries":[{"id":"1fa92f11","delivery_state":"Created","kind":"giftcard","deliverable":{"denomination":"25 USD","localized_denomination":"$25","family":"Amazon.com","brand_name":"Amazon.com","beneficiary_account":"test@example.com","country_code":"US","delivery_type":"by_email","points":"22","coin_amount":"0.00032833","original_price":"22.1678","currency_code":"USD"}}],"problems":null}`

func mustUnmarshal(t *testing.T, raw string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
}

/* ------------------------------- order shape ------------------------------ */

func TestOrderRealCreateResponse(t *testing.T) {
	var o Order
	mustUnmarshal(t, realCreateOrderResponse, &o)
	if o.ID != "f9d624ad-dc5e-4efd-9ecf-f8fb08a22677" {
		t.Fatalf("order_id not parsed: %q", o.ID)
	}
	if o.Status != "WaitingForPayment" {
		t.Fatalf("order_state not parsed: %q", o.Status)
	}
	if o.PaymentState != "WalletCreated" {
		t.Fatalf("payment_state not parsed: %q", o.PaymentState)
	}
	if o.Coin != "BTC" || o.Network != "Lightning" {
		t.Fatalf("coin/network wrong: %q %q", o.Coin, o.Network)
	}
	if o.CoinAmount != "0.00032833" {
		t.Fatalf("coin_amount wrong: %q", o.CoinAmount)
	}
	if o.PaymentValue != 22.1678 {
		t.Fatalf("payment_value wrong: %v", o.PaymentValue)
	}
	if o.QRText == "" || o.QRText[:10] != "lightning:" {
		t.Fatalf("qr_text not parsed: %q", o.QRText)
	}
	if len(o.Deliveries) != 1 || o.Deliveries[0].DeliveryState != "WaitingForPayment" {
		t.Fatalf("deliveries wrong: %+v", o.Deliveries)
	}
	if !o.IsLightning() {
		t.Fatal("order must be detected as Lightning")
	}
	if !IsBOLT11(o.WalletAddress) {
		t.Fatal("wallet_address must parse as BOLT11")
	}
	if o.LightningInvoice() == "" {
		t.Fatal("LightningInvoice must return the wallet address")
	}
	ct := o.CreatedTime()
	if ct.IsZero() || ct.Unix() != 1787679708 {
		t.Fatalf("created_at epoch not parsed: %v", ct)
	}
}

func TestOrderRealDoneResponseFulfills(t *testing.T) {
	var o Order
	mustUnmarshal(t, realGetOrderDone, &o)
	if o.Status != "Done" || !IsTerminal(o.Status) {
		t.Fatalf("Done must be terminal: %q", o.Status)
	}
	payload := FulfillmentPayload(&o)
	if payload == nil {
		t.Fatal("fulfillment payload missing")
	}
	var items []map[string]any
	if err := json.Unmarshal(payload, &items); err != nil || len(items) != 1 {
		t.Fatalf("fulfillment payload wrong: %v %s", err, payload)
	}
	if items[0]["code"] != "4GYY-77HKKG-2J6M" {
		t.Fatalf("redemption code missing: %+v", items[0])
	}
	if items[0]["barcode_image_url"] != "https://cdn.cryptorefills.com/barcodes/x.png" {
		t.Fatalf("barcode url missing: %+v", items[0])
	}
}

func TestOrderLegacyAliasesStillAccepted(t *testing.T) {
	// The mockstack used id/status before it was aligned; other API
	// revisions may rename again — both shapes must keep working.
	var o Order
	mustUnmarshal(t, `{"id":"abc","status":"Expired"}`, &o)
	if o.ID != "abc" || o.Status != "Expired" {
		t.Fatalf("legacy alias broken: %+v", o)
	}
}

// TestOrderNumericTimestamps is the regression for the production failure
// `json: cannot unmarshal number into Go struct field orderAlias.created_at
// of type string` — some supplier responses emit created_at/updated_at as
// bare JSON numbers instead of epoch strings. Both shapes must decode.
func TestOrderNumericTimestamps(t *testing.T) {
	var o Order
	mustUnmarshal(t, `{
		"order_id": "ord-1",
		"order_state": "WaitingForPayment",
		"created_at": 1787679708,
		"updated_at": 1787679999.75
	}`, &o)
	if o.ID != "ord-1" || o.Status != "WaitingForPayment" {
		t.Fatalf("order fields broken: %+v", o)
	}
	if string(o.CreatedAt) != "1787679708" {
		t.Fatalf("numeric created_at not kept as epoch string: %q", o.CreatedAt)
	}
	ct := o.CreatedTime()
	if ct.IsZero() || ct.Unix() != 1787679708 {
		t.Fatalf("numeric created_at not parsed: %v", ct)
	}
	if string(o.UpdatedAt) != "1787679999" {
		t.Fatalf("fractional numeric updated_at not truncated to seconds: %q", o.UpdatedAt)
	}
	// String form must keep working too (the common shape).
	var s Order
	mustUnmarshal(t, `{"order_id":"ord-2","order_state":"Done","created_at":"1787679708"}`, &s)
	if s.CreatedTime().Unix() != 1787679708 {
		t.Fatalf("string created_at broken: %v", s.CreatedTime())
	}
}

/* -------------------------------- brands --------------------------------- */

func TestBrandsRealShape(t *testing.T) {
	var b BrandsResponse
	mustUnmarshal(t, realBrandsUS, &b)
	if b.CountryCode != "US" || len(b.Categories) != 2 {
		t.Fatalf("brands shape: %+v", b)
	}
	gc := b.Categories[1]
	if gc.Kind != "giftcard" || gc.Category != "e-commerce" || len(gc.Brands) != 2 {
		t.Fatalf("giftcard category wrong: %+v", gc)
	}
	if gc.Brands[0].Family != "Amazon.com" || gc.Brands[0].OutOfStock {
		t.Fatalf("brand wrong: %+v", gc.Brands[0])
	}
	if !gc.Brands[1].OutOfStock {
		t.Fatal("out-of-stock flag not parsed")
	}
	if gc.Brands[0].Min != "$5" || gc.Brands[0].Max != "$500" {
		t.Fatalf("min/max not parsed: %q %q", gc.Brands[0].Min, gc.Brands[0].Max)
	}
}

func TestFamilyRealShape(t *testing.T) {
	var fams []Family
	mustUnmarshal(t, realProductsCountry, &fams)
	if len(fams) != 1 {
		t.Fatalf("families: %d", len(fams))
	}
	f := fams[0]
	if f.Family != "Amazon.com" || f.Kind != "giftcard" || f.Category != "e-commerce" {
		t.Fatalf("family header wrong: %+v", f)
	}
	if len(f.AdditionalCats) != 1 || f.AdditionalCats[0] != "electronics" {
		t.Fatalf("additional categories wrong: %+v", f.AdditionalCats)
	}
	if len(f.Products) != 1 {
		t.Fatalf("products: %d", len(f.Products))
	}
	p := f.Products[0]
	if !p.IsDynamic || p.Range == nil || p.Range.Min != 5 || p.Range.Max != 500 || p.Range.StepSize != 1 {
		t.Fatalf("range product wrong: %+v", p)
	}
}

func TestPriceRealShape(t *testing.T) {
	var q PriceQuote
	mustUnmarshal(t, realPriceResponse, &q)
	if q.CoinAmount != "0.00032666" || q.Coin != "BTC" {
		t.Fatalf("price wrong: %+v", q)
	}
	if q.Range == nil || q.Range.Max != 500 {
		t.Fatalf("range wrong: %+v", q.Range)
	}
}

func TestValidationRealShape(t *testing.T) {
	var v ValidationResult
	mustUnmarshal(t, realValidationResponse, &v)
	if v.CoinAmount != "0.00032833" || len(v.Deliveries) != 1 {
		t.Fatalf("validation wrong: %+v", v)
	}
	if len(v.ProblemList) != 0 {
		t.Fatalf("no problems expected: %+v", v.ProblemList)
	}
}

/* --------------------------- request shape ------------------------------- */

// TestCreateOrderRequestBeneficiaryShape pins the documented REQUEST shape
// (https://www.cryptorefills.com/en/api-docs/developers#create-order):
// each delivery carries beneficiary_account FLAT at the delivery level —
// the end-user's EMAIL for gift cards/eSIMs, the recipient's E.164 PHONE
// for mobile topups. If this ever marshals differently, deliveries break.
func TestCreateOrderRequestBeneficiaryShape(t *testing.T) {
	v := 25.0
	build := func(beneficiary string) *CreateOrderRequest {
		return &CreateOrderRequest{
			Deliveries: []Delivery{{
				BrandName:          "t-mobile",
				CountryCode:        "TR",
				Denomination:       "range",
				ProductValue:       &v,
				BeneficiaryAccount: beneficiary,
			}},
			Email:   "buyer@example.com",
			Payment: OrderPayment{Type: "via", PaymentVia: "USER_WALLET", Coin: "BTC", Network: "Lightning"},
			User:    &OrderUser{Email: "buyer@example.com"},
			Lang:    "en",
		}
	}

	for name, c := range map[string]struct {
		req     *CreateOrderRequest
		wantBen string
	}{
		"topup-e164":     {build("+905551234567"), "+905551234567"},
		"giftcard-email": {build("buyer@example.com"), "buyer@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			req := c.req
			wantBen := c.wantBen
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var parsed struct {
				Email string `json:"email"`
				User  struct {
					Email string `json:"email"`
				} `json:"user"`
				Deliveries []struct {
					BrandName          string   `json:"brand_name"`
					CountryCode        string   `json:"country_code"`
					Denomination       string   `json:"denomination"`
					ProductValue       *float64 `json:"product_value"`
					BeneficiaryAccount string   `json:"beneficiary_account"`
				} `json:"deliveries"`
			}
			if err := json.Unmarshal(b, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(parsed.Deliveries) != 1 {
				t.Fatalf("deliveries: %+v", parsed.Deliveries)
			}
			d := parsed.Deliveries[0]
			if d.BeneficiaryAccount != wantBen {
				t.Errorf("flat beneficiary_account = %q, want %q (raw: %s)", d.BeneficiaryAccount, wantBen, b)
			}
			if d.BrandName != "t-mobile" || d.CountryCode != "TR" || d.Denomination != "range" || d.ProductValue == nil || *d.ProductValue != 25 {
				t.Errorf("flat delivery fields lost: %+v", d)
			}
			if parsed.Email != "buyer@example.com" || parsed.User.Email != "buyer@example.com" {
				t.Errorf("end-user email missing: %+v", parsed)
			}
		})
	}
}

/* ------------------------------- BOLT11 ---------------------------------- */

func TestIsBOLT11(t *testing.T) {
	valid := []string{
		"lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6n",
		"LNBC328330N1P4GM57APP5CKTDDUKY6UGJXFUN84RT5AKR3QS05PCKRE4547", // upper case
		"lntb100n1qs3yyxakzpgxqyp2dzn3unv7usdcexjyjgpsgqzrwqqqqqqqqqqqqqqq",
		"lnbcrt1qtestnetregtestinvoice012345678901234567890123456789",
	}
	if !IsBOLT11(valid[0]) || !IsBOLT11(valid[1]) || !IsBOLT11(valid[2]) {
		t.Fatal("valid invoices rejected")
	}
	invalid := []string{
		"",
		"bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
		"0x1234",
		"lnbc",            // no data
		"lnbc1",           // too short
		"lightning:lnbc1", // URI, not invoice
		"lnbc short but ok!invalid char",
		"lnbc1" + strings.Repeat("q", 28) + "!",
	}
	for _, s := range invalid {
		if IsBOLT11(s) {
			t.Errorf("invalid accepted: %q", s)
		}
	}
	if IsBOLT11("lntb" + "1") {
		// "lntb1" is 5 chars < 30 — must be rejected by length
		t.Error("short invoice accepted")
	}
}

func TestLightningURI(t *testing.T) {
	inv := "lnbc328330n1p4gm57app5cktdduky6ugjxfun84rt5akr3qs05pckre4547scv0ha0q6nywasd9s2psp5v6my"
	if got := LightningURI(inv); got != "lightning:"+inv {
		t.Fatalf("uri wrong: %q", got)
	}
	if got := LightningURI("bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"); got != "" {
		t.Fatalf("non-invoice must yield empty uri, got %q", got)
	}
}

func TestMapToQuoteStatusRealStates(t *testing.T) {
	cases := map[string]string{
		"Created":                QuoteAwaitingPay,
		"WaitingForPayment":      QuoteAwaitingPay,
		"PaymentStarted":         QuotePaidStarted,
		"PartialPaymentStarted":  QuotePaidStarted,
		"PaymentReceived":        QuotePaidReceived,
		"WaitingForDelivery":     QuoteDelivering,
		"Done":                   QuoteFulfilled,
		"Expired":                QuoteExpired,
		"PaymentFailed":          QuoteFailed,
		"PaymentSetupFailed":     QuoteFailed,
		"Refunded":               QuoteRefunded,
		"WaitingForManualAction": QuoteManualReview,
		"SomethingNew":           QuoteManualReview,
	}
	for supplier, want := range cases {
		if got := MapToQuoteStatus(supplier); got != want {
			t.Errorf("MapToQuoteStatus(%q)=%q want %q", supplier, got, want)
		}
	}
}

func TestParseEpochOrRFC3339(t *testing.T) {
	if got := ParseEpochOrRFC3339("1787679708"); got.Unix() != 1787679708 {
		t.Fatalf("epoch parse: %v", got)
	}
	ts := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	if got := ParseEpochOrRFC3339("2026-08-25T12:00:00Z"); !got.Equal(ts) {
		t.Fatalf("rfc3339 parse: %v", got)
	}
	if !ParseEpochOrRFC3339("").IsZero() || !ParseEpochOrRFC3339("garbage").IsZero() {
		t.Fatal("unparseable must be zero")
	}
}

func TestWebhookPayloadShapes(t *testing.T) {
	// documented shape + observed aliases
	for _, raw := range []string{
		`{"order_id":"abc","status":"Done"}`,
		`{"orderId":"abc","state":"Done"}`,
		`{"id":"abc","order_status":"Done"}`,
		`{"reference_id":"abc"}`,
	} {
		if _, ok := ParseWebhookPayload([]byte(raw)); !ok {
			t.Errorf("rejected webhook payload: %s", raw)
		}
	}
	for _, raw := range []string{``, `[]`, `{"status":"Done"}`, `not json`} {
		if _, ok := ParseWebhookPayload([]byte(raw)); ok {
			t.Errorf("accepted bad payload: %s", raw)
		}
	}
}
