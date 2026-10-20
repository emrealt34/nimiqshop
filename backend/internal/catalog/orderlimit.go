package catalog

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"nimiqshop/internal/cryptorefills"
)

// OrderLimit mirrors the supplier's hard ceiling on ONE order on the shop's
// only payment rail (BTC Lightning through Nimiq Pay).
//
// Observed live on 2026-10-20 (Amazon.fr, FR, €200, verify@nimiqbase.com):
// Cryptorefills' /v5/orders/validations answered 422
// LIGHTNING_ORDER_LIMIT_EXCEEDED with kyc_level NOT_REGISTERED, limits
// per_order 100 EUR / daily 100 EUR / monthly 300 EUR, and an order_total of
// 208.07 EUR — the face value plus the supplier's own 4.04% margin. The
// "next level" keeps per_order at 100 EUR. Until this mirror existed the
// catalog advertised Amazon.fr as €5–€445 and the buyer only learned at the
// very last checkout step, behind a generic "could not approve" line, that
// nothing above ~€96 can be bought at all.
//
// The supplier measures the limit on its TOTAL (face value + margin), so
// the face-value cap the catalog may show is TotalEUR / (1 + MarginPct/100).
// Everything downstream (range clamp, hidden fixed denominations, the
// purchase-time gate) consumes that face cap; the supplier stays the final
// authority and its own refusal is still translated for the buyer.
type OrderLimit struct {
	// TotalEUR is the supplier's per-order ceiling on the order total. 0
	// disables the shop-side mirror.
	TotalEUR float64
	// MarginPct is the allowance for the supplier margin hidden inside that
	// total when the ceiling is turned into a face-value cap.
	MarginPct float64
}

// Enabled reports whether the limit carries a usable ceiling.
func (l OrderLimit) Enabled() bool {
	return l.TotalEUR > 0 && !math.IsNaN(l.TotalEUR) && !math.IsInf(l.TotalEUR, 0)
}

// FaceCapEUR is the largest face value whose supplier total still fits
// under TotalEUR once the margin allowance is added.
func (l OrderLimit) FaceCapEUR() float64 {
	if !l.Enabled() {
		return 0
	}
	m := l.MarginPct
	if m < 0 || math.IsNaN(m) || math.IsInf(m, 0) {
		m = 0
	}
	return l.TotalEUR / (1 + m/100)
}

// FaceCapUSD is FaceCapEUR in the catalog's USD cap unit.
func (l OrderLimit) FaceCapUSD() float64 {
	if !l.Enabled() {
		return 0
	}
	return ToUSD(l.FaceCapEUR(), "EUR")
}

// FaceCapIn converts the face cap into a product's local currency, rounded
// DOWN to whole units. 0 means the currency is unknown.
func (l OrderLimit) FaceCapIn(currency string) float64 {
	if !l.Enabled() {
		return 0
	}
	code := strings.ToUpper(strings.TrimSpace(currency))
	if code == "EUR" {
		return math.Floor(l.FaceCapEUR())
	}
	if !CurrencyKnown(code) {
		return 0
	}
	return math.Floor(l.FaceCapUSD() / usdRateOr(code))
}

// WithMarginFloor returns the limit with the supplier's OBSERVED fee for one
// product folded in. The configured margin is a floor: an observed fee
// below it never widens the cap, a higher one tightens it.
func (l OrderLimit) WithMarginFloor(observedPct float64) OrderLimit {
	if math.IsNaN(observedPct) || math.IsInf(observedPct, 0) || observedPct <= l.MarginPct {
		return l
	}
	l.MarginPct = observedPct
	return l
}

// Apply lowers rules.MaxFaceValueUSD to the supplier face cap when that is
// the tighter ceiling, so every browse filter (ranges clamped, fixed
// denominations hidden, families whose minimum is out of reach dropped)
// inherits it without a second code path. It reports whether the supplier
// cap is now the binding one; the rules remember the limit so the clamp can
// tell the buyer WHY a range stops where it does.
func (l OrderLimit) Apply(r *Rules) bool {
	capUSD := l.FaceCapUSD()
	if r == nil || capUSD <= 0 {
		return false
	}
	if r.MaxFaceValueUSD > 0 && r.MaxFaceValueUSD <= capUSD {
		return false
	}
	r.MaxFaceValueUSD = capUSD
	r.orderLimit = l
	return true
}

// Gate is the purchase-time twin of Apply: the same face cap, applied to the
// whole order (face value × quantity, in USD), with an error that names the
// real ceiling instead of a generic "price cap".
func (l OrderLimit) Gate(totalFaceUSD float64, currency string) error {
	capUSD := l.FaceCapUSD()
	if capUSD <= 0 || totalFaceUSD <= 0 || math.IsNaN(totalFaceUSD) || totalFaceUSD <= capUSD {
		return nil
	}
	return &OrderLimitError{Limit: l, Currency: strings.ToUpper(strings.TrimSpace(currency)), MaxFace: l.FaceCapIn(currency)}
}

// OrderLimitError is the purchase-time refusal; its fields feed the JSON
// payload so the storefront can phrase it in the buyer's language.
type OrderLimitError struct {
	Limit    OrderLimit
	Currency string
	// MaxFace is the largest face value of this product that still fits,
	// in the product's currency (0 = unknown currency).
	MaxFace float64
}

func (e *OrderLimitError) Error() string {
	msg := "this order exceeds the supplier's per-order limit for Lightning payments (" +
		FormatAmount(e.Limit.TotalEUR) + " EUR in total, including the supplier fee)"
	if e.MaxFace > 0 && e.Currency != "" {
		msg += "; for this product choose at most " + FormatAmount(e.MaxFace) + " " + e.Currency + " per order, or lower the quantity"
	}
	return msg
}

// Payload is the machine-readable half of the refusal.
func (e *OrderLimitError) Payload() map[string]any {
	out := map[string]any{"total_eur": e.Limit.TotalEUR}
	if e.MaxFace > 0 && e.Currency != "" {
		out["max_face_value"] = e.MaxFace
		out["currency"] = e.Currency
	}
	return out
}

// FormatAmount prints a money amount without float noise ("95", "208.07").
func FormatAmount(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// rangeCap annotates a clamped dynamic range with the reason the slider
// stops short of the supplier's own maximum.
func (l OrderLimit) rangeCap(originalMax float64) *cryptorefills.RangeCap {
	if !l.Enabled() {
		return nil
	}
	return &cryptorefills.RangeCap{Reason: cryptorefills.RangeCapSupplierOrderLimit, TotalEUR: l.TotalEUR, SupplierMax: originalMax}
}

// String is for logs and the admin summary.
func (l OrderLimit) String() string {
	if !l.Enabled() {
		return "off"
	}
	return fmt.Sprintf("%s EUR total (margin %s%%, face cap %s EUR)", FormatAmount(l.TotalEUR), FormatAmount(l.MarginPct), FormatAmount(math.Floor(l.FaceCapEUR()*100)/100))
}
