package nimiq

import (
	"encoding/base32"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Nimiq addresses are user-friendly IBAN-style strings, e.g.
// "NQ07 0000 0000 0000 0000 0000 0000 0000 0000".
// Format: "NQ" + 2-digit ISO 7064 mod-97-10 check digits + base32
// (RFC4648, no padding) encoding of the 20-byte hash.
// Reference: https://github.com/nimiq/core-js/blob/master/src/main/generic/consensus/base/account/Address.js

// Nimiq's base32 alphabet deliberately omits I, O, W and Z to avoid visual
// ambiguity, leaving exactly the 32 symbols base32 requires. (A 36-symbol
// alphabet makes base32.NewEncoding panic at init.)
const nimiqAlphabet = "0123456789ABCDEFGHJKLMNPQRSTUVXY"

var base32Enc = base32.NewEncoding(nimiqAlphabet).WithPadding(base32.NoPadding)

// AddressFromPublicKey derives the user-friendly Nimiq address string from a
// raw 32-byte Ed25519 public key (blake2b(pubkey)[0:20], base32-encoded, with
// an ISO7064 mod97-10 check).
func AddressFromPublicKey(pubKey []byte) (string, error) {
	if len(pubKey) != 32 {
		return "", fmt.Errorf("public key must be 32 bytes, got %d", len(pubKey))
	}
	// Nimiq uses blake2b for the address hash; sha256 is NOT correct here,
	// so this file depends on the blake2b hash computed by HashPublicKey.
	hash := HashPublicKey(pubKey)
	base32Str := base32Enc.EncodeToString(hash)
	check := iso7064Check("NQ00" + base32Str)
	return fmt.Sprintf("NQ%s %s", check, spaced(base32Str)), nil
}

// NormalizeAddress strips spaces/case so "NQ07 0000..." and "nq070000..." compare equal.
func NormalizeAddress(addr string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(addr), " ", ""))
}

// ValidateAddress checks the ISO7064 check digits on a user-friendly address string.
func ValidateAddress(addr string) error {
	norm := NormalizeAddress(addr)
	if len(norm) != 36 || !strings.HasPrefix(norm, "NQ") {
		return fmt.Errorf("malformed nimiq address")
	}
	check := norm[2:4]
	body := norm[4:]
	expected := iso7064Check("NQ00" + body)
	if expected != check {
		return fmt.Errorf("invalid address checksum")
	}
	return nil
}

func spaced(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		end := i + 4
		if end > len(s) {
			end = len(s)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:end])
	}
	return b.String()
}

// iso7064Check implements the ISO 7064 mod 97-10 check used by IBAN-style
// identifiers (rearrange, map letters to digits, mod 97, 98-remainder).
func iso7064Check(rearranged string) string {
	// rearranged is e.g. "NQ00" + base32body; IBAN algorithm moves the first
	// 4 chars to the end before the numeric conversion.
	moved := rearranged[4:] + rearranged[:4]
	var numeric strings.Builder
	for _, c := range moved {
		switch {
		case c >= '0' && c <= '9':
			numeric.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			numeric.WriteString(fmt.Sprintf("%d", c-'A'+10))
		}
	}
	remainder := mod97(numeric.String())
	check := 98 - remainder
	return fmt.Sprintf("%02d", check)
}

func mod97(numeric string) int {
	remainder := 0
	for _, c := range numeric {
		remainder = (remainder*10 + int(c-'0')) % 97
	}
	return remainder
}

// HashPublicKey returns the first 20 bytes of blake2b-256(pubKey), which is
// the account identifier Nimiq addresses are derived from.
func HashPublicKey(pubKey []byte) []byte {
	h := blake2b.Sum256(pubKey)
	return h[:20]
}
