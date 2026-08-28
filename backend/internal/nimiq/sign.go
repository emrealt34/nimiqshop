package nimiq

// ⚠️  WARNING — UNTESTED CRYPTO, READ ME  ⚠️
//
// This file builds and signs a Nimiq "basic" transaction for the 1-Luna
// notification system. It implements the basic-transaction scheme to the best
// of the public Nimiq spec (Ed25519 signatures over a Blake2b-256 hash), but it
// could NOT be exercised in this environment — there is no funded Nimiq key or
// node here, only a read-only RPC. BEFORE enabling notifications in production
// you MUST validate a real send with the standalone tool:
//
//   go run ./cmd/notif-test -to <friendly-address> -memo "test" \
//       -key <hex-private-key> -rpc <your-rpc>
//
// and confirm the 1-Luna + memo lands. If the network rejects the tx, the
// exact serialization below (field order / network-id placement / proof layout)
// is the place to adjust; the surrounding pipeline (idempotency, worker hook,
// broadcast RPC) is independent of those details.

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Nimiq network ids.
const (
	NetworkTestnet = 1
	NetworkMainnet = 42
)

// NimiqBase32Alphabet is the custom alphabet Nimiq uses for user-friendly
// addresses (no B, I, O, Z to avoid look-alikes).
const nimiqBase32Alphabet = "0123456789ABCDEFGHJKLMNPQRSTUVWXY"

func nimiqBase32Decode(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		return r
	}, s)
	s = strings.ReplaceAll(s, " ", "")
	out := make([]byte, 0, len(s)*5/8)
	var buf uint64
	var bits uint
	for _, c := range s {
		idx := strings.IndexRune(nimiqBase32Alphabet, c)
		if idx < 0 {
			return nil, errors.New("nimiq: invalid base32 char")
		}
		buf = (buf << 5) | uint64(idx)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(buf>>bits))
			buf &= (1 << bits) - 1
		}
	}
	return out, nil
}

// DecodeUserFriendlyAddress turns a "NQ07 0000 …" address into its 20-byte
// account hash. The real Nimiq format (see core-js Address and address.go in
// this package): "NQ" + ISO 7064 mod-97-10 check digits + base32 (nimiq
// alphabet, 32 symbols) of the 20-byte hash. The check digits are verified
// before decoding, so a mistyped/paste-mangled refund destination is rejected
// BEFORE any transaction is built — refunds can never go to a wrong address
// because of bad input handling.
func DecodeUserFriendlyAddress(addr string) ([]byte, error) {
	norm := NormalizeAddress(addr)
	if !strings.HasPrefix(norm, "NQ") {
		return nil, errors.New("nimiq: address must start with NQ")
	}
	body := norm[4:]
	if len(body) != 32 {
		return nil, errors.New("nimiq: address has wrong length")
	}
	if err := ValidateAddress(norm); err != nil {
		return nil, err
	}
	hash := make([]byte, 20)
	n, err := base32Enc.Decode(hash, []byte(body))
	if err != nil || n != 20 {
		return nil, errors.New("nimiq: failed to decode address body")
	}
	return hash, nil
}

func checksum(b []byte) uint32 {
	// CCITT-16 over the bytes, then packed into a uint32 the way Nimiq does
	// (two 16-bit halves). See notif-test caveat above — validate on your node.
	crc := uint16(0xFFFF)
	for _, x := range b {
		crc ^= uint16(x) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return uint32(crc)
}

// BuildBasicTransaction builds, signs and hex-encodes a Nimiq basic transaction
// carrying `valueLuna` + `memo` from the key derived from `seedHex` (32-byte
// Ed25519 seed) to `recipientFriendly`.
func BuildBasicTransaction(seedHex, recipientFriendly string, valueLuna, feeLuna int64, validityStartHeight uint32, networkID byte, memo []byte) (string, error) {
	seed, err := hex.DecodeString(strings.TrimPrefix(seedHex, "0x"))
	if err != nil || len(seed) != 32 {
		return "", errors.New("nimiq: private key must be a 32-byte hex seed")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey) // 32 bytes

	recipient, err := DecodeUserFriendlyAddress(recipientFriendly)
	if err != nil {
		return "", err
	}

	// Content that is hashed + signed (signature excluded).
	content := make([]byte, 0, 1+32+20+8+8+4+1+len(memo))
	content = append(content, networkID)
	content = append(content, pub...)       // sender public key
	content = append(content, recipient...) // recipient hash (20)
	content = binary.LittleEndian.AppendUint64(content, uint64(valueLuna))
	content = binary.LittleEndian.AppendUint64(content, uint64(feeLuna))
	content = binary.LittleEndian.AppendUint32(content, validityStartHeight)
	content = append(content, memo...) // recipient data (the notification text)

	h, _ := blake2b.New256(nil)
	h.Write(content)
	digest := h.Sum(nil)

	sig := ed25519.Sign(priv, digest)

	// Wire serialization = content || signature. (Field order per the caveat
	// above — validate with notif-test against your node before enabling.)
	tx := make([]byte, 0, len(content)+len(sig))
	tx = append(tx, content...)
	tx = append(tx, sig...)
	return hex.EncodeToString(tx), nil
}
