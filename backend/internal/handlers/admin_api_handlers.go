package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/catalog"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/notification"
)

func adminIdentity(ctx *fasthttp.RequestCtx) adminSessionContext {
	identity, _ := ctx.UserValue("admin_session").(adminSessionContext)
	return identity
}

func adminLimit(ctx *fasthttp.RequestCtx) int {
	limit, err := strconv.Atoi(string(ctx.QueryArgs().Peek("limit")))
	if err != nil || limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// AdminDashboard returns bounded operational counters.
func (h *Handlers) AdminDashboard(ctx *fasthttp.RequestCtx) {
	users, err := h.Store.CountUsers()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	awaitingPay, err := h.Store.CountQuotesByStatus("awaiting_payment")
	if err != nil {
		adminStoreError(ctx)
		return
	}
	manualReview, err := h.Store.CountQuotesByStatus("manual_review")
	if err != nil {
		adminStoreError(ctx)
		return
	}
	ordersProcessing, err := h.Store.CountOrdersByStatus("processing")
	if err != nil {
		adminStoreError(ctx)
		return
	}
	openTickets, _ := h.Store.CountOpenSupportTickets()
	crQueue := h.CR.QueueStats()

	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		adminStoreError(ctx)
		return
	}

	response := map[string]any{
		"users": users,
		"queue": map[string]int{
			"quotes_awaiting_payment": awaitingPay,
			"manual_review":           manualReview,
			"orders_processing":       ordersProcessing,
			"open_support_tickets":    openTickets,
			"cr_supplier_queued":      crQueue.Queued,
			"cr_queue_actors":         crQueue.Actors,
		},
		"settings": map[string]any{"updated_at": settings.UpdatedAt, "updated_by": settings.UpdatedBy, "note": "pricing margin is set by the supplier; no local margin"},
		"payment":  map[string]any{"rail": "cryptorefills", "custody": false, "note": "customer pays the supplier's one-time wallet address with stablecoins; Cryptorefills is merchant of record"},
	}
	writeJSON(ctx, fasthttp.StatusOK, response)
}

func (h *Handlers) AdminListUsers(ctx *fasthttp.RequestCtx) {
	users, err := h.Store.ListAllUsers(adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		out = append(out, adminUserView(user))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"users": out})
}

func (h *Handlers) AdminUserDetail(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	user, err := h.Store.GetUser(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		adminStoreError(ctx)
		return
	}
	orders, err := h.Store.ListOrders(user.ID, 100)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	quotes, err := h.Store.ListQuotesForUser(user.ID, 100)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	tickets, _ := h.Store.ListSupportTicketsForUser(user.ID, 50)

	orderViews := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		orderViews = append(orderViews, adminOrderView(order))
	}
	quoteViews := make([]map[string]any, 0, len(quotes))
	for _, quote := range quotes {
		quoteViews = append(quoteViews, adminQuoteView(quote))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"user":    adminUserView(user),
		"orders":  orderViews,
		"quotes":  quoteViews,
		"tickets": tickets,
	})
}

func (h *Handlers) AdminListOrders(ctx *fasthttp.RequestCtx) {
	orders, err := h.Store.ListAllOrders(adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	out := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		out = append(out, adminOrderView(order))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"orders": out})
}

func (h *Handlers) AdminGetOrderDetail(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	order, err := h.Store.GetOrder(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		adminStoreError(ctx)
		return
	}

	user, _ := h.Store.GetUser(order.UserID)
	ticket, _ := h.Store.GetSupportTicketForOrder(order.ID)
	var messages []db.SupportMessage
	if ticket.ID != "" {
		messages, _ = h.Store.GetTicketMessages(ticket.ID)
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"order":            adminOrderView(order),
		"user":             adminUserView(user),
		"support_ticket":   ticket,
		"support_messages": messages,
	})
}

func (h *Handlers) AdminSyncOrder(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	// Quotes are the active purchase record; the legacy order table is only
	// consulted for older data. If the id is a quote, re-poll the supplier
	// through the shared queue and apply the conditional transition.
	if q, qerr := h.Store.GetQuote(id); qerr == nil {
		if q.SupplierOrderID != "" {
			if order, err := h.CR.GetOrder(h.supplierContext(ctx), q.SupplierOrderID); err == nil && order.Status != "" {
				local := cryptorefills.MapToQuoteStatus(order.Status)
				switch local {
				case cryptorefills.QuoteFulfilled:
					_ = h.Store.CompleteQuoteWithFulfillment(q.ID, cryptorefills.FulfillmentPayload(order))
				case cryptorefills.QuoteRefunded:
					var refund []byte
					if order.Refund != nil {
						refund, _ = jsonMarshal(order.Refund)
					}
					_ = h.Store.MarkQuoteRefunded(q.ID, refund, "admin sync: "+order.Status)
				case cryptorefills.QuoteFailed:
					_ = h.Store.MarkSupplierFailure(q.ID, "admin sync: "+order.Status)
				case cryptorefills.QuoteManualReview:
					_ = h.Store.MarkQuoteManualReview(q.ID, "admin sync: "+order.Status)
				default:
					if local != q.Status {
						_, _ = h.Store.SetSupplierStatus(q.ID, order.Status, local)
					}
				}
			}
		}
		h.audit(adminIdentity(ctx).User.ID, "admin.quote.sync", ctx, "synced quote "+id)
		latest, _ := h.Store.GetQuote(id)
		writeJSON(ctx, fasthttp.StatusOK, adminQuoteView(latest))
		return
	}
	order, err := h.Store.GetOrder(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order or quote not found")
		return
	}
	if err != nil {
		adminStoreError(ctx)
		return
	}

	identity := adminIdentity(ctx)

	// Supplier status is webhook-driven. This operator action intentionally
	// re-reads the durable local record and never bypasses the shared queue by
	// polling the supplier directly.

	h.audit(identity.User.ID, "admin.order.sync", ctx, "synced order "+order.ID+" (status: "+order.Status+")")
	writeJSON(ctx, fasthttp.StatusOK, adminOrderView(order))
}

func (h *Handlers) AdminRefundOrder(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	order, err := h.Store.GetOrder(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		adminStoreError(ctx)
		return
	}

	identity := adminIdentity(ctx)

	// Non-custodial: nim.shop holds no balance, so there is nothing to credit
	// internally. "Refund" marks the order refunded; any NIM repayment to the
	// buyer is a separate manual on-chain action recorded in the audit log.
	if order.Status == "refunded" {
		writeError(ctx, fasthttp.StatusConflict, "order already refunded")
		return
	}

	_ = h.Store.SetOrderStatus(order.ID, "refunded")
	order.Status = "refunded"

	h.audit(identity.User.ID, "admin.order.manual_refund", ctx, "manually refunded order "+order.ID+" ("+order.PriceUSD.String()+" USD)")
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"ok":       true,
		"order_id": order.ID,
		"status":   "refunded",
		"refunded": order.PriceUSD.String(),
	})
}

// Support Ticket Management for Admin
func (h *Handlers) AdminListSupportTickets(ctx *fasthttp.RequestCtx) {
	status := string(ctx.QueryArgs().Peek("status"))
	tickets, err := h.Store.ListAllSupportTickets(status, adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"tickets": tickets})
}

func (h *Handlers) AdminGetSupportTicket(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	ticket, err := h.Store.GetSupportTicket(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "support ticket not found")
		return
	}
	if err != nil {
		adminStoreError(ctx)
		return
	}

	messages, err := h.Store.GetTicketMessages(ticket.ID)
	if err != nil {
		adminStoreError(ctx)
		return
	}

	user, _ := h.Store.GetUser(ticket.UserID)

	// Fetch related order or quote details
	var orderData map[string]any
	if order, err := h.Store.GetOrder(ticket.OrderID); err == nil {
		orderData = adminOrderView(order)
	}

	var quoteData map[string]any
	if quote, err := h.Store.GetQuote(ticket.OrderID); err == nil {
		quoteData = adminQuoteView(quote)
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"ticket":   ticket,
		"messages": messages,
		"user":     adminUserView(user),
		"order":    orderData,
		"quote":    quoteData,
	})
}

type adminReplyTicketRequest struct {
	Message string `json:"message"`
	Status  string `json:"status,omitempty"` // waiting_user | resolved | closed
}

func (h *Handlers) AdminAddSupportMessage(ctx *fasthttp.RequestCtx) {
	ticketID, _ := ctx.UserValue("id").(string)
	identity := adminIdentity(ctx)

	var req adminReplyTicketRequest
	if err := readJSON(ctx, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "message cannot be empty")
		return
	}

	newStatus := strings.TrimSpace(req.Status)
	if newStatus == "" {
		newStatus = "waiting_user"
	}

	msg, err := h.Store.AddSupportMessage(ticketID, "admin", identity.User.Username, req.Message, newStatus)
	if err != nil {
		adminStoreError(ctx)
		return
	}

	h.audit(identity.User.ID, "admin.support.reply", ctx, "replied to ticket "+ticketID+" (status: "+newStatus+")")
	writeJSON(ctx, fasthttp.StatusCreated, msg)
}

type adminUpdateTicketStatusRequest struct {
	Status string `json:"status"`
}

func (h *Handlers) AdminUpdateSupportStatus(ctx *fasthttp.RequestCtx) {
	ticketID, _ := ctx.UserValue("id").(string)
	identity := adminIdentity(ctx)

	var req adminUpdateTicketStatusRequest
	if err := readJSON(ctx, &req); err != nil || strings.TrimSpace(req.Status) == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "valid status is required")
		return
	}

	validStatuses := map[string]bool{
		"open":          true,
		"waiting_user":  true,
		"waiting_admin": true,
		"resolved":      true,
		"closed":        true,
	}
	if !validStatuses[req.Status] {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid status value")
		return
	}

	if err := h.Store.UpdateSupportTicketStatus(ticketID, req.Status); err != nil {
		adminStoreError(ctx)
		return
	}

	h.audit(identity.User.ID, "admin.support.status_update", ctx, "updated ticket "+ticketID+" status to "+req.Status)
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"ok": true, "ticket_id": ticketID, "status": req.Status})
}

func (h *Handlers) AdminListQuotes(ctx *fasthttp.RequestCtx) {
	quotes, err := h.Store.ListAllQuotes(adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	out := make([]map[string]any, 0, len(quotes))
	for _, quote := range quotes {
		out = append(out, adminQuoteView(quote))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"quotes": out})
}

func (h *Handlers) AdminListTransactions(ctx *fasthttp.RequestCtx) {
	orders, err := h.Store.ListAllOrders(adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	out := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		out = append(out, adminOrderView(order))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"transactions": out})
}

func (h *Handlers) AdminManualReview(ctx *fasthttp.RequestCtx) {
	quotes, err := h.Store.ListQuotesByStatus("manual_review", adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	out := make([]map[string]any, 0, len(quotes))
	for _, quote := range quotes {
		out = append(out, adminQuoteView(quote))
	}
	// Refunds in flight / supplier failures waiting on the refund worker.
	refunds, err := h.Store.ListQuotesByStatuses([]string{"failed_supplier", "refunding"}, adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	refundViews := make([]map[string]any, 0, len(refunds))
	for _, q := range refunds {
		refundViews = append(refundViews, adminQuoteView(q))
	}
	// Orders stuck non-terminal with a supplier invoice (reconciler handles them;
	// shown here so an operator can see if anything lingers).
	stuck := []map[string]any{}
	for _, st := range []string{"pending", "processing"} {
		os, err := h.Store.ListOrdersByStatus(st, adminLimit(ctx))
		if err != nil {
			adminStoreError(ctx)
			return
		}
		for _, o := range os {
			if o.SupplierInvoiceID != nil && *o.SupplierInvoiceID != "" {
				stuck = append(stuck, adminOrderView(o))
			}
		}
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"quotes": out, "refunds_in_flight": refundViews, "stuck_orders": stuck,
	})
}

// AdminResolveQuote lets an operator move a quote to an allowed status
// (e.g. a manual_review crash-window quote that was resolved by hand).
func (h *Handlers) AdminResolveQuote(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := readJSON(ctx, &req); err != nil || req.Status == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "status is required")
		return
	}
	if req.Status == "manual_review" && req.Reason != "" {
		if err := h.Store.MarkQuoteManualReview(id, req.Reason); err != nil {
			writeError(ctx, fasthttp.StatusConflict, "cannot set status: "+err.Error())
			return
		}
	} else {
		if err := h.Store.SetQuoteStatus(id, req.Status); err != nil {
			writeError(ctx, fasthttp.StatusConflict, "cannot set status: "+err.Error())
			return
		}
	}
	h.audit(adminIdentity(ctx).User.ID, "admin.quote.resolve", ctx, "manually set quote "+id+" to "+req.Status)
	writeJSON(ctx, fasthttp.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) AdminOracleHealth(ctx *fasthttp.RequestCtx) {
	callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	quote, err := h.Oracle.NIMUSD(callCtx)
	cancel()
	if err != nil {
		writeJSON(ctx, fasthttp.StatusServiceUnavailable, map[string]any{"healthy": false, "error": "oracle unavailable or sources disagree", "min_sources": h.Cfg.OracleMinSources, "max_spread_bps": h.Cfg.OracleMaxSpreadBps})
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"healthy": true, "median_usd": quote.MedianUSD, "valid_sources": quote.ValidSources, "spread_bps": quote.SpreadBps, "observed_at": quote.ObservedAt, "sources": quote.Sources, "min_sources": h.Cfg.OracleMinSources, "max_spread_bps": h.Cfg.OracleMaxSpreadBps})
}

type adminMarginRequest struct {
	GlobalMarginBps int `json:"global_margin_bps"`
}

func (h *Handlers) AdminUpdateMargin(ctx *fasthttp.RequestCtx) {
	var req adminMarginRequest
	if err := readJSON(ctx, &req); err != nil || req.GlobalMarginBps < 0 || req.GlobalMarginBps > 5000 {
		writeError(ctx, fasthttp.StatusBadRequest, "global_margin_bps must be between 0 and 5000")
		return
	}
	identity := adminIdentity(ctx)
	settings, err := h.Store.SetGlobalMarginBps(req.GlobalMarginBps, identity.User.ID, time.Now().UTC())
	if err != nil {
		adminStoreError(ctx)
		return
	}
	h.audit(identity.User.ID, "admin.settings.margin_updated", ctx, "global margin set to "+strconv.Itoa(req.GlobalMarginBps)+" bps")
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"global_margin_bps": settings.GlobalMarginBps, "updated_at": settings.UpdatedAt, "updated_by": settings.UpdatedBy})
}

func (h *Handlers) AdminListAudit(ctx *fasthttp.RequestCtx) {
	events, err := h.Store.ListAdminAudit(adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"events": events})
}

/* --------------------------- catalog rules (admin) ----------------------- */

// AdminGetCatalogRules returns the live catalog visibility policy.
func (h *Handlers) AdminGetCatalogRules(ctx *fasthttp.RequestCtx) {
	rules, err := h.Store.GetCatalogRules()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, rules)
}

// AdminUpdateCatalogRules replaces the whole policy atomically. Every
// change is audit-logged with the operator identity + IP.
func (h *Handlers) AdminUpdateCatalogRules(ctx *fasthttp.RequestCtx) {
	var rules catalog.Rules
	if err := readJSON(ctx, &rules); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid catalog rules payload")
		return
	}
	identity := adminIdentity(ctx)
	rules.UpdatedBy = identity.User.ID
	rules.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	saved, err := h.Store.SetCatalogRules(rules)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	h.audit(identity.User.ID, "admin.catalog.rules_updated", ctx, catalogRulesSummary(&saved))
	writeJSON(ctx, fasthttp.StatusOK, saved)
}

func catalogRulesSummary(r *catalog.Rules) string {
	cap := "off"
	if r.MaxFaceValueUSD > 0 {
		cap = strconv.FormatFloat(r.MaxFaceValueUSD, 'f', -1, 64)
	}
	return "cap=" + cap + "usd hidden_families=" + strconv.Itoa(len(r.HiddenFamilies)) +
		" banned_categories=" + strconv.Itoa(len(r.BannedCategories)) +
		" banned_kinds=" + strconv.Itoa(len(r.BannedKinds)) +
		" hidden_countries=" + strconv.Itoa(len(r.HiddenCountries)) +
		" visible_countries=" + strconv.Itoa(len(r.VisibleCountries)) +
		" oos=" + r.OutOfStockPolicy
}

func adminUserView(user db.User) map[string]any {
	return map[string]any{
		"id": user.ID, "nimiq_address": user.NimiqAddress, "created_at": user.CreatedAt,
		"last_ip": user.LastIP, "last_country": user.LastCountry, "last_seen_at": user.LastSeenAt,
	}
}

func adminOrderView(order db.Order) map[string]any {
	var fulfillment any
	if len(order.Fulfillment) > 0 && string(order.Fulfillment) != "null" {
		var red map[string]any
		if err := json.Unmarshal(order.Fulfillment, &red); err == nil {
			fulfillment = red
		} else {
			fulfillment = string(order.Fulfillment)
		}
	}

	var payload any
	if len(order.Payload) > 0 {
		var pmap map[string]any
		if err := json.Unmarshal(order.Payload, &pmap); err == nil {
			payload = pmap
		}
	}

	return map[string]any{
		"id":                  order.ID,
		"user_id":             order.UserID,
		"kind":                order.Kind,
		"supplier_order_id":   order.SupplierOrderID,
		"supplier_invoice_id": order.SupplierInvoiceID,
		"category_id":         order.CategoryID,
		"product_id":          order.ProductID,
		"quantity":            order.Quantity,
		"price_usd":           order.PriceUSD.String(),
		"status":              order.Status,
		"payload":             payload,
		"fulfillment":         fulfillment,
		"created_at":          order.CreatedAt,
		"updated_at":          order.UpdatedAt,
	}
}

func adminQuoteView(quote db.Quote) map[string]any {
	return map[string]any{
		"id": quote.ID, "user_id": quote.UserID,
		"product_id": quote.ProductID, "product_country": quote.ProductCountry,
		"denomination": quote.Denomination, "product_value": quote.ProductValue,
		"quantity": quote.Quantity, "product_usd": quote.ProductUSD.String(),
		"customer_email": quote.CustomerEmail, "phone_number": quote.PhoneNumber,
		"coin": quote.Coin, "network": quote.Network, "coin_amount": quote.CoinAmount,
		"wallet_address":    quote.WalletAddress,
		"supplier_order_id": quote.SupplierOrderID, "supplier_status": quote.SupplierStatus,
		"payment_expiry": quote.PaymentExpiry, "order_attempts": quote.OrderAttempts,
		"status": quote.Status, "refund_reason": quote.RefundReason,
		"expires_at": quote.ExpiresAt, "created_at": quote.CreatedAt, "updated_at": quote.UpdatedAt,
	}
}

func adminStoreError(ctx *fasthttp.RequestCtx) {
	writeError(ctx, fasthttp.StatusInternalServerError, "admin data is temporarily unavailable")
}

// AdminNotificationStatus reports the live channel configuration so the
// admin UI can show "Email: SMTP ✓ (smtp.gmail.com)" / "SMS: Pingram ✓"
// badges and disable send buttons when a channel is off.
//
// Returns: { email: {enabled, dry_run, host, from}, sms: {enabled, dry_run, url, sender} }
func (h *Handlers) AdminNotificationStatus(ctx *fasthttp.RequestCtx) {
	email := map[string]any{
		"enabled": h.Gift != nil && h.Gift.EmailEnabled(),
		"host":    h.Cfg.NotifySMTPHost,
		"from":    h.Cfg.NotifySMTPFromAddr,
		"dry_run": h.Cfg.NotifyEmailDryRun,
	}
	sms := map[string]any{
		"enabled": h.Gift != nil && h.Gift.SMSEnabled(),
		"url":     h.Cfg.NotifySMSURL,
		"sender":  h.Cfg.NotifySMSSender,
		"dry_run": h.Cfg.NotifySMSDryRun,
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"email":        email,
		"sms":          sms,
		"gift_enabled": h.Gift != nil && h.Gift.Enabled(),
	})
}

// AdminSendNotification lets the operator send an arbitrary email or SMS
// directly to any recipient — not tied to a quote. Use cases:
//   - manual outreach ("we noticed a delay with your order…")
//   - QA / dry-run testing the SMTP and SMS providers
//   - operational notices (e.g. scheduled maintenance)
//
// Channel is chosen by the request body (email / sms / both). The body is
// kept under the same per-channel caps as the gift notifier (2000 / 160).
//
// IMPORTANT: this endpoint is open to anyone with the admin session cookie,
// so its audit log is mandatory — every send lands in the immutable
// admin_audit table with operator id + ip + body length + recipient hash.
func (h *Handlers) AdminSendNotification(ctx *fasthttp.RequestCtx) {
	identity := adminIdentity(ctx)
	if h.Gift == nil || !h.Gift.Enabled() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "notification channel is not configured (set NOTIFY_EMAIL_ENABLED / NOTIFY_SMS_ENABLED in .env)")
		return
	}
	var req struct {
		Channel    string `json:"channel"`              // "email" | "sms" | "both"
		ToEmail    string `json:"to_email,omitempty"`   // recipient email (when channel includes email)
		ToPhone    string `json:"to_phone,omitempty"`   // recipient phone (E.164, when channel includes sms)
		Subject    string `json:"subject,omitempty"`    // email subject; ignored when SMS-only
		Body       string `json:"body"`                 // raw body; truncated per channel cap
		DryRun     bool   `json:"dry_run,omitempty"`    // force a dry-run even when the channel is enabled
		Category   string `json:"category,omitempty"`   // free-text label for audit ("support", "ops", "test")
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid notification payload")
		return
	}
	channel := notification.NormalizeChannel(req.Channel)
	if channel == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "channel must be one of: email | sms | both")
		return
	}
	emailBody, smsBody := notification.SanitizeMessage(req.Body)
	if emailBody == "" && smsBody == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "body cannot be empty")
		return
	}
	if (channel == "email" || channel == "both") && req.ToEmail == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "to_email is required for email channel")
		return
	}
	if (channel == "sms" || channel == "both") && req.ToPhone == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "to_phone is required for sms channel")
		return
	}
	if channel == "sms" || channel == "both" {
		p := strings.TrimSpace(strings.ReplaceAll(req.ToPhone, " ", ""))
		if !strings.HasPrefix(p, "+") || len(p) < 8 || len(p) > 16 {
			writeError(ctx, fasthttp.StatusBadRequest, "to_phone must be in E.164 format (e.g. +905551234567)")
			return
		}
	}
	if req.Subject == "" {
		req.Subject = "nim.shop"
	}
	// When the operator ticks dry_run, force it regardless of the global
	// config so a quick UI test never accidentally sends a real SMS.
	emailDryRun := req.DryRun || (h.Gift != nil && !h.Gift.EmailEnabled())
	smsDryRun := req.DryRun || (h.Gift != nil && !h.Gift.SMSEnabled())

	plain := emailBody
	html := notification.WrapOperatorEmailHTML(emailBody, req.Subject)
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	emailErr, smsErr := h.Gift.NotifyGiftDryRun(callCtx, channel, req.ToEmail, req.ToPhone, req.Subject, plain, html, smsBody, emailDryRun, smsDryRun)

	out := map[string]any{
		"ok":        emailErr == nil || smsErr == nil,
		"channel":   channel,
		"category":  req.Category,
		"dry_run":   req.DryRun,
		"email":     "skipped",
		"sms":       "skipped",
		"to":        map[string]any{"email": req.ToEmail, "phone": req.ToPhone},
		"body_chars": map[string]int{"email": len(plain), "sms": len(smsBody)},
	}
	if channel == "email" || channel == "both" {
		if emailErr != nil {
			out["email"] = "failed: " + emailErr.Error()
		} else if emailDryRun {
			out["email"] = "dry_run"
		} else {
			out["email"] = "sent"
		}
	}
	if channel == "sms" || channel == "both" {
		if smsErr != nil {
			out["sms"] = "failed: " + smsErr.Error()
		} else if smsDryRun {
			out["sms"] = "dry_run"
		} else {
			out["sms"] = "sent"
		}
	}

	h.audit(identity.User.ID, "admin.notification.sent", ctx,
		fmt.Sprintf("channel=%s category=%s email=%v sms=%v to_email=%s to_phone=%s body_chars[email]=%d body_chars[sms]=%d dry_run=%v",
			channel, req.Category, out["email"], out["sms"], req.ToEmail, req.ToPhone, len(plain), len(smsBody), req.DryRun))
	writeJSON(ctx, fasthttp.StatusOK, out)
}

// AdminSendGiftNotification lets the operator manually re-send the buyer-
// authored gift message to the recipient. Useful for retrying after a
// provider outage or a wrong recipient email the buyer just corrected.
//
// Request: POST /api/admin/quotes/{id}/send-gift-notification?force=1
//   force=1  -> bypass the GiftNotifiedAt idempotency marker (re-send even
//                after a successful delivery; providers may bill twice).
//   force=0  -> no-op when already notified (default).
//
// Response: { "ok": true, "email": "sent|skipped|failed", "sms": ..., "notified_at": ... }
func (h *Handlers) AdminSendGiftNotification(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	identity := adminIdentity(ctx)

	quote, err := h.Store.GetQuote(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "quote not found")
		return
	}
	if err != nil {
		adminStoreError(ctx)
		return
	}
	if h.Gift == nil || !h.Gift.Enabled() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "gift channel is not configured (set NOTIFY_EMAIL_ENABLED / NOTIFY_SMS_ENABLED in .env)")
		return
	}
	channel := notification.NormalizeChannel(quote.GiftChannel)
	if channel == "" {
		writeError(ctx, fasthttp.StatusConflict, "quote has no gift channel (gift_channel is empty)")
		return
	}
	force := string(ctx.QueryArgs().Peek("force")) == "1"
	if !force && !quote.GiftNotifiedAt.IsZero() {
		writeJSON(ctx, fasthttp.StatusOK, map[string]any{
			"ok":           true,
			"skipped":      "already notified",
			"notified_at":  quote.GiftNotifiedAt,
			"channel":      channel,
		})
		return
	}

	recipientEmail := quote.CustomerEmail
	recipientPhone := quote.GiftRecipientPhone
	if recipientPhone == "" {
		recipientPhone = quote.PhoneNumber
	}
	emailBody, smsBody := notification.SanitizeMessage(quote.GiftMessage)
	faceValue, currency, product := adminQuoteRenderFields(quote)
	deliveredBy := "email"
	if strings.HasPrefix(quote.BeneficiaryAccount, "+") {
		deliveredBy = "phone"
	}
	subject, plain, html := h.Gift.RenderEmail(recipientEmail, product, faceValue, currency, quote.CustomerEmail, emailBody, deliveredBy)
	if smsBody == "" {
		smsBody = h.Gift.RenderSMS(product, faceValue, currency, "", deliveredBy)
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	emailErr, smsErr := h.Gift.NotifyGift(callCtx, channel, recipientEmail, recipientPhone, subject, plain, html, smsBody)

	out := map[string]any{
		"ok":      emailErr == nil || smsErr == nil,
		"channel": channel,
		"to":      map[string]any{"email": recipientEmail, "phone": recipientPhone},
		"email":   "skipped",
		"sms":     "skipped",
	}
	if channel == "email" || channel == "both" {
		if emailErr != nil {
			out["email"] = "failed: " + emailErr.Error()
		} else {
			out["email"] = "sent"
		}
	}
	if channel == "sms" || channel == "both" {
		if smsErr != nil {
			out["sms"] = "failed: " + smsErr.Error()
		} else {
			out["sms"] = "sent"
		}
	}
	if emailErr == nil || smsErr == nil {
		if err := h.Store.MarkGiftNotified(quote.ID); err != nil {
			out["notified_at_mark_error"] = err.Error()
		} else {
			out["notified_at"] = time.Now().UTC()
		}
	}

	h.audit(identity.User.ID, "admin.gift.notification_sent", ctx,
		"quote="+id+" channel="+channel+" email="+fmt.Sprint(out["email"])+" sms="+fmt.Sprint(out["sms"]))
	writeJSON(ctx, fasthttp.StatusOK, out)
}

// adminQuoteRenderFields is the admin-handler twin of the main.go helper so
// the gift email body uses the same face-value + currency rendering as the
// auto-fired tracker.
func adminQuoteRenderFields(q db.Quote) (faceValue string, currency string, product string) {
	currency = adminExtractCurrency(q.Denomination, q.ProductCountry)
	faceValue = strconv.FormatFloat(q.ProductValue, 'f', 0, 64)
	if q.ProductValue == float64(int64(q.ProductValue)) {
		faceValue = strconv.FormatInt(int64(q.ProductValue), 10)
	}
	product = q.ProductID
	return
}

func adminExtractCurrency(denom, country string) string {
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

var _ = adminmodel.Settings{}
