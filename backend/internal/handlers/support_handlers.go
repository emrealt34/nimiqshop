package handlers

import (
	"errors"
	"strings"

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

	if req.OrderID == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "order_id is required")
		return
	}
	if req.Subject == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "subject is required")
		return
	}
	if req.Message == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "message cannot be empty")
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

	// Validate order or quote belongs to user
	orderKind := ""
	productID := ""
	if order, err := h.Store.GetOrderForUser(req.OrderID, userID); err == nil {
		orderKind = order.Kind
		productID = order.ProductID
	} else if quote, err := h.Store.GetQuoteForUser(req.OrderID, userID); err == nil {
		orderKind = "quote"
		productID = quote.ProductID
	}

	// Check if a ticket is already open for this order
	if existing, err := h.Store.GetSupportTicketForOrder(req.OrderID); err == nil && existing.ID != "" && existing.Status != "closed" && existing.Status != "resolved" {
		// Append message to existing open ticket instead
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

	messages, err := h.Store.GetTicketMessages(ticketID)
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

	msg, err := h.Store.AddSupportMessage(ticket.ID, "user", userID, req.Message, "waiting_admin")
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not send message")
		return
	}

	writeJSON(ctx, fasthttp.StatusCreated, msg)
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

	messages, _ := h.Store.GetTicketMessages(ticket.ID)
	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"ticket":   ticket,
		"messages": messages,
	})
}
