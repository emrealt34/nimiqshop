// Package mailtrap is the shop's outbound email transport: the Mailtrap Email
// API (https://send.api.mailtrap.io/api/send) wrapped in the two things the shop
// actually needs — a gift note for the recipient and a delivery receipt for the
// buyer.
//
// It deliberately knows NOTHING about the database, the supplier or the quote
// lifecycle: callers hand it a Message, it hands back Mailtrap's message IDs.
// That keeps the transport testable on its own and keeps "at-most-once" where it
// belongs — on the quote row (db.Quote.GiftNotifiedAt), not in the mailer.
//
// CONFIG (environment variables, like every other setup value in this app)
// -------------------------------------------------------------------------
//
//	MAILTRAP_API_TOKEN     required.  Secrets stay in the environment: this is
//	                      the ONLY place the token is read. Never commit it,
//	                      never pass it as a flag (it would show up in ps(1)).
//	MAILTRAP_FROM_EMAIL   required.  Must live on a domain Mailtrap accepts for
//	                      sending: a verified sending domain, or the demo
//	                      domain demomailtrap.co (e.g. hello@demomailtrap.co).
//	                      Sending anything else returns 422 unverified_domain.
//	MAILTRAP_FROM_NAME    optional.  Display name, default "shop.nimiqbase.com".
//	MAILTRAP_REPLY_TO     optional.  Where a recipient's reply lands. Put the
//	                      SUPPORT inbox here, not the buyer's address: a gift
//	                      recipient replying must not reach the gifter's inbox
//	                      (nor learn it) unless that is explicitly wanted.
//	MAILTRAP_CATEGORY     optional.  Mailtrap category for logs/stats,
//	                      default "nimshop". Gift notes use "<category>-gift".
//	MAILTRAP_USE_SANDBOX  optional. "true" routes sends to the Email Sandbox —
//	                      captured in a test inbox, never delivered. Flip it in
//	                      staging without touching any call site.
//	MAILTRAP_SANDBOX_ID   required when sandbox mode is on (project/inbox id).
//	MAILTRAP_TIMEOUT      optional. Per-request timeout, default "10s".
//
// With no token (or no from-address) the client reports itself disabled and
// Send returns ErrDisabled, so a dev box or CI run sends nothing and logs
// nothing instead of failing an order.
package mailtrap

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for the non-secret setup values. A zero Config is NOT usable
// (Enabled() reports false) — these are what ConfigFromEnv fills in when the
// environment is silent.
const (
	DefaultFromName  = "shop.nimiqbase.com"
	DefaultFromEmail = "hello@shop.nimiqbase.com"
	DefaultCategory  = "nimshop"
	DefaultTimeout   = 10 * time.Second

	// GiftCategorySuffix marks the recipient-facing note in Mailtrap's logs,
	// so support can filter "did the note go out?" apart from receipts.
	GiftCategorySuffix = "-gift"
	// ReceiptCategorySuffix marks the buyer's own delivery receipt.
	ReceiptCategorySuffix = "-receipt"
)

// Env var names, exported so a deploy manifest can reference them without
// duplicating strings.
const (
	EnvAPIToken    = "MAILTRAP_API_TOKEN"
	EnvFromEmail   = "MAILTRAP_FROM_EMAIL"
	EnvFromName    = "MAILTRAP_FROM_NAME"
	EnvReplyTo     = "MAILTRAP_REPLY_TO"
	EnvCategory    = "MAILTRAP_CATEGORY"
	EnvUseSandbox  = "MAILTRAP_USE_SANDBOX"
	EnvSandboxID   = "MAILTRAP_SANDBOX_ID"
	EnvTimeout     = "MAILTRAP_TIMEOUT"
	EnvAPIEndpoint = "MAILTRAP_API_ENDPOINT"
)

// Config is the whole setup surface of the transport. It is plain data so the
// app's existing config struct can hold it verbatim:
//
//	// internal/config: add one field, set it once at startup.
//	Cfg.Mailtrap = mailtrap.ConfigFromEnv()
type Config struct {
	// APIToken is read from EnvAPIToken only. It is never logged, never
	// rendered into an error, and never part of a struct String().
	APIToken string
	// FromEmail is the envelope sender. Required.
	FromEmail string
	// FromName is the display name; empty falls back to DefaultFromName.
	FromName string
	// ReplyTo is optional; empty means Mailtrap's default (no Reply-To added).
	ReplyTo string
	// Category groups sends in Mailtrap's logs; empty falls back to
	// DefaultCategory.
	Category string
	// UseSandbox routes sends to the Email Sandbox instead of real delivery.
	UseSandbox bool
	// SandboxID is the sandbox project id, used only when UseSandbox is true.
	SandboxID int64
	// Timeout bounds one API request. Zero means DefaultTimeout.
	Timeout time.Duration
	// APIEndpoint replaces Mailtrap's send host. Leave it EMPTY in production.
	// It exists for two things only: a test suite pointing at a local fake, and a
	// locked-down network that must relay through a proxy. With UseSandbox on it
	// overrides the sandbox host instead.
	APIEndpoint string
}

// timeout guards against a zero Config (a caller that built Config{} by hand).
func (c Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// Enabled reports whether the transport has the two values it cannot work
// without. Everything else has a default.
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.APIToken) != "" && strings.TrimSpace(c.FromEmail) != ""
}

// ConfigFromEnv reads the environment. Unknown/malformed numeric values fall
// back to the default rather than failing startup, because a typo in a
// non-secret knob must not take the shop down; a malformed address or an
// on-but-unconfigured sandbox is surfaced by New instead.
func ConfigFromEnv() Config {
	c := Config{
		APIToken:   strings.TrimSpace(os.Getenv(EnvAPIToken)),
		FromEmail:  strings.TrimSpace(os.Getenv(EnvFromEmail)),
		FromName:   strings.TrimSpace(os.Getenv(EnvFromName)),
		ReplyTo:    strings.TrimSpace(os.Getenv(EnvReplyTo)),
		Category:   strings.TrimSpace(os.Getenv(EnvCategory)),
		UseSandbox: strings.EqualFold(strings.TrimSpace(os.Getenv(EnvUseSandbox)), "true"),
	}
	if c.FromEmail == "" {
		c.FromEmail = DefaultFromEmail
	}
	if c.FromName == "" {
		c.FromName = DefaultFromName
	}
	if c.Category == "" {
		c.Category = DefaultCategory
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(EnvSandboxID)), 10, 64); err == nil {
		c.SandboxID = id
	}
	c.APIEndpoint = strings.TrimRight(strings.TrimSpace(os.Getenv(EnvAPIEndpoint)), "/")
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(EnvTimeout))); err == nil && d > 0 {
		c.Timeout = d
	} else {
		c.Timeout = DefaultTimeout
	}
	return c
}

// Errors callers branch on.
var (
	// ErrDisabled means no token / no from-address: the send was NOT attempted.
	// Callers should log at info level and move on — an unsent receipt must not
	// fail an already-paid order.
	ErrDisabled = errors.New("mailtrap: sending is not configured (MAILTRAP_API_TOKEN / MAILTRAP_FROM_EMAIL)")
	// ErrNoRecipient means the caller passed a message nobody can receive.
	ErrNoRecipient = errors.New("mailtrap: message has no recipient")
	// ErrSandboxID is a setup mistake, surfaced at startup rather than on the
	// first send.
	ErrSandboxID = errors.New("mailtrap: MAILTRAP_SANDBOX_ID is required when MAILTRAP_USE_SANDBOX=true")
	// ErrEndpoint is a MAILTRAP_API_ENDPOINT that is not an absolute http(s)
	// URL — again a startup typo rather than a runtime surprise.
	ErrEndpoint = errors.New("mailtrap: MAILTRAP_API_ENDPOINT must be an absolute http(s) URL")
)
