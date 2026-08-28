package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238's interoperable default HMAC algorithm.
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// These limits reject malformed or deliberately expensive PHC strings
	// before Argon2 allocates memory for an untrusted hash.
	minArgonMemoryKiB uint32 = 16 * 1024
	maxArgonMemoryKiB uint32 = 512 * 1024
	maxArgonTime      uint32 = 10
	maxArgonParallel  uint8  = 16
)

type argon2idParams struct {
	memory      uint32
	time        uint32
	parallelism uint8
	salt        []byte
	hash        []byte
}

// ValidateArgon2idPHC validates the production password-hash format without
// accepting parameters that could turn a login request into a memory DoS.
func ValidateArgon2idPHC(encoded string) error {
	_, err := parseArgon2idPHC(encoded)
	return err
}

// VerifyArgon2idPassword verifies a standard Argon2id PHC password hash in
// constant time. Plaintext passwords are caller-owned and never persisted.
func VerifyArgon2idPassword(encoded, password string) bool {
	params, err := parseArgon2idPHC(encoded)
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), params.salt, params.time, params.memory, params.parallelism, uint32(len(params.hash)))
	return subtle.ConstantTimeCompare(actual, params.hash) == 1
}

// HashArgon2idPassword is supplied for a one-time, trusted bootstrap utility
// or tests. Production deployments should generate the PHC value offline and
// inject only that value as ADMIN_PASSWORD_HASH.
func HashArgon2idPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password cannot be empty")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("random salt: %w", err)
	}
	const memory uint32 = 64 * 1024
	const iterations uint32 = 3
	const parallelism uint8 = 2
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func parseArgon2idPHC(encoded string) (argon2idParams, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return argon2idParams{}, fmt.Errorf("invalid Argon2id PHC format")
	}

	var memory, iterations uint64
	var parallelism uint64
	for _, field := range strings.Split(parts[3], ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return argon2idParams{}, fmt.Errorf("invalid Argon2id parameter")
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return argon2idParams{}, fmt.Errorf("invalid Argon2id parameter")
		}
		switch key {
		case "m":
			memory = parsed
		case "t":
			iterations = parsed
		case "p":
			parallelism = parsed
		default:
			return argon2idParams{}, fmt.Errorf("unsupported Argon2id parameter")
		}
	}
	if memory < uint64(minArgonMemoryKiB) || memory > uint64(maxArgonMemoryKiB) ||
		iterations < 1 || iterations > uint64(maxArgonTime) || parallelism < 1 || parallelism > uint64(maxArgonParallel) {
		return argon2idParams{}, fmt.Errorf("unsafe Argon2id parameters")
	}

	decode := func(v string) ([]byte, error) {
		if out, err := base64.RawStdEncoding.DecodeString(v); err == nil {
			return out, nil
		}
		return base64.StdEncoding.DecodeString(v)
	}
	salt, err := decode(parts[4])
	if err != nil || len(salt) < 16 {
		return argon2idParams{}, fmt.Errorf("invalid Argon2id salt")
	}
	hash, err := decode(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 128 {
		return argon2idParams{}, fmt.Errorf("invalid Argon2id hash")
	}
	return argon2idParams{memory: uint32(memory), time: uint32(iterations), parallelism: uint8(parallelism), salt: salt, hash: hash}, nil
}

// ValidateTOTPSecret checks the unpadded/padded base32 secret typically
// emitted by authenticator applications. Secrets are normalized at use time
// so spaces and lowercase setup strings remain usable.
func ValidateTOTPSecret(secret string) error {
	_, err := decodeTOTPSecret(secret)
	return err
}

// VerifyTOTP validates a six-digit RFC 6238 code. A single 30-second window
// on either side tolerates normal device clock drift without accepting a
// broad replay window.
func VerifyTOTP(secret, code string, now time.Time) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false
	}
	for _, offset := range []int64{-1, 0, 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, now.Unix()/30+offset)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""), "-", ""))
	normalized = strings.TrimRight(normalized, "=")
	if len(normalized) < 16 {
		return nil, fmt.Errorf("TOTP secret too short")
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil || len(key) < 10 {
		return nil, fmt.Errorf("invalid TOTP secret")
	}
	return key, nil
}

func totpCode(key []byte, counter int64) string {
	var message [8]byte
	for i := 7; i >= 0; i-- {
		message[i] = byte(counter)
		counter >>= 8
	}
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 0x0f)
	value := (int(sum[offset])&0x7f)<<24 | int(sum[offset+1])<<16 | int(sum[offset+2])<<8 | int(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}
