package handlers

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"nimiqshop/internal/auth"
)

/*
auth_session_renewal_test.go — the sliding window that keeps a USED session from
expiring out from under the person using it.

Owner (2026-10-06): "sürekli giriş yapıyorum çıkıyor". The cookie was issued once
at login and simply ran out, so a returning shopper was thrown out on a fixed
schedule no matter how much they used the shop. /api/auth/session — which already
runs on every page load — now renews the cookie once less than half the session
is left.

What these tests pin is the boundary, because both sides of it are live bugs:
renewing too eagerly rotates the cookie on every page view for nothing, and
renewing too late leaves a session that ends between two page views.
*/

// claimsExpiringIn builds the claim set the session cookie would carry if it
// expired `d` from now — the only input the renewal decision reads.
func claimsExpiringIn(d time.Duration) *auth.Claims {
	return &auth.Claims{
		UserID:           "u1",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(d))},
	}
}

func TestSessionRenewsOnlyWhenLessThanHalfIsLeft(t *testing.T) {
	const expiryMins = 60 // one hour, so half is thirty minutes

	cases := []struct {
		name    string
		left    time.Duration
		renew   bool
		explain string
	}{
		{"brand new session", 59 * time.Minute, false, "renewing here would rotate the cookie on every page view"},
		{"just over half", 31 * time.Minute, false, "still more than half its life left"},
		{"just under half", 29 * time.Minute, true, "the session must not lapse between two page views"},
		{"nearly over", time.Minute, true, "the whole point of the window"},
		{"expired", -time.Minute, true, "an expired token is replaced rather than slid; the handler refuses it first"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expires, renew := sessionNeedsRenewal(claimsExpiringIn(tc.left), expiryMins)
			if renew != tc.renew {
				t.Fatalf("renew = %v, want %v (%s)", renew, tc.renew, tc.explain)
			}
			if expires == 0 {
				t.Fatal("the current expiry must be reported even when nothing is renewed")
			}
		})
	}
}

func TestSessionRenewalIsSkippedWhenItCannotBeReasonedAbout(t *testing.T) {
	// No claims, no expiry, or a configuration with no lifetime: nothing to
	// slide, and the handler must fall back to the token's own expiry rather
	// than inventing one.
	if _, renew := sessionNeedsRenewal(nil, 60); renew {
		t.Fatal("nil claims must not renew")
	}
	if _, renew := sessionNeedsRenewal(&auth.Claims{UserID: "u1"}, 60); renew {
		t.Fatal("a token without an expiry must not renew")
	}
	if _, renew := sessionNeedsRenewal(claimsExpiringIn(time.Minute), 0); renew {
		t.Fatal("a zero lifetime must not renew — that would be an endless session")
	}
}
