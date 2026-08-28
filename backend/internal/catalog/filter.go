package catalog

import (
	"regexp"
	"strings"

	"nimiqshop/internal/cryptorefills"
)

var markdownURLRe = regexp.MustCompile(`https?://[^\s\)\]]+`)

func cleanLogoURL(s string) string {
	if s == "" {
		return s
	}
	// If it's markdown like "[https://...](https://...)" or "[https://...](https://...)"
	// Extract first https URL
	if m := markdownURLRe.FindString(s); m != "" {
		return m
	}
	return strings.Trim(s, "[]()")
}

func cleanFamilyName(s string) string {
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, "[") {
		if idx := strings.Index(s, "]"); idx > 0 {
			text := s[1:idx]
			if strings.HasPrefix(text, "http") {
				return text
			}
			return text
		}
	}
	return strings.Trim(s, "[]()")
}

var payWithRe = regexp.MustCompile(`(?i)Pay with Bitcoin[\s\S]*?(Arbitrum|Binance Chain|and Arbitrum|and DAI[\s\S]*?Arbitrum)\.?`)
var buyNowRe = regexp.MustCompile(`(?i)Buy now a .*? with Bitcoin and other Crypto\.\s*`)

func cleanDescription(s string) string {
	if s == "" {
		return s
	}
	cleaned := payWithRe.ReplaceAllString(s, "")
	cleaned = buyNowRe.ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`(?i)Pay with Bitcoin and other Crypto\.?`).ReplaceAllString(cleaned, "")
	// Remove leftover fragments like ", and Arbitrum.", "and Arbitrum.", ", and DAI...", "on Lightning Network, Avalanche, Polygon, Fantom, Binance Chain, and Arbitrum"
	cleaned = regexp.MustCompile(`(?i),?\s*and Arbitrum\.?`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`(?i),?\s*on Lightning Network[\s\S]*?Arbitrum\.?`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`(?i),?\s*Pay with Bitcoin[\s\S]*`).ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.Trim(cleaned, ",. ")
	cleaned = regexp.MustCompile(`\s{2,}`).ReplaceAllString(cleaned, " ")
	cleaned = strings.ReplaceAll(cleaned, " ,", ",")
	// If after cleaning it's empty or too short, provide clean description
	if cleaned == "" || len(cleaned) < 10 {
		return "Instant email delivery. Pay with Nimiq Pay (NIM → BTC Lightning) — no internal balance, direct to CryptoRefills."
	}
	// Ensure it mentions Nimiq Pay if needed, but don't duplicate
	lower := strings.ToLower(cleaned)
	if !strings.Contains(lower, "nimiq") && !strings.Contains(lower, "instant email") {
		cleaned = cleaned + " Instant email delivery. Pay with Nimiq Pay."
	}
	return cleaned
}

/* Filter functions map supplier payloads to rule-compliant payloads.
 * They never mutate the cached original: the output is a fresh copy, so a
 * later rule change is applied to the NEXT response without cache flush.
 */

// FilterBrands applies the full rule set to a /v2/brands listing.
// Categories whose kind is banned disappear; banned categories disappear;
// hidden families disappear; over-cap families disappear (when even their
// minimum is above the cap); country rules hide the entire listing.
// It returns nil when nothing is left.
func FilterBrands(rules *Rules, in *cryptorefills.BrandsResponse) *cryptorefills.BrandsResponse {
	if in == nil {
		return nil
	}
	if !rules.CountryVisible(in.CountryCode) {
		return &cryptorefills.BrandsResponse{CountryCode: in.CountryCode, Categories: []cryptorefills.BrandCategory{}}
	}
	out := &cryptorefills.BrandsResponse{CountryCode: in.CountryCode}
	for _, cat := range in.Categories {
		if rules.IsKindBanned(cat.Kind) || rules.IsCategoryBanned(cat.Category) {
			continue
		}
		filtered := cryptorefills.BrandCategory{Kind: cat.Kind, Category: cat.Category}
		for _, b := range cat.Brands {
			if !brandVisible(rules, b) {
				continue
			}
			// Clean markdown-wrapped URLs and family names that sometimes come from supplier
			b.Family = cleanFamilyName(b.Family)
			b.LogoURL = cleanLogoURL(b.LogoURL)
			b.LogoBaseURL = cleanLogoURL(b.LogoBaseURL)
			filtered.Brands = append(filtered.Brands, b)
		}
		if len(filtered.Brands) > 0 {
			out.Categories = append(out.Categories, filtered)
		}
	}
	if out.Categories == nil {
		out.Categories = []cryptorefills.BrandCategory{}
	}
	return out
}

func brandVisible(rules *Rules, b cryptorefills.Brand) bool {
	if rules.IsFamilyHidden(b.Family) {
		return false
	}
	if rules.IsCategoryBanned(b.Category) {
		return false
	}
	if rules.IsKindBanned(b.Kind) {
		return false
	}
	if rules.HideOutOfStock() && b.OutOfStock {
		return false
	}
	// The min label is in the brand's LOCAL currency ("150.000 IDR");
	// convert to its USD-equivalent before comparing with the admin's
	// USD cap. Raw comparison hid every high-denomination country.
	if rules.MaxFaceValueUSD > 0 {
		if v, code := ParseDenominationLabel(b.Min); v > 0 && CurrencyKnown(code) {
			if ToUSD(v, code) > rules.MaxFaceValueUSD {
				return false
			}
		}
	}
	return true
}

// FilterFamilies applies the rules to a /v5/products/country listing (a
// family with its products). Fixed denominations above the cap are
// dropped; dynamic ranges are clamped; a family left with no products is
// dropped. Returns a possibly-empty slice (never nil-vs-original reuse).
func FilterFamilies(rules *Rules, in []cryptorefills.Family) []cryptorefills.Family {
	out := make([]cryptorefills.Family, 0, len(in))
	for _, f := range in {
		if rules.IsFamilyHidden(f.Family) || rules.IsFamilyHidden(f.Brand) {
			continue
		}
		cats := append([]string{f.Category}, f.AdditionalCats...)
		if rules.IsCategoryBanned(cats...) {
			continue
		}
		if rules.IsKindBanned(f.Kind) {
			continue
		}
		if !rules.CountryVisible(f.CountryCode) {
			continue
		}
		if rules.HideOutOfStock() && f.OutOfStock {
			continue
		}
		kept := f
		kept.Family = cleanFamilyName(kept.Family)
		kept.Brand = cleanFamilyName(kept.Brand)
		kept.ProductTc = cleanDescription(kept.ProductTc)
		kept.LogoURL = cleanLogoURL(kept.LogoURL)
		kept.LogoBaseURL = cleanLogoURL(kept.LogoBaseURL)
		kept.Products = filterProducts(rules, f)
		if len(kept.Products) == 0 {
			continue
		}
		out = append(out, kept)
	}
	return out
}

func filterProducts(rules *Rules, f cryptorefills.Family) []cryptorefills.Product {
	out := make([]cryptorefills.Product, 0, len(f.Products))
	for _, p := range f.Products {
		if !rules.CountryVisible(f.CountryCode) {
			continue
		}
		if p.IsDynamic && p.Range != nil {
			q := p
			q.Range = clampValueRange(rules, p.Range)
			if q.Range == nil {
				continue // entire range above the cap
			}
			out = append(out, q)
			continue
		}
		// Fixed denomination products: the cap is applied on the label's
		// value converted to USD ("150.000 IDR" ≈ $10 passes a $20 cap).
		// Labels without a usable number are label-only SKUs — the
		// supplier prices them; they are never cap-filtered here (quote
		// time GateQuote enforces the admin's hidden/ban rules regardless).
		if rules.MaxFaceValueUSD > 0 {
			// Game-currency labels ("575 Points") carry no real currency code:
			// their point count is NOT a USD amount, so the cap must not drop
			// them (fail open, exactly like label-only SKUs). Real-currency
			// labels ("$25", "TRY500") keep the honest cap: over-cap tiers
			// hide, the family's cheaper tiers stay buyable.
			if v, code := ParseDenominationLabel(p.Denomination); v > 0 && CurrencyKnown(code) {
				if ToUSD(v, code) > rules.MaxFaceValueUSD {
					continue
				}
			}
		}
		out = append(out, p)
	}
	return out
}

// clampValueRange returns nil when the whole range is above the cap.
// IMPORTANT: Range.Min/Max are in the FAMILY'S LOCAL currency — the admin
// cap is USD. "150.000 IDR" (≈$10) must stay under a $20 cap; the old raw
// comparison hid every high-denomination country. Convert, compare, and
// clamp BACK to local units.
func clampValueRange(rules *Rules, r *cryptorefills.ValueRange) *cryptorefills.ValueRange {
	if r == nil {
		return nil
	}
	if rules.MaxFaceValueUSD <= 0 {
		return r
	}
	minUSD := ToUSD(r.Min, r.Currency)
	maxUSD := ToUSD(r.Max, r.Currency)
	if minUSD > rules.MaxFaceValueUSD {
		return nil
	}
	if maxUSD > rules.MaxFaceValueUSD {
		q := *r
		// Convert the USD cap back to local units and round DOWN to the
		// range step (so the slider never offers a value above the cap).
		localCap := rules.MaxFaceValueUSD / usdRateOr(r.Currency)
		if r.StepSize > 0 {
			localCap = float64(int64(localCap/r.StepSize)) * r.StepSize //nolint:gosec // price math
		}
		if localCap < r.Min {
			return nil
		}
		q.Max = localCap
		return &q
	}
	return r
}

func usdRateOr(code string) float64 {
	if r, ok := UsdPerUnit(code); ok && r > 0 {
		return r
	}
	return 1
}

// BrandSearchRow is one flattened search result.
type BrandSearchRow struct {
	Family   string `json:"family"`
	Kind     string `json:"kind"`
	Category string `json:"category"`
	Country  string `json:"country_code,omitempty"`
}

// FilterSearch applies the rules to flattened search rows.
func FilterSearch(rules *Rules, rows []BrandSearchRow) []BrandSearchRow {
	out := make([]BrandSearchRow, 0, len(rows))
	for _, r := range rows {
		if rules.IsFamilyHidden(r.Family) || rules.IsKindBanned(r.Kind) ||
			rules.IsCategoryBanned(r.Category) || !rules.CountryVisible(r.Country) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// QuoteGateError is the denial reason returned by GateQuote.
type QuoteGateError struct{ Reason string }

func (e *QuoteGateError) Error() string { return e.Reason }

// GateQuote enforces every rule at PURCHASE time using authoritative
// supplier family data (already fetched, not client-provided). faceUSD is
// the requested total face value (0 = unknown/range product).
func GateQuote(rules *Rules, family, category, kind, country string, additionalCats []string, minFaceUSD, faceUSD float64) error {
	if rules.IsFamilyHidden(family) {
		return &QuoteGateError{Reason: "this product is currently unavailable"}
	}
	cats := append([]string{category}, additionalCats...)
	if rules.IsCategoryBanned(cats...) {
		return &QuoteGateError{Reason: "this product category is currently unavailable"}
	}
	if rules.IsKindBanned(kind) {
		return &QuoteGateError{Reason: "this product type is currently unavailable"}
	}
	if !rules.CountryVisible(country) {
		return &QuoteGateError{Reason: "this country's catalog is currently unavailable"}
	}
	if rules.MaxFaceValueUSD > 0 && faceUSD > 0 && faceUSD > rules.MaxFaceValueUSD {
		return &QuoteGateError{Reason: "orders above the current price cap are not accepted"}
	}
	if rules.MaxFaceValueUSD > 0 && minFaceUSD > 0 && minFaceUSD > rules.MaxFaceValueUSD {
		return &QuoteGateError{Reason: "this product is currently unavailable"}
	}
	return nil
}

// familyKeyLower builds a stable lowercase identity for a family within a
// country (used by rule-affected caches).
func FamilyCacheKey(prefix, family, country string) string {
	return prefix + strings.ToLower(family) + ":" + strings.ToUpper(country)
}
