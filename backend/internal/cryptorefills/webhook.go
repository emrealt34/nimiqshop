package cryptorefills

import "encoding/json"

// WebhookPayload is the incoming order-status webhook body. Cryptorefills'
// webhook event list is account-managed; the field names below are parsed
// defensively (common aliases accepted) so a shape evolution degrades to
// "log + 500" instead of a silent drop. After parsing, the handler ALWAYS
// re-fetches the order through the queued API client — the webhook is only
// a trigger, never a source of truth.
type WebhookPayload struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

// ParseWebhookPayload accepts the documented order status event and
// reasonable aliases. It returns false when no order id can be extracted.
func ParseWebhookPayload(raw []byte) (*WebhookPayload, bool) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil || generic == nil {
		return nil, false
	}
	peekStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := generic[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil && s != "" {
					return s
				}
			}
		}
		return ""
	}
	id := peekStr("order_id", "orderId", "id", "reference_id")
	status := peekStr("status", "state", "order_status")
	if id == "" {
		return nil, false
	}
	return &WebhookPayload{OrderID: id, Status: status}, true
}
