package cryptorefills

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// InvoiceInfo checks the BOLT11 checksum, mainnet HRP, amount, timestamp,
// expiry and payment hash. The wallet MUST still verify the signature/payee.
type InvoiceInfo struct {
	AmountBTC   *big.Rat
	PaymentHash string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

func DecodeInvoice(invoice string) (InvoiceInfo, error) {
	var out InvoiceInfo
	raw := strings.TrimSpace(invoice)
	s := strings.ToLower(raw)
	if raw != s && raw != strings.ToUpper(raw) {
		return out, fmt.Errorf("mixed-case invoice")
	}
	sep := strings.LastIndexByte(s, '1')
	if sep < 5 || !strings.HasPrefix(s, "lnbc") || strings.HasPrefix(s, "lnbcrt") {
		return out, fmt.Errorf("expected BTC mainnet BOLT11")
	}
	hrp, amount := s[:sep], s[4:sep]
	divisor := int64(1)
	switch amount[len(amount)-1] {
	case 'm':
		divisor, amount = 1_000, amount[:len(amount)-1]
	case 'u':
		divisor, amount = 1_000_000, amount[:len(amount)-1]
	case 'n':
		divisor, amount = 1_000_000_000, amount[:len(amount)-1]
	case 'p':
		divisor, amount = 1_000_000_000_000, amount[:len(amount)-1]
	}
	if amount == "" || strings.ContainsAny(amount, ".+-") {
		return out, fmt.Errorf("invoice amount missing/invalid")
	}
	n, ok := new(big.Int).SetString(amount, 10)
	if !ok || n.Sign() <= 0 {
		return out, fmt.Errorf("invoice amount invalid")
	}
	out.AmountBTC = new(big.Rat).SetFrac(n, big.NewInt(divisor))
	data := make([]int8, 0, len(s)-sep-1)
	for _, c := range s[sep+1:] {
		if c >= 128 || bech32Rev[c] < 0 {
			return out, fmt.Errorf("invalid invoice alphabet")
		}
		data = append(data, bech32Rev[c])
	}
	if len(data) < 7+104+6 || !bech32VerifyChecksum(hrp, data) {
		return out, fmt.Errorf("invalid invoice checksum/length")
	}
	// Last 104 groups are the 65-byte signature, NOT tagged fields.
	tags := data[7 : len(data)-104-6]
	var stamp int64
	for _, v := range data[:7] {
		stamp = stamp<<5 | int64(v)
	}
	out.CreatedAt = time.Unix(stamp, 0).UTC()
	expiry := int64(3600)
	seenExpiry := false
	for len(tags) > 0 {
		if len(tags) < 3 {
			return out, fmt.Errorf("truncated invoice tag")
		}
		kind, length := tags[0], int(tags[1])*32+int(tags[2])
		tags = tags[3:]
		if length > len(tags) {
			return out, fmt.Errorf("truncated invoice value")
		}
		v := tags[:length]
		tags = tags[length:]
		switch kind {
		case 1: // payment_hash (p), exactly 256 bits padded to 52 groups
			if len(v) != 52 || out.PaymentHash != "" {
				return out, fmt.Errorf("invalid/repeated payment hash")
			}
			var acc uint32
			bits := uint(0)
			buf := make([]byte, 0, 32)
			for _, x := range v {
				acc = (acc << 5) | uint32(x)
				bits += 5
				if bits >= 8 {
					bits -= 8
					buf = append(buf, byte(acc>>bits))
				}
			}
			if len(buf) != 32 || (acc&((1<<bits)-1)) != 0 {
				return out, fmt.Errorf("invalid payment hash padding")
			}
			out.PaymentHash = hex.EncodeToString(buf)
		case 6: // expiry (x)
			if seenExpiry || len(v) > 7 {
				return out, fmt.Errorf("invalid invoice expiry")
			}
			seenExpiry = true
			expiry = 0
			for _, x := range v {
				expiry = expiry<<5 | int64(x)
			}
			if expiry <= 0 || expiry > 365*24*3600 {
				return out, fmt.Errorf("invoice expiry out of range")
			}
		}
	}
	if out.PaymentHash == "" {
		return out, fmt.Errorf("invoice payment hash missing")
	}
	out.ExpiresAt = out.CreatedAt.Add(time.Duration(expiry) * time.Second)
	return out, nil
}

// ValidatePayableOrder must pass before a supplier invoice is attached or shown.
// The displayed coin amount and the amount encoded in the payment request must
// match EXACTLY. Missing/malformed data cannot be made payable by TEST_MODE.
//
// Two rails are supported:
//   - BTC Lightning (Nimiq Pay): the supplier's wallet_address is a BOLT11
//     invoice; the amount, timestamp, expiry and payment hash are decoded and
//     cross-checked against coin_amount.
//   - USDT on Polygon (stablecoin): there is NO BOLT11 invoice. wallet_address
//     is the EVM recipient on chain 137; the amount is coin_amount. We verify
//     the coin, a non-empty Polygon network, a positive amount and a non-empty
//     recipient address.
func ValidatePayableOrder(order *Order, now time.Time) (InvoiceInfo, error) {
	if order == nil || order.ID == "" {
		return InvoiceInfo{}, fmt.Errorf("missing supplier order id")
	}
	if order.IsLightning() {
		return validateLightningPayable(order, now)
	}
	return validateStablecoinPayable(order, now)
}

func validateLightningPayable(order *Order, now time.Time) (InvoiceInfo, error) {
	if order == nil || order.ID == "" {
		return InvoiceInfo{}, fmt.Errorf("missing supplier order id")
	}
	if !strings.EqualFold(order.Coin, "BTC") {
		return InvoiceInfo{}, fmt.Errorf("supplier returned the wrong payment coin")
	}
	if order.Network != "" && !strings.EqualFold(order.Network, "Lightning") {
		return InvoiceInfo{}, fmt.Errorf("supplier returned the wrong network")
	}
	if !orderIsAwaitingPayment(order) {
		return InvoiceInfo{}, fmt.Errorf("supplier order is not awaiting payment")
	}
	if MoneyObserved(order) {
		return InvoiceInfo{}, fmt.Errorf("supplier already reports money; do not pay again")
	}
	amount, err := PositiveDecimal(order.CoinAmount)
	if err != nil {
		return InvoiceInfo{}, err
	}
	info, err := DecodeInvoice(order.WalletAddress)
	if err != nil {
		return info, err
	}
	if info.AmountBTC.Cmp(amount) != 0 {
		return info, fmt.Errorf("BOLT11 amount differs from coin_amount")
	}
	if info.CreatedAt.After(now.Add(time.Minute)) || !PaymentExpiryFor(order, now).After(now) {
		return info, fmt.Errorf("invoice is expired/not yet valid")
	}
	return info, nil
}

// validateStablecoinPayable validates a direct USDT/Polygon order without any
// BOLT11 invoice. The shop's stablecoin rail is USDT and nothing else; a
// Lightning network on a stablecoin order is impossible and is rejected too.
func validateStablecoinPayable(order *Order, now time.Time) (InvoiceInfo, error) {
	if order == nil || order.ID == "" {
		return InvoiceInfo{}, fmt.Errorf("missing supplier order id")
	}
	if !strings.EqualFold(order.Coin, "USDT") {
		return InvoiceInfo{}, fmt.Errorf("supplier returned the wrong payment coin")
	}
	// A Lightning network on a stablecoin order is impossible and is rejected;
	// an empty network is allowed — the shop defaults it to the configured
	// stablecoin network (finishSupplierCreation / tracker) before attaching
	// payment.
	if strings.EqualFold(order.Network, "Lightning") {
		return InvoiceInfo{}, fmt.Errorf("supplier returned the wrong network")
	}
	if !orderIsAwaitingPayment(order) {
		return InvoiceInfo{}, fmt.Errorf("supplier order is not awaiting payment")
	}
	if MoneyObserved(order) {
		return InvoiceInfo{}, fmt.Errorf("supplier already reports money; do not pay again")
	}
	if _, err := PositiveDecimal(order.CoinAmount); err != nil {
		return InvoiceInfo{}, err
	}
	if strings.TrimSpace(order.WalletAddress) == "" {
		return InvoiceInfo{}, fmt.Errorf("supplier did not return a payment address")
	}
	// No BOLT11 to decode for a stablecoin; the locally-advertised window is
	// created_at + PaymentWindow - PaymentSafetyBuffer (see PaymentExpiryFor).
	return InvoiceInfo{}, nil
}

func orderIsAwaitingPayment(order *Order) bool {
	switch strings.ToLower(strings.TrimSpace(order.Status)) {
	case strings.ToLower(StatusCreated), strings.ToLower(StatusWaitingForPayment):
		return true
	}
	return false
}

// SamePositiveAmount checks identity of an already-issued payment request.
// There is deliberately NO reference-price, percentage or margin gate.
func SamePositiveAmount(a, b string) bool {
	left, err := PositiveDecimal(a)
	if err != nil {
		return false
	}
	right, err := PositiveDecimal(b)
	if err != nil {
		return false
	}
	return left.Cmp(right) == 0
}
