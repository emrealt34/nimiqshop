package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"nimiqshop/internal/auth"
)

type Config struct {
	ListenAddr          string
	BadgerDir           string
	JWTSecret           string
	JWTExpiryMins       int
	AllowedOrigins      []string
	MaxRequestBodyBytes int
	MaxOrderQuantity    int

	PriceFeedURL         string
	OracleMinSources     int
	OracleMaxSpreadBps   int64
	DailyOrderLimit      int
	DailySpendLimitUSD   float64
	CRBaseURL            string
	CRPartnerID          string
	CRAppVersion         string
	CRUserAgent          string
	CRWebhookKey         string
	PublicWebhookBaseURL string
	// CRPollSecs is the fulfillment polling interval; CRStaleSecs bounds how
	// long an order_creating intent may stay stuck PAST its durable supplier
	// request-start marker before it is flagged for manual review (the
	// supplier has no order-listing endpoint, so a crash that lost the order
	// id is unrecoverable and must fail visible, never silent). It must
	// exceed the 20s supplier call timeout so a healthy in-flight creation
	// is never mistaken for a crash; intents without the marker (crashed
	// before dispatch) are re-dispatched instead, no stale bound involved.
	CRPollSecs  int
	CRStaleSecs int
	TestMode    bool

	// Admin credentials are deliberately distinct from JWT settings. The
	// password value is an Argon2id PHC hash, never a raw password.
	AdminUsername       string
	AdminPasswordHash   string
	// AdminDevUsername/AdminDevPassword: TEST-ONLY plaintext login pair
	// (ADMIN_DEV_USERNAME / ADMIN_DEV_PASSWORD). When set, that username+
	// password logs into the admin console WITHOUT TOTP. Never in production.
	AdminDevUsername string
	AdminDevPassword string
	AdminTOTPSecret     string
	AdminSessionSecret  string
	AdminSessionMins    int
	AdminCookieSecure   bool
	AdminBootstrapToken string

	// AllowHTTPLocal permits plain-HTTP URLs on loopback hosts only (the
	// mocks / local dev stack). Production must keep this false: every
	// external endpoint then strictly requires HTTPS.
	AllowHTTPLocal bool

	// Rate limiter (per minute / burst) for the customer API.
	RateLimitPerMinute int
	RateLimitBurst     int

	// Shared CryptoRefills partner-account queue. The queue is global to the
	// one Client instance used by the process; these bounds stop one user
	// from monopolising the partner account while endpoint windows enforce
	// conservative local budgets.
	CRQueueMax         int
	CRQueuePerActorMax int
	CRActorPerMinute   int
	CRActorBurst       int

	// TrustProxy controls how the real client IP is resolved when the app is
	// behind a reverse proxy / CDN (e.g. Cloudflare). When true, the backend
	// trusts Cloudflare's CF-Connecting-IP (and Cloudflare's CF-IPCountry), and
	// falls back to the first X-Forwarded-For entry, before RemoteIP. When the
	// backend is reachable only through Cloudflare (origin IP hidden), forwarding
	// spoofing is prevented; with a publicly exposed origin keep this false.
	TrustProxy bool

	// --- Gift notification channel (email + SMS) ---
	// Both channels are OFF by default and must be opted in via env vars.
	// A failed delivery is logged but never blocks order flow.
	NotifyEmailEnabled   bool
	NotifySMTPHost       string
	NotifySMTPPort       int
	NotifySMTPUsername   string
	NotifySMTPPassword   string
	NotifySMTPFromName   string
	NotifySMTPFromAddr   string
	NotifyEmailSubject   string
	NotifyEmailDryRun    bool

	NotifySMSEnabled     bool
	NotifySMSURL         string // e.g. https://api.pingram.com/v1/sms/send
	NotifySMSAuthHeader  string // e.g. "Bearer XYZ" — full header value
	NotifySMSSender      string // optional sender id
	NotifySMSBodyTmpl    string // JSON template; placeholders {phone} {message} {sender}
	NotifySMSDryRun      bool
}

// LoadDotEnv supports the project's local development .env file without
// overriding real process environment variables. Production should inject
// secrets through its platform's secret manager instead.
func LoadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid .env line")
		}
		key, value := strings.TrimSpace(parts[0]), strings.Trim(strings.TrimSpace(parts[1]), "\"")
		if key == "" {
			return fmt.Errorf("invalid .env key")
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func origins(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func Load() Config {
	return Config{
		ListenAddr: env("LISTEN_ADDR", ":8080"), BadgerDir: env("BADGER_DIR", "./data/badger"),
		JWTSecret: env("JWT_SECRET", ""), JWTExpiryMins: envInt("JWT_EXPIRY_MINS", 60*24*7),
		AllowedOrigins: origins(os.Getenv("ALLOWED_ORIGINS")), MaxRequestBodyBytes: envInt("MAX_REQUEST_BODY_BYTES", 1<<20), MaxOrderQuantity: envInt("MAX_ORDER_QUANTITY", 10),
		PriceFeedURL: env("PRICE_FEED_URL", "https://api.coingecko.com/api/v3/simple/price?ids=nimiq-2&vs_currencies=usd"),
		// Default 2: at least TWO independent price feeds are used for the
		// informational NIM estimate shown in the UI (never the payment
		// authority — the supplier's coin amount is authoritative).
		OracleMinSources: envInt("ORACLE_MIN_SOURCES", 2), OracleMaxSpreadBps: int64(envInt("ORACLE_MAX_SPREAD_BPS", 250)),
		DailyOrderLimit: envInt("DAILY_ORDER_LIMIT", 3), DailySpendLimitUSD: envFloat("DAILY_SPEND_LIMIT_USD", 50),
		CRBaseURL: env("CRYPTOREFILLS_BASE_URL", "https://api.cryptorefills.com"), CRPartnerID: os.Getenv("CRYPTOREFILLS_PARTNER_ID"), CRAppVersion: env("CRYPTOREFILLS_APP_VERSION", "nimshop/1.0"), CRUserAgent: env("CRYPTOREFILLS_USER_AGENT", "nimshop/1.0 +https://nim.shop"), CRWebhookKey: os.Getenv("CRYPTOREFILLS_WEBHOOK_KEY"), PublicWebhookBaseURL: strings.TrimRight(os.Getenv("PUBLIC_WEBHOOK_BASE_URL"), "/"),
		CRPollSecs: envInt("WORKER_ORDER_POLL_SECS", 5), CRStaleSecs: envInt("WORKER_ORDER_STALE_SECS", 300), TestMode: envBool("TEST_MODE", false),
		AdminUsername: os.Getenv("ADMIN_USERNAME"), AdminPasswordHash: os.Getenv("ADMIN_PASSWORD_HASH"), AdminTOTPSecret: os.Getenv("ADMIN_TOTP_SECRET"), AdminSessionSecret: os.Getenv("ADMIN_SESSION_SECRET"),
	AdminDevUsername: os.Getenv("ADMIN_DEV_USERNAME"), AdminDevPassword: os.Getenv("ADMIN_DEV_PASSWORD"),
		AdminSessionMins: envInt("ADMIN_SESSION_MINS", 8*60), AdminCookieSecure: envBool("ADMIN_COOKIE_SECURE", true), AdminBootstrapToken: os.Getenv("ADMIN_BOOTSTRAP_TOKEN"),
		AllowHTTPLocal:     envBool("ALLOW_HTTP_LOCAL", false),
		TrustProxy:         envBool("TRUST_PROXY", true),
		RateLimitPerMinute: envInt("RATE_LIMIT_PER_MINUTE", 60),
		RateLimitBurst:     envInt("RATE_LIMIT_BURST", 20),
		CRQueueMax:         envInt("CRYPTOREFILLS_QUEUE_MAX", 2000),
		CRQueuePerActorMax: envInt("CRYPTOREFILLS_QUEUE_PER_ACTOR_MAX", 100),
		CRActorPerMinute:   envInt("CRYPTOREFILLS_ACTOR_REQUESTS_PER_MINUTE", 30),
		CRActorBurst:       envInt("CRYPTOREFILLS_ACTOR_BURST", 6),

		NotifyEmailEnabled:  envBool("NOTIFY_EMAIL_ENABLED", false),
		NotifySMTPHost:      os.Getenv("NOTIFY_SMTP_HOST"),
		NotifySMTPPort:      envInt("NOTIFY_SMTP_PORT", 587),
		NotifySMTPUsername:  os.Getenv("NOTIFY_SMTP_USERNAME"),
		NotifySMTPPassword:  os.Getenv("NOTIFY_SMTP_PASSWORD"),
		NotifySMTPFromName:  env("NOTIFY_SMTP_FROM_NAME", "nim.shop gifts"),
		NotifySMTPFromAddr:  os.Getenv("NOTIFY_SMTP_FROM_ADDR"),
		NotifyEmailSubject:  env("NOTIFY_EMAIL_SUBJECT", "🎁 A gift for you from nim.shop"),
		NotifyEmailDryRun:   envBool("NOTIFY_EMAIL_DRY_RUN", false),

		NotifySMSEnabled:    envBool("NOTIFY_SMS_ENABLED", false),
		NotifySMSURL:        os.Getenv("NOTIFY_SMS_URL"),
		NotifySMSAuthHeader: os.Getenv("NOTIFY_SMS_AUTH_HEADER"),
		NotifySMSSender:     os.Getenv("NOTIFY_SMS_SENDER"),
		NotifySMSBodyTmpl:   env("NOTIFY_SMS_BODY_TEMPLATE", `{"phone":"{phone}","message":"{message}","sender":"{sender}"}`),
		NotifySMSDryRun:     envBool("NOTIFY_SMS_DRY_RUN", false),
	}
}

// HasAdminSeed reports whether all one-time startup bootstrap credentials are
// present. It intentionally does not depend on a user JWT configuration.
func (c Config) HasAdminSeed() bool {
	return c.AdminUsername != "" && c.AdminPasswordHash != "" && c.AdminTOTPSecret != ""
}
func (c Config) AdminSessionsEnabled() bool { return len(c.AdminSessionSecret) >= 32 }

// AdminDevMode reports whether the plaintext test login pair is configured.
func (c Config) AdminDevMode() bool { return c.AdminDevUsername != "" && c.AdminDevPassword != "" }

func httpsURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s must be an absolute https URL", name)
	}
	if u.Scheme == "https" {
		return nil
	}
	// Loopback-only development exception (ALLOW_HTTP_LOCAL=true): local
	// mock stacks run plain HTTP on 127.0.0.1. Mirrors the ALLOWED_ORIGINS
	// rule; never applies to public hosts.
	if u.Scheme == "http" && allowHTTPLocal && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s must be an absolute https URL", name)
}

var allowHTTPLocal bool

func initAllowHTTPLocal() { allowHTTPLocal = envBool("ALLOW_HTTP_LOCAL", false) }

func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
func allowedOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid ALLOWED_ORIGINS entry")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("ALLOWED_ORIGINS entries must use HTTPS (except loopback development origins)")
}
func (c Config) Validate() error {
	allowHTTPLocal = c.AllowHTTPLocal
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 random bytes; refusing to start")
	}
	if c.JWTExpiryMins > 60*24*30 {
		return fmt.Errorf("JWT_EXPIRY_MINS must not exceed 30 days")
	}
	if c.MaxRequestBodyBytes > 4<<20 || c.MaxOrderQuantity > 100 {
		return fmt.Errorf("configured request/order limits are unsafe")
	}
	if c.CRQueueMax < 1 || c.CRQueueMax > 10000 || c.CRQueuePerActorMax < 1 || c.CRQueuePerActorMax > c.CRQueueMax || c.CRActorPerMinute < 1 || c.CRActorPerMinute > 60 || c.CRActorBurst < 1 || c.CRActorBurst > c.CRActorPerMinute {
		return fmt.Errorf("unsafe cryptorefills queue configuration")
	}
	if c.CRPartnerID == "" {
		return fmt.Errorf("CRYPTOREFILLS_PARTNER_ID is required")
	}
	if c.CRPollSecs < 2 || c.CRPollSecs > 60 {
		return fmt.Errorf("WORKER_ORDER_POLL_SECS must be 2-60")
	}
	// The stale bound is measured from the durable supplier-request marker,
	// so it must exceed the longest legitimate in-flight creation (the 20s
	// supplier call context + transport margin); a shorter value would flag
	// healthy in-flight creations as crashed.
	if c.CRStaleSecs < 25 || c.CRStaleSecs > 3600 {
		return fmt.Errorf("WORKER_ORDER_STALE_SECS must be 25-3600 (it must exceed the 20s supplier call timeout so in-flight order creations are never flagged stale)")
	}
	if c.CRWebhookKey != "" && len(c.CRWebhookKey) < 32 {
		return fmt.Errorf("CRYPTOREFILLS_WEBHOOK_KEY must be at least 32 random bytes when set")
	}
	if c.CRWebhookKey != "" && c.PublicWebhookBaseURL == "" {
		return fmt.Errorf("PUBLIC_WEBHOOK_BASE_URL is required when CRYPTOREFILLS_WEBHOOK_KEY is set")
	}
	if c.OracleMinSources < 2 || c.OracleMinSources > 4 || c.OracleMaxSpreadBps < 10 || c.OracleMaxSpreadBps > 1000 {
		return fmt.Errorf("unsafe oracle configuration")
	}
	for _, o := range c.AllowedOrigins {
		if err := allowedOrigin(o); err != nil {
			return err
		}
	}
	for n, v := range map[string]string{"PRICE_FEED_URL": c.PriceFeedURL, "CRYPTOREFILLS_BASE_URL": c.CRBaseURL} {
		if err := httpsURL(n, v); err != nil {
			return err
		}
	}
	if c.PublicWebhookBaseURL != "" {
		if err := httpsURL("PUBLIC_WEBHOOK_BASE_URL", c.PublicWebhookBaseURL); err != nil {
			return err
		}
	}

	credentialFields := 0
	for _, value := range []string{c.AdminUsername, c.AdminPasswordHash, c.AdminTOTPSecret} {
		if value != "" {
			credentialFields++
		}
	}
	if credentialFields != 0 && credentialFields != 3 {
		return fmt.Errorf("ADMIN_USERNAME, ADMIN_PASSWORD_HASH, and ADMIN_TOTP_SECRET must be configured together")
	}
	if c.HasAdminSeed() {
		if len(c.AdminUsername) < 3 || len(c.AdminUsername) > 64 {
			return fmt.Errorf("ADMIN_USERNAME must be 3-64 characters")
		}
		if err := auth.ValidateArgon2idPHC(c.AdminPasswordHash); err != nil {
			return fmt.Errorf("ADMIN_PASSWORD_HASH must be a safe Argon2id PHC hash: %w", err)
		}
		if err := auth.ValidateTOTPSecret(c.AdminTOTPSecret); err != nil {
			return fmt.Errorf("ADMIN_TOTP_SECRET must be base32: %w", err)
		}
	}
	if (c.HasAdminSeed() || c.AdminBootstrapToken != "") && !c.AdminSessionsEnabled() {
		return fmt.Errorf("ADMIN_SESSION_SECRET must be a separate random secret of at least 32 bytes")
	}
	if c.AdminSessionMins < 15 || c.AdminSessionMins > 60*24*7 {
		return fmt.Errorf("ADMIN_SESSION_MINS must be between 15 minutes and 7 days")
	}
	if c.AdminBootstrapToken != "" && len(c.AdminBootstrapToken) < 32 {
		return fmt.Errorf("ADMIN_BOOTSTRAP_TOKEN must be at least 32 random bytes")
	}
	return nil
}
