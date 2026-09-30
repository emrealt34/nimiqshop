package nimiq

// BuildBasicTransaction implements the Nimiq Albatross wire format used by the
// proven, mainnet-tested implementation in github.com/nimjump/game
// (backend/game/nimiq.go → nimiqBuildAndSignTx). The previous version in this
// file was never exercised against a real node (old Nimiq 1.0 layout,
// little-endian, network ids 42/1) and every transaction it produced was
// rejected. This file is a line-for-line port of the working game code:
//
//   network_id:        24 = mainnet, 5 = testnet (Albatross)
//   encodings:         big-endian for value/fee/validity_start_height
//   signature payload: recipient_data_len(2be) | recipient_data | sender(20) |
//                      sender_type(1) | recipient(20) | recipient_type(1) |
//                      value(8be) | fee(8be) | validity_start_height(4be) |
//                      network_id(1) | flags(1) | sender_data ULEB128(0)
//   wire (memo set → Extended tx):
//                      variant(0x01) | sender(20) | sender_type(0) |
//                      sender_data_len ULEB128(0) | recipient(20) |
//                      recipient_type(0) | recipient_data_len ULEB128 |
//                      recipient_data | value(8be) | fee(8be) |
//                      validity(4be) | network_id(1) | flags(0) |
//                      proof_len ULEB128 | proof{ proof_type(0x00) |
//                      pubkey(32) | merkle_path_len ULEB128(0) | sig(64) }
//   wire (no memo → Basic tx, 139 bytes):
//                      variant(0x00) | proof_type(0x00) | pubkey(32) |
//                      recipient(20) | value(8be) | fee(8be) |
//                      validity(4be) | network_id(1) | signature(64)
//
// Validated live on rpc.nimiqwatch.com / rpc.testnet.nimiqwatch.com.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"nimiqshop/internal/xcrypto/blake2b"
)

// Nimiq Albatross network ids (same values as the game backend).
const (
	NetworkTestnet = 5
	NetworkMainnet = 24
)

// validityStartBuffer is added to the current head height so the tx stays
// valid while a few blocks are produced between signing and broadcast
// (Albatross validity window is ~120 blocks, blocks ~1s).
const validityStartBuffer uint32 = 5

// MaxMemoLen is the maximum recipient_data length the game implementation
// allows (and Nimiq basic/extended txs accept for extra_data).
const MaxMemoLen = 64

// decodeSigningKey accepts a 32-byte hex seed OR a 64-byte hex Ed25519
// private key (seed || public key), exactly like the game backend.
func decodeSigningKey(hexKey string) (ed25519.PrivateKey, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(hexKey), "0x"))
	if err != nil {
		return nil, errors.New("nimiq: private key is not valid hex")
	}
	switch len(raw) {
	case 32:
		return ed25519.NewKeyFromSeed(raw), nil
	case 64:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, errors.New("nimiq: private key must be a 32-byte seed or 64-byte hex key")
	}
}

// AddressFromKeyHex derives the user-friendly Nimiq address of a hex seed /
// private key. Used to display the shop cashback wallet and its balance.
func AddressFromKeyHex(hexKey string) (string, error) {
	priv, err := decodeSigningKey(hexKey)
	if err != nil {
		return "", err
	}
	return AddressFromPublicKey(priv.Public().(ed25519.PublicKey))
}

// AddressFromSeed derives the user-friendly Nimiq address of a 32-byte hex
// seed (or 64-byte hex private key). Kept for existing callers.
func AddressFromSeed(seedHex string) (string, error) { return AddressFromKeyHex(seedHex) }

// uleb128 encodes n as ULEB128 (postcard/protobuf variable-length uint).
func uleb128(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7F)
		n >>= 7
		if n != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return out
}

// BuildBasicTransaction builds, signs and hex-encodes a Nimiq Albatross
// transaction carrying valueLuna + feeLuna + memo from the key in hexKey to
// recipientFriendly. currentHeight is the head height at signing time; the
// validity start is currentHeight + 5 (same buffer as the game backend).
//
// It returns the hex-encoded wire bytes AND the transaction's canonical hash
// (Blake2b-256 of SerializeContent — the exact bytes signed, which is what a
// Nimiq node reports as the tx hash). Returning the hash here lets a caller
// persist it BEFORE broadcasting, so a crash between "sent" and "hash saved"
// can still confirm the payout by hash instead of re-sending blind.
func BuildBasicTransaction(hexKey, recipientFriendly string, valueLuna, feeLuna int64, currentHeight uint32, networkID byte, memo []byte) (txHex string, txHash string, err error) {
	priv, err := decodeSigningKey(hexKey)
	if err != nil {
		return "", "", err
	}
	pub := priv.Public().(ed25519.PublicKey) // 32 bytes

	sender, err := AddressFromPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	senderBytes, err := DecodeUserFriendlyAddress(sender)
	if err != nil {
		return "", "", err
	}
	recipient, err := DecodeUserFriendlyAddress(recipientFriendly)
	if err != nil {
		return "", "", err
	}

	memoBytes := memo
	if len(memoBytes) > MaxMemoLen {
		memoBytes = memoBytes[:MaxMemoLen]
	}

	validityStart := currentHeight + validityStartBuffer

	// SerializeContent — same layout for Basic and Extended:
	// recipient_data_len(2be) | recipient_data | sender(20) | sender_type(1) |
	// recipient(20) | recipient_type(1) | value(8be) | fee(8be) |
	// validity_start_height(4be) | network_id(1) | flags(1) | sender_data ULEB128(0)
	var content bytes.Buffer
	_ = binary.Write(&content, binary.BigEndian, uint16(len(memoBytes))) // recipient_data_len
	content.Write(memoBytes)                                             // recipient_data
	content.Write(senderBytes)                                           // sender 20 bytes
	content.WriteByte(0)                                                 // sender_type = Basic
	content.Write(recipient)                                             // recipient 20 bytes
	content.WriteByte(0)                                                 // recipient_type = Basic
	_ = binary.Write(&content, binary.BigEndian, uint64(valueLuna))      // value 8 bytes BE
	_ = binary.Write(&content, binary.BigEndian, uint64(feeLuna))        // fee 8 bytes BE
	_ = binary.Write(&content, binary.BigEndian, validityStart)          // validity 4 bytes BE
	content.WriteByte(networkID)                                         // network_id
	content.WriteByte(0)                                                 // flags = 0
	content.WriteByte(0x00)                                              // sender_data = ULEB128(0)

	sig := ed25519.Sign(priv, content.Bytes())

	// Canonical Nimiq tx hash = Blake2b-256 of SerializeContent (verified live
	// to equal the hash the node returns from sendRawTransaction).
	ch, _ := blake2b.New256(nil)
	ch.Write(content.Bytes())
	txHash = hex.EncodeToString(ch.Sum(nil))

	var tx bytes.Buffer
	if len(memoBytes) == 0 {
		// Basic tx — compact format (139 bytes)
		tx.WriteByte(0x00)  // enum variant = Basic
		tx.WriteByte(0x00)  // proof_type_and_flags = Ed25519
		tx.Write(pub)       // pubkey 32 bytes
		tx.Write(recipient) // recipient 20 bytes
		_ = binary.Write(&tx, binary.BigEndian, uint64(valueLuna))
		_ = binary.Write(&tx, binary.BigEndian, uint64(feeLuna))
		_ = binary.Write(&tx, binary.BigEndian, validityStart)
		tx.WriteByte(networkID)
		tx.Write(sig) // signature 64 bytes
	} else {
		// Extended tx — carries recipient_data (the memo)
		// proof = proof_type(0x00) | pubkey(32) | merkle_path_len ULEB128(0) | sig(64)
		proof := make([]byte, 0, 1+len(pub)+1+len(sig))
		proof = append(proof, 0x00)
		proof = append(proof, pub...)
		proof = append(proof, 0x00) // merkle_path_len ULEB128(0)
		proof = append(proof, sig...)

		tx.WriteByte(0x01) // enum variant = Extended
		tx.Write(senderBytes)
		tx.WriteByte(0x00) // sender_type = Basic
		tx.Write(uleb128(0))
		tx.Write(recipient)
		tx.WriteByte(0x00) // recipient_type = Basic
		tx.Write(uleb128(len(memoBytes)))
		tx.Write(memoBytes)
		_ = binary.Write(&tx, binary.BigEndian, uint64(valueLuna))
		_ = binary.Write(&tx, binary.BigEndian, uint64(feeLuna))
		_ = binary.Write(&tx, binary.BigEndian, validityStart)
		tx.WriteByte(networkID)
		tx.WriteByte(0x00) // flags = 0
		tx.Write(uleb128(len(proof)))
		tx.Write(proof)
	}

	return hex.EncodeToString(tx.Bytes()), txHash, nil
}

// DecodeUserFriendlyAddress turns a "NQ07 0000 …" address into its 20-byte
// account hash. The ISO 7064 mod-97-10 check digits are verified before
// decoding, so a mistyped/paste-mangled refund destination is rejected
// BEFORE any transaction is built — refunds can never go to a wrong address
// because of bad input handling.
func DecodeUserFriendlyAddress(addr string) ([]byte, error) {
	norm := NormalizeAddress(addr)
	if !strings.HasPrefix(norm, "NQ") {
		return nil, errors.New("nimiq: address must start with NQ")
	}
	body := norm[4:]
	if len(body) != 32 {
		return nil, fmt.Errorf("nimiq: address body must be 32 base32 chars, got %d", len(body))
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

// SignedTxHash is a local blake2b-256 of the signed wire bytes, used only as
// a fallback lookup key when the RPC accepted the tx but did not echo a
// hash (crash between send and persist). Paid still requires RPC confirmation.
func SignedTxHash(txHex string) string {
	raw, err := hex.DecodeString(strings.TrimSpace(txHex))
	if err != nil || len(raw) == 0 {
		return ""
	}
	h, _ := blake2b.New256(nil)
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
