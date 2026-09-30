package cashbackcode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	adminmodel "nimiqshop/internal/admin"
)

// Reward is one configured promo-code cashback rule.
type Reward struct {
	Code string
	Bps  int
}

// Normalize canonicalises a buyer-entered code so lookups are case-insensitive
// and surrounding whitespace never matters.
func Normalize(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// ValidateCode rejects obviously malformed promo codes before a lookup hits
// the configured table. The allowed alphabet is intentionally boring so codes
// stay easy to type on mobile: A-Z, 0-9, dash and underscore.
func ValidateCode(code string) error {
	code = Normalize(code)
	if code == "" {
		return fmt.Errorf("enter a cashback code")
	}
	if len(code) < 2 || len(code) > 40 {
		return fmt.Errorf("cashback codes must be 2-40 characters")
	}
	for _, r := range code {
		if unicode.IsUpper(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("cashback codes may only contain letters, digits, dashes and underscores")
	}
	return nil
}

// ParseTable reads CASHBACK_CODES from either JSON ({"NIM10":1000}) or a
// light operator-friendly CSV form (NIM10:1000, VIP7=700). Values are basis
// points and are capped by the same global admin ceiling as every other
// cashback source.
func ParseTable(raw string) (map[string]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]int{}, nil
	}

	type rawPair struct {
		code string
		bps  int
	}
	pairs := make([]rawPair, 0, 8)
	if strings.HasPrefix(raw, "{") {
		var obj map[string]int
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		for code, bps := range obj {
			pairs = append(pairs, rawPair{code: code, bps: bps})
		}
	} else {
		for _, chunk := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				continue
			}
			sep := strings.IndexAny(chunk, ":=")
			if sep <= 0 || sep == len(chunk)-1 {
				return nil, fmt.Errorf("invalid entry %q (use CODE:1000)", chunk)
			}
			code := strings.TrimSpace(chunk[:sep])
			var bps int
			if _, err := fmt.Sscanf(strings.TrimSpace(chunk[sep+1:]), "%d", &bps); err != nil {
				return nil, fmt.Errorf("invalid cashback bps for %q", code)
			}
			pairs = append(pairs, rawPair{code: code, bps: bps})
		}
	}

	out := make(map[string]int, len(pairs))
	for _, pair := range pairs {
		rawCode, bps := pair.code, pair.bps
		code := Normalize(rawCode)
		if err := ValidateCode(code); err != nil {
			return nil, fmt.Errorf("%q: %w", rawCode, err)
		}
		if bps <= 0 || bps > adminmodel.MaxCashbackBps {
			return nil, fmt.Errorf("%s cashback must be between 1 and %d bps", code, adminmodel.MaxCashbackBps)
		}
		if _, dup := out[code]; dup {
			return nil, fmt.Errorf("duplicate cashback code %s", code)
		}
		out[code] = bps
	}
	return out, nil
}

// Lookup resolves one buyer-entered code against the configured table.
func Lookup(tableRaw, codeRaw string) (Reward, bool, error) {
	code := Normalize(codeRaw)
	if code == "" {
		return Reward{}, false, nil
	}
	if err := ValidateCode(code); err != nil {
		return Reward{}, false, err
	}
	table, err := ParseTable(tableRaw)
	if err != nil {
		return Reward{}, false, err
	}
	bps, ok := table[code]
	if !ok {
		return Reward{Code: code}, false, nil
	}
	return Reward{Code: code, Bps: bps}, true, nil
}

// Enabled reports whether at least one cashback code is configured.
func Enabled(tableRaw string) bool {
	table, err := ParseTable(tableRaw)
	return err == nil && len(table) > 0
}

// Sorted is a stable operator/debug view of the configured table.
func Sorted(tableRaw string) ([]Reward, error) {
	table, err := ParseTable(tableRaw)
	if err != nil {
		return nil, err
	}
	out := make([]Reward, 0, len(table))
	for code, bps := range table {
		out = append(out, Reward{Code: code, Bps: bps})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bps == out[j].Bps {
			return out[i].Code < out[j].Code
		}
		return out[i].Bps > out[j].Bps
	})
	return out, nil
}
