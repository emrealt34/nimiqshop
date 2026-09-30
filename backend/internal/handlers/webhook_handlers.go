package handlers

import (
	"crypto/subtle"
	"log"
	"strings"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/settlement"
)

// CryptoRefillsWebhook is the optional acceleration path for fulfillment.
// The polling tracker is the guarantee; the webhook (when the supplier has
// it enabled for this account) just makes state changes faster.
//
// The body is unsigned, so — exactly like before — the webhook is only a
// TRIGGER: the order is always re-fetched through the queued API client and
// only the supplier's own response may move local state.
func (h *Handlers) CryptoRefillsWebhook(ctx *fasthttp.RequestCtx) {
	if h.Cfg.CRWebhookKey == "" {
		writeError(ctx, fasthttp.StatusNotFound, "webhooks are not configured")
		return
	}
	// The shared secret is accepted as a header OR a query parameter.
	//
	// A secret in a URL is a secret that gets written down: it lands in the
	// supplier's outbound request log, in any proxy or CDN access log in
	// front of this box, and in fasthttp's own error paths that echo the
	// request URI. The header form is the one to configure; the query form
	// stays supported because Cryptorefills' account-level webhook setting
	// only takes a URL, and dropping it would silently stop fulfillment
	// acceleration for every existing deployment.
	//
	// The comparison is constant-time against BOTH candidates so a timing
	// difference cannot reveal which one the operator configured.
	headerKey := string(ctx.Request.Header.Peek("X-Webhook-Key"))
	if headerKey == "" {
		headerKey = string(ctx.Request.Header.Peek("Authorization"))
		headerKey = strings.TrimPrefix(strings.TrimPrefix(headerKey, "Bearer "), "bearer ")
	}
	queryKey := string(ctx.QueryArgs().Peek("key"))
	want := []byte(h.Cfg.CRWebhookKey)
	okHeader := subtle.ConstantTimeCompare([]byte(headerKey), want) == 1
	okQuery := subtle.ConstantTimeCompare([]byte(queryKey), want) == 1
	if !okHeader && !okQuery {
		// Deliberately no log line: this endpoint is unauthenticated and
		// internet-facing, so every rejected call would be an attacker-
		// controlled write into the operator's disk. The 401 rate is visible
		// in the access log, which is the right place for it.
		writeError(ctx, fasthttp.StatusUnauthorized, "invalid webhook key")
		return
	}

	payload, ok := cryptorefills.ParseWebhookPayload(ctx.PostBody())
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid webhook payload")
		return
	}

	// Find the owner-scoped link before spending supplier budget. A callback
	// is only an account-managed trigger; its state is never trusted.
	q, err := h.Store.GetQuoteBySupplierOrderID(payload.OrderID)
	if err != nil {
		ctx.Response.Header.Set("Retry-After", "5")
		writeError(ctx, 503, "order linkage pending")
		return
	}
	// A test-mode order (TESTSIM- supplier id) is driven by the simulated
	// pay endpoint, never by real supplier webhooks.
	if q.TestMode {
		writeError(ctx, fasthttp.StatusConflict, "test-mode order: not driven by supplier webhooks")
		return
	}

	sctx := cryptorefills.WithActor(h.supplierContext(ctx), "system:webhook")
	sctx = cryptorefills.WithEndUserIP(sctx, q.EndUserIP)
	sctx = cryptorefills.WithEndUserAgent(sctx, q.EndUserAgent)
	order, err := h.CR.GetOrder(sctx, payload.OrderID)
	if err != nil {
		h.supplierError(ctx, err, "could not verify order")
		return
	}
	changed, err := h.Store.ApplySupplierOrder(q.ID, order)
	if err != nil {
		log.Printf("webhook: quote %s could not persist supplier observation: %v", q.ID, err)
		ctx.Response.Header.Set("Retry-After", "5")
		writeError(ctx, 503, "verified update not saved; retry webhook")
		return
	}
	if changed && cryptorefills.MapToQuoteStatus(order.Status) == cryptorefills.QuoteFulfilled {
		if latest, e := h.Store.GetQuote(q.ID); e == nil {
			settlement.NotifyFulfilled(latest)
		}
	}
	writeJSON(ctx, 200, map[string]bool{"ok": true})
}

// WebhookURLFor builds the inbound webhook callback URL (key + optional
// kind/ref for operator debugging). It is included in order creation only
// when the operator has configured CRYPTOREFILLS_WEBHOOK_KEY.
// The public developer guide does not document webhook registration; arrange
// it with Cryptorefills for the account. Polling works without a webhook.
func (h *Handlers) WebhookURLFor(kind, ref string) string {
	if h.Cfg.PublicWebhookBaseURL == "" || h.Cfg.CRWebhookKey == "" {
		return ""
	}
	endpoint := h.Cfg.PublicWebhookBaseURL + "/api/webhooks/cryptorefills?key=" + h.Cfg.CRWebhookKey
	if kind != "" && ref != "" {
		endpoint += "&kind=" + urlQueryEscape(kind) + "&ref=" + urlQueryEscape(ref)
	}
	return endpoint
}
