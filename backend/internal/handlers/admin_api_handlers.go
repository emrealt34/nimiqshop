package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/catalog"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/mailtrap"
	"nimiqshop/internal/notification"
	"nimiqshop/internal/poolstake"
	"nimiqshop/internal/sampleassets"
	"nimiqshop/internal/stakeledger"
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
	// QueueStats dereferences its receiver, so a deployment wired without a
	// supplier client would panic here instead of showing an empty queue.
	var crQueue cryptorefills.QueueStats
	if h.CR != nil {
		crQueue = h.CR.QueueStats()
	}

	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		adminStoreError(ctx)
		return
	}

	// Players: registration + activity counters for the console header.
	players, perr := h.Store.PlayerStats(time.Now().UTC())
	if perr != nil {
		adminStoreError(ctx)
		return
	}
	// Cashback payment queue: one bucket per payout status, with the NIM
	// sitting in it and how long the oldest row has been waiting.
	queue, qerr := h.Store.CashbackQueueStats()
	if qerr != nil {
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
		"players":        adminPlayerView(players),
		"cashback_queue": adminCashbackQueueView(queue),
		"settings":       map[string]any{"updated_at": settings.UpdatedAt, "updated_by": settings.UpdatedBy, "note": "pricing margin is set by the supplier; no local margin"},
		"payment":        map[string]any{"rail": "cryptorefills", "custody": false, "note": "customer pays the supplier's one-time wallet address with stablecoins; Cryptorefills is merchant of record"},
	}
	writeJSON(ctx, fasthttp.StatusOK, response)
}

// adminPlayerView is the "Players" card set: how many customers exist, how
// many are new, and how many actually did something in each window.
func adminPlayerView(p db.PlayerStats) map[string]any {
	return map[string]any{
		"total_registered": p.TotalRegistered,
		"new_today":        p.NewToday,
		"new_this_week":    p.NewThisWeek,
		"active_today":     p.ActiveToday,
		"active_this_week": p.ActiveThisWeek,
		"with_orders":      p.WithOrders,
		"stakers":          p.Stakers,
		"now":              p.Now,
		"today_start":      p.TodayStart,
		// The windows are stated explicitly so the console can label them
		// honestly instead of implying a calendar week.
		"week_window_days": 7,
	}
}

// adminCashbackQueueView flattens the per-status buckets into the shape the
// console renders, and adds the roll-ups an operator reads first.
func adminCashbackQueueView(queue map[string]db.CashbackQueueStat) map[string]any {
	bucket := func(status string) map[string]any {
		st, ok := queue[status]
		if !ok {
			return map[string]any{"count": 0, "amount_nim": 0.0, "retrying": 0, "last_error_count": 0}
		}
		out := map[string]any{
			"count":            st.Count,
			"amount_luna":      st.AmountLuna,
			"amount_nim":       float64(st.AmountLuna) / 100_000,
			"retrying":         st.Retrying,
			"boosted":          st.Boosted,
			"last_error_count": st.LastErrorCount,
		}
		if st.OldestAt != nil {
			out["oldest_at"] = *st.OldestAt
		}
		if st.NewestAt != nil {
			out["newest_at"] = *st.NewestAt
		}
		return out
	}
	pending := queue[db.CashbackQueued]
	sending := queue[db.CashbackSending]
	broadcast := queue[db.CashbackBroadcast]
	paid := queue[db.CashbackPaid]
	skipped := queue[db.CashbackSkipped]

	oldestPending := pending.OldestAt
	if oldestPending == nil {
		oldestPending = sending.OldestAt
	}

	return map[string]any{
		"pending":   bucket(db.CashbackQueued),
		"retrying":  bucket(db.CashbackSending),
		"broadcast": bucket(db.CashbackBroadcast),
		"paid":      bucket(db.CashbackPaid),
		"failed":    bucket(db.CashbackSkipped),
		// Roll-ups: "sent" is everything that reached the chain, confirmed or
		// still confirming; "in flight" is what the worker is working on now.
		"totals": map[string]any{
			"sent_count":      broadcast.Count + paid.Count,
			"sent_nim":        float64(broadcast.AmountLuna+paid.AmountLuna) / 100_000,
			"in_flight_count": pending.Count + sending.Count,
			"in_flight_nim":   float64(pending.AmountLuna+sending.AmountLuna) / 100_000,
			"failed_count":    skipped.Count,
			"boosted_count":   pending.Boosted + sending.Boosted + broadcast.Boosted + paid.Boosted,
			"oldest_pending_at": func() any {
				if oldestPending == nil {
					return nil
				}
				return *oldestPending
			}(),
		},
	}
}

// AdminListUsers is the console's customer list. Sorting happens server-side
// over every registered buyer, THEN the limit is applied — sorting a page of
// 50 would only ever reorder those 50 and hide the real top spender.
//
//	GET /api/admin/users?sort=orders&dir=desc&limit=50
//	sort: registered | orders | spend | cashback | last_seen | address
func (h *Handlers) AdminListUsers(ctx *fasthttp.RequestCtx) {
	sortBy := string(ctx.QueryArgs().Peek("sort"))
	switch sortBy {
	case "registered", "orders", "spend", "cashback", "last_seen", "address":
	default:
		sortBy = "registered"
	}
	desc := strings.ToLower(string(ctx.QueryArgs().Peek("dir"))) != "asc"

	rows, err := h.Store.ListUsersWithStats(sortBy, desc, adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	baseBps := 0
	settings, serr := h.Store.GetAdminSettings(0)
	if serr == nil {
		baseBps = settings.EffectiveCashbackBps()
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		view := adminUserView(row.User)
		view["order_count"] = row.Agg.OrderCount
		view["quote_count"] = row.Agg.QuoteCount
		view["spend_usd"] = float64(row.Agg.SpendMicroUSD) / 1_000_000
		view["spend_micro_usd"] = row.Agg.SpendMicroUSD
		view["cashback_paid_nim"] = float64(row.Agg.CashbackPaidLuna) / 100_000
		view["cashback_pending_nim"] = float64(row.Agg.CashbackPendingLuna) / 100_000
		view["cashback_count"] = row.Agg.CashbackCount
		view["cashback_boosted_count"] = row.Agg.CashbackBoostedCount
		view["cashback_skipped"] = row.Agg.CashbackSkipped
		view["last_order_at"] = nilIfZero(row.Agg.LastOrderAt)
		view["last_activity_at"] = nilIfZero(row.Agg.LastActivityAt)
		view["last_cashback_at"] = nilIfZero(row.Agg.LastCashbackAt)
		view["last_cashback_bps"] = row.Agg.LastCashbackBps
		view["last_cashback_percent"] = float64(row.Agg.LastCashbackBps) / 100.0
		view["last_cashback_source"] = row.Agg.LastCashbackSource
		view["last_cashback_code"] = row.Agg.LastCashbackCode
		rowBase := baseBps
		if h.Pool != nil {
			// Per-user: the staker base applies when the wallet is staked.
			// The operator's admin-set StakerBaseBps overrides the pool's
			// number so the console shows what the shop will actually pay.
			if st := h.Store.UserStakerStake(ctx, row.ID); st.Staked {
				st.BaseBps = settings.EffectiveStakerCashbackBps()
				rowBase = st.EffectiveBaseBps(baseBps)
			}
		}
		rate := h.adminLiveCashbackRate(row.NimiqAddress, rowBase)
		for k, v := range rate {
			view[k] = v
		}
		out = append(out, view)
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"users": out,
		"sort":  sortBy,
		"dir":   map[bool]string{true: "desc", false: "asc"}[desc],
		"count": len(out),
	})
}

func nilIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
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
		quoteViews = append(quoteViews, h.adminQuoteView(quote))
	}

	// The customer's whole history in one ordered feed: purchases,
	// payment stages, expiries, cashback payouts and support tickets.
	events, err := h.Store.UserActivity(user.ID, 200)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	timeline := make([]map[string]any, 0, len(events))
	for _, e := range events {
		row := map[string]any{
			"at":     e.At,
			"kind":   e.Kind,
			"label":  e.Label,
			"detail": e.Detail,
			"status": e.Status,
		}
		if e.Ref != "" {
			row["ref"] = e.Ref
		}
		if e.AmountNIM != 0 {
			row["amount_nim"] = e.AmountNIM
		}
		if e.Bps > 0 {
			row["bps"] = e.Bps
			row["cashback_percent"] = float64(e.Bps) / 100.0
		}
		if e.Source != "" {
			row["source"] = e.Source
		}
		if e.Code != "" {
			row["code"] = e.Code
		}
		timeline = append(timeline, row)
	}

	// The same aggregate row the list shows, so the detail view and the list
	// can never disagree about a customer's totals.
	agg := db.UserAggregate{}
	if rows, aerr := h.Store.ListUsersWithStats("registered", false, 0); aerr == nil {
		for _, r := range rows {
			if r.ID == user.ID {
				agg = r.Agg
				break
			}
		}
	}
	paidLuna, pendingLuna, paidCount, pendingCount, _ := h.Store.UserCashbackTotals(user.ID)
	baseBps, stake := h.buyerBaseBps(ctx, user.ID)
	liveRate := h.adminLiveCashbackRate(user.NimiqAddress, baseBps)
	liveRate["staked"] = stake.Staked
	liveRate["stake_nim"] = float64(stake.StakeLuna) / lunaPerNIM
	h.syncProfitOnDemand(ctx, user.NimiqAddress)
	ledger := h.ledgerView(user.NimiqAddress)

	cbs, _ := h.Store.ListCashbacksByUser(user.ID, 80)
	cbViews := make([]map[string]any, 0, len(cbs))
	for _, cb := range cbs {
		row := adminCashbackView(cb)
		row["id"] = cb.ID
		row["quote_id"] = cb.QuoteID
		row["product_id"] = cb.ProductID
		row["created_at"] = cb.CreatedAt
		row["cashback_percent"] = float64(cb.Bps) / 100.0
		cbViews = append(cbViews, row)
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"user":      adminUserView(user),
		"orders":    orderViews,
		"quotes":    quoteViews,
		"tickets":   tickets,
		"cashbacks": cbViews,
		"ledger":    ledger,
		"rate":      liveRate,
		"stats": map[string]any{
			"order_count":            agg.OrderCount,
			"quote_count":            agg.QuoteCount,
			"spend_usd":              float64(agg.SpendMicroUSD) / 1_000_000,
			"cashback_paid_nim":      float64(paidLuna) / 100_000,
			"cashback_pending_nim":   float64(pendingLuna) / 100_000,
			"cashback_count":         paidCount + pendingCount,
			"cashback_boosted_count": agg.CashbackBoostedCount,
			"cashback_skipped":       agg.CashbackSkipped,
			"last_order_at":          nilIfZero(agg.LastOrderAt),
			"last_activity_at":       nilIfZero(agg.LastActivityAt),
		},
		"staking": map[string]any{
			"staked":       stake.Staked,
			"stake_luna":   stake.StakeLuna,
			"stake_nim":    float64(stake.StakeLuna) / 100_000,
			"locked_days":  stake.LockedDays,
			"pool_checked": h.Cfg.PoolAPIURL != "",
		},
		"timeline": timeline,
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
	if q, qerr := h.Store.GetQuote(id); qerr == nil {
		user, _ := h.Store.GetUser(q.UserID)
		writeJSON(ctx, fasthttp.StatusOK, map[string]any{
			"quote": h.adminQuoteView(q),
			"user":  adminUserView(user),
		})
		return
	}
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
		if q.TestMode {
			writeError(ctx, fasthttp.StatusConflict, "test-center quote: drive it from the admin test center (test-pay), not a real supplier sync")
			return
		}
		if q.SupplierOrderID == "" {
			writeError(ctx, 409, "supplier acceptance is unresolved; no supplier id is known, so automatic retry is unsafe")
			return
		}
		sctx := cryptorefills.WithEndUserIP(h.supplierContext(ctx), q.EndUserIP)
		sctx = cryptorefills.WithEndUserAgent(sctx, q.EndUserAgent)
		order, err := h.CR.GetOrderFresh(sctx, q.SupplierOrderID)
		if err != nil {
			h.supplierError(ctx, err, "could not verify supplier status")
			return
		}
		if _, err := h.Store.ApplySupplierOrder(q.ID, order); err != nil {
			adminStoreError(ctx)
			return
		}
		h.audit(adminIdentity(ctx).User.ID, "admin.quote.sync", ctx, "synced quote "+id)
		latest, _ := h.Store.GetQuote(id)
		writeJSON(ctx, fasthttp.StatusOK, h.adminQuoteView(latest))
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

// There is no documented supplier refund-creation endpoint. Do not fabricate
// a Refunded state from an operator click without a verified supplier response.
func (h *Handlers) AdminRefundOrder(ctx *fasthttp.RequestCtx) {
	writeError(ctx, 409, "Refunds are handled by Cryptorefills. Verify the supplier refund and sync the order; this action does not send money or mark a refund as completed.")
}

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
		quoteData = h.adminQuoteView(quote)
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
	Message  string `json:"message"`
	Status   string `json:"status,omitempty"`   // waiting_user | resolved | closed
	Internal bool   `json:"internal,omitempty"` // true = staff-only internal note
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
	if req.Internal {
		// Staff-only note: never touches the conversation status.
		newStatus = ""
	} else if newStatus == "" {
		newStatus = "waiting_user"
	}

	sender := "admin"
	if req.Internal {
		sender = "admin_note"
	}

	msg, err := h.Store.AddSupportMessage(ticketID, sender, identity.User.Username, req.Message, newStatus)
	if err != nil {
		adminStoreError(ctx)
		return
	}

	auditNote := "replied to ticket " + ticketID + " (status: " + newStatus + ")"
	if req.Internal {
		auditNote = "added an internal note to ticket " + ticketID
	}
	h.audit(identity.User.ID, "admin.support.reply", ctx, auditNote)

	// 1-Luna on-chain ping: "the shop replied to your ticket — reply in
	// Support". Best-effort and policy-gated (opt-out, budget); a disabled
	// notifier makes this a no-op so replies never depend on it.
	if !req.Internal && h.WalletNotifier != nil && h.WalletNotifier.Enabled() {
		if tk, terr := h.Store.GetSupportTicket(ticketID); terr == nil {
			addr := tk.UserAddress
			if addr == "" {
				addr = tk.UserID
			}
			refID := "ticket:" + ticketID + ":msg:" + msg.ID
			go func() {
				ctxN, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if _, err := h.WalletNotifier.NotifyReason(ctxN, notification.ReasonSupportReply, refID, addr, "reply in Support"); err != nil {
					log.Printf("notif: support-reply ping failed for ticket %s: %v", ticketID, err)
				}
			}()
		}
	}

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
		out = append(out, h.adminQuoteView(quote))
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
		out = append(out, h.adminQuoteView(quote))
	}
	// Refunds in flight / supplier failures waiting on the refund worker.
	refunds, err := h.Store.ListQuotesByStatuses([]string{"failed_supplier", "refunding"}, adminLimit(ctx))
	if err != nil {
		adminStoreError(ctx)
		return
	}
	refundViews := make([]map[string]any, 0, len(refunds))
	for _, q := range refunds {
		refundViews = append(refundViews, h.adminQuoteView(q))
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
	if req.Status != "manual_review" || strings.TrimSpace(req.Reason) == "" {
		writeError(ctx, 409, "payment, delivery and refund outcomes must come from verified supplier sync; manual review requires a reason")
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

func (h *Handlers) AdminGetCashback(ctx *fasthttp.RequestCtx) {
	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	recent, _ := h.Store.ListRecentCashbacks(40)
	rows := make([]map[string]any, 0, len(recent))
	for _, cb := range recent {
		row := adminCashbackView(cb)
		row["id"] = cb.ID
		row["quote_id"] = cb.QuoteID
		row["product_id"] = cb.ProductID
		row["product_nim"] = cb.ProductNIM
		row["memo"] = cb.Memo
		row["created_at"] = cb.CreatedAt
		row["updated_at"] = cb.UpdatedAt
		rows = append(rows, row)
	}

	ruleViews, usageViews, codesManaged, codeSource, err := h.adminCashbackCodeData()
	if err != nil {
		adminStoreError(ctx)
		return
	}

	ledgerCount, _ := h.Store.LedgerCount()
	watermark, _ := h.Store.LedgerWatermark()

	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"cashback_bps":            settings.EffectiveCashbackBps(),
		"cashback_percent":        float64(settings.EffectiveCashbackBps()) / 100.0,
		"staker_cashback_bps":     settings.EffectiveStakerCashbackBps(),
		"staker_cashback_percent": float64(settings.EffectiveStakerCashbackBps()) / 100.0,
		"wallet_configured":       h.Cfg.CashbackEnabled && h.Cfg.CashbackWalletSeed != "",
		"network":                 h.Cfg.CashbackNetwork,

		// Single-ledger staker programme: one parameter set, no ladder, no
		// lock list. "configured" and "live" are separate: a programme
		// without a pool+feed wiring is inert, and the console says so.
		"stake_cashback":           settings.EffectiveStakeCashback(),
		"stake_program_configured": true,
		"stake_program_live":       h.stakeProgramEnabled(),
		"pool_api_configured":      h.Cfg.PoolAPIURL != "",
		"pool_feed_configured":     h.Cfg.PoolFeedAPIKey != "",
		"pool_validator_address":   h.Cfg.PoolValidatorAddress,
		"ledger_count":             ledgerCount,
		"ledger_watermark_batch":   watermark,

		"cashback_code_enabled":  h.cashbackCodeEnabled(),
		"cashback_codes_managed": codesManaged,
		"cashback_codes_source":  codeSource,
		"cashback_code_rules":    ruleViews,
		"cashback_code_uses":     usageViews,

		"updated_at": settings.UpdatedAt,
		"updated_by": settings.UpdatedBy,
		"recent":     rows,
	})
}

func (h *Handlers) AdminUpdateCashback(ctx *fasthttp.RequestCtx) {
	var req struct {
		CashbackBps     *int     `json:"cashback_bps"`
		CashbackPercent *float64 `json:"cashback_percent"`
		// StakerBaseBps is the operator's staker base rate (what any staked
		// buyer earns before the ledger boost). A present value sets it;
		// ClearStakerCashback=true resets it to the 1% default; absent
		// leaves it alone.
		StakerBaseBps         *int     `json:"staker_cashback_bps"`
		StakerCashbackPercent *float64 `json:"staker_cashback_percent"`
		ClearStakerCashback   *bool    `json:"clear_staker_cashback"`
		// Pointer so "field absent" (leave the programme alone) is
		// distinguishable from an explicit set.
		StakeCashback     *adminmodel.StakeCashbackParams `json:"stake_cashback"`
		CashbackCodeRules *[]adminmodel.CashbackCodeRule  `json:"cashback_code_rules"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid cashback payload")
		return
	}
	identity := adminIdentity(ctx)
	now := time.Now().UTC()
	settings, err := h.Store.GetAdminSettings(0)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	bps := settings.EffectiveCashbackBps()
	baseTouched := false
	if req.CashbackBps != nil {
		bps = *req.CashbackBps
		baseTouched = true
	} else if req.CashbackPercent != nil {
		bps = int(*req.CashbackPercent*100 + 0.5)
		baseTouched = true
	}
	if bps < 0 || bps > 2000 {
		writeError(ctx, fasthttp.StatusBadRequest, "cashback must be 0\u201320% (0\u20132000 bps)")
		return
	}
	// Staker base: an explicit value sets it, ClearStakerCashback=true resets
	// it to the 1% default, absent leaves it alone.
	stakerTouched := false
	stakerBps := settings.EffectiveStakerCashbackBps()
	clearStaker := req.ClearStakerCashback != nil && *req.ClearStakerCashback
	if req.StakerBaseBps != nil {
		stakerBps = *req.StakerBaseBps
		stakerTouched = true
	} else if req.StakerCashbackPercent != nil {
		stakerBps = int(*req.StakerCashbackPercent*100 + 0.5)
		stakerTouched = true
	} else if clearStaker {
		stakerTouched = true
	}
	if stakerBps < 0 || stakerBps > 2000 {
		writeError(ctx, fasthttp.StatusBadRequest, "staker cashback must be 0\u201320% (0\u20132000 bps)")
		return
	}
	if !baseTouched && !stakerTouched && req.StakeCashback == nil && req.CashbackCodeRules == nil {
		writeError(ctx, fasthttp.StatusBadRequest, "nothing to update")
		return
	}
	if baseTouched {
		settings, err = h.Store.SetCashbackBps(bps, identity.User.ID, now)
		if err != nil {
			adminStoreError(ctx)
			return
		}
		h.audit(identity.User.ID, "admin.settings.cashback_updated", ctx, "base cashback set to "+strconv.Itoa(bps)+" bps")
	}
	if stakerTouched {
		var ptr *int
		if !clearStaker {
			v := stakerBps
			ptr = &v
		}
		settings, err = h.Store.SetStakerCashbackBps(ptr, identity.User.ID, now)
		if err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		if clearStaker {
			h.audit(identity.User.ID, "admin.settings.staker_cashback_updated", ctx, "staker cashback reset to default")
		} else {
			h.audit(identity.User.ID, "admin.settings.staker_cashback_updated", ctx, "staker cashback set to "+strconv.Itoa(stakerBps)+" bps")
		}
	}

	if req.StakeCashback != nil {
		settings, err = h.Store.SetStakeCashbackParams(*req.StakeCashback, identity.User.ID, now)
		if err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		p := settings.EffectiveStakeCashback()
		h.audit(identity.User.ID, "admin.settings.stake_cashback_updated", ctx,
			fmt.Sprintf("stake_cashback set to k=%g q=%g g0=%g t=%dd min=%vNIM cap=%dbps amax=$%v daily=$%v monthly=$%v",
				p.K, p.Q, p.G0, p.TDays, p.MinStakeNIM, p.MaxBoostBps, p.AMaxUSD, p.DailyCapUSD, p.MonthlyCapUSD))
	}

	if req.CashbackCodeRules != nil {
		settings, err = h.Store.SetCashbackCodeRules(*req.CashbackCodeRules, identity.User.ID, now)
		if err != nil {
			writeError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		rules := settings.EffectiveCashbackCodeRules()
		summary := make([]string, 0, len(rules))
		for _, rule := range rules {
			summary = append(summary, rule.Code+"="+strconv.Itoa(rule.CashbackBps)+"bps")
		}
		detail := "cashback codes disabled"
		if len(summary) > 0 {
			detail = "cashback codes set to [" + strings.Join(summary, ", ") + "]"
		}
		h.audit(identity.User.ID, "admin.settings.cashback_codes_updated", ctx, detail)
	}

	ruleViews, usageViews, codesManaged, codeSource, err := h.adminCashbackCodeData()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"cashback_bps":           settings.EffectiveCashbackBps(),
		"cashback_percent":       float64(settings.EffectiveCashbackBps()) / 100.0,
		"stake_cashback":         settings.EffectiveStakeCashback(),
		"stake_program_live":     h.stakeProgramEnabled(),
		"cashback_code_enabled":  h.cashbackCodeEnabled(),
		"cashback_codes_managed": codesManaged,
		"cashback_codes_source":  codeSource,
		"cashback_code_rules":    ruleViews,
		"cashback_code_uses":     usageViews,
		"updated_at":             settings.UpdatedAt,
		"updated_by":             settings.UpdatedBy,
	})
}

// AdminListStakeLedger is the operator's single-ledger table: every wallet
// in the programme with its book, loyalty age and current boost rate.
// NIM values are converted to USD with the live oracle price when available.
func (h *Handlers) AdminListStakeLedger(ctx *fasthttp.RequestCtx) {
	nimUsd := h.bestNIMUSD()
	settings, _ := h.Store.GetAdminSettings(0)
	p := settings.EffectiveStakeCashback()
	rows, err := h.Store.ListStakeLedgers(500)
	if err != nil {
		adminStoreError(ctx)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		l := r.Ledger
		bps := stakeledger.BoostBps(&l, nimUsd, p)
		out = append(out, map[string]any{
			"address":            l.Address,
			"stake_nim":          l.S,
			"ledger_nim":         l.A,
			"ledger_usd":         l.A * nimUsd,
			"loyalty_days":       l.D,
			"loyalty_multiplier": stakeledger.G(l.D, p),
			"boost_bps":          bps,
			"boost_percent":      float64(bps) / 100.0,
			"spent_day_usd":      float64(l.HDay) / 100,
			"spent_month_usd":    float64(l.HMonth) / 100,
			"month_accrual_nim":  l.AccM,
			"last_batch":         l.LastBatch,
			"updated_at":         l.UpdatedAt,
		})
	}
	watermark, _ := h.Store.LedgerWatermark()
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"nim_usd":         nimUsd,
		"watermark_batch": watermark,
		"count":           len(out),
		"ledgers":         out,
	})
}

// AdminResetStakeLedger is the operator's per-wallet "start over": wipes the
// balance, loyalty age and month accrual of one wallet (fraud / chargeback /
// goodwill). The observed stake and the day/month spend counters survive.
func (h *Handlers) AdminResetStakeLedger(ctx *fasthttp.RequestCtx) {
	var req struct {
		Address string `json:"address"`
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid reset payload")
		return
	}
	if req.Address == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "address is required")
		return
	}
	identity := adminIdentity(ctx)
	if err := h.Store.ResetStakeLedger(req.Address); err != nil {
		writeError(ctx, fasthttp.StatusNotFound, "no ledger for that address")
		return
	}
	h.audit(identity.User.ID, "admin.stake_ledger_reset", ctx, "reset ledger for "+poolstake.CanonicalAddress(req.Address))
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"ok": true})
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

// adminLiveCashbackRate is what the next fulfilled order would pay without a
// promo code: shop base + this wallet's current staker-ledger boost.
func (h *Handlers) adminLiveCashbackRate(addr string, baseBps int) map[string]any {
	boost := 0
	if lv := h.ledgerView(addr); lv != nil {
		switch v := lv["boost_bps"].(type) {
		case int:
			boost = v
		case int64:
			boost = int(v)
		case float64:
			boost = int(v)
		}
	}
	total := baseBps + boost
	src := "base"
	if boost > 0 {
		src = "staker"
	}
	return map[string]any{
		"base_bps":            baseBps,
		"base_percent":        float64(baseBps) / 100.0,
		"boost_bps":           boost,
		"boost_percent":       float64(boost) / 100.0,
		"live_bps":            total,
		"live_percent":        float64(total) / 100.0,
		"live_source":         src,
		"staker_program_live": h.stakeProgramEnabled(),
	}
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
		"estimated_nim": quote.EstimatedNIM,
		// Test-center orders are clearly marked so an operator never
		// mistakes a simulated purchase for a real one.
		"test_mode":         quote.TestMode,
		"cashback_code":     quote.CashbackCode,
		"cashback_code_bps": quote.CashbackCodeBps,
	}
}

func adminCashbackView(cb db.Cashback) map[string]any {
	return map[string]any{
		"status":      cb.Status,
		"paid":        cb.Status == db.CashbackPaid,
		"amount_nim":  float64(cb.AmountLuna) / 100000.0,
		"amount_luna": cb.AmountLuna,
		"bps":         cb.Bps,
		"tx_hash":     cb.TxHash,
		"skip_reason": cb.SkipReason,
		"last_error":  cb.LastError,
		"paid_at":     cb.PaidAt,
		"recipient":   cb.Recipient,
		// Test-center payouts carry a TESTTX- hash; flag them so the queue
		// view can badge simulated rows.
		"test_mode":   cb.TestMode,
		"destination": cb.CashbackDestination,

		"boosted":             cb.Boosted,
		"stake_luna":          cb.StakeLuna,
		"stake_nim":           float64(cb.StakeLuna) / 100000.0,
		"tier_min_stake_luna": cb.TierMinStakeLuna,
		"cashback_source":     cb.CashbackSource,
		"cashback_code":       cb.CashbackCode,
	}
}

func (h *Handlers) adminQuoteView(quote db.Quote) map[string]any {
	view := adminQuoteView(quote)
	if cb, err := h.Store.GetCashbackByQuote(quote.ID); err == nil {
		view["cashback"] = adminCashbackView(cb)
	}
	return view
}

func adminStoreError(ctx *fasthttp.RequestCtx) {
	writeError(ctx, fasthttp.StatusInternalServerError, "admin data is temporarily unavailable")
}

// AdminNotificationStatus reports the live mail transport so the admin UI
// can show a "Mailtrap ✓ / OFF" badge and disable send buttons when the
// transport is off. Mailtrap is the ONLY channel — the SMTP client and the
// SMS sender no longer exist, so there is nothing else to report.
//
// Returns: { email: {enabled, sandbox, from, category}, gift_enabled }
func (h *Handlers) AdminNotificationStatus(ctx *fasthttp.RequestCtx) {
	email := map[string]any{
		"enabled":  h.Mail != nil && h.Mail.Enabled(),
		"sandbox":  h.Mail != nil && h.Mail.Sandbox(),
		"from":     "",
		"category": mailtrap.DefaultCategory,
	}
	if h.Mail != nil {
		if cfg := h.Mail.Config(); cfg.Enabled() {
			email["from"] = cfg.FromEmail
			if cfg.Category != "" {
				email["category"] = cfg.Category
			}
		}
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"email":        email,
		"gift_enabled": h.Mail != nil && h.Mail.Enabled(),
	})
}

// AdminSendNotification lets the operator send an arbitrary email directly
// to any recipient — not tied to a quote. Email is the one channel (Mailtrap
// is the transport; the SMTP client and the SMS sender are gone). Use cases:
//   - manual outreach ("we noticed a delay with your order…")
//   - QA / dry-run testing the mail transport
//   - operational notices (e.g. scheduled maintenance)
//
// The body is capped at emailBodyMaxLen (2000), as the gift note is.
//
// IMPORTANT: this endpoint is open to anyone with the admin session cookie,
// so its audit log is mandatory — every send lands in the immutable
// admin_audit table with operator id + ip + body length + recipient hash.
func (h *Handlers) AdminSendNotification(ctx *fasthttp.RequestCtx) {
	identity := adminIdentity(ctx)
	if h.Mail == nil || !h.Mail.Enabled() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "mail transport is not configured (set MAILTRAP_API_TOKEN and MAILTRAP_FROM_EMAIL in .env)")
		return
	}
	var req struct {
		ToEmail  string `json:"to_email"`           // recipient email (required — email is the one channel)
		Subject  string `json:"subject,omitempty"`  // email subject
		Body     string `json:"body"`               // raw body; capped at the email limit
		DryRun   bool   `json:"dry_run,omitempty"`  // log the payload instead of sending
		Category string `json:"category,omitempty"` // free-text label for audit ("support", "ops", "test")
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid notification payload")
		return
	}
	to := strings.TrimSpace(req.ToEmail)
	if !validEmail(to) {
		writeError(ctx, fasthttp.StatusBadRequest, "to_email must be a valid email address")
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "body cannot be empty")
		return
	}
	if len(body) > emailBodyMaxLen {
		writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("body is %d characters — %d is the limit", len(body), emailBodyMaxLen))
		return
	}
	if req.Subject == "" {
		req.Subject = h.Cfg.SiteName()
	}

	out := map[string]any{
		"ok":       true,
		"category": req.Category,
		"dry_run":  req.DryRun,
		"email":    "sent",
		"to":       map[string]any{"email": to},
	}
	if req.DryRun {
		// The operator asked to see, not send: log the payload, deliver
		// nothing. Mirrors the old per-leg dry-run, minus the SMS leg.
		log.Printf("notify(DRY) operator email to=%s subject=%q body=%d chars", to, req.Subject, len(body))
		out["email"] = "dry_run"
	} else {
		msg := mailtrap.Message{
			To:      []mailtrap.Address{{Email: to}},
			Subject: req.Subject,
			Text:    body,
			HTML:    h.operatorEmailHTML(body, req.Subject),
		}
		if strings.TrimSpace(req.Category) != "" {
			msg.Category = strings.TrimSpace(req.Category)
		}
		callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ids, err := h.Mail.Send(callCtx, msg)
		cancel()
		if err != nil {
			out["ok"] = false
			out["email"] = "failed: " + err.Error()
		} else {
			out["message_ids"] = ids
		}
	}
	out["body_chars"] = map[string]int{"email": len(body)}

	h.audit(identity.User.ID, "admin.notification.sent", ctx,
		fmt.Sprintf("category=%s email=%v to_email=%s body_chars[email]=%d dry_run=%v",
			req.Category, out["email"], to, len(body), req.DryRun))
	writeJSON(ctx, fasthttp.StatusOK, out)
}

// emailBodyMaxLen is the operator-email body ceiling, carried over from the
// removed SMTP gift client so a direct admin email stays the same shape it
// always was.
const emailBodyMaxLen = 2000

// AdminSendGiftNotification lets the operator manually re-send the buyer-
// authored gift message to the recipient. Useful for retrying after a
// provider outage or a wrong recipient email the buyer just corrected.
//
// Request: POST /api/admin/quotes/{id}/send-gift-notification?force=1
//
//	force=1  -> bypass the GiftNotifiedAt idempotency marker (re-send even
//	             after a successful delivery; providers may bill twice).
//	force=0  -> no-op when already notified (default).
//
// Response: { "ok": true, "email": "sent|failed", "message_ids": [...], "notified_at": ... }
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
	if h.Mail == nil || !h.Mail.Enabled() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "mail transport is not configured (set MAILTRAP_API_TOKEN and MAILTRAP_FROM_EMAIL in .env)")
		return
	}
	if strings.TrimSpace(quote.GiftChannel) != "email" {
		writeError(ctx, fasthttp.StatusConflict, "quote has no gift channel (gift_channel is empty)")
		return
	}
	force := string(ctx.QueryArgs().Peek("force")) == "1"
	if !force && !quote.GiftNotifiedAt.IsZero() {
		writeJSON(ctx, fasthttp.StatusOK, map[string]any{
			"ok":          true,
			"skipped":     "already notified",
			"notified_at": quote.GiftNotifiedAt,
			"channel":     "email",
		})
		return
	}

	// The same builder the settlement tracker uses: a manual retry sends
	// byte-for-byte what the automatic fulfillment mail sent.
	note := h.BuildGiftNoteFromQuote(quote, quote.Lang)
	callCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ids, sendErr := h.Mail.SendGiftNote(callCtx, note)

	out := map[string]any{
		"ok":      sendErr == nil,
		"channel": "email",
		"to":      map[string]any{"email": note.Recipient.Email},
		"email":   "sent",
	}
	if sendErr != nil {
		out["ok"] = false
		out["email"] = "failed: " + sendErr.Error()
	} else {
		out["message_ids"] = ids
		if err := h.Store.MarkGiftNotified(quote.ID); err != nil {
			out["notified_at_mark_error"] = err.Error()
		} else {
			out["notified_at"] = time.Now().UTC()
		}
	}

	h.audit(identity.User.ID, "admin.gift.notification_sent", ctx,
		"quote="+id+" email="+fmt.Sprint(out["email"])+" anonymous="+fmt.Sprint(quote.Anonymous))
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

// AdminSendTestEmail sends a realistic gift/purchase email to an operator-
// supplied address, built by the SAME GiftNote builder + Mailtrap transport the
// fulfillment chain uses. It is a demo/testing surface: the note uses sample
// data (a sample gifter, a sample product), never a real buyer, and it records
// nothing as a gift or a purchase. Handy to confirm the flagship email renders
// correctly ("as if someone bought something") before trusting it on a real
// order.
//
// Request: POST /api/admin/test-email
//
//	{ "to_email":"you@example.com", "kind":"card|esim|topup", "product_label":"...", "message":"..." }
//
// The note carries the REAL identicon avatar (the same browser-rasterized
// @nimiq/identicons face a live order's email would embed as a mosaic).
//
// Response: { "ok":true, "to":..., "kind":..., "message_ids":[...] }
func (h *Handlers) AdminSendTestEmail(ctx *fasthttp.RequestCtx) {
	identity := adminIdentity(ctx)
	var req struct {
		ToEmail      string `json:"to_email"`
		Kind         string `json:"kind,omitempty"`          // "card" | "esim" | "topup"
		ProductLabel string `json:"product_label,omitempty"` // optional override
		Message      string `json:"message,omitempty"`       // optional note text override
	}
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid body")
		return
	}
	to := strings.TrimSpace(req.ToEmail)
	if !validEmail(to) {
		writeError(ctx, fasthttp.StatusBadRequest, "to_email must be a valid email address")
		return
	}
	if h.Mail == nil || !h.Mail.Enabled() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "Mailtrap transport is not configured (set MAILTRAP_API_TOKEN and MAILTRAP_FROM_EMAIL in .env)")
		return
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	var productLabel, message string
	switch kind {
	case mailtrap.DeliveryTopUp:
		productLabel = "Turkcell · 100 TRY top-up"
		message = "I topped you up on " + h.Cfg.SiteName() + ". 🌳"
		kind = mailtrap.DeliveryTopUp
	case mailtrap.DeliveryEsim:
		productLabel = "Airalo · 10 GB · 30 days"
		message = "Enjoy your eSIM on " + h.Cfg.SiteName() + ". 📶"
		kind = mailtrap.DeliveryEsim
	default:
		productLabel = "Steam · 50 USD gift card"
		message = "A little something for you on " + h.Cfg.SiteName() + ". 🎮"
		kind = mailtrap.DeliveryCard
	}
	if strings.TrimSpace(req.ProductLabel) != "" {
		productLabel = strings.TrimSpace(req.ProductLabel)
	}
	// The operator's own note text wins — same trim/cap discipline as a real
	// gift note, so the preview shows exactly what a buyer's message does.
	if msg := strings.TrimSpace(req.Message); msg != "" {
		if len(msg) > 280 {
			msg = msg[:280]
		}
		message = msg
	}
	note := mailtrap.GiftNote{
		Recipient:              mailtrap.Address{Email: to},
		GifterNimiqAddress:     "NQ08 D44A 44B9 0F77 2E22 8C13 C345 6FBD XKH9",
		GifterIdenticonDataURI: sampleIdenticonDataURI(),
		TreesDonation:          true,
		SiteName:               h.Cfg.SiteName(),
		ProductLabel:           productLabel,
		Message:                message,
		OrderID:                fmt.Sprintf("ADMINTEST-%d", time.Now().Unix()),
		PurchasedAt:            time.Now().UTC(),
		ShopURL:                h.Cfg.SiteURL(),
		SupportURL:             h.Cfg.SiteURL() + "/support",
		StakeValidatorAddress:  stakeValidatorAddress,
		Delivery:               kind,
		Category:               "admin_test",
	}
	note.Subject = fmt.Sprintf("%s admin test email %s — %s", h.Cfg.SiteName(), note.OrderID, productLabel)

	callCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ids, err := h.Mail.SendGiftNote(callCtx, note)
	if err != nil {
		if errors.Is(err, mailtrap.ErrDisabled) {
			writeError(ctx, fasthttp.StatusServiceUnavailable, "Mailtrap transport is disabled (check MAILTRAP_API_TOKEN / MAILTRAP_FROM_EMAIL)")
			return
		}
		h.audit(identity.User.ID, "admin.test_email", ctx, "send_failed to="+to+" err="+err.Error())
		writeError(ctx, fasthttp.StatusBadGateway, "send failed: "+err.Error())
		return
	}
	h.audit(identity.User.ID, "admin.test_email", ctx, "to="+to+" kind="+kind+" ids="+strings.Join(ids, ","))
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"ok":          true,
		"to":          to,
		"kind":        kind,
		"message_ids": ids,
		"url":         "https://mailtrap.io/sending/email_logs",
	})
}

// sampleIdenticonDataURI is the REAL @nimiq/identicons face (rasterized in a
// browser from the exact vendored module the site ships, 160px) that test
// emails carry as the sender avatar — the same PNG a live named gift's email
// would embed, so the preview shows the true mosaic rendering.
func sampleIdenticonDataURI() string {
	return sampleassets.IdenticonDataURI()
}

var _ = adminmodel.Settings{}
