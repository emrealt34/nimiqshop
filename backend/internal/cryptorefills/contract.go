package cryptorefills

// Wire contract checked against the public developer reference on 2026-09-07.
// Do not send response-only fields (including nested deliverable) in requests.
import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

type requestDelivery struct {
	BrandName          string   `json:"brand_name"`
	CountryCode        string   `json:"country_code"`
	Denomination       string   `json:"denomination"`
	ProductValue       *float64 `json:"product_value,omitempty"`
	BeneficiaryAccount string   `json:"beneficiary_account"`
}

func (r *CreateOrderRequest) requestDeliveries() []requestDelivery {
	out := make([]requestDelivery, 0, len(r.Deliveries))
	for _, d := range r.Deliveries {
		v := d.ProductValue
		if !strings.EqualFold(d.Denomination, "range") {
			v = nil
		}
		out = append(out, requestDelivery{d.BrandName, d.CountryCode, d.Denomination, v, d.BeneficiaryAccount})
	}
	return out
}

func (r *CreateOrderRequest) createPayload() any {
	// The frozen payload records the idempotency key even when the running
	// config is on v5 (the key is metadata about the quote, and a later boot
	// with a partner key must be able to reuse it). Only the wire payload is
	// generation-aware — see createPayloadFor, which strips it for v5.
	return r.createPayloadFor(true)
}

// createPayloadFor builds the create body for one API generation. v5 has no
// external_order_id and must not be sent an unknown field, so the key is
// emitted only for the partner API — even though it is stored either way.
func (r *CreateOrderRequest) createPayloadFor(partnerMode bool) any {
	user := r.User
	if user == nil && r.Email != "" {
		user = &OrderUser{Email: r.Email}
	}
	externalID := ""
	if partnerMode {
		externalID = strings.TrimSpace(r.ExternalOrderID)
	}
	return struct {
		ExternalOrderID string            `json:"external_order_id,omitempty"`
		Deliveries      []requestDelivery `json:"deliveries"`
		Payment         OrderPayment      `json:"payment"`
		User            *OrderUser        `json:"user,omitempty"`
		Lang            string            `json:"lang"`
		Acquisition     *Acquisition      `json:"acquisition,omitempty"`
	}{externalID, r.requestDeliveries(), r.Payment, user, r.Lang, r.Acquisition}
}

func (r *CreateOrderRequest) validationPayload() any {
	email := r.Email
	if email == "" && r.User != nil {
		email = r.User.Email
	}
	return struct {
		Email      string            `json:"email,omitempty"`
		Payment    OrderPayment      `json:"payment"`
		Deliveries []requestDelivery `json:"deliveries"`
		Lang       string            `json:"lang"`
	}{email, r.Payment, r.requestDeliveries(), r.Lang}
}

// MarshalCreateRequest is the exact payload persisted BEFORE dispatch and used
// verbatim in crash recovery. In particular a batch never becomes a joined name.
func MarshalCreateRequest(r *CreateOrderRequest) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("missing supplier request")
	}
	return json.Marshal(r.createPayload())
}

type endUserAgentKey struct{}

func WithEndUserAgent(ctx context.Context, agent string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, endUserAgentKey{}, strings.TrimSpace(agent))
}
func endUserAgentFromContext(ctx context.Context) string {
	if ctx != nil {
		s, _ := ctx.Value(endUserAgentKey{}).(string)
		return s
	}
	return ""
}

// decimalPattern is a plain or exponent-form decimal: "0.00000052", "52",
// "5.2E-7". The exponent form is NOT a courtesy: the supplier serialises its
// BigDecimal amounts with Java semantics, and BigDecimal.toString() switches
// to scientific notation as soon as the adjusted exponent drops below -6 —
// i.e. for every Lightning amount under 100 sats. A 50 NGN Airtel top-up
// (52 sats) came back as "5.2E-7" on the live shop (2026-10-20); the strict
// pattern refused it as "invalid decimal amount" and a perfectly payable
// order was parked in manual review with its payment blocked.
var decimalPattern = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(?:[eE]([-+]?[0-9]{1,3}))?$`)
var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$`)

func isProblemCode(s string) bool { return codePattern.MatchString(s) }

// PlainDecimal canonicalises a decimal string to its plain positional form
// with no exponent, no leading zeros and no trailing fractional zeros
// ("5.2E-7" → "0.00000052", "1500.00" → "1500", "007" → "7"). It is pure
// string arithmetic — no float is ever involved — so the value is exact.
// NaN, Infinity, signs, thousands separators and trailing garbage are
// refused; exponents beyond ±40 are refused as hostile.
func PlainDecimal(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return "", false
	}
	m := decimalPattern.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	intPart, frac, expS := m[1], m[2], m[3]
	exp := 0
	if expS != "" {
		n, err := strconv.Atoi(expS)
		if err != nil || n < -40 || n > 40 {
			return "", false
		}
		exp = n
	}
	digits := intPart + frac
	point := len(intPart) + exp // index of the decimal point inside digits
	if point <= 0 {
		digits = strings.Repeat("0", 1-point) + digits
		point = 1
	}
	if point > len(digits) {
		digits += strings.Repeat("0", point-len(digits))
	}
	whole := strings.TrimLeft(digits[:point], "0")
	if whole == "" {
		whole = "0"
	}
	fraction := strings.TrimRight(digits[point:], "0")
	if fraction == "" {
		return whole, true
	}
	return whole + "." + fraction, true
}

// PositiveDecimal refuses NaN, Infinity, trailing garbage and zero; exponent
// notation is accepted and read exactly (see PlainDecimal). Rational
// arithmetic keeps BTC amounts exact; no float equality for payments.
func PositiveDecimal(s string) (*big.Rat, error) {
	plain, ok := PlainDecimal(s)
	if !ok {
		return nil, fmt.Errorf("invalid decimal amount")
	}
	r, ok := new(big.Rat).SetString(plain)
	if !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return r, nil
}

func normalizeDecimalFields(b []byte, fields ...string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	for _, f := range fields {
		v := obj[f]
		if len(v) == 0 || string(v) == "null" {
			continue
		}
		if v[0] == '"' {
			// Already a string: canonicalise an exponent form ("5.2E-7")
			// to its plain decimal so every consumer — the payment guard,
			// the database, the storefront — sees "0.00000052". Anything
			// that is not a decimal is left for the guard to refuse.
			var str string
			if err := json.Unmarshal(v, &str); err == nil {
				if plain, ok := PlainDecimal(str); ok {
					obj[f], _ = json.Marshal(plain)
				}
			}
			continue
		}
		var n json.Number
		if err := json.Unmarshal(v, &n); err != nil {
			return nil, fmt.Errorf("invalid %s", f)
		}
		// json.Number keeps the wire literal, exponent included.
		if plain, ok := PlainDecimal(n.String()); ok {
			obj[f], _ = json.Marshal(plain)
		} else {
			obj[f], _ = json.Marshal(n.String())
		}
	}
	return json.Marshal(obj)
}

func (p *PriceQuote) UnmarshalJSON(b []byte) error {
	type alias PriceQuote
	raw, err := normalizeDecimalFields(b, "coin_amount", "original_coin_amount")
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, (*alias)(p))
}

func (v *ValidationResult) UnmarshalJSON(b []byte) error {
	type alias ValidationResult
	raw, err := normalizeDecimalFields(b, "coin_amount", "original_coin_amount")
	if err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	// The public contract contains a problems array. An empty object or HTML
	// error masquerading as HTTP 200 is NOT permission to create an order.
	p, ok := obj["problems"]
	if !ok || len(p) == 0 || (p[0] != '[' && string(p) != "null") {
		return fmt.Errorf("validation response missing problems array")
	}
	return json.Unmarshal(raw, (*alias)(v))
}
