package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID string `json:"uid"`
	jwt.RegisteredClaims
}

// Challenge purposes. Everything the Hub signs is just a message string, so two
// flows that each ask for "a signature" could otherwise borrow each other's
// proofs: a login challenge signature would prove ownership of the wallet to
// the stake-address linking endpoint just as convincingly as a purpose-built
// one. The purpose travels inside the server-signed challenge token and each
// endpoint accepts only its own, so the distinct message text is a second line
// of defence rather than the only one.
const (
	// PurposeLogin is the historical purpose: "sign in as this wallet".
	PurposeLogin = "login"
	// PurposeStakeLink proves ownership of a staking wallet so that it can be
	// linked to an already signed-in shop account.
	PurposeStakeLink = "stake_link"
)

// ChallengeClaims backs the short-lived challenge issued by
// /api/auth/challenge. The Hub-signed message embeds the nonce, so this
// token round-trips through the client without needing server-side storage.
type ChallengeClaims struct {
	Nonce string `json:"nonce"`
	// Purpose pins which endpoint may consume this challenge. Tokens minted
	// before purposes existed leave it empty; ParseChallengeFor treats that as
	// PurposeLogin so in-flight logins survive a deploy.
	Purpose string `json:"purpose,omitempty"`
	jwt.RegisteredClaims
}

const challengeExpiry = 5 * time.Minute

// NewNonce returns a random hex string used as the challenge text the user's
// Nimiq wallet signs to prove address ownership.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IssueChallenge wraps a nonce in a short-lived JWT so the server doesn't
// need to persist pending logins; the client sends this token back alongside
// the wallet signature and we just re-verify it. It mints a login-purpose
// challenge, which is what the Hub sign-in flow wants.
func IssueChallenge(secret, nonce string) (string, error) {
	return IssueChallengeFor(secret, nonce, PurposeLogin)
}

// IssueChallengeFor is IssueChallenge for an explicitly named purpose. Every
// caller outside the login flow must use this so its challenge cannot be
// replayed against another endpoint.
func IssueChallengeFor(secret, nonce, purpose string) (string, error) {
	claims := ChallengeClaims{
		Nonce:   nonce,
		Purpose: purpose,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(challengeExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func ParseChallenge(secret, raw string) (*ChallengeClaims, error) {
	claims := &ChallengeClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid or expired challenge: %w", err)
	}
	return claims, nil
}

// ParseChallengeFor parses a challenge and then refuses to hand it to a flow it
// was not minted for. An empty purpose means the token predates purposes and is
// treated as a login challenge: those tokens expire within challengeExpiry of a
// deploy, so the compatibility window closes by itself.
func ParseChallengeFor(secret, raw, purpose string) (*ChallengeClaims, error) {
	claims, err := ParseChallenge(secret, raw)
	if err != nil {
		return nil, err
	}
	got := claims.Purpose
	if got == "" {
		got = PurposeLogin
	}
	if got != purpose {
		return nil, fmt.Errorf("challenge was issued for %q, not %q", got, purpose)
	}
	return claims, nil
}

func IssueToken(secret, userID string, expiryMins int) (string, error) {
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expiryMins) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func ParseToken(secret, raw string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	return claims, nil
}
