package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"nimiqshop/internal/memlimit"
	"os"
	"strconv"
	"strings"

	"nimiqshop/internal/auth"
	"nimiqshop/internal/cashbackcode"
	"nimiqshop/internal/clientip"
	"nimiqshop/internal/loopback"
)

type Config struct {
	ListenAddr string
	// StaticDir, when set, serves the built frontend (Astro dist/) from this
	// process so a release install is one binary + one folder. Empty = API only.
	StaticDir           string
	BadgerDir           string
	JWTSecret           string
	JWTExpiryMins       int
	AllowedOrigins      []string
	FrontendURL         string // FRONTEND_URL — one line that feeds CORS when ALLOWED_ORIGINS is empty
	MaxRequestBodyBytes int
	MaxOrderQuantity    int

	PriceFeedURL         string
	OracleMinSources     int
	OracleMaxSpreadBps   int64
	DailyOrderLimit      int
	DailySpendLimitUSD   float64
	MonthlySpendLimitUSD float64
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
	// simple ADMIN_USERNAME + ADMIN_PASSWORD pair is the normal login mode.
	// AdminPasswordHash/TOTP are retained only for legacy compatibility.
	AdminUsername     string
	AdminPasswordHash string
	// The variables are read under their one canonical ADMIN_* names
	// aliases; simple ADMIN_* values are mirrored into them at load time.
	AdminDevUsername string
	AdminDevPassword string
	// AdminPassword is the simple env-based admin password. It is mirrored
	// into the dev credential fields for the intentionally simple two-variable
	// deployment mode: ADMIN_USERNAME + ADMIN_PASSWORD.
	AdminPassword       string
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

	// Forwarding headers are trusted only from configured immediate peers.
	// The supplied tunnel launcher authenticates the Node -> Go hop as well.
	TrustProxy            bool
	TrustedProxyCIDRs     []string
	ProxyHeaderMode       string
	ForwardedHeaderSecret string

	// --- Outbound email ---
	// Mailtrap (internal/mailtrap) is the ONLY mail transport — the generic
	// SMTP client and the SMS sender were removed outright. There is
	// nothing to configure here: the transport reads MAILTRAP_* env vars
	// itself (token, from-address, sandbox); see mailtrap.ConfigFromEnv.
	// A failed delivery is logged but never blocks order flow.

	NimiqRPCURL        string
	NimiqRPCURL2       string
	CashbackEnabled    bool
	CashbackWalletSeed string
	// CashbackWalletAccount: which Hub account a pasted BIP39 phrase pays
	// from (m/44'/242'/<account>'). 0 = the first account the Hub shows.
	CashbackWalletAccount int
	CashbackNetwork       string
	CashbackFeeLuna       int
	// CASHBACK_CODES is an optional promo-code table (CSV or JSON) whose
	// cashback rate overrides the base/staker logic for that order.
	CashbackCodes string

	// Pool-staker cashback. Buyers who delegate NIM to the operator's own
	// validator earn the admin-configured ladder instead of the base rate.
	// PoolAPIURL is the GoPool base URL (its GET /api/stakers/{address} is
	// public); PoolValidatorAddress is what the shop hands to Nimiq Pay so
	// the "stake with us" button delegates to the right validator.
	PoolAPIURL           string
	PoolValidatorAddress string
	PoolStakeCacheTTL    int // seconds; 0 = poolstake.DefaultTTL
	PoolStakeTimeout     int // seconds; 0 = poolstake.DefaultTimeout
	// PoolFeedAPIKey is the shared secret for the pool's /api/cashback/profit
	// (the realized-fee source of the single-ledger staker boost). Empty =
	// ledger feed off (the base rate still works, the boost simply never
	// accrues). PoolFeedPollSeconds is the feed cadence (default 30).
	PoolFeedAPIKey      string
	PoolFeedPollSeconds int

	// 1-Luna wallet-memo notification channel. OFF by default: it signs
	// and broadcasts real transactions, so it must be validated against a
	// funded key via cmd/notif-test first. It reuses the cashback wallet
	// seed unless NOTIFY_WALLET_SEED overrides it — one funded key is
	// enough, and asking operators to fund two is a footgun.
	NotifyWalletEnabled bool
	NotifyWalletSeed    string
	NotifyWalletNetwork string
	NotifyWalletFeeLuna int

	// SiteHost is the public shop hostname (no scheme), e.g. "shop.nimiqbase.com".
	// One env — SITE_HOST — drives every user-facing shop name in backend
	// copy, emails, cashback memos, Hub login messages, and /api/site.
	SiteHost string

	// --- Payment rails ---
	// The USDT-on-Polygon payment option is ALWAYS on — there is no env
	// switch and no env-chosen network. The frontend always shows the
	// payment-method picker (Nimiq Pay / USDT Polygon) and the network is
	// fixed to "Polygon (Matic)" in code (handlers.PaymentNetworkStable).
	// Both payment rails pay the SAME cashback rate — there is no
	// stablecoin reduction. BTC Lightning (Nimiq Pay) remains the default.
	USDTCoin string // "USDT"

	// --- Cashback burn destination ---
	// When a buyer selects "Burn" as their cashback destination, the cashback
	// NIM is sent directly to the Nimiq burn wallet instead of their wallet.
	BurnNimAddress string

	// --- Edge hardening & scale envelope (internal/httpx) ---
	// Every one of these is a knob on the DDoS/throughput posture of the
	// process. They all have production-safe defaults, so a deployment that
	// never sets them is already hardened.

	// AllowTunnelOrigins re-enables the "*.trycloudflare.com" CORS shortcut.
	// It is a DEVELOPMENT convenience: a quick tunnel hands out a random
	// subdomain of a domain ANYONE can register a tunnel on, so allowing it
	// with credentials in production lets an attacker's own tunnel origin
	// read an authenticated shopper's API responses. It defaults to
	// TEST_MODE || ALLOW_HTTP_LOCAL and must never be true on a public host.
	AllowTunnelOrigins bool
	// MaxInFlight is the admission-control ceiling on simultaneously
	// executing handlers. 0 = httpx.DefaultMaxInFlight() (cores × 4096,
	// clamped to [8192, 262144]).
	MaxInFlight int
	// ResponseCompression gzips JSON bodies above CompressMinBytes.
	ResponseCompression bool
	CompressLevel       int
	CompressMinBytes    int
	// HSTS emits Strict-Transport-Security on TLS-terminated responses.
	HSTS bool
	// HSTSPreload adds the preload directive (only enable once every
	// subdomain is HTTPS).
	HSTSPreload bool
	// ServerHeader replaces fasthttp's default Server banner. "" removes it.
	ServerHeader string
	// CDNCachePublic marks public, user-independent GET responses as
	// shareable with a CDN. It is only honoured when the CORS answer is
	// deterministic (zero or one ALLOWED_ORIGINS entry); otherwise the
	// middleware downgrades it to private automatically, because a URL-keyed
	// shared cache would hand one origin's Access-Control-Allow-Origin to
	// another.
	CDNCachePublic bool

	// TierRateOverrides re-prices one of the edge cost tiers (see
	// internal/httpx/tiers.go) without a code change. Keyed by tier name
	// ("checkout", "login", "support", …); a tier that is not mentioned keeps
	// its compiled default.
	//
	// Two reasons this exists. Operationally, the right number depends on
	// facts only the operator has: how much supplier budget their
	// Cryptorefills tier actually allows, whether they sit behind a corporate
	// egress that collapses a whole office onto one IP, how aggressive their
	// own monitoring is. A load harness needs the same knob for a different
	// reason — it deliberately drives far more checkouts per minute than any
	// human would, and the honest way to accommodate that is to tell the
	// server "this run is not a normal shopper", not to loosen the default
	// every real deployment inherits.
	//
	// Parsed from RATE_TIER_OVERRIDES as a comma-separated list of
	// name=perMinute:burst, e.g. "checkout=600:200,login=120:40".
	TierRateOverrides map[string]TierRate

	// DailyQuoteAttemptLimit bounds how many quotes (paid OR abandoned) one
	// account may OPEN in a rolling 24h. Abandoned checkouts are free to the
	// buyer but not to the shop: each one is a supplier dry-run plus a stored
	// record, and every later checkout scans the buyer's history. 0 disables.
	DailyQuoteAttemptLimit int

	// BadgerSyncWrites fsyncs every commit. True is the safe default (a
	// power loss cannot drop a committed cashback row); a deployment that has
	// measured its disk and accepted the WAL-recovery window can turn it off
	// for a large write-throughput gain.
	BadgerSyncWrites bool
	// BadgerValueThresholdKB is the size above which a value goes to the
	// value log instead of staying inline in the LSM. The old hardcoded 1 KB
	// pushed EVERY quote record (they carry a supplier request blob) into the
	// value log — a second disk read per Get plus value-log GC forever.
	// 1024 KB keeps this workload LSM-only, which is what the comment in
	// db.go always claimed it was doing.
	BadgerValueThresholdKB int

	// HTTPReadBufferBytes and HTTPWriteBufferBytes are the sizes of the bufio
	// buffers fasthttp uses to read requests and write responses.
	//
	// How expensive these are depends entirely on HTTPReduceMemoryUsage, and
	// getting that relationship backwards was the original mistake here:
	//
	//   With ReduceMemoryUsage OFF, every connection acquires both buffers and
	//   keeps them for its whole lifetime, so these two numbers are multiplied
	//   by the connection count. At 8192/8192 that is 16 KB of buffers per
	//   socket before anything else is counted.
	//
	//   With ReduceMemoryUsage ON, they are not per-connection at all. fasthttp
	//   acquires a reader lazily (server.go:2181) and returns it to a
	//   sync.Pool as soon as the request is drained (server.go:2285:
	//   "if s.ReduceMemoryUsage && br.Buffered() == 0 { releaseReader(...) }"),
	//   and it returns the writer after every flush (server.go:2341). An idle
	//   keep-alive socket therefore holds ZERO bufio buffers; the pool is sized
	//   by the number of requests in flight, which the admission shedder
	//   already bounds, not by the number of users.
	//
	// Measured consequence at 20 000 kernel-verified idle connections:
	// 8192/8192 with reduction on costs 10.45 KB per connection and 4096/2048
	// costs 10.24 KB — a 2% difference, within noise. Shrinking the buffers
	// buys essentially nothing once the pool is in play.
	//
	// So the defaults stay generous, deliberately. ReadBufferSize is a
	// CORRECTNESS CEILING: when a request header does not fit, fasthttp does
	// not grow the buffer, it returns ErrSmallBuffer and the server answers 431
	// "Too big request header" and closes the socket (v1.55 header.go:2304,
	// server.go:2855). The largest legitimate header this deployment can
	// receive — Chrome client hints, a full Accept-Language ladder,
	// Cloudflare's CF-* and X-Forwarded-* set, an analytics cookie jar, the
	// session JWT cookie and the CSRF cookie — measures 1983 bytes, so 8192
	// leaves 4x headroom against a failure mode that would look like a random
	// network error in the access log. WriteBufferSize is only a batching hint
	// (bufio writes large payloads straight through when its buffer is empty,
	// and there is no small-buffer error on the write path), but the largest
	// response header measured here is 939 bytes and a login response carries
	// two Set-Cookie headers on top of that, so 8192 keeps small responses to a
	// single syscall with room to spare.
	//
	// Both remain configurable because the trade does flip if an operator turns
	// reduction off: then these numbers are multiplied by the connection count
	// and 4096/2048 becomes the right choice.
	HTTPReadBufferBytes  int
	HTTPWriteBufferBytes int

	// HTTPReduceMemoryUsage maps to fasthttp's Server.ReduceMemoryUsage. It
	// stops the per-connection Request and Response from retaining their body
	// buffers between requests (Init2 sets keepBodyBuffer=false), so an idle
	// keep-alive socket holds only the two bufio buffers and fasthttp's own
	// structures instead of whatever the largest body it ever served was.
	//
	// It does two things, and the second is worth far more than the first:
	//
	//   1. It stops Request and Response from retaining their body buffers
	//      between requests. Without it, every idle socket keeps the capacity
	//      of the largest body it ever served — and this shop serves catalog
	//      JSON in the hundreds of kilobytes, so a socket that once served a
	//      catalog holds that allocation for its whole idle life.
	//   2. It makes the bufio reader and writer pooled rather than
	//      per-connection, returning both as soon as a request is drained
	//      (server.go:2285, :2341). Idle sockets then hold no I/O buffers at
	//      all, which is why buffer size stops mattering once this is on.
	//
	// fasthttp documents this option for exactly one situation: "Try enabling
	// this option only if the server consumes too much memory serving mostly
	// idle keep-alive connections. This may reduce memory usage by more than
	// 50%." A shop fronted by Cloudflare is that situation — the great
	// majority of origin sockets are open and idle between page views, and
	// they are the sockets whose memory is multiplied by the user count.
	// Measured here it is worth far more than 50%: 28.61 KB per idle
	// connection down to 10.45 KB, a 63% reduction, for 3.5% of throughput.
	//
	// The trade is CPU: body buffers are re-acquired per request instead of
	// reused, which costs allocation and GC work on the active minority of
	// connections. Default on, because this deployment is sized for connection
	// count rather than for request rate, and because the CPU cost lands on
	// requests that are already doing JSON encoding, Badger reads and Argon2.
	HTTPReduceMemoryUsage bool

	// Session cookie delivery. These control how the customer JWT reaches the
	// browser; the JWT itself is unchanged, and Authorization: Bearer keeps
	// working for mobile and server-to-server clients.
	//
	// SessionCookieSecure marks the cookie HTTPS-only. Default true, matching
	// AdminCookieSecure. Set false only for a plaintext local deployment
	// (ALLOW_HTTP_LOCAL), never on a public host: without it the cookie rides
	// any accidental http:// request in cleartext.
	//
	// SessionCookieSameSite is "lax" (default), "strict" or "none". Lax is the
	// load-bearing CSRF defence — the browser does not attach the cookie to a
	// cross-site POST at all, so a forged write arrives unauthenticated and
	// fails before any CSRF logic runs. "strict" also withholds it from a
	// cross-site top-level GET navigation, which breaks arriving at the shop
	// from a link while signed in. "none" is needed only when the frontend and
	// the API sit on different registrable domains, because then even a Lax
	// cookie is cross-site; browsers reject SameSite=None without Secure, so
	// that combination is refused at startup rather than silently degrading to
	// a cookie the browser will not store.
	//
	// SessionCookieDomain scopes the cookie. Empty (default) means host-only,
	// which is the safer choice: a host-only cookie is not sent to sibling
	// subdomains, so a compromised or attacker-registered sibling cannot
	// collect sessions.
	SessionCookieSecure   bool
	SessionCookieSameSite string
	SessionCookieDomain   string

	// MaxConcurrentConns maps to fasthttp's Server.Concurrency, which despite
	// its name is NOT a limit on in-flight requests — it is a limit on
	// simultaneously held connections. fasthttp documents it as "the maximum
	// number of concurrent connections the Server may serve" and, on exceeding
	// it, answers the next connection with 503 "The connection cannot be served
	// because Server.Concurrency limit exceeded" and closes it (v1.55
	// server.go:1785 and :2033).
	//
	// That distinction is the whole reason this is configurable. An idle
	// keep-alive connection consumes a slot here while doing no work at all, so
	// a low value does not shed load — it refuses customers who are merely
	// between page views. A previous revision set this to about 12 000 on the
	// reasoning that it would stop a flood from becoming an unbounded
	// goroutine explosion; what it actually did was put a hard ceiling of
	// ~12 000 simultaneous users on the shop, which is four orders of magnitude
	// below the deployment target.
	//
	// Flood protection is someone else's job and is already implemented: the
	// admission shedder bounds in-flight work, the per-tier rate limiters bound
	// request rate per IP, and IdleTimeout plus MaxKeepaliveDuration bound how
	// long an idle socket may be held. This value is the last-resort backstop
	// that keeps total connection memory inside the box's RAM, so it should be
	// set from that arithmetic — MaxConcurrentConns x per-connection bytes must
	// fit the memory budget — and not from a guess about attack traffic.
	//
	// Defaults to fasthttp's own DefaultConcurrency (262144). At the measured
	// ~10 KB per idle connection that is ~2.6 GB, which is a deliberate choice
	// for a single origin node; deployments fronting the origin with
	// Cloudflare or nginx can hold far fewer upstream connections and may lower
	// it, and deployments sizing a node for a known user count should set it to
	// that count plus headroom.
	MaxConcurrentConns int

	// GCPercent tunes the Go GC trigger (debug.SetGCPercent). Higher trades
	// memory for CPU: at 10M users the GC, not the handler, is often the
	// thing stealing latency. 0 = leave the runtime default (100).
	GCPercent int
	// MemoryLimitMB sets a soft Go heap ceiling (debug.SetMemoryLimit) so a
	// flood degrades into GC pressure instead of an OOM kill. 0 = unset before
	// applyMemoryEnvelope runs, which then derives it from the memory this
	// process is actually allowed (a container's cgroup limit, or the host's
	// RAM). GO_MEMORY_LIMIT_MB always wins.
	MemoryLimitMB int
	// MemoryLimitAuto marks the ceiling as DERIVED rather than configured, so
	// the runtime log can say where the number came from.
	MemoryLimitAuto bool
	// MemoryBudgetMB is the memory this process may use in MB, whatever the
	// source. The database cache sizes and the console's "how much room does
	// this box have" line are computed from it, so one number explains the
	// whole envelope.
	MemoryBudgetMB int
	// MemoryLimitSource says where the budget came from: "cgroup" (a container
	// cap), "host" (a machine's RAM), "env" (set explicitly) or "" (unknown).
	MemoryLimitSource string
	// MaxConnsAuto marks the connection ceiling as DERIVED from the budget
	// rather than configured, so the console can say so plainly.
	MaxConnsAuto bool
	// GCPercentAuto marks GOGC as DERIVED (the RAM-first default) rather than
	// configured, so the runtime log says where 50 came from.
	GCPercentAuto bool

	// MaxKeepaliveDurationSecs bounds how long one TCP connection may keep
	// being reused (enforced by the server handler via "Connection: close",
	// see cmd/server). Without it a single client can pin a connection (and
	// its buffers) indefinitely. 0 = the 75 s default.
	MaxKeepaliveDurationSecs int
	// IdleTimeoutSecs closes connections that stop sending. 0 = ReadTimeout.
	IdleTimeoutSecs int
}

// SiteName is the display brand (same as the host by default).
func (c Config) SiteName() string {
	if c.SiteHost == "" {
		return "shop.nimiqbase.com"
	}
	return c.SiteHost
}

// SiteURL is https://<SiteHost> with no trailing slash.
func (c Config) SiteURL() string {
	return "https://" + c.SiteName()
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

// envIntOrZero is envInt for settings where ZERO is a meaningful value
// ("disabled" / "use the library default") rather than a parse failure.
// Negative values are treated as unset so a typo cannot invert a policy.
// TierRate is one cost tier's token-bucket pricing: how many requests refill
// per minute and how many may be spent in a single burst.
type TierRate struct {
	PerMinute int
	Burst     int
}

// parseTierRateOverrides reads RATE_TIER_OVERRIDES. Malformed entries are
// dropped rather than fatal: a typo in an optional tuning variable must not
// stop the shop from booting, and the compiled default is always a safe value
// to fall back to. Validate() reports the ones that were unusable so the
// operator still finds out.
func parseTierRateOverrides(raw string) map[string]TierRate {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make(map[string]TierRate)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, spec, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		perRaw, burstRaw, ok := strings.Cut(strings.TrimSpace(spec), ":")
		if !ok || name == "" {
			continue
		}
		per, err1 := strconv.Atoi(strings.TrimSpace(perRaw))
		burst, err2 := strconv.Atoi(strings.TrimSpace(burstRaw))
		if err1 != nil || err2 != nil || per <= 0 || burst <= 0 || per > 10_000_000 || burst > 1_000_000 {
			continue
		}
		out[name] = TierRate{PerMinute: per, Burst: burst}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultDailyQuoteAttemptLimit is the out-of-the-box rolling 24h ceiling on
// how many checkouts one account may OPEN, paid or abandoned.
//
// 200/day is one every seven minutes around the clock with no sleep — far past
// anything a real shopper does, and far below what it costs the shop to absorb
// an abandoned cart (a supplier dry-run, a stored record, a settlement sweep).
// It is a constant rather than an inline literal because Load() has to compare
// the resolved value against it to tell "operator chose this" from "we did".
const defaultDailyQuoteAttemptLimit = 200

// envIsSet reports whether a variable was given at all, as opposed to being
// present-but-empty. Load uses it to tell "the operator chose this" from "this
// is our default", which matters wherever two settings have to stay consistent
// with each other: a contradiction the operator typed is a boot error, while a
// contradiction produced by our own default is ours to resolve.
func envIsSet(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}

func envIntOrZero(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

// Purchase ceilings explicitly support zero (disabled). Unlike positive-only
// rate settings, invalid values must fail Validate, not silently change policy.
func envBudgetUSD(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return math.NaN()
	}
	return value
}
func envOrderLimit(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
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

// allowedOriginsFromEnv resolves the CORS allowlist from ONE place:
// ALLOWED_ORIGINS wins when set (comma list). Otherwise FRONTEND_URL (a
// single origin such as https://shop.example.com or http://localhost:8085)
// becomes the allowlist — one line in .env fixes every CORS problem.
func allowedOriginsFromEnv() []string {
	var list []string
	if v := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS")); v != "" {
		list = origins(v)
	} else if v := strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_URL")), "/"); v != "" {
		list = []string{v}
	}
	// GitHub Pages preview of this repo (static frontend talking to the public API).
	const pages = "https://emrealt34.github.io"
	seen := false
	for _, o := range list {
		if o == pages {
			seen = true
			break
		}
	}
	if !seen {
		list = append(list, pages)
	}
	return list
}

func normalizeSiteHost(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, ".")
	if s == "" {
		return "shop.nimiqbase.com"
	}
	return s
}

// applyMemoryEnvelope derives the process's memory budget when the deployment
// did not set one explicitly.
//
// The number decides the Go heap's soft ceiling (so a flood turns into GC
// pressure instead of an OOM kill), the Badger cache sizes, and the console's
// line about how much room the box has. Deriving it from the CGROUP limit —
// not from the host's RAM — is what stops a container from being killed while
// reporting a perfectly healthy heap.
func (c *Config) applyMemoryEnvelope() {
	if c.MemoryLimitMB > 0 {
		c.MemoryLimitSource = "env"
		c.MemoryBudgetMB = c.MemoryLimitMB
		return
	}
	// GOGC 50 unless the deployment asked for something else: the owner's
	// request is less memory, and halving the heap's growth step is the single
	// cheapest way to get it. On this shop's traffic the extra collections cost
	// nothing measurable, and GO_GC_PERCENT still overrides it.
	if c.GCPercent == 0 {
		c.GCPercent = 50
		c.GCPercentAuto = true
	}
	bytes, source := memlimit.LimitBytes()
	if bytes <= 0 {
		// Nothing readable (no cgroup, no /proc/meminfo). The parallel
		// deployment change to this code used a flat 150 MB here; it is kept
		// as the last resort because a soft limit that is merely conservative
		// costs GC work, while no limit at all costs an OOM kill. It is a
		// FLOOR, never a cap on a bigger box: a readable budget always wins.
		c.MemoryLimitMB = 150
		c.MemoryBudgetMB = 150
		c.MemoryLimitAuto = true
		c.MemoryLimitSource = "default"
		return
	}
	// Three quarters of the allowance: the runtime needs headroom above the
	// heap for stacks, scheduler structures and the kernel's own accounting of
	// this process, and a soft limit at 100% would sit where the hard one
	// already is.
	budget := int(bytes >> 20 * 3 / 4)
	if budget < 32 {
		budget = 32
	}
	c.MemoryLimitMB = budget
	c.MemoryBudgetMB = budget
	c.MemoryLimitAuto = true
	c.MemoryLimitSource = source
}

// applyConnectionCeiling sizes fasthttp's held-connection cap from the memory
// envelope when the deployment did not say otherwise.
//
// This is the setting the config comment calls out by name ("MaxConcurrentConns
// x per-connection bytes must fit the memory budget"): the default of 262144 is
// 2.6 GB of socket buffers at the measured ~10 KB per idle keep-alive
// connection, which on a small container is more memory than the container has
// — the box would be killed long before the cap ever became a real limit.
// Deriving it means the ceiling can only ever be as large as the box, which is
// what the number is for.
//
// Connections are allowed a third of the budget: the rest has to hold the Go
// heap, the database caches and the kernel's accounting of this process.
// MAX_CONCURRENT_CONNS still wins when set; the floor stays at 1024 so a
// mis-sized budget can never lock everyone out.
func (c *Config) applyConnectionCeiling() {
	if strings.TrimSpace(os.Getenv("MAX_CONCURRENT_CONNS")) != "" || c.MemoryBudgetMB <= 0 {
		return
	}
	const perConnBytes = 10 * 1024 // measured; see the Server.Concurrency note
	conns := (c.MemoryBudgetMB / 3) * 1024 * 1024 / perConnBytes
	if conns < 1024 {
		conns = 1024
	}
	if conns > 262144 {
		conns = 262144
	}
	if conns == c.MaxConcurrentConns {
		return
	}
	c.MaxConcurrentConns = conns
	c.MaxConnsAuto = true
}

func Load() Config {
	host := normalizeSiteHost(env("SITE_HOST", "shop.nimiqbase.com"))
	cfg := Config{
		SiteHost:   host,
		ListenAddr: env("LISTEN_ADDR", ":8084"), StaticDir: strings.TrimSpace(os.Getenv("STATIC_DIR")), BadgerDir: env("BADGER_DIR", "./data/badger"),
		JWTSecret: env("JWT_SECRET", ""), JWTExpiryMins: envInt("JWT_EXPIRY_MINS", 60*24*30),
		AllowedOrigins: allowedOriginsFromEnv(), FrontendURL: strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_URL")), "/"), MaxRequestBodyBytes: envInt("MAX_REQUEST_BODY_BYTES", 1<<20), MaxOrderQuantity: envInt("MAX_ORDER_QUANTITY", 100),
		PriceFeedURL: env("PRICE_FEED_URL", "https://api.coingecko.com/api/v3/simple/price?ids=nimiq-2&vs_currencies=usd"),
		// Default 2: at least TWO independent price feeds are used for the
		// informational NIM estimate shown in the UI (never the payment
		// authority — the supplier's coin amount is authoritative).
		OracleMinSources: envInt("ORACLE_MIN_SOURCES", 2), OracleMaxSpreadBps: int64(envInt("ORACLE_MAX_SPREAD_BPS", 250)),
		// Published purchase budgets (owner, 2026-10-05): $500/day and
		// $1000/month — the same numbers the staking spend caps carry, so a
		// buyer never reads two different ceilings on two screens. The daily
		// ORDER ceiling stays a separate knob (DAILY_ORDER_LIMIT).
		DailyOrderLimit: envOrderLimit("DAILY_ORDER_LIMIT", 100), DailySpendLimitUSD: envBudgetUSD("DAILY_SPEND_LIMIT_USD", 500),
		MonthlySpendLimitUSD: envBudgetUSD("MONTHLY_SPEND_LIMIT_USD", 1000),
		CRBaseURL:            env("CRYPTOREFILLS_BASE_URL", "https://api.cryptorefills.com"), CRPartnerID: os.Getenv("CRYPTOREFILLS_PARTNER_ID"), CRAppVersion: env("CRYPTOREFILLS_APP_VERSION", "nimshop/1.0"), CRUserAgent: env("CRYPTOREFILLS_USER_AGENT", "nimshop/1.0 +https://"+host), CRWebhookKey: os.Getenv("CRYPTOREFILLS_WEBHOOK_KEY"), PublicWebhookBaseURL: strings.TrimRight(os.Getenv("PUBLIC_WEBHOOK_BASE_URL"), "/"),
		CRPollSecs: envInt("WORKER_ORDER_POLL_SECS", 5), CRStaleSecs: envInt("WORKER_ORDER_STALE_SECS", 300), TestMode: envBool("TEST_MODE", false),
		AdminUsername: os.Getenv("ADMIN_USERNAME"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), AdminPasswordHash: os.Getenv("ADMIN_PASSWORD_HASH"), AdminTOTPSecret: os.Getenv("ADMIN_TOTP_SECRET"), AdminSessionSecret: os.Getenv("ADMIN_SESSION_SECRET"),
		// ADMIN_USERNAME + ADMIN_PASSWORD is the simple production/local
		// login.
		AdminDevUsername: env("ADMIN_USERNAME", ""), AdminDevPassword: env("ADMIN_PASSWORD", ""),
		AdminSessionMins: envInt("ADMIN_SESSION_MINS", 8*60), AdminCookieSecure: envBool("ADMIN_COOKIE_SECURE", true), AdminBootstrapToken: os.Getenv("ADMIN_BOOTSTRAP_TOKEN"),
		AllowHTTPLocal:        envBool("ALLOW_HTTP_LOCAL", false),
		TrustProxy:            envBool("TRUST_PROXY", true),
		TrustedProxyCIDRs:     origins(env("TRUSTED_PROXY_CIDRS", strings.Join(loopback.DefaultProxyCIDRs(), ","))),
		ProxyHeaderMode:       env("PROXY_HEADER_MODE", "forwarded"),
		ForwardedHeaderSecret: os.Getenv("FORWARDED_HEADER_SECRET"),
		RateLimitPerMinute:    envInt("RATE_LIMIT_PER_MINUTE", 3600),
		RateLimitBurst:        envInt("RATE_LIMIT_BURST", 1200),
		CRQueueMax:            envInt("CRYPTOREFILLS_QUEUE_MAX", 2000),
		CRQueuePerActorMax:    envInt("CRYPTOREFILLS_QUEUE_PER_ACTOR_MAX", 100),
		CRActorPerMinute:      envInt("CRYPTOREFILLS_ACTOR_REQUESTS_PER_MINUTE", 600),
		CRActorBurst:          envInt("CRYPTOREFILLS_ACTOR_BURST", 120),

		// --- Outbound email ---
		// Mailtrap (internal/mailtrap) is the ONLY mail transport — the generic
		// SMTP client and the SMS sender were removed outright. There is
		// nothing to configure here: the transport reads MAILTRAP_* env vars
		// itself (token, from-address, sandbox); see mailtrap.ConfigFromEnv.
		// A failed delivery is logged but never blocks order flow.

		NimiqRPCURL:           strings.TrimRight(env("NIMIQ_RPC_URL", "https://rpc.nimiqwatch.com"), "/"),
		NimiqRPCURL2:          nimiqRPCURL2(),
		CashbackEnabled:       envBool("CASHBACK_ENABLED", false),
		CashbackWalletSeed:    strings.TrimSpace(os.Getenv("CASHBACK_WALLET_SEED")),
		CashbackWalletAccount: envInt("CASHBACK_WALLET_ACCOUNT", 0),
		CashbackNetwork:       strings.ToLower(env("CASHBACK_NETWORK", "mainnet")),
		CashbackFeeLuna:       envInt("CASHBACK_FEE_LUNA", 1),
		CashbackCodes:         strings.TrimSpace(os.Getenv("CASHBACK_CODES")),

		PoolAPIURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("POOL_API_URL")), "/"),
		PoolValidatorAddress: strings.TrimSpace(os.Getenv("POOL_VALIDATOR_ADDRESS")),
		PoolStakeCacheTTL:    envInt("POOL_STAKE_CACHE_TTL_SECONDS", 0),
		PoolStakeTimeout:     envInt("POOL_STAKE_TIMEOUT_SECONDS", 0),
		PoolFeedAPIKey:       strings.TrimSpace(os.Getenv("POOL_FEED_API_KEY")),
		PoolFeedPollSeconds:  envInt("POOL_FEED_POLL_SECONDS", 30),

		NotifyWalletEnabled: envBool("NOTIFY_WALLET_ENABLED", false),
		NotifyWalletSeed:    notifyWalletSeed(),
		NotifyWalletNetwork: strings.ToLower(env("NOTIFY_WALLET_NETWORK", env("CASHBACK_NETWORK", "mainnet"))),
		NotifyWalletFeeLuna: envInt("NOTIFY_WALLET_FEE_LUNA", 1),

		// USDT (Polygon) payment rail — always enabled; network fixed to
		// Polygon (Matic) in handlers (no ENABLE_USDT_PAYMENT / USDT_NETWORK env).
		USDTCoin: env("USDT_COIN", "USDT"),

		// Cashback burn wallet destination
		BurnNimAddress: strings.TrimSpace(env("BURN_NIM_ADDRESS", "NQ07 0000 0000 0000 0000 0000 0000 0000 0000")),

		// --- Edge hardening & scale envelope ---
		MaxInFlight:              envIntOrZero("MAX_IN_FLIGHT_REQUESTS", 0),
		ResponseCompression:      envBool("RESPONSE_COMPRESSION", true),
		CompressLevel:            envIntOrZero("RESPONSE_COMPRESS_LEVEL", 5),
		CompressMinBytes:         envIntOrZero("RESPONSE_COMPRESS_MIN_BYTES", 1024),
		HSTS:                     envBool("HSTS_ENABLED", true),
		HSTSPreload:              envBool("HSTS_PRELOAD", true),
		ServerHeader:             env("SERVER_HEADER", "nimshop"),
		CDNCachePublic:           envBool("CDN_CACHE_PUBLIC_API", true),
		DailyQuoteAttemptLimit:   envIntOrZero("DAILY_QUOTE_ATTEMPT_LIMIT", defaultDailyQuoteAttemptLimit),
		TierRateOverrides:        parseTierRateOverrides(os.Getenv("RATE_TIER_OVERRIDES")),
		BadgerSyncWrites:         envBool("BADGER_SYNC_WRITES", true),
		BadgerValueThresholdKB:   envIntOrZero("BADGER_VALUE_THRESHOLD_KB", 1024),
		GCPercent:                envIntOrZero("GO_GC_PERCENT", 0),
		MemoryLimitMB:            envIntOrZero("GO_MEMORY_LIMIT_MB", 0),
		MaxKeepaliveDurationSecs: envIntOrZero("MAX_KEEPALIVE_DURATION_SECS", 75),
		IdleTimeoutSecs:          envIntOrZero("IDLE_TIMEOUT_SECS", 30),
		HTTPReadBufferBytes:      envIntOrZero("HTTP_READ_BUFFER_BYTES", 8192),
		HTTPWriteBufferBytes:     envIntOrZero("HTTP_WRITE_BUFFER_BYTES", 8192),
		HTTPReduceMemoryUsage:    envBool("HTTP_REDUCE_MEMORY_USAGE", true),
		SessionCookieSecure:      envBool("SESSION_COOKIE_SECURE", true),
		SessionCookieSameSite:    strings.ToLower(strings.TrimSpace(env("SESSION_COOKIE_SAME_SITE", "lax"))),
		SessionCookieDomain:      strings.TrimSpace(os.Getenv("SESSION_COOKIE_DOMAIN")),
		MaxConcurrentConns:       envIntOrZero("MAX_CONCURRENT_CONNS", 262144),
	}
	// Reconcile the quote-attempt ceiling with the daily ORDER limit.
	//
	// The attempt ceiling counts abandoned checkouts as well as paid ones, so
	// it has to be the LOOSER of the two or it silently overrides the order
	// limit the operator chose. When DAILY_QUOTE_ATTEMPT_LIMIT was left at our
	// default and the operator raised DAILY_ORDER_LIMIT above it, follow the
	// operator instead of refusing to start: a deployment that sets
	// DAILY_ORDER_LIMIT=1000000 is deliberately running without a purchase
	// cap (load harnesses and very large shops both do this), and turning that
	// into a boot failure would be our default breaking their configuration.
	// An EXPLICIT contradictory pair is still a hard error, because then the
	// operator has told us two incompatible things and guessing is worse than
	// stopping.
	if !envIsSet("DAILY_QUOTE_ATTEMPT_LIMIT") && cfg.DailyQuoteAttemptLimit == defaultDailyQuoteAttemptLimit &&
		cfg.DailyOrderLimit > cfg.DailyQuoteAttemptLimit {
		cfg.DailyQuoteAttemptLimit = cfg.DailyOrderLimit
	}

	// The tunnel-origin CORS shortcut is a development affordance. It follows
	// TEST_MODE / ALLOW_HTTP_LOCAL unless the operator states otherwise, so a
	// local preview keeps working and a public host never silently inherits it.
	cfg.AllowTunnelOrigins = envBool("ALLOW_TUNNEL_ORIGINS", cfg.TestMode || cfg.AllowHTTPLocal)
	if cfg.AdminSessionSecret == "" && cfg.AdminDevMode() {
		cfg.AdminSessionSecret = processSessionSecret()
	}
	// One place decides how much memory this process may use, and everything
	// that is sized in memory reads it from here.
	cfg.applyMemoryEnvelope()
	cfg.applyConnectionCeiling()

	return cfg
}

func processSessionSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is exceptionally serious; keep startup from
		// silently using a predictable session signing key.
		panic("unable to generate admin session secret: " + err.Error())
	}
	return hex.EncodeToString(buf)
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
	// mock stacks run plain HTTP on this machine. Mirrors the ALLOWED_ORIGINS
	// rule; never applies to public hosts.
	if u.Scheme == "http" && allowHTTPLocal && loopback.IsHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s must be an absolute https URL", name)
}

var allowHTTPLocal bool

func allowedOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid ALLOWED_ORIGINS entry")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && loopback.IsHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("ALLOWED_ORIGINS entries must use HTTPS (except loopback development origins)")
}
func (c Config) Validate() error {
	if c.DailyOrderLimit < 0 {
		return fmt.Errorf("DAILY_ORDER_LIMIT must be a non-negative integer (0 disables the ceiling)")
	}
	for name, value := range map[string]float64{"DAILY_SPEND_LIMIT_USD": c.DailySpendLimitUSD, "MONTHLY_SPEND_LIMIT_USD": c.MonthlySpendLimitUSD} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 9e12 || (value > 0 && value < 0.000001) {
			return fmt.Errorf("%s must be 0 (disabled) or a finite USD amount from 0.000001 through 9000000000000", name)
		}
	}
	if err := c.ClientIPPolicy().Validate(); err != nil {
		return err
	}
	if !c.TrustProxy && (c.ForwardedHeaderSecret != "" || c.ProxyHeaderMode == "cloudflare") {
		return fmt.Errorf("proxy identity settings require TRUST_PROXY=true")
	}

	// TEST MODE (sandbox) semantics: the whole shop runs NORMALLY — real
	// catalog, real checkout validation and pricing, real Mailtrap emails,
	// the real cashback engine and tree contributions — but every PAYMENT
	// is simulated: quotes attach simulated invoices (no supplier order is
	// ever created), cashback payouts and wallet memos never touch a chain.
	// Cashback/notify stay configurable: their seeds are simply unused for
	// test rows (payouts run simulated through the same worker). The
	// frontend flag (public/config.js TEST_MODE) must be set to match so
	// customers see the simulated pay button.

	allowHTTPLocal = c.AllowHTTPLocal
	if host := c.SiteName(); len(host) < 3 || len(host) > 253 || strings.ContainsAny(host, " \t\n:/?#") {
		return fmt.Errorf("SITE_HOST must be a hostname such as shop.nimiqbase.com")
	}
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 random bytes; refusing to start")
	}
	if strings.HasPrefix(c.JWTSecret, "REPLACE_WITH_") || strings.HasPrefix(c.JWTSecret, "CHANGE_THIS_") {
		return fmt.Errorf("JWT_SECRET is still an example placeholder; generate a private random secret")
	}
	if c.JWTExpiryMins > 60*24*30 {
		return fmt.Errorf("JWT_EXPIRY_MINS must not exceed 30 days")
	}
	if c.MaxRequestBodyBytes > 4<<20 || c.MaxOrderQuantity > 100 {
		return fmt.Errorf("configured request/order limits are unsafe")
	}
	if c.CRQueueMax < 1 || c.CRQueueMax > 10000 || c.CRQueuePerActorMax < 1 || c.CRQueuePerActorMax > c.CRQueueMax || c.CRActorPerMinute < 1 || c.CRActorPerMinute > 1200 || c.CRActorBurst < 1 || c.CRActorBurst > c.CRActorPerMinute {
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
	if c.FrontendURL != "" {
		if err := allowedOrigin(c.FrontendURL); err != nil {
			return fmt.Errorf("FRONTEND_URL must be an https origin like https://shop.example.com (plain http is accepted for loopback development hosts only): %w", err)
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

	legacyFields := 0
	for _, value := range []string{c.AdminUsername, c.AdminPasswordHash, c.AdminTOTPSecret} {
		if value != "" {
			legacyFields++
		}
	}
	simpleFields := 0
	for _, value := range []string{c.AdminUsername, c.AdminPassword} {
		if value != "" {
			simpleFields++
		}
	}
	if legacyFields != 0 && legacyFields != 3 && c.AdminPassword == "" {
		return fmt.Errorf("legacy admin credentials require ADMIN_USERNAME, ADMIN_PASSWORD_HASH, and ADMIN_TOTP_SECRET together")
	}
	if c.AdminPassword != "" && (c.AdminUsername == "" || c.AdminPasswordHash != "" || c.AdminTOTPSecret != "") {
		return fmt.Errorf("simple admin login uses only ADMIN_USERNAME and ADMIN_PASSWORD; do not mix legacy hash/TOTP fields")
	}
	if simpleFields == 1 && legacyFields != 3 {
		return fmt.Errorf("ADMIN_USERNAME and ADMIN_PASSWORD must be configured together")
	}
	if strings.HasPrefix(c.AdminPassword, "CHANGE_THIS_") || strings.HasPrefix(c.AdminPassword, "REPLACE_WITH_") {
		return fmt.Errorf("ADMIN_PASSWORD is still an example placeholder; choose a private password")
	}
	if c.AdminPassword != "" && (len(c.AdminUsername) < 3 || len(c.AdminUsername) > 64) {
		return fmt.Errorf("ADMIN_USERNAME must be 3-64 characters")
	}
	if c.AdminPassword != "" && (len(c.AdminPassword) == 0 || len(c.AdminPassword) > 1024) {
		return fmt.Errorf("ADMIN_PASSWORD must be 1-1024 characters")
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
	if c.NotifyWalletEnabled {
		seed := strings.TrimPrefix(c.NotifyWalletSeed, "0x")
		if len(seed) != 64 {
			return fmt.Errorf("NOTIFY_WALLET_ENABLED requires NOTIFY_WALLET_SEED (or CASHBACK_WALLET_SEED) as a 32-byte hex seed (64 hex chars) or a BIP39 recovery phrase")
		}
		if c.NotifyWalletNetwork != "mainnet" && c.NotifyWalletNetwork != "testnet" {
			return fmt.Errorf("NOTIFY_WALLET_NETWORK must be mainnet or testnet")
		}
		if c.NotifyWalletFeeLuna < 0 {
			return fmt.Errorf("NOTIFY_WALLET_FEE_LUNA cannot be negative")
		}
		if c.NimiqRPCURL == "" {
			return fmt.Errorf("NIMIQ_RPC_URL is required when NOTIFY_WALLET_ENABLED is set")
		}
	}
	if c.CashbackEnabled {
		seed := strings.TrimPrefix(c.CashbackWalletSeed, "0x")
		if len(seed) != 64 {
			return fmt.Errorf("CASHBACK_ENABLED requires CASHBACK_WALLET_SEED as a 32-byte hex seed (64 hex chars) or a BIP39 recovery phrase")
		}
		if c.CashbackNetwork != "mainnet" && c.CashbackNetwork != "testnet" {
			return fmt.Errorf("CASHBACK_NETWORK must be mainnet or testnet")
		}
		if c.CashbackWalletAccount < 0 || c.CashbackWalletAccount > 100 {
			return fmt.Errorf("CASHBACK_WALLET_ACCOUNT must be an account index 0-100")
		}
		if c.NimiqRPCURL == "" {
			return fmt.Errorf("NIMIQ_RPC_URL is required when cashback is enabled")
		}
		if err := httpsURL("NIMIQ_RPC_URL", c.NimiqRPCURL); err != nil {
			return err
		}
		if c.NimiqRPCURL2 != "" {
			if err := httpsURL("NIMIQ_RPC_URL_2", c.NimiqRPCURL2); err != nil {
				return err
			}
		}
	}
	if _, err := cashbackcode.ParseTable(c.CashbackCodes); err != nil {
		return fmt.Errorf("CASHBACK_CODES: %w", err)
	}

	// --- Edge hardening & scale envelope ---
	if c.AllowTunnelOrigins && !c.TestMode && !c.AllowHTTPLocal {
		// Not a hard failure (an operator may run a public preview tunnel on
		// purpose) but it is the single most dangerous flag in the file, so
		// it is refused unless the deployment already declares itself
		// non-production. See applyCORS in cmd/server/main.go.
		return fmt.Errorf("ALLOW_TUNNEL_ORIGINS=true is unsafe on a production host: any *.trycloudflare.com tunnel would be granted credentialed CORS access. Set TEST_MODE=true for a sandbox, or unset ALLOW_TUNNEL_ORIGINS")
	}
	if c.MaxInFlight < 0 || c.MaxInFlight > 10_000_000 {
		return fmt.Errorf("MAX_IN_FLIGHT_REQUESTS must be 0 (auto) or at most 10000000")
	}
	if c.CompressLevel < 0 || c.CompressLevel > 9 {
		return fmt.Errorf("RESPONSE_COMPRESS_LEVEL must be 0-9")
	}
	if c.CompressMinBytes < 0 || c.CompressMinBytes > 1<<20 {
		return fmt.Errorf("RESPONSE_COMPRESS_MIN_BYTES must be 0-1048576")
	}
	for name, rate := range c.TierRateOverrides {
		if rate.Burst > rate.PerMinute {
			return fmt.Errorf("RATE_TIER_OVERRIDES: tier %q burst (%d) cannot exceed its per-minute rate (%d) — the burst is the ceiling on a single instant, the rate is what refills it", name, rate.Burst, rate.PerMinute)
		}
	}
	if c.DailyQuoteAttemptLimit < 0 || c.DailyQuoteAttemptLimit > 1_000_000 {
		return fmt.Errorf("DAILY_QUOTE_ATTEMPT_LIMIT must be 0 (disabled) or at most 1000000")
	}
	if c.DailyQuoteAttemptLimit > 0 && c.DailyOrderLimit > 0 && c.DailyQuoteAttemptLimit < c.DailyOrderLimit {
		// Load() already auto-raised the ceiling when it was still our
		// default, so reaching here means the operator typed both numbers and
		// they contradict each other.
		return fmt.Errorf("DAILY_QUOTE_ATTEMPT_LIMIT (%d) must be >= DAILY_ORDER_LIMIT (%d): the attempt ceiling counts abandoned checkouts too, so it has to be the looser of the two", c.DailyQuoteAttemptLimit, c.DailyOrderLimit)
	}
	if c.BadgerValueThresholdKB < 0 || c.BadgerValueThresholdKB > 1024 {
		return fmt.Errorf("BADGER_VALUE_THRESHOLD_KB must be 0-1024 (1024 = keep every value inline in the LSM)")
	}
	if c.GCPercent < -1 || c.GCPercent > 2000 {
		return fmt.Errorf("GO_GC_PERCENT must be -1 (off) or 0-2000")
	}
	if c.MemoryLimitMB < 0 || c.MemoryLimitMB > 1<<20 {
		return fmt.Errorf("GO_MEMORY_LIMIT_MB must be 0 (unset) or at most 1048576")
	}
	// The floors here are the point of the check. A too-small read buffer does
	// not degrade gracefully: it makes the server answer 431 to legitimate
	// requests, which is an outage an operator would struggle to diagnose
	// because the requests look perfectly well-formed in the access log. The
	// measured worst-case header is ~1983 bytes, so 2048 is an absolute floor
	// and 4096 is what should actually be used.
	if c.HTTPReadBufferBytes < 2048 || c.HTTPReadBufferBytes > 1<<20 {
		return fmt.Errorf("HTTP_READ_BUFFER_BYTES must be 2048-1048576: below 2048 a real browser request (Chrome client hints + Cloudflare headers + session cookies, measured ~1983 bytes) is refused with 431")
	}
	// The floor matters more than the ceiling. Below roughly a thousand, the
	// server starts answering 503 to ordinary customers under normal load, and
	// because the refusal happens at connection level it looks like a network
	// fault rather than a configuration fault. The ceiling is a RAM guard: at
	// the measured per-connection cost, a million slots is already ~10 GB.
	if c.MaxConcurrentConns < 1024 || c.MaxConcurrentConns > 1<<24 {
		return fmt.Errorf("MAX_CONCURRENT_CONNS must be 1024-16777216: this is fasthttp's cap on simultaneously held connections, not on in-flight requests, so a low value answers 503 to idle customers")
	}
	switch c.SessionCookieSameSite {
	case "", "lax", "strict", "none":
	default:
		return fmt.Errorf("SESSION_COOKIE_SAME_SITE must be lax, strict or none (got %q)", c.SessionCookieSameSite)
	}
	// Browsers refuse to store SameSite=None without Secure, so accepting this
	// combination would produce a session cookie that silently does not exist
	// and a shop where nobody can stay signed in. Failing at startup points at
	// the real mistake.
	if c.SessionCookieSameSite == "none" && !c.SessionCookieSecure {
		return fmt.Errorf("SESSION_COOKIE_SAME_SITE=none requires SESSION_COOKIE_SECURE=true: browsers reject a SameSite=None cookie that is not Secure")
	}
	if strings.HasPrefix(c.SessionCookieDomain, ".") {
		return fmt.Errorf("SESSION_COOKIE_DOMAIN must not start with a dot: a leading dot widens the cookie to every subdomain, and an empty value (host-only) is the safe default")
	}
	if c.HTTPWriteBufferBytes < 512 || c.HTTPWriteBufferBytes > 1<<20 {
		return fmt.Errorf("HTTP_WRITE_BUFFER_BYTES must be 512-1048576: below 512 a normal response header no longer fits in one write")
	}
	if c.MaxKeepaliveDurationSecs < 0 || c.MaxKeepaliveDurationSecs > 3600 {
		return fmt.Errorf("MAX_KEEPALIVE_DURATION_SECS must be 0-3600")
	}
	if c.IdleTimeoutSecs < 0 || c.IdleTimeoutSecs > 600 {
		return fmt.Errorf("IDLE_TIMEOUT_SECS must be 0-600")
	}
	if strings.ContainsAny(c.ServerHeader, "\r\n") {
		return fmt.Errorf("SERVER_HEADER must not contain newlines")
	}
	return nil
}

// nimiqRPCURL2 is empty by default: a SINGLE provider (NIMIQ_RPC_URL =
// https://rpc.nimiqwatch.com) is the supported setup. Set NIMIQ_RPC_URL_2
// explicitly to enable a second, cross-checking provider.
// notifyWalletSeed falls back to the cashback wallet seed so a single
// funded key powers both on-chain features.
func notifyWalletSeed() string {
	if s := strings.TrimSpace(os.Getenv("NOTIFY_WALLET_SEED")); s != "" {
		return s
	}
	return strings.TrimSpace(os.Getenv("CASHBACK_WALLET_SEED"))
}

func nimiqRPCURL2() string {
	v, set := os.LookupEnv("NIMIQ_RPC_URL_2")
	if !set {
		return "" // single-provider mode (NimiqWatch only) unless explicitly configured
	}
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

// IsLocalSupplierMock cannot be bypassed by a hostname suffix or URL userinfo.
// It is checked both at boot and at the test-only request boundary.
func (c Config) IsLocalSupplierMock() bool {
	u, err := url.Parse(c.CRBaseURL)
	if err != nil || !c.AllowHTTPLocal || u.Scheme != "http" || u.User != nil {
		return false
	}
	return loopback.IsHost(u.Hostname())
}

func (c Config) ClientIPPolicy() clientip.Policy {
	return clientip.Policy{TrustedProxyCIDRs: c.TrustedProxyCIDRs, HeaderMode: c.ProxyHeaderMode, SharedSecret: c.ForwardedHeaderSecret}
}
