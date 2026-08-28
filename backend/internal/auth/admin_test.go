package auth

import (
	"testing"
	"time"
)

func TestArgon2idPasswordRoundTrip(t *testing.T) {
	hash, err := HashArgon2idPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateArgon2idPHC(hash); err != nil {
		t.Fatalf("generated PHC was rejected: %v", err)
	}
	if !VerifyArgon2idPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password did not verify")
	}
	if VerifyArgon2idPassword(hash, "incorrect") {
		t.Fatal("incorrect password verified")
	}
}

func TestTOTPVerification(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP" // base32 for a non-production test key.
	now := time.Unix(1_700_000_000, 0).UTC()
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(key, now.Unix()/30)
	if !VerifyTOTP(secret, code, now) {
		t.Fatal("current TOTP code did not verify")
	}
	if VerifyTOTP(secret, "000000", now) && code != "000000" {
		t.Fatal("invalid TOTP code verified")
	}
	if VerifyTOTP(secret, code, now.Add(2*time.Minute)) {
		t.Fatal("TOTP code was accepted outside the permitted clock-skew window")
	}
}
