// Package admin contains persistence-safe models for the separate operations
// console identity domain. These records deliberately never cross the public
// user/JWT authentication boundary.
package admin

import "time"

// User is an operations-console identity. PasswordHash and TOTPSecret are
// intentionally omitted from every HTTP response by admin handlers.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	TOTPSecret   string    `json:"totp_secret"`
	CreatedAt    time.Time `json:"created_at"`
	Disabled     bool      `json:"disabled"`
}

// Session stores only an HMAC of the random cookie credential. The cookie
// credential itself is never persisted or logged.
type Session struct {
	ID        string     `json:"id"`
	AdminID   string     `json:"admin_id"`
	TokenHash string     `json:"token_hash"`
	IP        string     `json:"ip"`
	UserAgent string     `json:"user_agent"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// AuditEvent is append-only. Detail must be an operator-safe description and
// must never contain a password, TOTP code, session cookie, or API secret.
type AuditEvent struct {
	ID        string    `json:"id"`
	AdminID   string    `json:"admin_id,omitempty"`
	Action    string    `json:"action"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// LoginFailure is a private lockout record keyed by canonical username.
// It is never returned to an unauthenticated client, which prevents account
// enumeration and lockout-state disclosure.
type LoginFailure struct {
	Username      string     `json:"username"`
	FailedCount   int        `json:"failed_count"`
	FirstFailedAt time.Time  `json:"first_failed_at"`
	LastFailedAt  time.Time  `json:"last_failed_at"`
	LockedUntil   *time.Time `json:"locked_until,omitempty"`
}

// Settings are global operations controls persisted independently of process
// configuration so a margin update affects newly issued quotes immediately.
type Settings struct {
	GlobalMarginBps int       `json:"global_margin_bps"`
	UpdatedAt       time.Time `json:"updated_at"`
	UpdatedBy       string    `json:"updated_by"`
}
