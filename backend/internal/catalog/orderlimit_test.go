package catalog

import (
	"math"
	"strings"
	"testing"

	"nimiqshop/internal/cryptorefills"
)

// The live refusal of 2026-10-20: 100 EUR per order on the order total,
// Amazon.fr carrying a 4.04% margin, so €96 was the real maximum and the
// catalog still advertised €445.
var liveLimit = OrderLimit{TotalEUR: 100, MarginPct: 5}

func TestOrderLimitFaceCap(t *testing.T) {
	if got := liveLimit.FaceCapEUR(); math.Abs(got-100/1.05) > 1e-9 {
		t.Fatalf("FaceCapEUR = %v, want %v", got, 100/1.05)
	}
	if got := liveLimit.FaceCapIn("EUR"); got != 95 {
		t.Fatalf("FaceCapIn(EUR) = %v, want 95 (rounded down to whole units)", got)
	}
	if got, want := liveLimit.FaceCapIn("usd"), math.Floor(liveLimit.FaceCapUSD()); got != want {
		t.Fatalf("FaceCapIn(usd) = %v, want %v", got, want)
	}
	if got := liveLimit.FaceCapIn("Points"); got != 0 {
		t.Fatalf("unknown currency must report 0, got %v", got)
	}
	// A €96 face value times 1.0404 is 99.88 EUR — under the ceiling — so
	// the 5% allowance must not hide more than a euro or two of range.
	if cap := liveLimit.FaceCapEUR(); cap < 95 || cap > 96.2 {
		t.Fatalf("face cap %v drifted away from the live maximum (~96)", cap)
	}
	for _, off := range []OrderLimit{{}, {TotalEUR: -1}, {TotalEUR: math.NaN()}, {TotalEUR: math.Inf(1)}} {
		if off.Enabled() || off.FaceCapUSD() != 0 || off.FaceCapIn("EUR") != 0 || off.Gate(1e9, "EUR") != nil {
			t.Fatalf("limit %+v must be inert", off)
		}
	}
	// A negative or absurd margin never INCREASES the cap above the total.
	if got := (OrderLimit{TotalEUR: 100, MarginPct: -50}).FaceCapEUR(); got != 100 {
		t.Fatalf("negative margin: cap %v, want 100", got)
	}
}

func TestOrderLimitApplyBindsOnlyWhenTighter(t *testing.T) {
	capUSD := liveLimit.FaceCapUSD()

	operator := Rules{MaxFaceValueUSD: 500} // the live admin cap of 2026-10-20
	if !liveLimit.Apply(&operator) || operator.MaxFaceValueUSD != capUSD || !operator.OrderLimitBinding() {
		t.Fatalf("a $500 operator cap must be lowered to the supplier face cap %v, got %+v", capUSD, operator)
	}

	open := Rules{}
	if !liveLimit.Apply(&open) || open.MaxFaceValueUSD != capUSD {
		t.Fatalf("an open catalog must receive the supplier face cap, got %+v", open)
	}

	tighter := Rules{MaxFaceValueUSD: 20}
	if liveLimit.Apply(&tighter) || tighter.MaxFaceValueUSD != 20 || tighter.OrderLimitBinding() {
		t.Fatalf("a tighter operator cap must win, got %+v", tighter)
	}

	var disabled OrderLimit
	untouched := Rules{MaxFaceValueUSD: 500}
	if disabled.Apply(&untouched) || untouched.MaxFaceValueUSD != 500 {
		t.Fatalf("a disabled limit must not touch the rules, got %+v", untouched)
	}
	if disabled.Apply(nil) {
		t.Fatal("nil rules must be a no-op")
	}
}

func TestOrderLimitClampsRangesAndHidesDenominations(t *testing.T) {
	rules := Rules{MaxFaceValueUSD: 500}
	liveLimit.Apply(&rules)
	fams := []cryptorefills.Family{{
		Family: "Amazon.fr", Brand: "Amazon.fr", CountryCode: "FR", Kind: KindGiftcard, Category: "e-commerce",
		Products: []cryptorefills.Product{
			{ProductID: "range", IsDynamic: true, Range: &cryptorefills.ValueRange{Min: 5, Max: 2000, Currency: "EUR", StepSize: 1, Default: "2000.0"}},
			{ProductID: "fixed-50", Denomination: "50 EUR", Amount: 50, CurrencyCode: "EUR"},
			{ProductID: "fixed-100", Denomination: "100 EUR", Amount: 100, CurrencyCode: "EUR"},
			{ProductID: "fixed-150", Denomination: "150 EUR", Amount: 150, CurrencyCode: "EUR"},
		},
	}}
	out := FilterFamilies(&rules, fams)
	if len(out) != 1 {
		t.Fatalf("family must survive, got %d", len(out))
	}
	ids := make([]string, 0, len(out[0].Products))
	for _, p := range out[0].Products {
		ids = append(ids, p.ProductID)
	}
	if strings.Join(ids, ",") != "range,fixed-50" {
		t.Fatalf("products after the clamp: %v (100 and 150 EUR cards exceed the 100 EUR total once the margin is added)", ids)
	}
	r := out[0].Products[0].Range
	if r.Max != 95 || r.Min != 5 {
		t.Fatalf("range must stop at the supplier face cap: got %v–%v", r.Min, r.Max)
	}
	if r.Default != "" {
		t.Fatalf("a supplier default above the clamped max must be dropped, got %q", r.Default)
	}
	if r.Cap == nil || r.Cap.Reason != cryptorefills.RangeCapSupplierOrderLimit || r.Cap.TotalEUR != 100 || r.Cap.SupplierMax != 2000 {
		t.Fatalf("the clamp must say why: %+v", r.Cap)
	}
	// The supplier's own data is never mutated in place.
	if fams[0].Products[0].Range.Max != 2000 || fams[0].Products[0].Range.Cap != nil {
		t.Fatal("FilterFamilies mutated the cached supplier range")
	}

	// When the OPERATOR cap is the binding one the annotation stays off:
	// that ceiling is policy, not a supplier constraint.
	operator := Rules{MaxFaceValueUSD: 20}
	liveLimit.Apply(&operator)
	out = FilterFamilies(&operator, fams)
	if got := out[0].Products[0].Range; got.Cap != nil || got.Max >= 95 {
		t.Fatalf("operator-bound clamp must carry no supplier annotation, got %+v", got)
	}
}

func TestOrderLimitGateNamesTheRealMaximum(t *testing.T) {
	// €200 Amazon.fr (the live order) is refused with the ceiling and the
	// product's own maximum; €95 passes; a two-card cart of €60 each fails
	// on the total even though each card alone would fit.
	err := liveLimit.Gate(ToUSD(200, "EUR"), "EUR")
	if err == nil {
		t.Fatal("€200 must be refused")
	}
	var le *OrderLimitError
	if !asOrderLimit(err, &le) {
		t.Fatalf("want *OrderLimitError, got %T", err)
	}
	msg := err.Error()
	for _, want := range []string{"100 EUR in total", "including the supplier fee", "at most 95 EUR per order", "lower the quantity"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
	p := le.Payload()
	if p["total_eur"] != 100.0 || p["max_face_value"] != 95.0 || p["currency"] != "EUR" {
		t.Fatalf("payload %+v", p)
	}
	if err := liveLimit.Gate(ToUSD(95, "EUR"), "EUR"); err != nil {
		t.Fatalf("€95 must pass: %v", err)
	}
	if err := liveLimit.Gate(ToUSD(120, "EUR"), "EUR"); err == nil {
		t.Fatal("two €60 cards (€120 total) must be refused on the order total")
	}
	if err := liveLimit.Gate(0, "EUR"); err != nil {
		t.Fatalf("unknown face value (0) is left to the supplier: %v", err)
	}
	// Unknown currency: still refused on the USD total, without inventing
	// a local maximum.
	err = liveLimit.Gate(ToUSD(200, "EUR"), "Points")
	if err == nil || strings.Contains(err.Error(), "at most") {
		t.Fatalf("unknown currency must refuse without a local maximum: %v", err)
	}
}

func asOrderLimit(err error, target **OrderLimitError) bool {
	e, ok := err.(*OrderLimitError)
	if ok {
		*target = e
	}
	return ok
}

func TestFormatAmount(t *testing.T) {
	cases := map[float64]string{95: "95", 100: "100", 208.0707: "208.07", 0.5: "0.50"}
	for in, want := range cases {
		if got := FormatAmount(in); got != want {
			t.Fatalf("FormatAmount(%v) = %q, want %q", in, got, want)
		}
	}
	if got := liveLimit.String(); !strings.Contains(got, "100 EUR total") || !strings.Contains(got, "95.23 EUR") {
		t.Fatalf("String() = %q", got)
	}
}
