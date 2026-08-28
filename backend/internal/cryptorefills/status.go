package cryptorefills

import (
	"encoding/json"
	"strings"
)

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Supplier order states (cryptorefills.com API docs):
//
//	Created              - order created, no payment activity yet
//	WaitingForPayment    - payment window open, countdown running
//	PaymentStarted       - tx broadcast, not confirmed yet
//	PartialPaymentStarted- partial payment detected
//	PaymentReceived      - confirmed on-chain, delivery not started
//	WaitingForDelivery   - queued for delivery
//	WaitingForManualAction - needs supplier team intervention
//	Done                 - delivered (terminal success)
//	Expired              - payment window elapsed (terminal)
//	PaymentFailed        - payment attempt failed (terminal)
//	PaymentSetupFailed   - payment could not be set up (terminal)
//	Refunded             - refunded to the customer (terminal)

const (
	StatusCreated               = "Created"
	StatusWaitingForPayment     = "WaitingForPayment"
	StatusPaymentStarted        = "PaymentStarted"
	StatusPartialPaymentStarted = "PartialPaymentStarted"
	StatusPaymentReceived       = "PaymentReceived"
	StatusWaitingForDelivery    = "WaitingForDelivery"
	StatusWaitingForManual      = "WaitingForManualAction"
	StatusDone                  = "Done"
	StatusExpired               = "Expired"
	StatusPaymentFailed         = "PaymentFailed"
	StatusPaymentSetupFailed    = "PaymentSetupFailed"
	StatusRefunded              = "Refunded"
)

// Local quote statuses used by the db layer (supplier-agnostic names).
const (
	QuoteOrderCreating = "order_creating" // WAL intent, before CreateOrder
	QuoteAwaitingPay   = "awaiting_payment"
	QuotePaidStarted   = "payment_started"
	QuotePaidReceived  = "payment_received"
	QuoteDelivering    = "delivering"
	QuoteFulfilled     = "fulfilled"
	QuoteExpired       = "expired"
	QuoteFailed        = "failed"
	QuoteManualReview  = "manual_review"
	QuoteRefunded      = "refunded"
)

// MapToQuoteStatus translates a supplier state to the local quote status.
// Unknown states map to manual_review (fail visible, never silent).
func MapToQuoteStatus(supplierStatus string) string {
	switch strings.ToLower(strings.TrimSpace(supplierStatus)) {
	case strings.ToLower(StatusCreated), strings.ToLower(StatusWaitingForPayment):
		return QuoteAwaitingPay
	case strings.ToLower(StatusPaymentStarted), strings.ToLower(StatusPartialPaymentStarted):
		return QuotePaidStarted
	case strings.ToLower(StatusPaymentReceived):
		return QuotePaidReceived
	case strings.ToLower(StatusWaitingForDelivery):
		return QuoteDelivering
	case strings.ToLower(StatusDone):
		return QuoteFulfilled
	case strings.ToLower(StatusExpired):
		return QuoteExpired
	case strings.ToLower(StatusPaymentFailed), strings.ToLower(StatusPaymentSetupFailed):
		return QuoteFailed
	case strings.ToLower(StatusRefunded):
		return QuoteRefunded
	case strings.ToLower(StatusWaitingForManual):
		return QuoteManualReview
	default:
		return QuoteManualReview
	}
}

// IsTerminal reports whether a supplier state will never change again.
func IsTerminal(supplierStatus string) bool {
	switch strings.ToLower(strings.TrimSpace(supplierStatus)) {
	case strings.ToLower(StatusDone), strings.ToLower(StatusExpired),
		strings.ToLower(StatusPaymentFailed), strings.ToLower(StatusPaymentSetupFailed),
		strings.ToLower(StatusRefunded):
		return true
	}
	return false
}

// IsPaidOrBeyond reports whether the supplier has seen money (any state
// from PaymentStarted onward, excluding pure setup failures).
func IsPaidOrBeyond(supplierStatus string) bool {
	switch strings.ToLower(strings.TrimSpace(supplierStatus)) {
	case strings.ToLower(StatusPaymentStarted), strings.ToLower(StatusPartialPaymentStarted),
		strings.ToLower(StatusPaymentReceived), strings.ToLower(StatusWaitingForDelivery),
		strings.ToLower(StatusDone), strings.ToLower(StatusRefunded):
		return true
	}
	return false
}

// FulfillmentPayload extracts the customer-facing redemption data from a
// Done order. It stores the relevant deliverable fields as stable JSON so
// the frontend can render code/PIN/link/QR/barcode without re-fetching.
func FulfillmentPayload(order *Order) []byte {
	if order == nil || len(order.Deliveries) == 0 {
		return nil
	}
	type item struct {
		BrandName    string `json:"brand_name,omitempty"`
		Family       string `json:"family,omitempty"`
		Denomination string `json:"denomination,omitempty"`
		CountryCode  string `json:"country_code,omitempty"`
		Kind         string `json:"kind,omitempty"`
		DeliveryType string `json:"delivery_type,omitempty"`
		Code         string `json:"code,omitempty"`
		Pin          string `json:"pin,omitempty"`
		SecurityCode string `json:"security_code,omitempty"`
		OperatorRef  string `json:"operator_reference,omitempty"`
		BarcodeURL   string `json:"barcode_image_url,omitempty"`
		QRURL        string `json:"qr_image_url,omitempty"`
		Instructions string `json:"instructions,omitempty"`
		HowToRedeem  string `json:"how_to_redeem,omitempty"`
	}
	out := make([]item, 0, len(order.Deliveries))
	for _, d := range order.Deliveries {
		inst := d.RedeemInstructions
		if inst == "" {
			inst = d.HowToRedeem
		}
		out = append(out, item{
			BrandName:    d.BrandName,
			Family:       d.Family,
			Denomination: d.Denomination,
			CountryCode:  d.CountryCode,
			Kind:         d.Kind,
			DeliveryType: d.DeliveryType,
			Code:         d.PinCode,
			Pin:          d.PinSerial,
			SecurityCode: d.SecurityCode,
			OperatorRef:  d.OperatorRef,
			BarcodeURL:   d.BarcodeImageURL,
			QRURL:        d.QRImageURL,
			Instructions: inst,
			HowToRedeem:  d.HowToRedeem,
		})
	}
	// json via the standard library to keep the shape stable.
	b, err := jsonMarshal(out)
	if err != nil {
		return nil
	}
	return b
}
