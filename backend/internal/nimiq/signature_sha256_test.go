package nimiq

import (
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"
)

// TestVerifySignedMessage_HubSHA256 reproduces EXACTLY how Nimiq Hub's
// signMessage() signs (per https://nimiq.github.io/api-reference/sign-message):
//
//	sign( sha256( '\x16Nimiq Signed Message:\n' + message.length + message ) )
//
// A signature produced this way MUST verify with VerifySignedMessage.
func TestVerifySignedMessage_HubSHA256(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	addr, err := AddressFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	msg := "nim.shop login: 393aff2f851a641619c3f873d1b80169"

	// Hub-compatible prefixed message, then SHA256 digest, then Ed25519 sign.
	prefixed := []byte(strings.Join([]string{nimiqSignedMessagePrefix, itoa(len(msg)), msg}, ""))
	digest := sha256.Sum256(prefixed)
	sig := ed25519.Sign(priv, digest[:])

	// The current (buggy) raw-verification should FAIL on a Hub signature.
	if err := VerifySignedMessage(pub, sig, msg, addr); err != nil {
		t.Fatalf("expected Hub sha256 signature to verify, got: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
