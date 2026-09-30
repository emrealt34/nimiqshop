package catalog

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"nimiqshop/internal/cryptorefills"
)

// ProductMoney reads structured supplier face-value fields only. A SKU label,
// localized label, product ID, point count or data allowance is never money.
// Missing/invalid data stays unknown; the live supplier invoice prices the SKU.
func ProductMoney(p cryptorefills.Product) (float64, string) {
	currency := strings.ToUpper(strings.TrimSpace(p.CurrencyCode))
	if v := positiveAmount(p.Amount); v > 0 {
		return v, currency
	}
	var raw any
	if json.Unmarshal(p.FaceValue, &raw) != nil {
		return 0, currency
	}
	if text, ok := raw.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			raw = decoded
		}
	}
	if obj, ok := raw.(map[string]any); ok {
		if c, ok := obj["currency_code"].(string); ok && strings.TrimSpace(c) != "" {
			currency = strings.ToUpper(strings.TrimSpace(c))
		}
		raw = obj["amount"]
		if amount, ok := raw.(map[string]any); ok {
			raw = amount["value"]
			if raw == nil {
				raw = amount["price"]
			}
		}
	}
	return positiveAmount(raw), currency
}

func positiveAmount(raw any) float64 {
	var v float64
	switch n := raw.(type) {
	case float64:
		v = n
	case string:
		// A numeric field may be serialized as a decimal string, never a label.
		if !decimalAmount.MatchString(strings.TrimSpace(n)) {
			return 0
		}
		var err error
		v, err = strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0
		}
	default:
		return 0
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v >= 100_000_000 {
		return 0
	}
	return v
}

var decimalAmount = regexp.MustCompile(`^[+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
