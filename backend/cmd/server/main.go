package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/config"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/handlers"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/notification"
	"nimiqshop/internal/presence"
	"nimiqshop/internal/safe"
	"nimiqshop/internal/settlement"
)

// quoteRenderFields extracts the buyer-facing product/face/currency triplet
// used by the gift email + SMS body renderers. It tolerates the supplier's
// loose denomination labels ("TRY300", "100 USD", "25", "Java & Bedrock Ed").
func quoteRenderFields(q db.Quote) (faceValue string, currency string, product string) {
	currency = extractCurrency(q.Denomination, q.ProductCountry)
	faceValue = strconv.FormatFloat(q.ProductValue, 'f', 0, 64)
	if q.ProductValue == float64(int64(q.ProductValue)) {
		faceValue = strconv.FormatInt(int64(q.ProductValue), 10)
	}
	product = q.ProductID
	return
}

// extractCurrency is the same fallback table as the activity feed so the
// gift notification body and the public activity row agree on the symbol.
func extractCurrency(denom, country string) string {
	d := strings.ToUpper(strings.TrimSpace(denom))
	for _, code := range []string{"USD", "EUR", "GBP", "TRY", "JPY", "CNY", "CAD", "AUD", "CHF", "INR", "BRL", "MXN"} {
		if strings.Contains(d, code) {
			return code
		}
	}
	c := strings.ToUpper(strings.TrimSpace(country))
	switch c {
	case "TR":
		return "TRY"
	case "GB", "UK":
		return "GBP"
	case "DE", "FR", "IT", "ES", "NL", "BE", "AT", "PT", "IE", "FI", "GR":
		return "EUR"
	}
	return "USD"
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("unsafe configuration: %v", err)
	}

	// BadgerDB is embedded: this opens a directory on disk rather than
	// dialing a server, and there is no migration step because the store
	// is schemaless (the keyspace is documented in internal/db/keys.go).
	store, err := db.New(cfg.BadgerDir)
	if err != nil {
		log.Fatalf("db init: %v", err)
	}
	defer func() {
		// Closing Badger flushes pending writes; skipping it can leave
		// recent commits to be recovered from the WAL on next boot.
		if cerr := store.Close(); cerr != nil {
			log.Printf("badger close: %v", cerr)
		}
	}()

	// The seed consists of a pre-generated Argon2id PHC value and a TOTP
	// secret, never a raw administrator password. The store's marker makes
	// this one-time even if the process restarts with the variables present.
	if cfg.HasAdminSeed() {
		if _, err := store.BootstrapAdmin(cfg.AdminUsername, cfg.AdminPasswordHash, cfg.AdminTOTPSecret); err != nil && !errors.Is(err, db.ErrConflict) {
			log.Fatalf("admin bootstrap: %v", err)
		} else if err == nil {
			log.Printf("initial admin %q bootstrapped", cfg.AdminUsername)
		}
	}

	cr := cryptorefills.NewClient(cfg.CRBaseURL, cfg.CRPartnerID, cfg.CRAppVersion, cfg.CRUserAgent, cryptorefills.QueueConfig{
		MaxQueue:            cfg.CRQueueMax,
		MaxQueuePerActor:    cfg.CRQueuePerActorMax,
		ActorRequestsPerMin: cfg.CRActorPerMinute,
		ActorBurst:          cfg.CRActorBurst,
	})
	defer cr.Close()

	h := handlers.New(store, cfg, cr)
	h.Presence = presence.New()
	// 60s background NIM/BTC rate refresher: /api/market/nim-rate always
	// serves the warm snapshot instantly — user requests never wait on the
	// oracle (see internal/handlers/market_rates.go).
	h.StartRatesRefresher(ctx)

	// Non-custodial: no wallet, treasury or refund signer on this server.
	// The only background task is the fulfillment tracker: it polls
	// non-terminal supplier orders (webhooks are an optional acceleration),
	// sweeps locally-unpaid quotes, and flags stuck order-creation intents.
	tracker := &settlement.OrderTracker{
		Store: store, CR: cr,
		Interval:   time.Duration(cfg.CRPollSecs) * time.Second,
		StaleAfter: time.Duration(cfg.CRStaleSecs) * time.Second,
	}
	tracker.Run(ctx)

	// Gift notification channel (email + SMS). Both are OFF by default —
	// the notifier becomes a no-op when the corresponding env vars are not
	// set. When enabled, it fires once per fulfilled gift order and
	// persists the GiftNotifiedAt marker so a retry never re-sends.
	giftNotifier := notification.NewGift(notification.GiftConfig{
		EmailEnabled:   cfg.NotifyEmailEnabled,
		SMTPHost:       cfg.NotifySMTPHost,
		SMTPPort:       cfg.NotifySMTPPort,
		SMTPUsername:   cfg.NotifySMTPUsername,
		SMTPPassword:   cfg.NotifySMTPPassword,
		SMTPFromName:   cfg.NotifySMTPFromName,
		SMTPFromAddr:   cfg.NotifySMTPFromAddr,
		EmailSubject:   cfg.NotifyEmailSubject,
		EmailDryRun:    cfg.NotifyEmailDryRun,
		SMSEnabled:     cfg.NotifySMSEnabled,
		SMSProviderURL: cfg.NotifySMSURL,
		SMSAuthHeader:  cfg.NotifySMSAuthHeader,
		SMSBodyTmpl:    cfg.NotifySMSBodyTmpl,
		SMSSender:      cfg.NotifySMSSender,
		SMSDryRun:      cfg.NotifySMSDryRun,
	})
	if giftNotifier.Enabled() {
		log.Printf("notify: gift channel enabled (email=%v sms=%v from=%q)", giftNotifier.EmailEnabled(), giftNotifier.SMSEnabled(), cfg.NotifySMTPFromAddr)
	}
	h.Gift = giftNotifier
	settlement.GiftNotifyFn = func(q db.Quote) {
		// Idempotency: skip when the marker is already set. This is the
		// gate that turns the fulfilled transition into at-most-once
		// delivery across crashes and tracker re-runs.
		if !q.GiftNotifiedAt.IsZero() {
			return
		}
		channel := notification.NormalizeChannel(q.GiftChannel)
		if channel == "" {
			return
		}
		// Recipient identity: gift cards / eSIMs are delivered to the
		// customer's email at the supplier; the "recipient" for the
		// notification is the buyer's CustomerEmail (gift-channel gift)
		// or the supplied GiftRecipientPhone (top-ups via SMS).
		recipientEmail := q.CustomerEmail
		recipientPhone := q.GiftRecipientPhone
		if recipientPhone == "" {
			recipientPhone = q.PhoneNumber // top-ups may have the phone already
		}
		emailBody, smsBody := notification.SanitizeMessage(q.GiftMessage)
		// Resolve the per-channel body using the supplier's denomination.
		faceValue, currency, product := quoteRenderFields(q)
		// Delivery channel: top-ups deliver to the beneficiary PHONE (E.164
		// in BeneficiaryAccount); everything else is delivered by EMAIL.
		deliveredBy := "email"
		if strings.HasPrefix(q.BeneficiaryAccount, "+") {
			deliveredBy = "phone"
		}
		subject, plain, html := giftNotifier.RenderEmail(recipientEmail, product, faceValue, currency, q.CustomerEmail, emailBody, deliveredBy)
		if smsBody == "" {
			smsBody = giftNotifier.RenderSMS(product, faceValue, currency, "", deliveredBy)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		emailErr, smsErr := giftNotifier.NotifyGift(ctx, channel, recipientEmail, recipientPhone, subject, plain, html, smsBody)
		if emailErr != nil {
			log.Printf("notify: gift email for quote %s failed: %v", q.ID, emailErr)
		}
		if smsErr != nil {
			log.Printf("notify: gift sms for quote %s failed: %v", q.ID, smsErr)
		}
		// Mark delivered only when at least one leg succeeded, so a full
		// failure leaves the door open for a manual retry from the admin
		// console. A partial failure (e.g. email ok, sms failed) is still
		// recorded as delivered to avoid double-charging the SMS provider.
		if emailErr == nil || smsErr == nil {
			if err := store.MarkGiftNotified(q.ID); err != nil {
				log.Printf("notify: gift delivered for quote %s but failed to mark: %v", q.ID, err)
			}
		}
	}

	// Serve the storefront from disk snapshots BEFORE the first request
	// arrives: even if the supplier is down or 429-throttled, a fresh
	// process boots with a full catalog.
	h.PreloadCatalogSnapshots()

	// Keep the catalog cache fresh in the background every 30 minutes.
	// CryptoRefills recovery from a budget storm is typically minutes-to-
	// hours, not seconds; without a background refresher the brand catalog
	// would ride the 6h TTL blindly. The refresher is polite by design:
	// SEQUENTIAL countries (never parallel bursts), 2s gaps, exponential
	// retry backoff on failure (1m doubling to 30m), and everything flows
	// through the shared supplier queue + caches, so it can never hit
	// upstream any harder than one extra browser tab would.
	go func() {
		countries := []string{"TR", "US", "DE", "GB"}
		refresh := func() bool {
			for i, cc := range countries {
				if i > 0 {
					select {
					case <-ctx.Done():
						return false
					case <-time.After(2 * time.Second):
					}
				}
				preCtx := cryptorefills.WithActor(context.Background(), "system:cachewarm")
				if _, err := cr.Brands(preCtx, cc); err != nil {
					log.Printf("cachewarm brands(%s): %v", cc, err)
					return false
				}
				log.Printf("cachewarm: refreshed brands(%s)", cc)
			}
			return true
		}
		time.Sleep(500 * time.Millisecond) // let the server start first
		lastFailed := !refresh()
		// Refresh cadence: 30 minutes after a healthy pass; after a failed
		// pass, retry sooner with exponential backoff (1m -> 30m cap).
		failureBackoff := time.Minute
		for {
			wait := 30 * time.Minute
			if lastFailed {
				wait = failureBackoff
				failureBackoff *= 2
				if failureBackoff > 30*time.Minute {
					failureBackoff = 30 * time.Minute
				}
			} else {
				failureBackoff = time.Minute
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			lastFailed = !refresh()
		}
	}()

	r := buildRouter(h, cfg)

	// Top-level panic logger. fasthttp already recovers handler panics
	// per-connection, but it swallows the stack trace silently. Wrapping the
	// whole router turns every handler panic into a loud, stack-bearing log
	// line and a clean 500, so an incident is diagnosable without an outage.
	inner := r.Handler
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("SAFE http %s %s: recovered panic: %v\n%s",
							ctx.Method(), ctx.Path(), p, safe.StackTrace())
						ctx.Error("internal error", fasthttp.StatusInternalServerError)
					}
				}()
				// Production gzip happens at the Cloudflare / nginx edge.
				// fasthttp.Server does not expose CompressLevel on the Server
				// struct in this Go version, so we keep the response plain
				// here (the proxy compresses it transparently).
				inner(ctx)
			}()
		},
		ReadTimeout:        15_000_000_000, // 15s
		WriteTimeout:       15_000_000_000, // 15s — a stuck writer can no longer pin a goroutine forever
		MaxRequestBodySize: cfg.MaxRequestBodyBytes,
		DisableKeepalive: false,
		// TCP keepalive: proxies (Cloudflare/nginx) and mobile browsers hold
		// connections open; without this fasthttp closes them and every
		// request repays the TCP handshake. Bigger IO buffers cut syscalls
		// on catalog payloads.
		TCPKeepalive:     true,
		ReadBufferSize:   8192,
		WriteBufferSize:  8192,
	}

	go func() {
		log.Printf("nimiqshop backend listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(cfg.ListenAddr); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	_ = server.Shutdown()
}

func buildRouter(h *handlers.Handlers, cfg config.Config) *router.Router {
	r := router.New()
	// CORS: supports separate frontend/backend domains.
	// Set ALLOWED_ORIGINS to your frontend origin(s), e.g. https://nim.shop,https://www.nim.shop
	// For same-origin (nginx proxy) you can leave it empty — same-origin requests need no CORS.
	// For dev with live-proxy, the proxy makes requests same-origin to the browser, so CORS is not needed.
	r.GlobalOPTIONS = func(ctx *fasthttp.RequestCtx) {
		applyCORS(ctx, cfg)
		if len(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) > 0 {
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		} else {
			// If origin not allowed, still return 204 for OPTIONS to avoid browser hanging,
			// but without CORS headers browser will block.
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		}
	}

	rateLimiter := middleware.NewRateLimiter(cfg.RateLimitPerMinute, cfg.RateLimitBurst, cfg.TrustProxy)
	wrap := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return rateLimiter.Limit(func(ctx *fasthttp.RequestCtx) {
			applyCORS(ctx, cfg)
			next(ctx)
		})
	}
	authed := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return wrap(middleware.RequireAuth(cfg.JWTSecret, next))
	}
	// This is a completely independent identity plane. It only accepts an
	// HttpOnly admin-session cookie; customer Bearer JWTs cannot satisfy it.
	adminLoginRateLimiter := middleware.NewRateLimiter(10, 5, cfg.TrustProxy)
	adminAuthPublic := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		// Password/TOTP endpoints receive a tighter independent limiter. The
		// normal authenticated console can load several dashboard panels at
		// once, so it keeps the regular API limiter below.
		return adminLoginRateLimiter.Limit(func(ctx *fasthttp.RequestCtx) { applyCORS(ctx, cfg); next(ctx) })
	}
	adminOnly := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return wrap(h.RequireAdminSession(next))
	}

	// Auth (Nimiq Hub wallet login: challenge -> Hub signMessage() -> verify)
	r.POST("/api/auth/challenge", wrap(h.AuthChallenge))
	r.POST("/api/auth/hub-login", wrap(h.HubLogin))

	// Admin authentication. Bootstrap accepts only a deployment secret and a
	// pre-generated password hash; all subsequent routes use the distinct
	// cookie session and never the customer JWT middleware.
	r.POST("/api/admin/auth/bootstrap", adminAuthPublic(h.AdminBootstrap))
	r.POST("/api/admin/auth/login", adminAuthPublic(h.AdminLogin))
	r.POST("/api/admin/auth/logout", adminOnly(h.AdminLogout))
	r.GET("/api/admin/auth/me", adminOnly(h.AdminMe))
	r.GET("/api/admin/dashboard", adminOnly(h.AdminDashboard))
	r.GET("/api/admin/users", adminOnly(h.AdminListUsers))
	r.GET("/api/admin/users/{id}", adminOnly(h.AdminUserDetail))
	r.GET("/api/admin/orders", adminOnly(h.AdminListOrders))
	r.GET("/api/admin/orders/{id}", adminOnly(h.AdminGetOrderDetail))
	r.POST("/api/admin/orders/{id}/sync", adminOnly(h.AdminSyncOrder))
	r.POST("/api/admin/orders/{id}/refund", adminOnly(h.AdminRefundOrder))
	r.GET("/api/admin/quotes", adminOnly(h.AdminListQuotes))
	r.GET("/api/admin/transactions", adminOnly(h.AdminListTransactions))
	r.GET("/api/admin/manual-review", adminOnly(h.AdminManualReview))
	r.POST("/api/admin/quotes/{id}/resolve", adminOnly(h.AdminResolveQuote))
	r.POST("/api/admin/quotes/{id}/send-gift-notification", adminOnly(h.AdminSendGiftNotification))
	// Operator-direct notifications (no quote required): email / SMS / both.
	r.POST("/api/admin/notification/send", adminOnly(h.AdminSendNotification))
	r.GET("/api/admin/notification/status", adminOnly(h.AdminNotificationStatus))
	r.GET("/api/admin/oracle", adminOnly(h.AdminOracleHealth))
	r.POST("/api/admin/settings/margin", adminOnly(h.AdminUpdateMargin))
	r.GET("/api/admin/audit", adminOnly(h.AdminListAudit))

	// Admin catalog visibility rules (price cap, hidden families, banned
	// categories/kinds, country rules) + the UNFILTERED catalog views the
	// operator uses to see everything before hiding it.
	r.GET("/api/admin/catalog-rules", adminOnly(h.AdminGetCatalogRules))
	r.PUT("/api/admin/catalog-rules", adminOnly(h.AdminUpdateCatalogRules))
	r.GET("/api/admin/catalog/brands", adminOnly(h.AdminListBrands))
	r.GET("/api/admin/catalog/products/{productId}", adminOnly(h.AdminGetFamily))

	// Admin Support Tickets
	r.GET("/api/admin/support/tickets", adminOnly(h.AdminListSupportTickets))
	r.GET("/api/admin/support/tickets/{id}", adminOnly(h.AdminGetSupportTicket))
	r.POST("/api/admin/support/tickets/{id}/messages", adminOnly(h.AdminAddSupportMessage))
	r.POST("/api/admin/support/tickets/{id}/status", adminOnly(h.AdminUpdateSupportStatus))

	// Cryptorefills stablecoin checkout — the ONLY production purchase path.
	// The quote creates a supplier order and returns the one-time wallet
	// address + exact coin amount; the customer pays from their own wallet
	// and the tracker/webhook records the delivery. The server has no payment
	// wallet, treasury, balance or deposit route.
	r.POST("/api/quotes", authed(h.CreateQuote))
	r.GET("/api/quotes", authed(h.ListUserQuotes))
	r.GET("/api/quotes/{id}", authed(h.GetUserQuote))
	r.POST("/api/quotes/{id}/rate", authed(h.RateQuote))

	// Catalog (public, no auth needed to browse)
	r.GET("/api/catalog/brands", wrap(h.ListBrands))
	r.GET("/api/catalog/products", wrap(h.GetFamily))
	r.GET("/api/catalog/price", wrap(h.ProductPrice))
	r.GET("/api/catalog/payment-vias", wrap(h.PaymentVias))
	// Legacy route aliases (kept for frontend compatibility)
	r.GET("/api/catalog/giftcards", wrap(h.ListGiftCardProducts))
	r.GET("/api/catalog/topups", wrap(h.ListTopupProducts))
	r.GET("/api/catalog/esims", wrap(h.ListEsimProducts))
	r.GET("/api/catalog/search", wrap(h.SearchProducts))
	r.GET("/api/catalog/products/{productId}", wrap(h.GetProduct))
	r.GET("/api/catalog/check-phone", wrap(h.CheckPhone))

	// Orders are read-only local views. New purchases always go through the
	// Supplier (CryptoRefills) Lightning quote above; status arrives via webhook.
	r.GET("/api/orders", authed(h.ListOrders))
	r.GET("/api/orders/{id}", authed(h.GetOrder))
	r.POST("/api/orders/{id}/refresh", authed(h.RefreshOrder))
	r.GET("/api/orders/{id}/support", authed(h.GetOrderSupport))

	// Public activity feed + ratings (NO auth — fully transparent by design).
	// Discloses only what is already public on-chain (the paying wallet, the
	// amount) plus the buyer's voluntary star rating.
	r.GET("/api/activity", wrap(h.ListActivity))
	r.GET("/api/ratings/summary", wrap(h.RatingSummary))
	// Public LIVE tracking: anyone can see an order's current stage by id, but
	// delivery codes stay owner-only. This is the anti-fraud transparency proof.
	r.GET("/api/track/{id}", wrap(h.TrackStatus))
	// Client IP / country, resolved server-side. No third-party geo API call:
	// with Cloudflare in front it uses CF-Connecting-IP + CF-IPCountry; in
	// direct mode it returns the TCP peer. Same-origin, so the frontend never
	// talks to an external IP service.
	r.GET("/api/geo", wrap(h.GeoInfo))
	r.GET("/api/market/nim-rate", wrap(h.NIMRate))
	r.GET("/api/market/fx", wrap(h.FXRates))
	r.POST("/api/presence", wrap(h.PresenceHeartbeat))
	r.POST("/api/orders/{id}/rate", authed(h.RateOrder))
	r.GET("/api/account/limits", authed(h.GetAccountLimits))
	// DEV-only free test products; not a customer payment rail.
	r.POST("/api/test/buy", authed(h.TestBuy))

	// Customer Support Tickets
	r.POST("/api/support/tickets", authed(h.CreateSupportTicket))
	r.GET("/api/support/tickets", authed(h.ListUserSupportTickets))
	r.GET("/api/support/tickets/{id}", authed(h.GetSupportTicket))
	r.POST("/api/support/tickets/{id}/messages", authed(h.AddSupportMessage))

	// Webhooks (optional acceleration; the polling tracker is the guarantee):
	// the supplier retries non-2xx callbacks, but random internet traffic must
	// not occupy the supplier verification queue. The callback has its own
	// inbound limiter before it can enqueue any re-fetch calls.
	webhookRateLimiter := middleware.NewRateLimiter(120, 20, cfg.TrustProxy)
	r.POST("/api/webhooks/cryptorefills", webhookRateLimiter.Limit(h.CryptoRefillsWebhook))

	// Backend build-integrity (public): reports the running binary's SHA-256
	// and the embedded source manifest so the deployed backend can be proven
	// identical to the open-source build. Frontend integrity is verified via
	// /integrity.json (static); this is the backend equivalent.
	r.GET("/api/integrity", wrap(h.Integrity))

	r.GET("/api/health", wrap(func(ctx *fasthttp.RequestCtx) {
		stats := h.CR.QueueStats()
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetContentType("application/json")
		throttled := "[]"
		if len(stats.Throttled) > 0 {
			// Values are built-in endpoint policy names, not user input.
			b, _ := json.Marshal(stats.Throttled)
			throttled = string(b)
		}
		ctx.SetBodyString(fmt.Sprintf(`{"ok":true,"cr_queue":{"queued":%d,"actors":%d,"throttled":%s}}`, stats.Queued, stats.Actors, throttled))
	}))

	return r
}

func applyCORS(ctx *fasthttp.RequestCtx, cfg config.Config) {
	origin := string(ctx.Request.Header.Peek("Origin"))
	// No Origin header = same-origin request (nginx proxy or live-proxy), no CORS needed.
	if origin == "" {
		return
	}
	// Check allowed origins — supports exact match and wildcard subdomains if configured.
	for _, allowed := range cfg.AllowedOrigins {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		// Exact match
		if origin == allowed {
			ctx.Response.Header.Set("Access-Control-Allow-Origin", allowed)
			ctx.Response.Header.Set("Vary", "Origin")
			ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Admin-Bootstrap-Token, X-Requested-With")
			ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
			ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
			return
		}
		// Wildcard subdomain support: https://*.example.com
		if strings.HasPrefix(allowed, "https://*.") {
			domain := strings.TrimPrefix(allowed, "https://*.")
			if strings.HasPrefix(origin, "https://") {
				originHost := strings.TrimPrefix(origin, "https://")
				originHost = strings.Split(originHost, "/")[0]
				originHost = strings.Split(originHost, ":")[0]
				if originHost == domain || strings.HasSuffix(originHost, "."+domain) {
					ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
					ctx.Response.Header.Set("Vary", "Origin")
					ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Admin-Bootstrap-Token, X-Requested-With")
					ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
					ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
					return
				}
			}
		}
	}
	// Auto-allow Cloudflare Tunnel origins (trycloudflare.com) for dev/preview
	// These are random subdomains like https://attachment-robert-boring-treated.trycloudflare.com
	// They are safe for dev and prevent CORS blocked errors during tunnel preview.
	if strings.Contains(origin, ".trycloudflare.com") {
		ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
		ctx.Response.Header.Set("Vary", "Origin")
		ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Admin-Bootstrap-Token, X-Requested-With")
		ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
		ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
		return
	}

	// Origin not in allowlist — log for debugging but don't set CORS headers (browser will block).
	if origin != "" {
		log.Printf("CORS blocked origin: %s (allowed: %v) path: %s", origin, cfg.AllowedOrigins, ctx.Path())
	}
}

// gzipWrap is a transparent gzip middleware for JSON responses. fasthttp.Server
// (this Go module's vendored version) does NOT expose CompressLevel on the
// Server struct — adding it as a field is a compile error. Production gzip
// happens at the Cloudflare / nginx edge (the devtools live-proxy also
// negotiates gzip transparently), so we keep the response plain here.
// (Historical helper retained as a no-op so older imports do not break;
// remove in a future cleanup pass.)
func gzipWrap(ctx *fasthttp.RequestCtx, next fasthttp.RequestHandler) {
	next(ctx)
}
