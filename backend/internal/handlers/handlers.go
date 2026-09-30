package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
	"nimiqshop/internal/config"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/httpx"
	"nimiqshop/internal/i18n"
	"nimiqshop/internal/mailtrap"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/notification"
	"nimiqshop/internal/poolstake"
	"nimiqshop/internal/presence"
)

type Handlers struct {
	Store  *db.Store
	Cfg    config.Config
	CR     *cryptorefills.Client
	Oracle *nimiq.MultiSourceOracle
	// NIMRPC is used only for public, read-only chain data such as the tree
	// donation wallet balance. It never holds keys or signs transactions.
	NIMRPC   *nimiq.Client
	Presence *presence.Tracker
	// WalletNotifier sends the 1-Luna memo channel (order ready, cashback,
	// unused-code nudge). Nil/disabled is safe: every call is a no-op.
	// (Email is not here on purpose: Mailtrap below is the one mail
	// transport — the old SMTP/SMS gift client was removed.)
	WalletNotifier *notification.Notifier
	// Pool answers "how much NIM has this buyer delegated to our
	// validator?" for the staker cashback ladder. Nil means the feature is
	// off and every buyer earns the base rate.
	Pool *poolstake.Client
	// Mail is the Mailtrap email transport — the ONLY one: the SMTP client
	// and the SMS sender are gone. It carries the fulfillment gift note
	// (settlement tracker + admin retry), the operator's direct emails and
	// the admin "send a test email" surface. Nil/disabled is safe: every
	// caller returns a 503 with a config hint instead of attempting a send.
	Mail *mailtrap.Client
	// NIMUSDPrice is the display price source for the single-ledger views
	// (available $, boost %). Default: the multi-source oracle. Tests
	// override it with a fixed quote so no network is needed.
	NIMUSDPrice func(ctx context.Context) (float64, error)
	// Admission is the process-wide in-flight shedder, wired at boot. It is
	// read-only from here: /api/health reports its counters so an operator
	// can see load approaching the ceiling before requests start failing.
	Admission *httpx.Admission
	// CDNCacheSafe records whether the deployment's CORS answer is
	// deterministic (zero or one configured origin). Only then may a
	// URL-keyed shared cache store a public response: otherwise the cached
	// Access-Control-Allow-Origin of one origin would be served to another.
	CDNCacheSafe bool
	cache        *ttlCache
	flights      *flightGroup
}

// setCachePolicy stamps the cache directives for one response. It is the ONE
// place the storefront's cacheability is decided, so a public endpoint can
// never accidentally ship a per-user body to a CDN (or vice versa).
func (h *Handlers) setCachePolicy(ctx *fasthttp.RequestCtx, p httpx.CachePolicy) {
	httpx.ApplyCache(ctx, p, h.CDNCacheSafe)
}

// publicCache is the standard profile for user-independent GETs: the browser
// may keep it for maxAge seconds and a CDN may serve a stale copy for up to
// swr more while it revalidates, so a cold cache is a background job rather
// than a user-visible stall.
func (h *Handlers) publicCache(ctx *fasthttp.RequestCtx, maxAge, swr int) {
	h.setCachePolicy(ctx, httpx.PublicCache(maxAge, swr))
}

// PublicCache is the exported form used by the route table in cmd/server,
// where cacheability is declared once per endpoint rather than sprinkled
// through handler bodies. Declaring it at the route is deliberate: a reader
// auditing "can a CDN serve user A's response to user B?" should be able to
// answer it from the routing block alone, without opening forty handlers.
func (h *Handlers) PublicCache(ctx *fasthttp.RequestCtx, maxAge, swr int) {
	h.publicCache(ctx, maxAge, swr)
}

func New(store *db.Store, cfg config.Config, cr *cryptorefills.Client) *Handlers {
	o := nimiq.NewMultiSourceOracle(cfg.OracleMinSources, cfg.OracleMaxSpreadBps)
	return &Handlers{
		Store:  store,
		Cfg:    cfg,
		CR:     cr,
		Oracle: o,
		NIMRPC: nimiq.NewClient(cfg.NimiqRPCURL, cfg.NimiqRPCURL2),
		NIMUSDPrice: func(ctx context.Context) (float64, error) {
			if cfg.TestMode {
				return 1, nil
			} // artificial local mock rate
			q, err := o.NIMUSD(ctx)
			if err != nil {
				return 0, err
			}
			return q.MedianUSD, nil
		},
		cache:   newTTLCache(10 * time.Minute),
		flights: &flightGroup{},
	}
}

// PreloadCatalogSnapshots warms the in-memory catalog caches from the
// Badger disk snapshots at boot. Called before the HTTP listener opens:
// even if the supplier is down or 429-throttled, the very first browser
// request after a restart is served a full storefront from disk.
func (h *Handlers) PreloadCatalogSnapshots() {
	type entry struct {
		memKey string
		decode func(json.RawMessage) (interface{}, error)
		ttl    time.Duration
	}
	entries := []entry{
		{"cr:brands:TR", decodeBrands, 6 * time.Hour},
		{"cr:brands:US", decodeBrands, 6 * time.Hour},
		{"cr:brands:DE", decodeBrands, 6 * time.Hour},
		{"cr:brands:GB", decodeBrands, 6 * time.Hour},
		{"cr:payment_vias", decodeVias, 12 * time.Hour},
	}
	loaded := 0
	for _, e := range entries {
		if v, ok := h.loadCatalogSnapshot("snap:"+e.memKey, e.decode); ok {
			h.cache.setTTL(e.memKey, v, e.ttl) // serve fresh immediately; refresher keeps it current
			loaded++
		}
	}
	if loaded > 0 {
		log.Printf("catalog: preloaded %d disk snapshot(s) at boot — storefront is live even if the supplier is not", loaded)
	}
}

// decodeBrands decodes a persisted BrandsResponse snapshot.
func decodeBrands(raw json.RawMessage) (interface{}, error) {
	var out cryptorefills.BrandsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// decodeVias decodes a persisted payment vias snapshot.
func decodeVias(raw json.RawMessage) (interface{}, error) {
	var out []cryptorefills.PaymentVia
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeFamilies decodes a persisted []Family snapshot.
func decodeFamilies(raw json.RawMessage) (interface{}, error) {
	var out []cryptorefills.Family
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// supplierContext gives every CryptoRefills call a fair-queue identity AND
// the end user's IP for the mandatory X-Forwarded-For header. A signed
// customer is isolated by user id; anonymous catalog traffic is isolated by
// the TCP peer; background work has an explicit system identity. This keeps
// a single noisy user from consuming the shared partner account's whole
// budget.
// supplierCallTimeout bounds both queue residence and the upstream round trip.
// fasthttp's RequestCtx does not reliably expose client disconnects as a
// cancellable context, so without an explicit deadline an overloaded supplier
// queue can leave handler goroutines waiting forever.
const supplierCallTimeout = 20 * time.Second

// supplierLang is the language the shop asks the SUPPLIER for. The
// CryptoRefills v5 API is language-dependent (rich_description:
// how_to_redeem / description / term_and_conditions) and accepts exactly the
// six languages this shop renders, so the buyer's active language — read from
// ?lang=, then the nimshop-lang cookie, then Accept-Language, exactly like the
// emails — travels to the supplier with every catalog call. An unknown or
// missing language becomes "en", which is also the supplier's own fallback.
func supplierLang(ctx *fasthttp.RequestCtx) string {
	lang := i18n.ParseLangCtx(ctx)
	if lang == "" {
		return "en"
	}
	return lang
}

func (h *Handlers) supplierContext(ctx *fasthttp.RequestCtx) context.Context {
	ip := clientip.Resolve(ctx, h.Cfg.TrustProxy, h.Cfg.ClientIPPolicy()).IP
	// Use a detached, bounded context rather than RequestCtx itself: request
	// contexts are pooled by fasthttp and must not outlive the handler.
	out, cancel := context.WithTimeout(context.Background(), supplierCallTimeout)
	// cancel cannot be deferred (the context intentionally outlives this
	// handler), so release the timer at the deadline instead. AfterFunc on
	// an already-expired context is a no-op, so this is always safe.
	context.AfterFunc(out, cancel)
	out = cryptorefills.WithEndUserIP(out, ip)
	out = cryptorefills.WithEndUserAgent(out, string(ctx.Request.Header.UserAgent()))
	actor := "ip:" + ip
	if userID := middleware.UserID(ctx); userID != "" {
		actor = "user:" + userID
	}
	return cryptorefills.WithActor(out, actor)
}

// supplierError maps a supplier failure to a stable client response while
// logging the full upstream detail server-side (the client never sees
// supplier internals). Validation problems (limits, KYC, stock, bad
// beneficiary) get 409 with the machine codes so the frontend can react.
func (h *Handlers) supplierError(ctx *fasthttp.RequestCtx, err error, fallback string) {
	// Every supplier failure is logged server-side with the upstream detail.
	log.Printf("supplier error on %s %s: %v", ctx.Method(), ctx.Path(), err)
	var limited *cryptorefills.RateLimitError
	if errors.As(err, &limited) {
		writeRequestThrottle(ctx, "SUPPLIER_RATE_LIMITED", limited.ResetAt, limited.Problems...)
		return
	}
	var budget *cryptorefills.BudgetWaitError
	if errors.As(err, &budget) {
		writeRequestThrottle(ctx, "CHECKOUT_BUDGET_WAIT", budget.ResetAt)
		return
	}
	if errors.Is(err, cryptorefills.ErrQueueFull) || errors.Is(err, cryptorefills.ErrBudgetWait) {
		writeRequestThrottle(ctx, "CHECKOUT_QUEUE_BUSY", time.Now().Add(15*time.Second))
		return
	}
	var problems *cryptorefills.ProblemError
	if errors.As(err, &problems) {
		status := fasthttp.StatusConflict
		if problems.HTTPStatus >= 500 {
			status = fasthttp.StatusBadGateway
		}
		writeJSON(ctx, status, map[string]interface{}{
			"error": "Cryptorefills could not approve this request.",
			"code":  "SUPPLIER_PROBLEMS", "supplier_error": true,
			"problems": problems.Problems, "upstream_status": problems.HTTPStatus,
		})
		return
	}
	var sup *cryptorefills.SupplierError
	if errors.As(err, &sup) {
		if isSupplierCode(sup.Code) {
			writeJSON(ctx, fasthttp.StatusConflict, map[string]interface{}{
				"error": cryptorefills.ProblemMessage(sup.Code), "supplier_error": true,
				"code": sup.Code, "detail": sup.Code, "moreDetails": sup.MoreDetails,
				"problems":        []cryptorefills.Problem{{Code: sup.Code, Details: sup.MoreDetails}},
				"upstream_status": sup.Status,
			})
			return
		}
		// Raw supplier detail can contain JSON, account data or credentials.
		// It is logged server-side, not copied into customer-facing messages.
		writeJSON(ctx, fasthttp.StatusBadGateway, map[string]interface{}{
			"error": fallback, "code": "SUPPLIER_UNAVAILABLE", "supplier_error": true,
			"upstream_status": sup.Status,
		})
		return
	}
	writeError(ctx, fasthttp.StatusBadGateway, fallback)
}

// jsonBufPool recycles the scratch buffer every response is encoded into.
//
// The previous writeJSON did json.Marshal (one allocation sized to the
// payload) and then ctx.SetBody (a copy into the response's own buffer). On a
// several-hundred-kilobyte catalog response that is two large allocations and
// a full memcpy per request, on the busiest endpoints in the process. Pooling
// the scratch buffer removes the first; SetBody still reuses the response
// buffer's capacity, so the steady state allocates nothing.
//
// Buffers are capped before being returned: a one-off huge response must not
// pin a megabyte of scratch space per goroutine forever.
var jsonBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 8*1024)
		return &b
	},
}

const jsonBufMaxPooled = 512 * 1024

func writeJSON(ctx *fasthttp.RequestCtx, status int, v interface{}) {
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")

	bp := jsonBufPool.Get().(*[]byte)
	buf := (*bp)[:0]
	enc := json.NewEncoder(bytesWriter{&buf})
	// HTML escaping is both wasted work and wrong for this content type: the
	// API is application/json served with X-Content-Type-Options: nosniff, so
	// no browser will ever parse this body as HTML. Leaving it on turned every
	// "&", "<" and ">" in a product description into a six-byte \uXXXX
	// sequence, inflating payloads that the CDN then has to ship.
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	// Encode appends a trailing newline; drop it so byte-for-byte responses
	// match what the old Marshal path produced.
	if err == nil && len(buf) > 0 && buf[len(buf)-1] == '\n' {
		buf = buf[:len(buf)-1]
	}
	if err != nil {
		*bp = buf
		if cap(buf) <= jsonBufMaxPooled {
			jsonBufPool.Put(bp)
		}
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(`{"error":"internal encoding error"}`)
		return
	}
	ctx.SetBody(buf)
	*bp = buf
	if cap(buf) <= jsonBufMaxPooled {
		jsonBufPool.Put(bp)
	}
}

// bytesWriter adapts a growable slice to io.Writer so encoding/json can write
// straight into the pooled buffer.
type bytesWriter struct{ p *[]byte }

func (w bytesWriter) Write(b []byte) (int, error) {
	*w.p = append(*w.p, b...)
	return len(b), nil
}

func writeError(ctx *fasthttp.RequestCtx, status int, msg string) {
	writeJSON(ctx, status, map[string]string{"error": msg})
}

func readJSON(ctx *fasthttp.RequestCtx, v interface{}) error {
	return json.Unmarshal(ctx.PostBody(), v)
}

// validEmail is a strictish syntactic check: Cryptorefills delivers the
// product to this address, and a bad address means a broken delivery.
func validEmail(s string) bool {
	if len(s) < 6 || len(s) > 254 {
		return false
	}
	_, err := mail.ParseAddress(s)
	return err == nil
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// Advertise the actual cooldown (rounded UP), not an unconditional 15 seconds.
func writeRequestThrottle(ctx *fasthttp.RequestCtx, code string, reset time.Time, problems ...cryptorefills.Problem) {
	if reset.IsZero() {
		reset = time.Now().Add(15 * time.Second)
	}
	seconds := int(math.Ceil(time.Until(reset).Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	ctx.Response.Header.Set("Retry-After", strconv.Itoa(seconds))
	body := map[string]interface{}{
		"error": "Checkout requests are temporarily paused. Please wait before trying again.",
		"code":  code, "retry_after_seconds": seconds, "retry_at": reset.UTC(),
	}
	if len(problems) > 0 {
		body["problems"] = problems
		body["supplier_error"] = true
	}
	writeJSON(ctx, fasthttp.StatusTooManyRequests, body)
}

func isSupplierCode(code string) bool {
	if _, ok := cryptorefills.LookupProblem(code); ok {
		return true
	}
	// Future machine codes remain structured, but get safe generic copy.
	if len(code) == 0 || len(code) > 100 {
		return false
	}
	for _, r := range code {
		ok := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
		if !ok {
			return false
		}
	}
	return strings.Contains(code, "_")
}
