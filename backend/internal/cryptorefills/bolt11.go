package cryptorefills

import (
	"time"
)

/* BOLT-11 invoice window decoding.
 *
 * A BOLT-11 Lightning invoice embeds, in its own bech32 payload:
 *   - a 35-bit creation timestamp (unix seconds) right after the "1"
 *   - tagged fields, including tag 'x' (type 6) = expiry in seconds
 *     (the BOLT-11 default when 'x' is absent is 3600s = 1 hour)
 *
 * The supplier creates the invoice at order time, so the invoice's own
 * "created + expiry" is the deadline the Lightning network will actually
 * enforce. Decoding it lets the shop NEVER advertise more payment time than
 * the invoice will accept (belt-and-suspenders on top of the documented
 * 30-minute window). We only need the window, not the signature, so a
 * checksum-valid decode of timestamp + 'x' is enough. */

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Rev = func() [128]int8 {
	var rev [128]int8
	for i := range rev {
		rev[i] = -1
	}
	for i, c := range bech32Charset {
		rev[c] = int8(i)
	}
	return rev
}()

func bech32Polymod(values []int32) uint32 {
	generators := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i, g := range generators {
			if (top>>uint(i))&1 == 1 {
				chk ^= g
			}
		}
	}
	return chk
}

func bech32HrpExpand(hrp string) []int32 {
	out := make([]int32, 0, len(hrp)*2+1)
	for _, c := range hrp {
		out = append(out, c>>5)
	}
	out = append(out, 0)
	for _, c := range hrp {
		out = append(out, c&31)
	}
	return out
}

// bech32VerifyChecksum validates the BOLT-11 bech32 checksum (constant 1,
// NOT bech32m) over hrp + data.
func bech32VerifyChecksum(hrp string, data []int8) bool {
	values := bech32HrpExpand(hrp)
	for _, d := range data {
		values = append(values, int32(d))
	}
	return bech32Polymod(values) == 1
}

// ParseBOLT11Window decodes a BOLT-11 invoice and returns the absolute window
// in which it is payable: validFrom = the invoice's embedded creation
// timestamp, expiresAt = validFrom + the invoice's expiry (3600s default when
// the 'x' field is absent). ok=false when the string is not a valid bech32
// BOLT-11 invoice (e.g. a test/mock string) — callers fall back to the
// documented window.
func ParseBOLT11Window(invoice string) (validFrom, expiresAt time.Time, ok bool) {
	info, err := DecodeInvoice(invoice)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return info.CreatedAt, info.ExpiresAt, true
}

/* Locally-advertised payment window ----------------------------------------
 *
 * PaymentWindow is the supplier-documented Lightning payment window (from
 * api.cryptorefills.com docs; the API does not return an explicit expiry).
 * PaymentSafetyBuffer shortens the window the shop advertises so buyers stop
 * paying before the true deadline — absorbing device-clock skew and
 * last-seconds races. Late payments the supplier still accepts are completed
 * by the verified webhook/poll ("expired -> fulfilled" is an allowed state
 * transition), so the buffer only ever releases the daily-limit slot a few
 * minutes early; it can never lose a paid order. */

const (
	// PaymentWindow is the supplier-documented Lightning payment window.
	PaymentWindow = 30 * time.Minute
	// PaymentSafetyBuffer is subtracted from the advertised window.
	PaymentSafetyBuffer = 5 * time.Minute
)

// PaymentExpiryFor computes the locally-advertised payment deadline for a
// supplier Lightning order:
//
//	deadline = min(orderCreatedAt + PaymentWindow, bolt11ValidFrom + bolt11Expiry) - PaymentSafetyBuffer
//
// The window is measured from the SUPPLIER's own timestamps (the order's
// created_at/updated_at echoed back by api.cryptorefills.com), never the
// customer's device clock. When the BOLT-11 invoice cannot be decoded the
// documented window minus the buffer is used. The result is clamped so it
// never lies in the past relative to `now`.
func PaymentExpiryFor(order *Order, now time.Time) time.Time {
	// Never restart an old invoice's clock from updated_at or from a retry.
	if order == nil {
		return now
	}
	base := order.CreatedTime()
	validFrom, invoiceEnd, decoded := ParseBOLT11Window(order.WalletAddress)
	if decoded && (base.IsZero() || validFrom.Before(base)) {
		base = validFrom
	}
	if base.IsZero() || base.After(now) {
		base = now
	}
	windowEnd := base.Add(PaymentWindow)
	if decoded && invoiceEnd.Before(windowEnd) {
		windowEnd = invoiceEnd
	}

	return windowEnd.Add(-PaymentSafetyBuffer)
}
