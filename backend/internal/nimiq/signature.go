package nimiq

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
)

// nimiqSignedMessagePrefix mirrors Nimiq Hub's signMessage() / Keyguard
// convention: the message is prefixed the same way Ethereum's personal_sign
// does it, so a signed message can never collide with a valid transaction
// signature. See @nimiq/hub-api's signMessage() and Nimiq's
// BufferUtils.fromAscii('\x16Nimiq Signed Message:\n' + message.length).
const nimiqSignedMessagePrefix = "\x16Nimiq Signed Message:\n"

// VerifySignedMessage checks that `signature` (64 raw bytes) over the
// Nimiq-prefixed `message`, made by the holder of `publicKey` (32 raw
// bytes), is valid, and that publicKey actually derives `claimedAddress`.
func VerifySignedMessage(publicKey, signature []byte, message string, claimedAddress string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(publicKey))
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("signature must be %d bytes, got %d", ed25519.SignatureSize, len(signature))
	}

	// Nimiq Hub's signMessage() does NOT sign the raw prefixed bytes. Per
	// https://nimiq.github.io/api-reference/sign-message it signs the SHA-256
	// digest of the prefixed message:
	//
	//   sign( sha256( '\x16Nimiq Signed Message:\n' + message.length + message ) )
	//
	// Verify over that digest, exactly as the Keyguard produced it. Verifying
	// the raw prefix is what made real wallet logins fail with 401.
	prefixed := []byte(fmt.Sprintf("%s%d%s", nimiqSignedMessagePrefix, len(message), message))
	digest := sha256.Sum256(prefixed)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), digest[:], signature) {
		return fmt.Errorf("signature verification failed")
	}

	derived, err := AddressFromPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("could not derive address from public key: %w", err)
	}
	if NormalizeAddress(derived) != NormalizeAddress(claimedAddress) {
		return fmt.Errorf("public key does not match claimed address")
	}
	return nil
}
