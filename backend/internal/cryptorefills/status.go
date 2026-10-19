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

// normalizeState folds the two spellings the supplier's API generations use
// into one comparable token. v5 answers CamelCase ("WaitingForPayment",
// "PaymentStarted"), the v6 PARTNER API answers SCREAMING_SNAKE
// ("WAITING_FOR_PAYMENT", "PAYMENT_STARTED"), and a state that fails to map
// is not a cosmetic problem: "is this order still awaiting payment?" is the
// question that decides whether an invoice may be shown at all.
func normalizeState(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case '_', '-', ' ', '.':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// MapToQuoteStatus translates a supplier state to the local quote status.
// Unknown states map to manual_review (fail visible, never silent).
func MapToQuoteStatus(supplierStatus string) string {
	switch normalizeState(supplierStatus) {
	case normalizeState(StatusCreated), normalizeState(StatusWaitingForPayment), "waitingforpay":
		return QuoteAwaitingPay
	case normalizeState(StatusPaymentStarted), normalizeState(StatusPartialPaymentStarted):
		return QuotePaidStarted
	case normalizeState(StatusPaymentReceived), "paymentconfirmed", "paymentcompleted", "paid":
		return QuotePaidReceived
	case normalizeState(StatusWaitingForDelivery), "inprogress", "processing":
		return QuoteDelivering
	case normalizeState(StatusDone), "completed", "complete", "delivered", "success", "successful":
		return QuoteFulfilled
	case normalizeState(StatusExpired), "canceled", "cancelled":
		// A cancel (ours or the supplier's) only ever happens before money
		// is seen, so locally it is the same outcome as a lapsed window:
		// dead, unpaid, never payable again.
		return QuoteExpired
	case normalizeState(StatusPaymentFailed), normalizeState(StatusPaymentSetupFailed), "failed":
		return QuoteFailed
	case normalizeState(StatusRefunded):
		return QuoteRefunded
	case normalizeState(StatusWaitingForManual), "waitingformanual":
		return QuoteManualReview
	default:
		return QuoteManualReview
	}
}

// IsTerminal reports whether a supplier state will never change again.
func IsTerminal(supplierStatus string) bool {
	switch normalizeState(supplierStatus) {
	case normalizeState(StatusDone), "completed", "complete", "delivered",
		normalizeState(StatusExpired), "canceled", "cancelled",
		normalizeState(StatusPaymentFailed), normalizeState(StatusPaymentSetupFailed), "failed",
		normalizeState(StatusRefunded):
		return true
	}
	return false
}

// IsPaidOrBeyond reports whether the supplier has seen money (any state
// from PaymentStarted onward, excluding pure setup failures).
func IsPaidOrBeyond(supplierStatus string) bool {
	switch normalizeState(supplierStatus) {
	case normalizeState(StatusPaymentStarted), normalizeState(StatusPartialPaymentStarted),
		normalizeState(StatusPaymentReceived), "paymentconfirmed", "paymentcompleted", "paid",
		normalizeState(StatusWaitingForDelivery), "inprogress", "processing",
		normalizeState(StatusDone), "completed", "complete", "delivered",
		normalizeState(StatusRefunded):
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
		brand, denom, country := d.BrandName, d.Denomination, d.CountryCode
		if brand == "" {
			brand = d.Deliverable.BrandName
		}
		if denom == "" {
			denom = d.Deliverable.Denomination
		}
		if country == "" {
			country = d.Deliverable.CountryCode
		}
		out = append(out, item{
			BrandName:    brand,
			Family:       d.Family,
			Denomination: denom,
			CountryCode:  country,
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

// MoneyObserved includes explicit amount evidence even if the supplier has not
// yet advanced order_state. Malformed nonempty amount evidence is uncertain,
// never permission to make another payment.
func MoneyObserved(order *Order) bool {
	if order == nil {
		return false
	}
	if IsPaidOrBeyond(order.Status) {
		return true
	}
	// v6 may report the payment progress as its own state in either spelling
	// ("payment_state": "PAYMENT_STARTED" is one observed shape).
	switch normalizeState(order.PaymentState) {
	case "paymentstarted", "partialpaymentstarted", "paymentreceived", "paymentcompleted", "paymentconfirmed", "paid":
		return true
	}
	for _, v := range []string{order.ReceivedAmount, order.SentCoinAmount} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		// All-zero decimal strings explicitly mean no money.
		if decimalPattern.MatchString(v) && strings.Trim(v, "0.") == "" {
			continue
		}
		return true
	}
	return false
}
