package cryptorefills

// Wire contract checked against the public developer reference on 2026-09-07.
// Do not send response-only fields (including nested deliverable) in requests.
import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
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
	user := r.User
	if user == nil && r.Email != "" {
		user = &OrderUser{Email: r.Email}
	}
	return struct {
		Deliveries  []requestDelivery `json:"deliveries"`
		Payment     OrderPayment      `json:"payment"`
		User        *OrderUser        `json:"user,omitempty"`
		Lang        string            `json:"lang"`
		Acquisition *Acquisition      `json:"acquisition,omitempty"`
	}{r.requestDeliveries(), r.Payment, user, r.Lang, r.Acquisition}
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

var decimalPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$`)

func isProblemCode(s string) bool { return codePattern.MatchString(s) }

// PositiveDecimal refuses NaN, Infinity, exponents, trailing garbage and zero.
// Rational arithmetic keeps BTC amounts exact; no float equality for payments.
func PositiveDecimal(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if len(s) > 80 || !decimalPattern.MatchString(s) {
		return nil, fmt.Errorf("invalid decimal amount")
	}
	r, ok := new(big.Rat).SetString(s)
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
		if len(v) == 0 || string(v) == "null" || v[0] == '"' {
			continue
		}
		var n json.Number
		if err := json.Unmarshal(v, &n); err != nil {
			return nil, fmt.Errorf("invalid %s", f)
		}
		obj[f], _ = json.Marshal(n.String())
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
