// Package money replaces Postgres NUMERIC(18,6) with fixed-point integer
// arithmetic.
//
// Postgres gave us exact decimal math for free via NUMERIC; Badger stores
// raw bytes and has no numeric type at all, so USD amounts are kept as int64
// counts of micro-USD (1 USD = 1_000_000 micros). Every stored amount
// round-trips through here, which means no float drift can ever creep into
// an order/quote total the way it would with float64.
//
// int64 micros covers ±9.2 trillion USD — far beyond anything this shop
// will see, so overflow is not a practical concern.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

// Scale is the number of decimal places kept, matching NUMERIC(18,6).
const Scale = 6

const unit int64 = 1_000_000

// Micros is a fixed-point USD amount: the number of millionths of a dollar.
type Micros int64

// Parse converts a decimal string ("12.50", "-0.000001", "3") into Micros.
// It is deliberately strict: anything it cannot represent exactly is an
// error rather than a silent rounding, because these values are money.
func Parse(s string) (Micros, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("money: empty amount")
	}

	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("money: malformed amount")
	}

	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	if hasFrac && len(fracPart) > Scale {
		// Trailing zeros beyond our scale are harmless; real precision is not.
		trimmed := strings.TrimRight(fracPart[Scale:], "0")
		if trimmed != "" {
			return 0, fmt.Errorf("money: %q has more than %d decimal places", s, Scale)
		}
		fracPart = fracPart[:Scale]
	}

	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: bad integer part in %q", s)
	}

	var frac int64
	if fracPart != "" {
		padded := fracPart + strings.Repeat("0", Scale-len(fracPart))
		frac, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("money: bad fractional part in %q", s)
		}
	}

	total := whole*unit + frac
	if neg {
		total = -total
	}
	return Micros(total), nil
}

// MustParse is Parse for values already known to be well-formed.
func MustParse(s string) Micros {
	m, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return m
}

// FromFloat converts a float64 (e.g. a price coming off the supplier API
// or a JSON request body) into exact fixed-point micros, rounding half away
// from zero at the 6th decimal place.
func FromFloat(f float64) Micros {
	if f < 0 {
		return Micros(int64(f*float64(unit) - 0.5))
	}
	return Micros(int64(f*float64(unit) + 0.5))
}

// Float returns the amount as a float64, for display or API payloads only.
func (m Micros) Float() float64 { return float64(m) / float64(unit) }

// String renders the amount with exactly 6 decimal places, matching how
// Postgres rendered NUMERIC(18,6)::text so API responses are unchanged.
func (m Micros) String() string {
	neg := m < 0
	v := int64(m)
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%d.%06d", v/unit, v%unit)
	if neg {
		return "-" + s
	}
	return s
}

// IsPositive reports whether the amount is strictly greater than zero.
func (m Micros) IsPositive() bool { return m > 0 }
