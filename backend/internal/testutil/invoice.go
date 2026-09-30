// Package testutil contains OFFLINE fixtures. These invoices are unsigned and
// cannot be paid. Never use them as a replacement for a supplier response.
package testutil

import (
	"crypto/sha256"
	"math/big"
	"strings"
	"time"
)

func Invoice(btc, unique string, created time.Time) string {
	r, ok := new(big.Rat).SetString(btc)
	if !ok {
		panic("bad fixture amount")
	}
	r.Mul(r, big.NewRat(1_000_000_000_000, 1))
	if !r.IsInt() {
		panic("fixture amount below picoBTC")
	}
	hrp := "lnbc" + r.Num().String() + "p"
	data := make([]byte, 7)
	ts := created.Unix()
	for i := 6; i >= 0; i-- {
		data[i] = byte(ts & 31)
		ts >>= 5
	}
	h := sha256.Sum256([]byte(unique))
	hash := make([]byte, 0, 52)
	var acc uint32
	var bits uint
	for _, b := range h {
		acc = (acc << 8) | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			hash = append(hash, byte(acc>>bits)&31)
		}
	}
	if bits > 0 {
		hash = append(hash, byte(acc<<(5-bits))&31)
	}
	data = append(data, 1, 1, 20)
	data = append(data, hash...)              // p + length 52
	data = append(data, 6, 0, 3, 1, 24, 8)    // x = 1800 seconds
	data = append(data, make([]byte, 104)...) // zero signature: intentionally UNPAYABLE
	values := make([]byte, 0, 2*len(hrp)+1+len(data)+6)
	for _, c := range hrp {
		values = append(values, byte(c>>5))
	}
	values = append(values, 0)
	for _, c := range hrp {
		values = append(values, byte(c)&31)
	}
	values = append(values, data...)
	values = append(values, make([]byte, 6)...)
	chk := uint32(1)
	generators := []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i, g := range generators {
			if (top>>i)&1 != 0 {
				chk ^= g
			}
		}
	}
	chk ^= 1
	for i := 5; i >= 0; i-- {
		data = append(data, byte(chk>>uint(i*5))&31)
	}
	const alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, v := range data {
		b.WriteByte(alphabet[v])
	}
	return b.String()
}
