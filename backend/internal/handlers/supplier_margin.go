package handlers

import (
	"context"
	"math"
	"strings"
	"time"

	"nimiqshop/internal/catalog"
	"nimiqshop/internal/cryptorefills"
)

/* Product-aware supplier ceiling.
 *
 * The supplier refuses a Lightning order whose TOTAL (face value plus its
 * own fee) exceeds SUPPLIER_ORDER_LIMIT_EUR. The configured
 * SUPPLIER_ORDER_MARGIN_PCT is a floor for that fee, not the truth: measured
 * on the live shop (2026-10-20) Amazon.fr costs +4.0 %, Amazon.ae +1.5 %,
 * Amazon.com.mx +6.9 %, Amazon.pl +8.0 % and Amazon.co.jp +20.3 % over face.
 * With a flat 5 % the product page promised ¥16,926 for Amazon.co.jp while
 * the supplier's total for it was €114 — refused at checkout.
 *
 * So the ceiling is calibrated per family from the supplier's own price
 * quote (the same /v4/products/price call the product page makes for its
 * NIM price): observed fee = quoted total / face value − 1, converted
 * through the shop's BTC/USD and fiat rates. The configured margin remains
 * the floor; the observed one only ever tightens the cap. Unknown rates, a
 * failed quote or an implausible ratio fall back to the configured limit —
 * and the supplier's own refusal (LIGHTNING_ORDER_LIMIT_EXCEEDED, explained
 * with its numbers) stays the last line of defence.
 */

const (
	supplierMarginTTL         = 15 * time.Minute
	supplierMarginFallbackTTL = 2 * time.Minute
	// supplierMarginMax bounds what the shop believes: above it the quote
	// is treated as noise (wrong rate, repriced SKU) and the floor is used.
	supplierMarginMax = 0.75
	// supplierMarginHeadroomPct is added to an observed fee before it caps
	// a range. The shop converts the quote with its own BTC/USD and fiat
	// rates while the supplier checks the ceiling with its own; measured
	// live, a cap computed from the bare observed fee landed at €99.7 of
	// the €100 ceiling (Amazon.co.jp) — accepted, but one rate tick from a
	// refusal. One point of headroom keeps the maximum on the page an
	// amount the supplier accepts after ordinary drift.
	supplierMarginHeadroomPct = 1.0
)

// orderLimitFor is the per-family twin of orderLimit(): the same ceiling,
// with the supplier's observed fee for this family folded into the margin.
// name is the brand/family name the supplier prices under; probeFace is the
// face value (local currency) to price — a range product's configured cap
// clamped into its range is the natural choice.
func (h *Handlers) orderLimitFor(ctx context.Context, name, country, currency string, probeFace float64) catalog.OrderLimit {
	base := h.orderLimit()
	if !base.Enabled() {
		return base
	}
	code := strings.ToUpper(strings.TrimSpace(currency))
	name = strings.TrimSpace(name)
	country = strings.ToUpper(strings.TrimSpace(country))
	if name == "" || len(country) != 2 || !catalog.CurrencyKnown(code) {
		return base
	}
	key := "margin:" + country + "|" + strings.ToLower(name)
	if cached, ok := h.cache.get(key); ok {
		if pct, ok := cached.(float64); ok {
			return base.WithMarginFloor(pct)
		}
	}
	pct, ok := h.observeSupplierMargin(ctx, name, country, code, probeFace)
	if !ok {
		h.cache.setTTL(key, base.MarginPct, supplierMarginFallbackTTL)
		return base
	}
	h.cache.setTTL(key, pct, supplierMarginTTL)
	return base.WithMarginFloor(pct)
}

// observeSupplierMargin prices one face value at the supplier and returns
// the fee it implies, in percent of face, rounded UP to one decimal plus
// the drift headroom.
func (h *Handlers) observeSupplierMargin(ctx context.Context, name, country, currency string, probeFace float64) (float64, bool) {
	if h.CR == nil || probeFace <= 0 || math.IsNaN(probeFace) || math.IsInf(probeFace, 0) {
		return 0, false
	}
	btcUSD := h.btcUSDNow()
	faceUSD := catalog.ToUSD(probeFace, currency)
	if btcUSD <= 0 || faceUSD <= 0 {
		return 0, false
	}
	quote, err := h.CR.Price(ctx, name, country, probeFace)
	if err != nil || quote == nil {
		return 0, false
	}
	amount, err := cryptorefills.PositiveDecimal(quote.CoinAmount)
	if err != nil {
		return 0, false
	}
	btc, _ := amount.Float64()
	margin := btc*btcUSD/faceUSD - 1
	if math.IsNaN(margin) || margin < -0.1 || margin > supplierMarginMax {
		return 0, false
	}
	if margin < 0 {
		margin = 0
	}
	return math.Ceil(margin*1000)/10 + supplierMarginHeadroomPct, true
}

// btcUSDNow is the BTC/USD rate the shop converts supplier quotes with.
// Tests pin it through BTCUSDPrice; production reads the oracle snapshot.
func (h *Handlers) btcUSDNow() float64 {
	if h.BTCUSDPrice != nil {
		if v, err := h.BTCUSDPrice(); err == nil && v > 0 {
			return v
		}
		return 0
	}
	return currentRates().btcUSD
}

// refineOrderLimit tightens the rules' face cap for ONE family response
// using the supplier's observed fee for that family. Only range products
// can be priced at an arbitrary face value, so a fixed-denomination family
// keeps the configured margin.
func (h *Handlers) refineOrderLimit(ctx context.Context, rules *catalog.Rules, country string, fams []cryptorefills.Family) {
	if rules == nil || len(fams) == 0 || !h.orderLimit().Enabled() {
		return
	}
	fam := fams[0]
	for _, p := range fam.Products {
		if p.Range == nil || !catalog.CurrencyKnown(p.Range.Currency) {
			continue
		}
		probe := h.orderLimit().FaceCapIn(p.Range.Currency)
		if probe <= 0 {
			continue
		}
		if p.Range.Max > 0 && probe > p.Range.Max {
			probe = p.Range.Max
		}
		if probe < p.Range.Min {
			probe = p.Range.Min
		}
		// Priced under the same name the product page prices with
		// (brand first, family as the fallback — see getProductPrice).
		name := fam.Brand
		if name == "" {
			name = fam.Family
		}
		h.orderLimitFor(ctx, name, country, p.Range.Currency, probe).Apply(rules)
		return
	}
}

// priceNameFor is the name a family is priced under at the supplier: the
// brand when the catalogue knows it, otherwise the family/product id.
func priceNameFor(meta familyMeta, productID string) string {
	if b := strings.TrimSpace(meta.BrandName); b != "" {
		return b
	}
	return productID
}
