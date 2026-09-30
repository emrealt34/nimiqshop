package handlers

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

type createSupportTicketRequest struct {
	OrderID string `json:"order_id"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

type addSupportMessageRequest struct {
	Message string `json:"message"`
}

/*
Support-surface limits.

This is the only place in the API where an authenticated customer can write an
UNBOUNDED amount of text into the shop's own storage. Before these constants
the sole ceiling was MAX_REQUEST_BODY_BYTES (1 MB), and nothing capped how many
tickets or replies one account could open — so a single wallet (and wallets are
free: login is one signature over a fresh keypair) could fill the disk with
1 MB support messages at whatever rate the global limiter allowed, and bury the
operator's console in the process.

The values are chosen so no real conversation comes near them:

	supportSubjectMax      200 runes   a subject line
	supportMessageMax      5,000 runes a long, detailed complaint
	supportOrderIDMax      64 runes    a UUID is 36
	supportOpenTicketMax   25          open+waiting tickets per account
	supportMessagePerTicket 500        replies per ticket

Text is rejected with a clear 400 rather than truncated: a buyer whose message
was silently cut would not know the shop never read the end of it.
*/
const (
	supportSubjectMax       = 200
	supportMessageMax       = 5000
	supportOrderIDMax       = 64
	supportOpenTicketMax    = 25
	supportMessagePerTicket = 500
)

// CreateSupportTicket creates a new support ticket tied to an order.
func (h *Handlers) CreateSupportTicket(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)

	var req createSupportTicketRequest
	if err := readJSON(ctx, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}

	req.OrderID = strings.TrimSpace(req.OrderID)
	req.Subject = strings.TrimSpace(req.Subject)
	req.Message = strings.TrimSpace(req.Message)

	// order_id is OPTIONAL: a buyer with no orders (or a general question)
	// can still open a ticket. When it is present it must belong to them.
	if req.Subject == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "subject is required")
		return
	}
	if req.Message == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "message cannot be empty")
		return
	}
	if err := validateSupportText(req.Subject, req.Message, req.OrderID); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// Per-account open-ticket ceiling: an authenticated client must not be
	// able to bury the operator's console (or the disk) with tickets.
	open, err := h.Store.CountOpenSupportTicketsForUser(userID, supportOpenTicketMax)
	if err == nil && open >= supportOpenTicketMax {
		writeJSON(ctx, fasthttp.StatusTooManyRequests, map[string]interface{}{
			"error": "you already have " + strconv.Itoa(open) + " open support tickets — please wait for one of them to be resolved before opening another",
			"code":  "SUPPORT_TICKET_LIMIT", "open_tickets": open, "max_open_tickets": supportOpenTicketMax,
		})
		return
	}

	// Look up user info for display address. BEST-EFFORT: a buyer who only
	// ever created quote purchases may have no full user record yet — the
	// old hard failure 500'd EVERY ticket creation for them ("could not
	// verify user"), which is exactly the "ticket disappears / can't open
	// a ticket" report. The address is display-only.
	userAddress := ""
	if user, err := h.Store.GetUser(userID); err == nil {
		userAddress = user.NimiqAddress
	}

	// Validate order or quote belongs to user (only when one was supplied).
	orderKind := ""
	productID := ""
	if req.OrderID != "" {
		if order, err := h.Store.GetOrderForUser(req.OrderID, userID); err == nil {
			orderKind = order.Kind
			productID = order.ProductID
		} else if quote, err := h.Store.GetQuoteForUser(req.OrderID, userID); err == nil {
			orderKind = "quote"
			productID = quote.ProductID
		} else {
			writeError(ctx, fasthttp.StatusBadRequest, "order_id does not match any of your orders")
			return
		}
	}

	// Check if a ticket is already open for this order
	if req.OrderID != "" {
		if existing, err := h.Store.GetSupportTicketForOrder(req.OrderID); err == nil && existing.ID != "" && existing.Status != "closed" && existing.Status != "resolved" {
			if existing.MessageCount >= supportMessagePerTicket {
				writeJSON(ctx, fasthttp.StatusConflict, map[string]interface{}{
					"error": "this conversation has reached its message limit — please open a new ticket if you need more help",
					"code":  "SUPPORT_MESSAGE_LIMIT", "message_count": existing.MessageCount,
				})
				return
			}
			msg, err := h.Store.AddSupportMessage(existing.ID, "user", userID, req.Message, "waiting_admin")
			if err != nil {
				writeError(ctx, fasthttp.StatusInternalServerError, "could not add message to ticket")
				return
			}
			updatedTicket, _ := h.Store.GetSupportTicket(existing.ID)
			writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
				"ticket":  updatedTicket,
				"message": msg,
			})
			return
		}
	}
	ticket := db.SupportTicket{
		UserID:      userID,
		UserAddress: userAddress,
		OrderID:     req.OrderID,
		OrderKind:   orderKind,
		ProductID:   productID,
		Subject:     req.Subject,
	}

	createdTicket, createdMsg, err := h.Store.CreateSupportTicket(ticket, req.Message)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not create support ticket")
		return
	}

	writeJSON(ctx, fasthttp.StatusCreated, map[string]interface{}{
		"ticket":  createdTicket,
		"message": createdMsg,
	})
}

// ListUserSupportTickets lists all support tickets opened by the authenticated user.
func (h *Handlers) ListUserSupportTickets(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)

	tickets, err := h.Store.ListSupportTicketsForUser(userID, 50)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load tickets")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, tickets)
}

// GetSupportTicket returns ticket details and all message history for the user.
func (h *Handlers) GetSupportTicket(ctx *fasthttp.RequestCtx) {
	ticketID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	ticket, err := h.Store.GetSupportTicketForUser(ticketID, userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "support ticket not found")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load ticket")
		return
	}

	messages, err := h.Store.GetTicketMessagesPublic(ticketID)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load ticket messages")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ticket":   ticket,
		"messages": messages,
	})
}

// AddSupportMessage adds a customer reply to an existing support ticket.
func (h *Handlers) AddSupportMessage(ctx *fasthttp.RequestCtx) {
	ticketID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	var req addSupportMessageRequest
	if err := readJSON(ctx, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "message cannot be empty")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if err := validateSupportText("", req.Message, ""); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Verify user owns the ticket
	ticket, err := h.Store.GetSupportTicketForUser(ticketID, userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "support ticket not found")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not verify ticket")
		return
	}

	if ticket.MessageCount >= supportMessagePerTicket {
		writeJSON(ctx, fasthttp.StatusConflict, map[string]interface{}{
			"error": "this conversation has reached its message limit — please open a new ticket if you need more help",
			"code":  "SUPPORT_MESSAGE_LIMIT", "message_count": ticket.MessageCount,
		})
		return
	}
	msg, err := h.Store.AddSupportMessage(ticket.ID, "user", userID, req.Message, "waiting_admin")
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not send message")
		return
	}

	writeJSON(ctx, fasthttp.StatusCreated, msg)
}

// validateSupportText is the one shape check every support write goes through.
// Rune counts, not byte counts: a Turkish buyer writing 4,000 characters of
// multi-byte text is well inside the intent of the limit, and a byte limit
// would have refused them.
func validateSupportText(subject, message, orderID string) error {
	if subject != "" && utf8.RuneCountInString(subject) > supportSubjectMax {
		return fmt.Errorf("the subject must be %d characters or fewer", supportSubjectMax)
	}
	if message != "" && utf8.RuneCountInString(message) > supportMessageMax {
		return fmt.Errorf("the message must be %d characters or fewer (yours is %d)", supportMessageMax, utf8.RuneCountInString(message))
	}
	if orderID != "" && len(orderID) > supportOrderIDMax {
		return errors.New("order_id is malformed")
	}
	return nil
}

// GetOrderSupport checks if a support ticket exists for a specific order and returns it with messages.
func (h *Handlers) GetOrderSupport(ctx *fasthttp.RequestCtx) {
	orderID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	// Verify order ownership
	_, errOrder := h.Store.GetOrderForUser(orderID, userID)
	_, errQuote := h.Store.GetQuoteForUser(orderID, userID)
	if errOrder != nil && errQuote != nil {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}

	ticket, err := h.Store.GetSupportTicketForOrder(orderID)
	if errors.Is(err, db.ErrNotFound) || ticket.ID == "" {
		writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
			"ticket":   nil,
			"messages": []db.SupportMessage{},
		})
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not check support ticket")
		return
	}

	messages, _ := h.Store.GetTicketMessagesPublic(ticket.ID)
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ticket":   ticket,
		"messages": messages,
	})
}

type updateTicketStatusRequest struct {
	Status string `json:"status"`
}

// UpdateSupportTicketStatusCustomer lets a buyer close their own ticket as
// resolved (from any open state) or reopen a resolved one. Ownership is always
// verified; arbitrary states (closed, waiting_*) stay admin-only.
func (h *Handlers) UpdateSupportTicketStatusCustomer(ctx *fasthttp.RequestCtx) {
	ticketID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	var req updateTicketStatusRequest
	if err := readJSON(ctx, &req); err != nil || strings.TrimSpace(req.Status) == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "status is required")
		return
	}

	// Verified ownership before anything else.
	ticket, err := h.Store.GetSupportTicketForUser(ticketID, userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "support ticket not found")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not verify ticket")
		return
	}

	allowedFrom := map[string][]string{
		"resolved": {"open", "waiting_user", "waiting_admin"},
		"open":     {"resolved"},
	}
	froms, ok := allowedFrom[strings.TrimSpace(req.Status)]
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid status value")
		return
	}
	if !containsStr(froms, ticket.Status) {
		writeError(ctx, fasthttp.StatusConflict, "this ticket cannot be changed from its current state")
		return
	}

	if err := h.Store.UpdateSupportTicketStatus(ticket.ID, strings.TrimSpace(req.Status)); err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not update ticket")
		return
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ok":        true,
		"ticket_id": ticket.ID,
		"status":    strings.TrimSpace(req.Status),
	})
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
