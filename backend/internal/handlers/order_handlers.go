package handlers

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

// orderRefund returns the stored supplier refund record, if any.
func orderRefund(o db.Order) interface{} {
	if len(o.Refund) == 0 {
		return nil
	}
	var refund map[string]interface{}
	if json.Unmarshal(o.Refund, &refund) == nil {
		return refund
	}
	return nil
}

// WebhookURL is the inbound supplier callback base (see WebhookURLFor in
// webhook_handlers.go). Kept for operator tooling.
func (h *Handlers) WebhookURL() string {
	return h.WebhookURLFor("", "")
}

// StageInfo represents a single step in the order fulfillment lifecycle.
type StageInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"` // completed | in_progress | pending | failed
	Timestamp   string `json:"timestamp,omitempty"`
}

// buildOrderStages computes detailed step-by-step progress for an order.
func buildOrderStages(o db.Order) ([]StageInfo, int) {
	createdTime := o.CreatedAt.Format(time.RFC3339)
	updatedTime := o.UpdatedAt.Format(time.RFC3339)

	stages := []StageInfo{
		{
			ID:          "order_placed",
			Title:       "Order Created",
			Description: "Your order request was received. You pay the supplier's invoice directly from your own wallet — nothing is reserved or held by nim.shop.",
			Status:      "completed",
			Timestamp:   createdTime,
		},
		{
			ID:          "payment_settled",
			Title:       "Payment Confirmed",
			Description: "Your USD / NIM payment was verified and forwarded to the supplier invoice.",
			Status:      "completed",
			Timestamp:   createdTime,
		},
		{
			ID:          "supplier_processing",
			Title:       "Supplier Processing",
			Description: "Gift card / top-up is being generated and activated on the supplier network.",
			Status:      "pending",
		},
		{
			ID:          "delivery_complete",
			Title:       "Delivery Completed",
			Description: "Gift card code, PIN and usage instructions are ready.",
			Status:      "pending",
		},
	}

	currentStage := 1

	switch o.Status {
	case "pending", "created", "payment_detected":
		stages[2].Status = "in_progress"
		stages[2].Timestamp = updatedTime
		currentStage = 2
	case "processing":
		stages[2].Status = "in_progress"
		stages[2].Timestamp = updatedTime
		currentStage = 2
	case "delivered", "fulfilled", "complete":
		stages[2].Status = "completed"
		stages[2].Timestamp = updatedTime
		stages[3].Status = "completed"
		stages[3].Timestamp = updatedTime
		currentStage = 3
	case "failed", "refunded":
		stages[2].Status = "failed"
		stages[2].Description = "The transaction could not be completed on the supplier side."
		stages[3].Status = "failed"
		stages[3].Title = "Refunded"
		stages[3].Description = "The supplier issued a full refund for this order."
		stages[3].Timestamp = updatedTime
		currentStage = 3
	default:
		stages[2].Status = "in_progress"
		currentStage = 2
	}

	return stages, currentStage
}

// ListOrders returns the user's order history enriched with supplier IDs,
// fulfillment flags, and support ticket metadata.
func (h *Handlers) ListOrders(ctx *fasthttp.RequestCtx) {
	userID := middleware.UserID(ctx)

	orders, err := h.Store.ListOrders(userID, 100)
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not load orders")
		return
	}

	type orderRow struct {
		ID                string                 `json:"id"`
		Kind              string                 `json:"kind"`
		CategoryID        string                 `json:"category_id"`
		ProductID         string                 `json:"product_id"`
		Quantity          int                    `json:"quantity"`
		PriceUSD          string                 `json:"price_usd"`
		Status            string                 `json:"status"`
		SupplierOrderID   *string                `json:"supplier_order_id,omitempty"`
		SupplierInvoiceID *string                `json:"supplier_invoice_id,omitempty"`
		HasFulfillment    bool                   `json:"has_fulfillment"`
		Refund            interface{}            `json:"refund,omitempty"`
		HasTicket         bool                   `json:"has_ticket"`
		TicketStatus      string                 `json:"ticket_status,omitempty"`
		CreatedAt         time.Time              `json:"created_at"`
		UpdatedAt         time.Time              `json:"updated_at"`
		Stages            []StageInfo            `json:"stages"`
		CurrentStage      int                    `json:"current_stage"`
		Payload           map[string]interface{} `json:"payload,omitempty"`
	}

	out := make([]orderRow, 0, len(orders))
	for _, o := range orders {
		stages, currStage := buildOrderStages(o)
		hasFul := len(o.Fulfillment) > 0 && string(o.Fulfillment) != "null"

		hasTicket := false
		ticketStatus := ""
		if t, err := h.Store.GetSupportTicketForOrder(o.ID); err == nil && t.ID != "" {
			hasTicket = true
			ticketStatus = t.Status
		}

		var payloadMap map[string]interface{}
		if len(o.Payload) > 0 {
			_ = json.Unmarshal(o.Payload, &payloadMap)
		}

		out = append(out, orderRow{
			ID:                o.ID,
			Kind:              o.Kind,
			CategoryID:        o.CategoryID,
			ProductID:         o.ProductID,
			Quantity:          o.Quantity,
			PriceUSD:          o.PriceUSD.String(),
			Status:            o.Status,
			SupplierOrderID:   o.SupplierOrderID,
			SupplierInvoiceID: o.SupplierInvoiceID,
			HasFulfillment:    hasFul,
			Refund:            orderRefund(o),
			HasTicket:         hasTicket,
			TicketStatus:      ticketStatus,
			CreatedAt:         o.CreatedAt,
			UpdatedAt:         o.UpdatedAt,
			Stages:            stages,
			CurrentStage:      currStage,
			Payload:           payloadMap,
		})
	}
	writeJSON(ctx, fasthttp.StatusOK, out)
}

// GetOrder returns comprehensive single order details including step-by-step stages,
// delivery fulfillment details (codes/PINs/claim link), and support messages.
func (h *Handlers) GetOrder(ctx *fasthttp.RequestCtx) {
	orderID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	order, err := h.Store.GetOrderForUser(orderID, userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not fetch order")
		return
	}

	// Supplier status is updated by the supplier webhook/tracker. Reading an
	// order never triggers a supplier poll.

	stages, currentStage := buildOrderStages(order)

	// Unmarshal fulfillment (supplier-defined shape; pass through opaquely)
	var redemption map[string]interface{}
	if len(order.Fulfillment) > 0 && string(order.Fulfillment) != "null" {
		_ = json.Unmarshal(order.Fulfillment, &redemption)
	}

	var payloadMap map[string]interface{}
	if len(order.Payload) > 0 {
		_ = json.Unmarshal(order.Payload, &payloadMap)
	}

	// Fetch support ticket & messages for this order if exists
	var ticketInfo *db.SupportTicket
	var messages []db.SupportMessage
	if t, err := h.Store.GetSupportTicketForOrder(order.ID); err == nil && t.ID != "" {
		ticketInfo = &t
		msgs, _ := h.Store.GetTicketMessages(t.ID)
		messages = msgs
	}

	// RECIPIENT INFO (owner-scoped): the Order record itself carries only the
	// supplier fulfillment payload; the delivery recipient (email / top-up
	// phone / gift metadata) lives on the QUOTE record. Orders inherit the
	// quote's ID lifecycle, so we read the quote by the same id — ownership
	// is re-checked — and surface those fields to the order's owner only.
	recipient := map[string]interface{}{}
	if q, qErr := h.Store.GetQuoteForUser(orderID, userID); qErr == nil {
		recipient = map[string]interface{}{
			"customer_email":       q.CustomerEmail,
			"phone_number":         q.PhoneNumber,
			"beneficiary_account":  q.BeneficiaryAccount,
			"gift_channel":         q.GiftChannel,
			"gift_message":         q.GiftMessage,
			"gift_recipient_phone": q.GiftRecipientPhone,
		}
	}

	response := map[string]interface{}{
		"id":                  order.ID,
		"user_id":             order.UserID,
		"kind":                order.Kind,
		"category_id":         order.CategoryID,
		"product_id":          order.ProductID,
		"quantity":            order.Quantity,
		"price_usd":           order.PriceUSD.String(),
		"status":              order.Status,
		"supplier_order_id":   order.SupplierOrderID,
		"supplier_invoice_id": order.SupplierInvoiceID,
		"created_at":          order.CreatedAt,
		"updated_at":          order.UpdatedAt,
		"payload":             payloadMap,
		"fulfillment":         redemption,
		"stages":              stages,
		"current_stage":       currentStage,
		"refund":              orderRefund(order),
		"support_ticket":      ticketInfo,
		"support_messages":    messages,
	}
	for k, v := range recipient {
		if s, ok := v.(string); ok && s != "" {
			response[k] = s
		}
	}

	writeJSON(ctx, fasthttp.StatusOK, response)
}

// RefreshOrder triggers a manual live status check with the supplier for the specified order.
func (h *Handlers) RefreshOrder(ctx *fasthttp.RequestCtx) {
	orderID, _ := ctx.UserValue("id").(string)
	userID := middleware.UserID(ctx)

	order, err := h.Store.GetOrderForUser(orderID, userID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not fetch order")
		return
	}

	// Manual refresh only re-reads our local state; the supplier is contacted
	// by the tracker/webhook, not by a customer/browser polling endpoint.

	stages, currentStage := buildOrderStages(order)
	var redemption map[string]interface{}
	if len(order.Fulfillment) > 0 && string(order.Fulfillment) != "null" {
		_ = json.Unmarshal(order.Fulfillment, &redemption)
	}

	var ticketInfo *db.SupportTicket
	var messages []db.SupportMessage
	if t, err := h.Store.GetSupportTicketForOrder(order.ID); err == nil && t.ID != "" {
		ticketInfo = &t
		messages, _ = h.Store.GetTicketMessages(t.ID)
	}

	writeJSON(ctx, fasthttp.StatusOK, map[string]interface{}{
		"id":                  order.ID,
		"status":              order.Status,
		"supplier_order_id":   order.SupplierOrderID,
		"supplier_invoice_id": order.SupplierInvoiceID,
		"updated_at":          order.UpdatedAt,
		"stages":              stages,
		"current_stage":       currentStage,
		"fulfillment":         redemption,
		"support_ticket":      ticketInfo,
		"support_messages":    messages,
	})
}
