package cryptorefills

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

/* Payment-window regression tests (batch 5, 2026-10-04).
 *
 * The bug: the shop advertised `created_at + 30 min - 5 min` (25 minutes) for
 * every Lightning order, but the invoices CryptoRefills issues carry an `x`
 * tag of 3600 s. A live order (129cbf42) was shown as "expired" at 25 minutes
 * while its invoice stayed payable for another 35 — the owner's
 * "durduk yere expire oldu" report. The invoice is the truth; the documented
 * window is only the fallback when nothing can be decoded.
 *
 * The invoices below are built by the test itself (real bech32, synthetic
 * payload) so no live payment hash is committed.
 */

// synthInvoice builds a checksum-valid mainnet BOLT11 carrying only what the
// decoder reads: the 35-bit timestamp and an `x` expiry tag.
func synthInvoice(t *testing.T, created time.Time, expirySeconds int) string {
	t.Helper()
	groups := make([]int32, 0, 32)
	stamp := uint64(created.Unix())
	for shift := 30; shift >= 0; shift -= 5 {
		groups = append(groups, int32((stamp>>uint(shift))&31))
	}
	tag := func(kind int32, value []int32) {
		groups = append(groups, kind, int32(len(value)>>5), int32(len(value)&31))
		groups = append(groups, value...)
	}
	digits := make([]int32, 0, 8)
	for v := expirySeconds; v > 0; v >>= 5 {
		digits = append([]int32{int32(v & 31)}, digits...)
	}
	payHash := make([]int32, 52) // a 256-bit payment hash, 5 bits per group
	for i := range payHash {
		payHash[i] = int32(i % 31)
	}
	payHash[len(payHash)-1] = 0                    // 52*5 = 260 bits: the trailing 4 must be zero-padding
	tag(1, payHash)                                // p — the decoder requires one
	tag(6, digits)                                 // x — the expiry this test is about
	groups = append(groups, make([]int32, 104)...) // signature is not verified

	hrp := "lnbc12760n" // 1276 sats, an amount the decoder accepts
	values := bech32HrpExpand(hrp)
	values = append(values, groups...)
	values = append(values, make([]int32, 6)...)
	chk := bech32Polymod(values) ^ 1
	for shift := 25; shift >= 0; shift -= 5 {
		groups = append(groups, int32((chk>>uint(shift))&31))
	}
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, g := range groups {
		b.WriteByte(bech32Charset[g])
	}
	out := b.String()

	// The fixture must be readable by the very decoder the shop uses.
	if _, err := DecodeInvoice(out); err != nil {
		t.Fatalf("synthetic invoice did not decode: %v (%s)", err, out)
	}
	return out
}

func lightningOrder(created time.Time, invoice string) *Order {
	return &Order{
		ID:            "ord_test",
		Status:        "WaitingForPayment",
		Coin:          "BTC",
		CoinAmount:    "0.00001276",
		WalletAddress: invoice,
		Network:       "Lightning",
		CreatedAt:     epochOrString(strings.TrimSpace(strings.Trim(strings.TrimSpace(created.Format(time.RFC3339)), "\""))),
	}
}

func TestPaymentExpiryFollowsTheInvoiceNotTheDocumentedWindow(t *testing.T) {
	created := time.Date(2026, 10, 4, 8, 22, 16, 0, time.UTC)
	invoice := synthInvoice(t, created, 3600)
	order := lightningOrder(created, invoice)
	now := created.Add(time.Second)

	got := PaymentExpiryFor(order, now)
	want := created.Add(3600*time.Second - InvoiceSkewBuffer)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got, want)
	}
	// The regression itself: at 40 minutes in, the buyer must still have a
	// live window (the old code expired them at 25).
	if !got.After(created.Add(40 * time.Minute)) {
		t.Fatalf("deadline %s still expires before 40 minutes", got)
	}
	if _, err := ValidatePayableOrder(order, created.Add(40*time.Minute)); err != nil {
		t.Fatalf("invoice refused 40 minutes in: %v", err)
	}
}

func TestPaymentExpiryRespectsAShorterInvoice(t *testing.T) {
	created := time.Date(2026, 10, 4, 8, 22, 16, 0, time.UTC)
	order := lightningOrder(created, synthInvoice(t, created, 600))

	got := PaymentExpiryFor(order, created)
	if want := created.Add(600*time.Second - InvoiceSkewBuffer); !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s (a short invoice may not be stretched to the documented window)", got, want)
	}
}

func TestPaymentExpiryLeavesAnExpiredInvoiceExpired(t *testing.T) {
	created := time.Now().UTC().Add(-2 * time.Hour)
	order := lightningOrder(created, synthInvoice(t, created, 3600))
	now := time.Now().UTC()

	if got := PaymentExpiryFor(order, now); got.After(now) {
		t.Fatalf("deadline %s is in the future for an invoice that died an hour ago", got)
	}
	if _, err := ValidatePayableOrder(order, now); err == nil {
		t.Fatal("an expired invoice was accepted for payment")
	}
}

func TestPaymentExpiryCapsAHostileInvoice(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	order := lightningOrder(created, synthInvoice(t, created, 365*24*3600))

	got := PaymentExpiryFor(order, created)
	if want := created.Add(PaymentMaxWindow); !got.Equal(want) {
		t.Fatalf("deadline = %s, want the %s cap", got, PaymentMaxWindow)
	}
}

func TestPaymentExpiryFallsBackWhenThereIsNoInvoice(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	order := &Order{ID: "ord_test", Status: "WaitingForPayment", Coin: "USDT",
		CoinAmount: "25.00", WalletAddress: "0x0000000000000000000000000000000000000000", CreatedAt: epochOrString(strconv.FormatInt(created.Unix(), 10))}

	got := PaymentExpiryFor(order, created)
	if want := created.Add(PaymentWindow - PaymentSafetyBuffer); !got.Equal(want) {
		t.Fatalf("deadline = %s, want the documented fallback %s", got, want)
	}
}

func TestPaymentExpiryNeverStartsTheClockInTheFuture(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(10 * time.Minute)
	order := &Order{ID: "ord_test", Status: "WaitingForPayment", Coin: "USDT", CoinAmount: "25.00",
		WalletAddress: "0x0000000000000000000000000000000000000000", CreatedAt: epochOrString(strconv.FormatInt(future.Unix(), 10))}

	got := PaymentExpiryFor(order, now)
	if got.Before(now.Add(PaymentWindow - PaymentSafetyBuffer - time.Second)) {
		t.Fatalf("deadline = %s collapsed because the supplier clock ran ahead", got)
	}
}
