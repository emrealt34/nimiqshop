package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"net/url"
	"strconv"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/clientip"
	"nimiqshop/internal/config"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/notification"
	"nimiqshop/internal/presence"
)

type Handlers struct {
	Store       *db.Store
	Cfg         config.Config
	CR          *cryptorefills.Client
	Oracle      *nimiq.MultiSourceOracle
	Presence    *presence.Tracker
	Gift        *notification.GiftClient
	cache       *ttlCache
	flights     *flightGroup
}

func New(store *db.Store, cfg config.Config, cr *cryptorefills.Client) *Handlers {
	return &Handlers{
		Store:   store,
		Cfg:     cfg,
		CR:      cr,
		Oracle:  nimiq.NewMultiSourceOracle(cfg.OracleMinSources, cfg.OracleMaxSpreadBps),
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

// decodePrice decodes a persisted PriceQuote snapshot.
func decodePrice(raw json.RawMessage) (interface{}, error) {
	var out cryptorefills.PriceQuote
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
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

func (h *Handlers) supplierContext(ctx *fasthttp.RequestCtx) context.Context {
	ip := clientip.Resolve(ctx, h.Cfg.TrustProxy).IP
	// Use a detached, bounded context rather than RequestCtx itself: request
	// contexts are pooled by fasthttp and must not outlive the handler.
	out, cancel := context.WithTimeout(context.Background(), supplierCallTimeout)
	// cancel cannot be deferred (the context intentionally outlives this
	// handler), so release the timer at the deadline instead. AfterFunc on
	// an already-expired context is a no-op, so this is always safe.
	context.AfterFunc(out, cancel)
	out = cryptorefills.WithEndUserIP(out, ip)
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
		if !limited.ResetAt.IsZero() {
			seconds := int(time.Until(limited.ResetAt).Seconds())
			if seconds < 1 {
				seconds = 1
			}
			ctx.Response.Header.Set("Retry-After", fmt.Sprintf("%d", seconds))
		}
		writeError(ctx, fasthttp.StatusTooManyRequests, "supplier rate limit reached; please retry after the supplied delay")
		return
	}
	if errors.Is(err, cryptorefills.ErrQueueFull) || errors.Is(err, cryptorefills.ErrBudgetWait) {
		ctx.Response.Header.Set("Retry-After", "15")
		writeError(ctx, fasthttp.StatusTooManyRequests, "supplier queue is busy; please retry shortly")
		return
	}
	var problems *cryptorefills.ProblemError
	if errors.As(err, &problems) {
		writeJSON(ctx, fasthttp.StatusConflict, map[string]interface{}{
			"error":    "order could not be created",
			"problems": problems.Problems,
			"detail":   problems.Error(),
		})
		return
	}
	var sup *cryptorefills.SupplierError
	if errors.As(err, &sup) {
		// KYC / limit / suspended problems the supplier reports at HTTP
		// level surface as 409 with the stable code.
		switch sup.Code {
		case "KYC_MISSING", "KYC_PENDING", "VERIFICATION_REQUIRED",
			"DAILY_SPENDING_LIMIT_EXCEEDED", "MONTHLY_SPENDING_LIMIT_EXCEEDED",
			"FULLNAME_MISSING", "SUSPENDED_COIN", "SUSPENDED_NETWORK":
			writeJSON(ctx, fasthttp.StatusConflict, map[string]interface{}{
				"error":  "order blocked by supplier policy",
				"code":   sup.Code,
				"detail": sup.Detail,
			})
			return
		}
		writeJSON(ctx, fasthttp.StatusBadGateway, map[string]interface{}{
			"error":  fallback,
			"detail": sup.Detail,
		})
		return
	}
	writeError(ctx, fasthttp.StatusBadGateway, fallback)
}

func writeJSON(ctx *fasthttp.RequestCtx, status int, v interface{}) {
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")
	b, err := json.Marshal(v)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(`{"error":"internal encoding error"}`)
		return
	}
	ctx.SetBody(b)
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

func jsonMarshal(v interface{}) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshal(b []byte, v interface{}) error { return json.Unmarshal(b, v) }

func strconvParse(s string) (float64, error) { return strconv.ParseFloat(s, 64) }

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
