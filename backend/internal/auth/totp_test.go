package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B vectors for HMAC-SHA-256 (32-byte ASCII seed, 30-second
// steps, 8-digit codes). The six-digit codes this server issues are the same
// values modulo 10^6.
var rfc6238SHA256 = []struct {
	unix int64
	code string // last six digits of the RFC's eight-digit code
}{
	{59, "119246"},          // 46119246
	{1111111109, "084774"},  // 68084774
	{1111111111, "062674"},  // 67062674
	{1234567890, "819424"},  // 91819424
	{2000000000, "698825"},  // 90698825
	{20000000000, "737706"}, // 77737706
}

// rfcSecret is the RFC 6238 SHA-256 seed: the ASCII digits 1..0 repeated to
// 32 bytes, base32-encoded the way an operator would supply it.
func rfcSecret() string {
	return base32.StdEncoding.EncodeToString([]byte(strings.Repeat("1234567890", 4)[:32]))
}

func TestTOTPMatchesRFC6238SHA256(t *testing.T) {
	key, err := decodeTOTPSecret(rfcSecret())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range rfc6238SHA256 {
		if got := totpCode(key, tc.unix/30); got != tc.code {
			t.Errorf("T=%d: got %s, want %s", tc.unix, got, tc.code)
		}
	}
}

func TestVerifyTOTPWindowAndShape(t *testing.T) {
	secret := rfcSecret()
	at := time.Unix(1234567890, 0)
	if !VerifyTOTP(secret, "819424", at) {
		t.Fatal("current-step code rejected")
	}
	// One step of drift either way is tolerated; two is not.
	if !VerifyTOTP(secret, "819424", at.Add(TOTPPeriod)) || !VerifyTOTP(secret, "819424", at.Add(-TOTPPeriod)) {
		t.Fatal("adjacent-step code rejected")
	}
	if VerifyTOTP(secret, "819424", at.Add(2*TOTPPeriod)) {
		t.Fatal("code two steps old accepted")
	}
	for _, bad := range []string{"", "81942", "8194244", "81942a", " 819424"} {
		if VerifyTOTP(secret, bad, at) {
			t.Errorf("malformed code %q accepted", bad)
		}
	}
	// Lower-case, spaced and padded secrets normalize to the same key.
	if !VerifyTOTP(strings.ToLower(secret[:8])+" "+secret[8:]+"==", "819424", at) {
		t.Fatal("normalized secret rejected")
	}
	if VerifyTOTP("TOOSHORT", "819424", at) {
		t.Fatal("short secret accepted")
	}
}

func TestTOTPProvisioningURI(t *testing.T) {
	got := TOTPProvisioningURI("nim.shop", "ops admin", " jbsw y3dp ehpk-3pxp=")
	want := "otpauth://totp/nim.shop:ops%20admin?algorithm=SHA256&digits=6&issuer=nim.shop&period=30&secret=JBSWY3DPEHPK3PXP"
	if got != want {
		t.Fatalf("uri = %s\nwant %s", got, want)
	}
}
