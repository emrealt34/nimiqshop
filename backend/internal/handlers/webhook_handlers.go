package handlers

import (
	"crypto/subtle"
	"log"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/cryptorefills"
)

// CryptoRefillsWebhook is the optional acceleration path for fulfillment.
// The polling tracker is the guarantee; the webhook (when the supplier has
// it enabled for this account) just makes state changes faster.
//
// The body is unsigned, so — exactly like before — the webhook is only a
// TRIGGER: the order is always re-fetched through the queued API client and
// only the supplier's own response may move local state.
func (h *Handlers) CryptoRefillsWebhook(ctx *fasthttp.RequestCtx) {
	// Webhook verification work belongs to one system actor, never to the
	// supplier's egress IP: all retries share one IP, and one supplier IP
	// must not be able to starve the per-actor budget of customer traffic.
	baseCtx := h.supplierContext(ctx)
	sctx := cryptorefills.WithActor(baseCtx, "system:webhook")

	if h.Cfg.CRWebhookKey == "" {
		writeError(ctx, fasthttp.StatusNotFound, "webhooks are not configured")
		return
	}
	key := string(ctx.QueryArgs().Peek("key"))
	if subtle.ConstantTimeCompare([]byte(key), []byte(h.Cfg.CRWebhookKey)) != 1 {
		writeError(ctx, fasthttp.StatusUnauthorized, "invalid webhook key")
		return
	}

	payload, ok := cryptorefills.ParseWebhookPayload(ctx.PostBody())
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid webhook payload")
		return
	}

	order, err := h.CR.GetOrder(sctx, payload.OrderID)
	if err != nil {
		log.Printf("webhook: could not re-fetch order %s: %v", payload.OrderID, err)
		h.supplierError(ctx, err, "could not verify order")
		return
	}
	if order.ID == "" || order.Status == "" {
		// Unknown order id: the supplier itself says "not found" — do not
		// retry forever (200), do not move any local state.
		writeJSON(ctx, fasthttp.StatusNotFound, map[string]bool{"ok": false})
		return
	}

	q, qerr := h.Store.GetQuoteBySupplierOrderID(order.ID)
	if qerr != nil {
		// Crash-adoption window: the supplier has a real order (possibly
		// already paid) but no local quote references it yet. The tracker
		// will flag the stuck intent; a 503 makes the supplier retry while
		// local state catches up. If the quote is genuinely gone, the
		// supplier's retries simply give up — no local damage either way.
		ctx.Response.Header.Set("Retry-After", "5")
		writeError(ctx, fasthttp.StatusServiceUnavailable, "order linkage pending; please retry webhook")
		return
	}

	// Refund info (Refunded orders).
	if cryptorefills.MapToQuoteStatus(order.Status) == cryptorefills.QuoteRefunded {
		var refund []byte
		if order.Refund != nil {
			refund, _ = jsonMarshal(order.Refund)
		}
		if err := h.Store.MarkQuoteRefunded(q.ID, refund, "supplier webhook: "+order.Status); err == nil {
			log.Printf("webhook: quote %s refunded by supplier", q.ID)
		}
		writeJSON(ctx, fasthttp.StatusOK, map[string]bool{"ok": true})
		return
	}

	// Delivery / payment states.
	switch cryptorefills.MapToQuoteStatus(order.Status) {
	case cryptorefills.QuoteFulfilled:
		if err := h.Store.CompleteQuoteWithFulfillment(q.ID, cryptorefills.FulfillmentPayload(order)); err == nil {
			log.Printf("webhook: quote %s fulfilled", q.ID)
		} else {
			latest, lerr := h.Store.GetQuoteBySupplierOrderID(order.ID)
			if lerr == nil && latest.Status != cryptorefills.QuoteFulfilled {
				ctx.Response.Header.Set("Retry-After", "5")
				writeError(ctx, fasthttp.StatusServiceUnavailable, "delivery still being finalized; please retry webhook")
				return
			}
			log.Printf("webhook: quote %s delivery transition: %v", q.ID, err)
		}
	case cryptorefills.QuoteFailed:
		if err := h.Store.MarkSupplierFailure(q.ID, "supplier webhook: "+order.Status); err == nil {
			log.Printf("webhook: quote %s failed at supplier", q.ID)
		}
	case cryptorefills.QuoteManualReview:
		if err := h.Store.MarkQuoteManualReview(q.ID, "supplier webhook: "+order.Status); err == nil {
			log.Printf("webhook: quote %s needs manual action", q.ID)
		}
	default:
		// Intermediate payment states: advance the conditional transition.
		local := cryptorefills.MapToQuoteStatus(order.Status)
		if local != q.Status {
			if _, err := h.Store.SetSupplierStatus(q.ID, order.Status, local); err == nil {
				log.Printf("webhook: quote %s -> %s (supplier %s)", q.ID, local, order.Status)
			}
		} else if order.Status != q.SupplierStatus {
			_, _ = h.Store.SetSupplierStatus(q.ID, order.Status, local)
		}
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]bool{"ok": true})
}

// WebhookURLFor builds the inbound webhook callback URL (key + optional
// kind/ref for operator debugging). It is included in order creation only
// when the operator has configured CRYPTOREFILLS_WEBHOOK_KEY.
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
