// Package catalog implements the operator-managed catalog visibility rules.
//
// Everything here is PURE logic (no I/O) so it is exhaustively unit
// testable; persistence lives in internal/db (single JSON blob) and the
// admin HTTP surface in internal/handlers. The rules are enforced at THREE
// layers so they cannot be bypassed:
//
//  1. browse  — /api/catalog/* responses are filtered server-side (the
//     browser never receives a hidden brand/denomination);
//  2. price   — /api/catalog/price and /api/catalog/products refuse hidden
//     or over-cap items;
//  3. buy     — POST /api/quotes re-checks every rule against the supplier
//     family data immediately before the order is created. A stale cached
//     page can therefore never buy a banned product.
//
// Rule changes take effect instantly for NEW responses (filters run after
// the response cache) and are persisted in BadgerDB, surviving crashes.
package catalog

import (
	"strconv"
	"strings"
	"unicode"
)

// Product kinds used by the supplier (family.Kind / category "kind").
const (
	KindGiftcard       = "giftcard"
	KindMobileRecharge = "mobile_recharge"
)

// esim is expressed as category "e-sim" under kind mobile_recharge.

// Rules is the whole operator-controlled catalog policy. Zero value = a
// fully open catalog (nothing hidden), which is the safe default.
type Rules struct {
	// MaxFaceValueUSD hides every purchasable denomination whose face
	// value exceeds the cap (e.g. 10000 = "under $10k only"). Dynamic
	// ranges are clamped down to the cap; a family whose MINIMUM exceeds
	// the cap disappears entirely. 0 disables the cap.
	MaxFaceValueUSD float64 `json:"max_face_value_usd"`
	// HiddenFamilies lists brand/family names ("Amazon.com", "Steam")
	// that must never be listed, priced or sold. Case-insensitive.
	HiddenFamilies []string `json:"hidden_families,omitempty"`
	// BannedCategories hides whole categories by slug ("e-money",
	// "gambling", "e-sim", ...).
	BannedCategories []string `json:"banned_categories,omitempty"`
	// BannedKinds hides whole kinds: "giftcard", "mobile_recharge".
	BannedKinds []string `json:"banned_kinds,omitempty"`
	// HiddenCountries hides an entire country catalog ("RU"). Takes
	// precedence over VisibleCountries.
	HiddenCountries []string `json:"hidden_countries,omitempty"`
	// VisibleCountries, when non-empty, is an ALLOW list: every country
	// NOT in it is hidden.
	VisibleCountries []string `json:"visible_countries,omitempty"`
	// OutOfStockPolicy: "hide" removes out-of-stock brands from listings;
	// any other value (default "show") keeps them visibly flagged.
	OutOfStockPolicy string `json:"out_of_stock_policy,omitempty"`

	// UpdatedAt/UpdatedBy are audit metadata set by the store/handler.
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// Normalize canonicalises a Rules value in place: trims spaces, uppercases
// country codes, lowercases kinds/categories, dedupes, sorts. It guarantees
// the matching helpers below behave deterministically.
func (r *Rules) Normalize() {
	r.HiddenFamilies = cleanSet(lowerAll(r.HiddenFamilies))
	r.BannedCategories = cleanSet(lowerAll(r.BannedCategories))
	r.BannedKinds = cleanSet(lowerAll(r.BannedKinds))
	r.HiddenCountries = cleanSet(upperAll(r.HiddenCountries))
	r.VisibleCountries = cleanSet(upperAll(r.VisibleCountries))
	if r.MaxFaceValueUSD < 0 {
		r.MaxFaceValueUSD = 0
	}
	p := strings.ToLower(strings.TrimSpace(r.OutOfStockPolicy))
	if p != "hide" {
		p = "show"
	}
	r.OutOfStockPolicy = p
}

// CountryVisible reports whether the given ISO-2 country catalog may be
// browsed. Empty country ("global" queries) is always visible.
func (r *Rules) CountryVisible(country string) bool {
	c := strings.ToUpper(strings.TrimSpace(country))
	if c == "" {
		return true
	}
	if containsFold(r.HiddenCountries, c) {
		return false
	}
	if len(r.VisibleCountries) > 0 && !containsFold(r.VisibleCountries, c) {
		return false
	}
	return true
}

// FamilyVisible reports whether a brand/family passes the name, category,
// kind and (optionally) price-cap rules. faceUSD min/max are the parsed
// numeric face-value bounds of the family (0 = unknown/non-numeric).
func (r *Rules) FamilyVisible(family, category, kind string, minFaceUSD, maxFaceUSD float64) bool {
	if r.IsFamilyHidden(family) {
		return false
	}
	if r.IsCategoryBanned(category) {
		return false
	}
	if r.IsKindBanned(kind) {
		return false
	}
	if r.MaxFaceValueUSD > 0 && minFaceUSD > 0 && minFaceUSD > r.MaxFaceValueUSD {
		// Even the cheapest denomination is above the cap.
		return false
	}
	return true
}

// IsFamilyHidden matches a family/brand name case-insensitively.
func (r *Rules) IsFamilyHidden(family string) bool {
	return containsFold(r.HiddenFamilies, family)
}

// IsCategoryBanned matches the primary category OR any additional category.
func (r *Rules) IsCategoryBanned(categories ...string) bool {
	for _, c := range categories {
		if c == "" {
			continue
		}
		if containsFold(r.BannedCategories, c) {
			return true
		}
	}
	return false
}

// IsKindBanned matches giftcard/mobile_recharge kinds.
func (r *Rules) IsKindBanned(kind string) bool {
	return containsFold(r.BannedKinds, kind)
}

// HideOutOfStock reports whether listings should drop out-of-stock brands.
func (r *Rules) HideOutOfStock() bool { return r.OutOfStockPolicy == "hide" }

// DenominationVisible applies only the price cap to one fixed face value.
func (r *Rules) DenominationVisible(faceUSD float64) bool {
	if r.MaxFaceValueUSD <= 0 || faceUSD <= 0 {
		return true
	}
	return faceUSD <= r.MaxFaceValueUSD
}

// ClampRange applies the price cap to a dynamic min/max/step range. It
// returns the (possibly reduced) max and whether anything <= the cap is
// still purchasable.
func (r *Rules) ClampRange(minUSD, maxUSD float64) (newMax float64, visible bool) {
	if r.MaxFaceValueUSD <= 0 {
		return maxUSD, true
	}
	if minUSD > r.MaxFaceValueUSD {
		return maxUSD, false
	}
	if maxUSD > r.MaxFaceValueUSD {
		return r.MaxFaceValueUSD, true
	}
	return maxUSD, true
}

/* ------------------------- face-value extraction ------------------------- */

// ParseFaceValue extracts a numeric face value from supplier brand strings
// like "$5", "€10.50", "5-500 USD" or "100". Data-style strings ("1 GB 3
// days", "Any") return their leading number (1 GB → 1) or 0 when there is
// none — never treated as free. Currency is ignored on purpose: the cap is
// an approximate operator control, not an FX-precise one.
func ParseFaceValue(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Scan for the first run of digits (with optional decimal part).
	start, end := -1, -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isDot := c == '.'
		if isDigit && start < 0 {
			start = i
			end = i + 1
			continue
		}
		if start >= 0 {
			if isDigit || (isDot && !strings.Contains(s[start:end], ".")) {
				end = i + 1
				continue
			}
			break
		}
	}
	if start < 0 {
		return 0
	}
	v, err := strconv.ParseFloat(s[start:end], 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// ParseFaceValueAny returns (min, max) from two brand strings, mapping
// unparsable values to the other bound or 0.
func ParseFaceValueAny(minS, maxS string) (float64, float64) {
	minV := ParseFaceValue(minS)
	maxV := ParseFaceValue(maxS)
	if minV == 0 {
		minV = maxV
	}
	if maxV == 0 {
		maxV = minV
	}
	return minV, maxV
}

/* ------------------------------- helpers -------------------------------- */

func containsFold(set []string, v string) bool {
	if v == "" {
		return false
	}
	lv := strings.ToLower(strings.TrimSpace(v))
	for _, s := range set {
		if strings.ToLower(strings.TrimSpace(s)) == lv {
			return true
		}
	}
	return false
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func upperAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cleanSpace removes control/space characters inside identifiers.
func cleanSpace(s string) string {
	var b strings.Builder
	for _, c := range s {
		if unicode.IsControl(c) {
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

func cleanSet(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = cleanSpace(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	// insertion order is kept on purpose: the admin UI edits this list.
	return out
}
