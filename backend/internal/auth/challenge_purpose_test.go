package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const purposeTestSecret = "unit-test-secret"

// legacyChallengeToken mints a challenge exactly the way the code did BEFORE
// purposes existed: a nonce and the registered claims, nothing else. Real tokens
// like this are in browsers for up to challengeExpiry after a deploy, so the
// rollout behaviour has to be a tested decision rather than a hope.
func legacyChallengeToken(t *testing.T, secret, nonce string) string {
	t.Helper()
	claims := struct {
		Nonce string `json:"nonce"`
		jwt.RegisteredClaims
	}{
		Nonce: nonce,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("mint legacy token: %v", err)
	}
	return raw
}

// TestChallengePurposeScoping is the guarantee behind the stake-wallet link:
// every signature the Hub produces is over a plain message, so without a
// purpose a login signature would also authorise a link (and a link signature
// would log someone in). Each flow must accept only its own challenge.
func TestChallengePurposeScoping(t *testing.T) {
	nonce := "nonce-1"

	login, err := IssueChallenge(purposeTestSecret, nonce)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	link, err := IssueChallengeFor(purposeTestSecret, nonce, PurposeStakeLink)
	if err != nil {
		t.Fatalf("IssueChallengeFor: %v", err)
	}
	if login == link {
		t.Fatal("a login and a link challenge for the same nonce must not be identical tokens")
	}

	// Each is accepted by its own flow.
	if _, err := ParseChallengeFor(purposeTestSecret, login, PurposeLogin); err != nil {
		t.Errorf("login challenge rejected by the login flow: %v", err)
	}
	got, err := ParseChallengeFor(purposeTestSecret, link, PurposeStakeLink)
	if err != nil {
		t.Errorf("link challenge rejected by the link flow: %v", err)
	}
	if got != nil && got.Nonce != nonce {
		t.Errorf("nonce = %q, want %q", got.Nonce, nonce)
	}
	if got != nil && got.Purpose != PurposeStakeLink {
		t.Errorf("purpose = %q, want %q", got.Purpose, PurposeStakeLink)
	}

	// And refused by the other one, in both directions.
	if _, err := ParseChallengeFor(purposeTestSecret, login, PurposeStakeLink); err == nil {
		t.Error("a LOGIN challenge was accepted by the stake-link flow — a login signature could link a wallet")
	} else if !strings.Contains(err.Error(), "login") {
		t.Errorf("the refusal should name the purpose it was issued for: %v", err)
	}
	if _, err := ParseChallengeFor(purposeTestSecret, link, PurposeLogin); err == nil {
		t.Error("a LINK challenge was accepted by the login flow — a link signature could sign someone in")
	}

	// Untouched behaviour: a wrong secret and a tampered token still fail.
	if _, err := ParseChallengeFor("another-secret", link, PurposeStakeLink); err == nil {
		t.Error("a challenge signed with a different secret was accepted")
	}
	if _, err := ParseChallengeFor(purposeTestSecret, link[:len(link)-2]+"xx", PurposeStakeLink); err == nil {
		t.Error("a tampered challenge was accepted")
	}
}

// TestLegacyChallengeIsStillALoginChallenge pins the rollout decision: tokens
// minted before this deploy carry no purpose and must keep working for login
// (a buyer mid-sign-in must not be 401'd by a release), while never being
// usable for the newer link flow. They expire within challengeExpiry, so the
// compatibility window closes on its own.
func TestLegacyChallengeIsStillALoginChallenge(t *testing.T) {
	legacy := legacyChallengeToken(t, purposeTestSecret, "nonce-legacy")

	claims, err := ParseChallengeFor(purposeTestSecret, legacy, PurposeLogin)
	if err != nil {
		t.Fatalf("a pre-purpose challenge must still sign a buyer in: %v", err)
	}
	if claims.Nonce != "nonce-legacy" {
		t.Fatalf("nonce = %q, want nonce-legacy", claims.Nonce)
	}
	if claims.Purpose != "" {
		t.Fatalf("purpose = %q, want empty on a legacy token", claims.Purpose)
	}
	if _, err := ParseChallengeFor(purposeTestSecret, legacy, PurposeStakeLink); err == nil {
		t.Fatal("a pre-purpose challenge was accepted by the stake-link flow")
	}
}
